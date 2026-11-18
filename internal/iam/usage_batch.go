package iam

import (
	"context"
	"sync"
)

// 每个用户最多有一批在事务中、一批排队；更多请求在调用栈等待空位，仍响应各自的取消。
// 第一笔立即开始，不为凑批增加定时器或引入新的持久化状态。
const maxUserUsageBatch = 16

type userUsageWrite struct {
	ctx    context.Context
	record UsageRecord
	done   chan error
}

type userUsageQueue struct {
	pending []userUsageWrite
	space   chan struct{}
}

type userUsageBatches struct {
	mu     sync.Mutex
	queues map[string]*userUsageQueue
}

// recordUserUsage 供单笔严格入账使用，按冻结记录的用户合并并发写入，上下文取消或提交后返回。
// 无用户记录沿用独立事务；内存队列只调度已有写入，绝不作为提交证据。锁和队列随请求排空而释放。
func (db *DB) recordUserUsage(ctx context.Context, record UsageRecord) error {
	if record.UserID == "" {
		return db.recordUsage(ctx, []UsageRecord{record}, true)
	}
	b := &db.userWrites
	for ctx.Err() == nil {
		b.mu.Lock()
		q := b.queues[record.UserID]
		if q == nil {
			if b.queues == nil {
				b.queues = make(map[string]*userUsageQueue)
			}
			q = &userUsageQueue{}
			b.queues[record.UserID] = q
			// 无争用时在当前调用立即入账。只有后来者已经排队才派一个短命工作协程。
			b.mu.Unlock()
			err := db.recordUsage(ctx, []UsageRecord{record}, true)
			if next := b.take(record.UserID, q); len(next) > 0 {
				go db.drainUserUsage(record.UserID, q, next)
			}
			return err
		}
		if len(q.pending) < maxUserUsageBatch {
			job := userUsageWrite{ctx: ctx, record: record, done: make(chan error, 1)}
			q.pending = append(q.pending, job)
			b.mu.Unlock()
			select {
			case err := <-job.done:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if q.space == nil {
			q.space = make(chan struct{})
		}
		space := q.space
		b.mu.Unlock()
		select {
		case <-space:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// take 供唯一领写者取走当前全部待办并通知等候空位的调用者；无待办时删除该用户队列。
// 参数 user、q 均由 recordUserUsage 建立；返回最多 maxUserUsageBatch 项，互斥范围只含内存操作。
func (b *userUsageBatches) take(user string, q *userUsageQueue) []userUsageWrite {
	b.mu.Lock()
	defer b.mu.Unlock()
	next := q.pending
	q.pending = nil
	if q.space != nil {
		close(q.space)
		q.space = nil
	}
	if len(next) == 0 {
		delete(b.queues, user)
	}
	return next
}

// drainUserUsage 串行处理一个活跃用户的后续短批次，排空后退出；事务仅在处理批次时取得连接。
// first 是上笔写入期间攒下的请求，通过各自的单槽结果通道返回，已取消的调用者不会卡住排队者。
func (db *DB) drainUserUsage(user string, q *userUsageQueue, first []userUsageWrite) {
	for jobs := first; len(jobs) > 0; jobs = db.userWrites.take(user, q) {
		db.writeUserUsageBatch(jobs)
	}
}

// writeUserUsageBatch 在一个已有的串行账单事务内严格核对每笔快照、释放其预占、更新五层消费。
// 任一参与者取消会终止该次合批；如整批失败，按各自上下文独立重试，避免坏单或取消者牵连有效账单。
// 仅发送各笔提交或失败的真实结果；提交结果不确定时仍由原指纹和事务保证幂等。
func (db *DB) writeUserUsageBatch(jobs []userUsageWrite) {
	active := jobs[:0]
	for _, job := range jobs {
		if err := job.ctx.Err(); err != nil {
			job.done <- err
		} else {
			active = append(active, job)
		}
	}
	if len(active) == 0 {
		return
	}
	if len(active) > 1 {
		ctx, cancel := context.WithCancel(active[0].ctx)
		stops := make([]func() bool, 0, len(active)-1)
		records := make([]UsageRecord, 0, len(active))
		for i, job := range active {
			records = append(records, job.record)
			if i > 0 {
				stops = append(stops, context.AfterFunc(job.ctx, cancel))
			}
		}
		err := db.recordUsage(ctx, records, true)
		for _, stop := range stops {
			stop()
		}
		cancel()
		if err == nil {
			for _, job := range active {
				job.done <- nil
			}
			return
		}
	}
	for _, job := range active {
		err := job.ctx.Err()
		if err == nil {
			err = db.recordUsage(job.ctx, []UsageRecord{job.record}, true)
		}
		job.done <- err
	}
}
