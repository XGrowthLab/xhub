package usagespool

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
)

// stageLocked 登记本批需要写入的完整文件名，保护在途空间及根目录，再在 mu 外执行 stage。
// 返回是否释放过锁。回锁时临时文件仍向扫描隐藏，调用方须在发布或处理错误后撤下登记。
func (s *Spool) stageLocked(batch []*mutation, stage func(string, []byte) error) bool {
	var files map[string]*mutation
	for _, m := range batch {
		if m.noop || m.kind == acknowledgeSnapshot {
			continue
		}
		suffix, err := token()
		if err != nil {
			m.err = err
			continue
		}
		if files == nil {
			files = make(map[string]*mutation)
		}
		m.temporary = ".pending-" + suffix + ".tmp"
		if m.conflictTarget != "" {
			m.temporary = "quarantine/" + m.temporary
		}
		files[m.temporary] = m
	}
	if len(files) == 0 {
		return false
	}
	if s.staging == nil {
		s.staging = make(map[string]*mutation)
	}
	for name, m := range files {
		s.staging[name] = m
	}
	done := make(chan struct{})
	s.stageDone = done
	s.mu.Unlock()
	if len(files) == 1 {
		for name, m := range files {
			m.err = stage(name, m.raw)
		}
	} else {
		var wg sync.WaitGroup
		for name, m := range files {
			wg.Go(func() { m.err = stage(name, m.raw) })
		}
		wg.Wait()
	}
	s.mu.Lock()
	close(done)
	s.stageDone = nil
	return true
}

// singleTargetLocked 为逐笔提交核对当前槽位、文件及所有权。返回实际发布路径和业务错误；
// 只有内容冲突可同时返回路径和 ErrConflict，须先把新证据持久化至隔离目录。校验本身不修改账本。
func (s *Spool) singleTargetLocked(m *mutation) (string, error) {
	if s.closed {
		return "", ErrUnavailable
	}
	size := int64(len(m.raw))
	if size > s.config.MaxRecordBytes {
		return "", ErrFull
	}
	if m.kind == completeMarker {
		old, err := s.markerSizeLocked(m.record)
		if err != nil {
			return "", err
		}
		state, err := s.statusLocked()
		if err != nil {
			return "", err
		}
		reserved := state.ReservedBytes
		if s.reserved[m.record.RequestID] {
			reserved -= s.config.MaxRecordBytes
		}
		if state.Bytes-old+size+reserved > s.config.MaxBytes {
			return "", ErrFull
		}
		return m.name, nil
	}
	if m.kind == beginMarker && s.activeDelivery[m.name] {
		return "", ErrConflict
	}
	old, err := s.readLocked(m.name)
	if err == nil {
		if bytes.Equal(old, m.raw) {
			return "", nil
		}
		state, err := s.statusLocked()
		if err != nil {
			return "", err
		}
		if state.Bytes+size > s.config.MaxBytes {
			return "", ErrFull
		}
		sum := sha256.Sum256(m.raw)
		dest := "quarantine/conflict-" + hex.EncodeToString(sum[:]) + ".evidence"
		if _, err = s.root.Lstat(dest); os.IsNotExist(err) {
			return dest, ErrConflict
		} else if err != nil {
			return "", err
		}
		return "", ErrConflict
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	state, err := s.statusLocked()
	if err != nil {
		return "", err
	}
	own := boolInt(s.reserved[m.record.RequestID])
	if state.Pending+state.Reserved-own >= s.config.MaxEntries || state.Bytes+state.ReservedBytes-int64(own)*s.config.MaxRecordBytes+size > s.config.MaxBytes {
		return "", ErrFull
	}
	return m.name, nil
}

// applySingleLocked 在同名依赖、容量边界或批次复核变化时逐笔确认。首次规划需要写盘时，
// stage 在目录锁外执行；已有暂存结果直接复用。返回前重新校验实际发布目标、同步目录并维护原有预留。
func (s *Spool) applySingleLocked(m *mutation, stage func(string, []byte) error, syncDir func(string) error) {
	if m.kind == acknowledgeSnapshot {
		if m.err != nil {
			return
		}
		if s.closed {
			m.err = ErrUnavailable
			return
		}
		m.err = s.acknowledgeLocked(m.name, m.raw)
		return
	}
	m.noop = false
	target, result := s.singleTargetLocked(m)
	if target != "" && result == ErrConflict {
		m.conflictTarget = target
	}
	if target != "" && m.temporary == "" && m.err == nil {
		if s.stageLocked([]*mutation{m}, stage) {
			target, result = s.singleTargetLocked(m)
			if m.conflictTarget != "" && result != ErrFull {
				// 已确认存在过冲突。旧账可能在暂存期间入库并删除，恢复也不能把隔离文件提升为新的正常账单。
				if _, e := s.root.Lstat(m.conflictTarget); os.IsNotExist(e) {
					target, result = m.conflictTarget, ErrConflict
				} else if e == nil {
					target, result = "", ErrConflict
				} else {
					target, result = "", e
				}
			}
		}
	}
	if m.err == nil {
		switch {
		case target != "":
			m.err = s.root.Rename(m.temporary, target)
			if m.err == nil {
				m.err = syncDir(".")
				if m.err == nil && filepath.Dir(target) != "." {
					m.err = syncDir(filepath.Dir(target))
				}
			}
		case m.temporary != "" && result == nil:
			m.err = s.root.Remove(m.temporary)
			if m.err == nil {
				m.err = syncDir(".")
			}
		case m.temporary != "" && m.conflictTarget != "":
			// 本文件已在隔离目录；重复冲突已有确定证据时可清除额外副本，异常则保留原件。
			if _, e := s.root.Lstat(m.conflictTarget); e == nil {
				m.err = s.root.Remove(m.temporary)
				if m.err == nil {
					m.err = syncDir("quarantine")
				}
			}
		case m.temporary != "":
			m.err = s.quarantineLocked(m.temporary)
		}
	}
	if m.temporary != "" {
		delete(s.staging, m.temporary)
	}
	if m.err == nil {
		m.err = result
	}
	if m.temporary != "" || m.err != nil && (m.kind == putSnapshot || m.kind == beginMarker && !s.activeDelivery[m.name]) {
		s.lastErr = m.err
	}
	if target == m.name && (m.temporary != "" || m.err != nil) {
		s.generation++
	}
	switch m.kind {
	case putSnapshot:
		delete(s.reserved, m.record.RequestID)
	case beginMarker:
		if m.err == nil {
			s.activeDelivery[m.name] = true
		} else if !s.activeDelivery[m.name] {
			delete(s.reserved, m.record.RequestID)
		}
	case completeMarker:
		if m.err == nil {
			delete(s.activeDelivery, m.name)
			delete(s.reserved, m.record.RequestID)
		}
	}
}
