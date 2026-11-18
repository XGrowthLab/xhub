package usagespool

import (
	"bytes"
	"os"
	"sync"

	"github.com/sunqirui1987/xhub/internal/iam"
)

type mutationKind uint8

const (
	putSnapshot mutationKind = iota
	beginMarker
	completeMarker
	acknowledgeSnapshot
)

// mutation 只持有本次请求已编码的 v1 快照，直到目录同步后才通知该调用者。不同组不会并行修改目录。
type mutation struct {
	kind      mutationKind
	record    iam.UsageRecord
	raw       []byte
	name      string
	temporary string
	err       error
	done      chan struct{}
}

// mutationQueue 只在处理磁盘时收集已经到达的调用，切批不等待定时器；mu 独立于完整目录锁。
type mutationQueue struct {
	mu      sync.Mutex
	waiting []*mutation
	running bool
}

// submitMutation 由已校验并编码的账单操作调用。空闲时立刻开始写盘；同一时间等待的操作可共用目录同步。
// 配置为 1 时直接使用原先的逐笔路径；所有模式均在成功返回之前完成各自的持久化边界。
func (s *Spool) submitMutation(kind mutationKind, record iam.UsageRecord, raw []byte) error {
	m := &mutation{kind: kind, record: record, raw: raw, name: basename(record.RequestID)}
	if s.config.GroupSize == 1 {
		s.mu.Lock()
		m.err = s.applyOneLocked(m)
		s.finishLocked(&m.err)
		return m.err
	}
	m.done = make(chan struct{})
	q := &s.writes
	q.mu.Lock()
	q.waiting = append(q.waiting, m)
	if !q.running {
		q.running = true
		go s.drainMutations()
	}
	q.mu.Unlock()
	<-m.done
	return m.err
}

// drainMutations 仅在有排队写入时运行。取得目录锁后切出队首一组，逐个通知，再将锁还给恢复、关闭及准入。
// 所有调用都有自己的结果，失败快照留在目录；队列变空时在同一队列锁内退出以避免漏唤醒。
func (s *Spool) drainMutations() {
	q := &s.writes
	for {
		s.mu.Lock()
		q.mu.Lock()
		if len(q.waiting) == 0 {
			q.running = false
			q.mu.Unlock()
			s.mu.Unlock()
			return
		}
		n := min(len(q.waiting), s.config.GroupSize)
		batch := q.waiting[:n:n]
		q.waiting = q.waiting[n:]
		if len(q.waiting) == 0 {
			q.waiting = nil
		}
		q.mu.Unlock()
		if len(batch) > 1 && s.canGroupLocked(batch) {
			s.applyGroupLocked(batch, s.stageFile, s.syncDirectoryLocked)
		} else {
			for _, m := range batch {
				m.err = s.applyOneLocked(m)
			}
		}
		for i, m := range batch {
			if m.err != nil {
				s.probedThrough = 0
			}
			close(m.done)
			batch[i] = nil
		}
		s.mu.Unlock()
	}
}

// applyOneLocked 执行原逐笔路径；容量临界、重复 ID、内容冲突、关闭或不安全文件仍按各笔原有规则处理。
func (s *Spool) applyOneLocked(m *mutation) error {
	switch m.kind {
	case putSnapshot:
		return s.putLocked(m.record, m.raw)
	case beginMarker:
		return s.beginLocked(m.record, m.raw)
	case completeMarker:
		return s.completeLocked(m.record, m.raw)
	default:
		if s.closed {
			return ErrUnavailable
		}
		return s.acknowledgeLocked(m.name, m.raw)
	}
}

// canGroupLocked 证明整组可以独立执行而不会改变逐笔容量或归属判定；不修改任何文件、预留或状态。
// 同组不能依赖另一笔释放的容量。采用保守上界，无法证明时回到逐笔校验，包括冲突证据的保留流程。
func (s *Spool) canGroupLocked(batch []*mutation) bool {
	if s.closed {
		return false
	}
	state, err := s.statusLocked()
	if err != nil || state.Quarantined != 0 {
		return false
	}
	space := s.config.MaxBytes - state.Bytes - state.ReservedBytes
	entries := s.config.MaxEntries - state.Pending - state.Reserved
	seen := make(map[string]bool, len(batch))
	for _, m := range batch {
		if seen[m.name] {
			return false
		}
		seen[m.name] = true
		if m.kind != acknowledgeSnapshot {
			size := int64(len(m.raw))
			if size > s.config.MaxRecordBytes || size > space {
				return false
			}
			space -= size
		}
		switch m.kind {
		case putSnapshot, beginMarker:
			if entries <= 0 || (m.kind == beginMarker && s.activeDelivery[m.name]) {
				return false
			}
			if _, err := s.root.Lstat(m.name); !os.IsNotExist(err) {
				return false
			}
			entries--
		case completeMarker:
			if _, err := s.markerSizeLocked(m.record); err != nil {
				return false
			}
		case acknowledgeSnapshot:
			current, err := s.readLocked(m.name)
			if err != nil || !bytes.Equal(current, m.raw) {
				return false
			}
		}
	}
	return true
}

// applyGroupLocked 持有目录锁执行已证明互不依赖的一批路径；stage 为每个文件完成原有数据 fsync，
// syncDir 在所有成功改名/删除之后同步根目录。失败仅报告给受影响的操作，不把未同步的终态交给回放者。
func (s *Spool) applyGroupLocked(batch []*mutation, stage func([]byte) (string, error), syncDir func(string) error) {
	var wg sync.WaitGroup
	for _, m := range batch {
		if m.kind != acknowledgeSnapshot {
			wg.Go(func() { m.temporary, m.err = stage(m.raw) })
		}
	}
	wg.Wait()
	changed := false
	for _, m := range batch {
		if m.err != nil {
			continue
		}
		if m.kind == acknowledgeSnapshot {
			m.err = s.root.Remove(m.name)
		} else {
			m.err = s.root.Rename(m.temporary, m.name)
		}
		if m.err == nil {
			changed = true
		}
	}
	var barrier error
	if changed {
		barrier = syncDir(".")
	}
	for _, m := range batch {
		if m.err == nil {
			m.err = barrier
		}
		switch m.kind {
		case putSnapshot:
			s.generation++
			delete(s.reserved, m.record.RequestID)
		case beginMarker:
			s.generation++
			if m.err == nil {
				s.activeDelivery[m.name] = true
			} else {
				delete(s.reserved, m.record.RequestID)
			}
		case completeMarker:
			s.generation++
			if m.err == nil {
				delete(s.activeDelivery, m.name)
				delete(s.reserved, m.record.RequestID)
			}
		case acknowledgeSnapshot:
			if m.err == nil {
				s.noteSettledLocked(m.name)
			}
		}
	}
	s.lastErr = barrier
	for _, m := range batch {
		if m.err != nil {
			s.lastErr = m.err
			break
		}
	}
}
