package usagespool

import (
	"sync/atomic"
	"time"
)

// 仅重叠同一次正常路由准备。请求在路由、调度或 GC 中长时间停顿后，重新在准入点实写磁盘。
const attemptProbeMaxAge = 50 * time.Millisecond

// AttemptProbe 将一次已开始的磁盘探针绑定到请求及队列。它不占槽，也不冻结余额或待结水位。
// 流式入口在首次尝试时可使用一次；其他请求、重试及回退仍须取得新票。
type AttemptProbe struct {
	spool    *Spool
	callID   string
	ticket   uint64
	done     chan struct{}
	err      error
	used     atomic.Bool
	finished time.Time
}

// StartAttemptProbe 在流式请求准备出站时并行验证磁盘。callID 标识本次请求，返回供准入使用的一次性证明。
// 队列已关闭或探针正忙时返回 nil。放弃请求无需清理槽位，探针自行结束，Close 会等待磁盘访问完成。
func (s *Spool) StartAttemptProbe(callID string) *AttemptProbe {
	if callID == "" || !s.probeMu.TryLock() {
		return nil
	}
	// Close 持有 probeMu 后才修改 closed 并关闭目录句柄，读取时与它互斥。
	if s.closed {
		s.probeMu.Unlock()
		return nil
	}
	p := &AttemptProbe{spool: s, callID: callID, ticket: s.admissionTicket.Add(1), done: make(chan struct{})}
	go func() {
		p.err = s.probeAndLock(func(name string) error {
			err := s.probeFile(name)
			// 从磁盘同步结束起算，后续等待目录锁的时间也计入有效期。
			p.finished = time.Now()
			return err
		})
		s.mu.Unlock()
		close(p.done)
	}()
	return p
}

// ReserveAfterProbe 在实际出站前消费本调用的探针。参数为请求标识及可空证明，返回当前待结数或原有准入错误。
// 仅复用取票后完成的刷盘；故障代次被更新时会补验，缺失、跨队列、跨请求或已用过的证明均走常规准入。
func (s *Spool) ReserveAfterProbe(callID string, p *AttemptProbe) (int, error) {
	if p == nil || p.spool != s || p.callID != callID || !p.used.CompareAndSwap(false, true) {
		return s.ReserveForAttemptWithBacklog(callID)
	}
	<-p.done
	s.mu.Lock()
	if p.err == nil && time.Since(p.finished) > attemptProbeMaxAge {
		s.mu.Unlock()
		return s.ReserveForAttemptWithBacklog(callID)
	}
	var pending int
	err := s.reserveLocked(callID, true, s.probeFile, &pending, p.ticket, p.err)
	return pending, err
}
