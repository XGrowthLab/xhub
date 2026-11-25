package usagespool

import (
	"bytes"
	"os"
	"sync"

	"github.com/sunqirui1987/xhub/internal/iam"
)

// mutationKind 区分磁盘快照的四种操作，用于选择校验、预留和持久化边界。
type mutationKind uint8

const (
	// putSnapshot 首次持久化账单并释放本次调用的预留。
	putSnapshot mutationKind = iota
	// beginMarker 发布零费在途标记，同时保留本次调用的终态空间。
	beginMarker
	// completeMarker 以同一归属的终态原子替换在途标记。
	completeMarker
	// acknowledgeSnapshot 核对已成功入账的完整快照后删除。
	acknowledgeSnapshot
)

// mutation 只持有本次请求已编码的 v1 快照，直到目录同步后才通知该调用者。不同组不会并行修改目录。
type mutation struct {
	kind           mutationKind
	record         iam.UsageRecord
	raw            []byte
	name           string
	temporary      string
	conflictTarget string // 首次核验已判定的冲突只进隔离目录，即使旧账在锁外阶段被核销。
	noop           bool   // 目标已确认相同或已不存在，按原有幂等规则完成。
	err            error
	done           chan struct{}
}

// mutationQueue 只在处理磁盘时收集已经到达的调用，切批不等待定时器；mu 独立于完整目录锁。
type mutationQueue struct {
	mu      sync.Mutex
	waiting []*mutation
	running bool
}

// submitMutation 由已校验并编码的账单操作调用。空闲时立刻开始写盘；同一时间等待的操作可共用目录同步。
// 配置为 1 时每批只发布一笔；所有模式均在成功返回之前完成各自的持久化边界。
func (s *Spool) submitMutation(kind mutationKind, record iam.UsageRecord, raw []byte) error {
	m := &mutation{kind: kind, record: record, raw: raw, name: basename(record.RequestID)}
	m.done = make(chan struct{})
	q := &s.writes
	q.mu.Lock()
	q.waiting = append(q.waiting, m)
	if !q.running {
		q.running = true
		go s.drainMutations(s.stageFile, s.syncDirectoryLocked)
	}
	q.mu.Unlock()
	<-m.done
	return m.err
}

// drainMutations 仅在有排队写入时运行。取得目录锁后切出队首一组，逐个通知，再将锁还给恢复、关闭及准入。
// 所有调用都有自己的结果，失败快照留在目录；队列变空时在同一队列锁内退出以避免漏唤醒。
// stage 和 syncDir 分别完成每份新文件的数据同步和该批目录同步，测试可检查真实屏障次数。
func (s *Spool) drainMutations(stage func(string, []byte) error, syncDir func(string) error) {
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
		s.applyBatchLocked(batch, stage, syncDir)
		for i, m := range batch {
			if m.err != nil {
				s.invalidateProbeLocked()
			}
			close(m.done)
			batch[i] = nil
		}
		s.mu.Unlock()
	}
}

// applyBatchLocked 按队首顺序发布能够独立执行的连续操作。stage 与 syncDir 完成文件及目录同步。
// 同名依赖、容量临界或冲突只让该条回退；执行可能改变容量的回退后，再扫描并处理余下部分。
func (s *Spool) applyBatchLocked(batch []*mutation, stage func(string, []byte) error, syncDir func(string) error) {
	for len(batch) > 0 && !s.closed {
		state, err := s.statusLocked()
		if err != nil || state.Quarantined != 0 {
			break
		}
		space := groupSpace{bytes: s.config.MaxBytes - state.Bytes - state.ReservedBytes, entries: s.config.MaxEntries - state.Pending - state.Reserved}
		seen := make(map[string]bool, len(batch))
		n := 0
		for _, m := range batch {
			if seen[m.name] || !s.planMutationLocked(m, &space) {
				break
			}
			seen[m.name] = true
			n++
		}
		if n > 0 {
			s.applyGroupLocked(batch[:n], stage, syncDir)
			batch = batch[n:]
		}
		if len(batch) > 0 {
			s.applySingleLocked(batch[0], stage, syncDir)
			batch = batch[1:]
		}
	}
	for _, m := range batch {
		s.applySingleLocked(m, stage, syncDir)
	}
}

// groupSpace 记录本次扫描后剩余的字节与条数。只计入同批操作可能增加的占用，不预支其他操作释放的空间。
type groupSpace struct {
	bytes   int64
	entries int
}

// planMutationLocked 在目录锁内核对一笔操作是否能独立执行；成功时从 space 扣除保守上界并标注无需写盘的操作。
// 无法证明则返回 false，保留逐笔路径对冲突证据、容量和身份的处理；本函数不修改队列和文件系统。
func (s *Spool) planMutationLocked(m *mutation, space *groupSpace) bool {
	if m.kind == acknowledgeSnapshot {
		current, err := s.readLocked(m.name)
		m.noop = os.IsNotExist(err)
		return m.noop || err == nil && bytes.Equal(current, m.raw)
	}
	size := int64(len(m.raw))
	if size > s.config.MaxRecordBytes {
		return false
	}
	var allowance int64
	entries := 1
	if s.reserved[m.record.RequestID] {
		allowance = s.config.MaxRecordBytes
		entries = 0
	}
	if m.kind == completeMarker {
		oldSize, err := s.markerSizeLocked(m.record)
		if err != nil || size-allowance > space.bytes+oldSize {
			return false
		}
		// 替换失败可能留下旧标记、原预留及完整临时文件。后续项按这个上界校验；成功释放的预留不会借给别人。
		space.bytes -= size
		space.entries--
		return true
	}
	if m.kind == beginMarker && s.activeDelivery[m.name] {
		return false
	}
	if _, err := s.root.Lstat(m.name); !os.IsNotExist(err) {
		if m.kind == putSnapshot && err == nil {
			current, err := s.readLocked(m.name)
			m.noop = err == nil && bytes.Equal(current, m.raw)
			return m.noop
		}
		return false
	}
	if m.kind == beginMarker {
		// 新标记成功后仍要持有终态预留，合并时保守计入文件本身和新增目录项。
		allowance, entries = 0, 1
	}
	if entries > space.entries || size-allowance > space.bytes {
		return false
	}
	space.entries -= entries
	space.bytes -= max(0, size-allowance)
	return true
}

// applyGroupLocked 发布已在锁内规划的不同目标。stage 写私有文件时放开目录锁，回锁后复查同名、容量及交付所有权。
// syncDir 确认最终目录项后才答复；检查变化时交给逐笔规则处理，复用已同步的文件。调用方持有 mu，返回时仍持有。
func (s *Spool) applyGroupLocked(batch []*mutation, stage func(string, []byte) error, syncDir func(string) error) {
	if s.stageLocked(batch, stage) {
		defer func() {
			for _, m := range batch {
				delete(s.staging, m.temporary)
			}
		}()
		state, err := s.statusLocked()
		space := groupSpace{bytes: s.config.MaxBytes - state.Bytes - state.ReservedBytes, entries: s.config.MaxEntries - state.Pending - state.Reserved}
		for _, m := range batch {
			wasNoop := m.noop
			m.noop = false
			valid := err == nil && state.Quarantined == 0 && s.planMutationLocked(m, &space)
			if m.kind == acknowledgeSnapshot && !wasNoop && m.noop && s.replaying != nil && s.replaying.pending[m.name] {
				m.err = ErrUnavailable // 观察到的账单意外丢失，尚未得到本批的耐久核销凭据。
			}
			if !valid || m.noop != wasNoop {
				// 先按顺序核对各条已写证据，再决定目标；失败或冲突不能用陈旧计划覆盖真实账单。
				for _, single := range batch {
					s.applySingleLocked(single, stage, syncDir)
				}
				return
			}
		}
	}
	changed := false
	for _, m := range batch {
		if m.noop || m.err != nil {
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
		if m.noop {
			if m.kind == putSnapshot {
				delete(s.reserved, m.record.RequestID)
			}
			continue
		}
		if m.err == nil {
			m.err = barrier
		}
		// 与逐笔发布相同，最后一笔实际写文件的结果决定健康状态；确认删除不改变写盘健康度。
		if m.kind != acknowledgeSnapshot {
			s.lastErr = m.err
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
}
