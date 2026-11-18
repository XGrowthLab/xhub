package iam

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
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
	if record.UserID == "" || validatePendingUsage(record) != nil {
		return db.runUserUsageTransaction(ctx, []UsageRecord{record})
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
			defer func() {
				if next := b.take(record.UserID, q); len(next) > 0 {
					go db.drainUserUsage(record.UserID, q, next)
				}
			}()
			return db.runUserUsageTransaction(ctx, []UsageRecord{record})
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

var errUserUsageWriterPanic = errors.New("usage settlement interrupted by internal panic")

// runUserUsageTransaction 在当前调用方或短命排空协程中执行一次严格入账；异常时由 db.tx 先回滚并释放锁。
// 真实提交才返回 nil，panic 转为可重放的结算错误，避免驱动异常退出整个网关，错误不携带账单正文或凭据。
func (db *DB) runUserUsageTransaction(ctx context.Context, records []UsageRecord) (err error) {
	defer func() {
		if recover() != nil {
			err = errUserUsageWriterPanic
		}
	}()
	return db.recordUsage(ctx, records, true)
}

// runActiveUserUsage 将一批仍存活调用放入新开的串行事务，并保留每个调用的撤回边界。
// 返回 SQL 结果并解除合批对所有原始上下文的订阅，避免失败或 panic 后继续持有已处理请求。
func (db *DB) runActiveUserUsage(active []*userUsageWrite) error {
	ctx, cancel := context.WithCancel(active[0].ctx)
	defer cancel()
	records := make([]UsageRecord, len(active))
	for i, job := range active {
		records[i] = job.record
		if i > 0 {
			stop := context.AfterFunc(job.ctx, cancel)
			defer stop()
		}
	}
	return db.runUserUsageTransaction(ctx, records)
}

// userUsageDataError 限定可通过拆分记录恢复的业务错误和 PostgreSQL 行级数据、约束或触发器错误。
// 全局存储故障和提交结果不确定时直接返回各调用方，保留持久快照等待真实恢复，避免同一故障导致重复写入。
func userUsageDataError(err error) bool {
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrBudgetUnavailable) {
		return true
	}
	var syntax *json.SyntaxError
	var fields *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &fields) {
		return true
	}
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (strings.HasPrefix(pg.Code, "22") || strings.HasPrefix(pg.Code, "23") || pg.Code == "P0001")
}

// reply 把单笔的最终结果放进独立的单槽通道，并把 worker 自己这份引用置空，标识已经答复。
// 仅排空协程使用；调用者持有入队前复制的通道，即使其已取消也不会阻塞通知。
func (job *userUsageWrite) reply(err error) {
	job.done <- err
	job.done = nil
}

// writeUserUsageBatch 为本批所有等待项持有独立的回复记录；任何未预期的异常均只会结束本批，不会终止网关或后续队列。
// 参数 jobs 归此排空协程独占。已经完成的子批保留真实结果，尚未答复者统一得到可重放的结算错误。
func (db *DB) writeUserUsageBatch(jobs []userUsageWrite) {
	defer func() {
		if recover() != nil {
			for i := range jobs {
				if jobs[i].done != nil {
					jobs[i].reply(errUserUsageWriterPanic)
				}
			}
		}
	}()
	active := make([]*userUsageWrite, len(jobs))
	for i := range jobs {
		active[i] = &jobs[i]
	}
	db.settleUserUsageJobs(active)
}

// settleUserUsageJobs 每次新开一个串行事务合并结算，回复每个调用的真实结果。
// 取消会回滚正在执行的批次，仍存活的调用重新合批。数据错误按原顺序二分隔离；存储故障直接返回，原始待办保留。
func (db *DB) settleUserUsageJobs(jobs []*userUsageWrite) {
	for len(jobs) > 0 {
		active := jobs[:0]
		for _, job := range jobs {
			if err := job.ctx.Err(); err != nil {
				job.reply(err)
			} else {
				active = append(active, job)
			}
		}
		if len(active) == 0 {
			return
		}
		if len(active) == 1 {
			active[0].reply(db.runUserUsageTransaction(active[0].ctx, []UsageRecord{active[0].record}))
			return
		}
		err := db.runActiveUserUsage(active)
		if err != nil {
			// PG 也可能将取消报告为 57014 或 driver.ErrBadConn；以实际参与者的状态识别。
			retry := false
			for _, job := range active {
				if job.ctx.Err() != nil {
					retry = true
					break
				}
			}
			if retry {
				jobs = active
				continue
			}
			if userUsageDataError(err) {
				mid := len(active) / 2
				db.settleUserUsageJobs(active[:mid])
				db.settleUserUsageJobs(active[mid:])
				return
			}
		}
		for _, job := range active {
			job.reply(err)
		}
		return
	}
}
