package usagespool

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
)

// stageLocked 由已规划的批次提交调用，stage 负责写入及同步具名临时文件。计数只包含实际有写入的操作。
// 单文件持锁写，至少两份文件才登记在途空间、放锁并行写。返回值说明是否需要回锁复核并撤下登记。
func (s *Spool) stageLocked(batch []*mutation, stage func(string, []byte) error) bool {
	var files []*mutation
	for _, m := range batch {
		if m.noop || m.kind == acknowledgeSnapshot {
			continue
		}
		suffix, err := token()
		if err != nil {
			m.err = err
			continue
		}
		m.temporary = ".pending-" + suffix + ".tmp"
		files = append(files, m)
	}
	switch len(files) {
	case 0:
		return false
	case 1:
		m := files[0]
		m.err = stage(m.temporary, m.raw)
		return false
	}
	if s.staging == nil {
		s.staging = make(map[string]*mutation)
	}
	for _, m := range files {
		s.staging[m.temporary] = m
	}
	done := make(chan struct{})
	s.stageDone = done
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, m := range files {
		wg.Go(func() { m.err = stage(m.temporary, m.raw) })
	}
	wg.Wait()
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

// finishStagedLocked 仅在多文件批次回锁后计划发生变化时，按队列顺序复核一笔操作。
// m 有已同步的证据时复用它，syncDir 确认改名或删除。未暂存的操作走原逐笔路径，错误保留在 m.err。
func (s *Spool) finishStagedLocked(m *mutation, syncDir func(string) error) {
	if m.temporary == "" {
		if m.err == nil {
			m.err = s.applyOneLocked(m)
		}
		return
	}
	target, result := s.singleTargetLocked(m)
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
		case result == nil:
			m.err = s.root.Remove(m.temporary)
			if m.err == nil {
				m.err = syncDir(".")
			}
		default:
			m.err = s.quarantineLocked(m.temporary)
		}
	}
	delete(s.staging, m.temporary)
	if m.err == nil {
		m.err = result
	}
	s.lastErr = m.err
	if target == m.name {
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
