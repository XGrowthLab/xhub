package iam

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"xorm.io/xorm/contexts"
)

// batchPanicHook 在真实 PostgreSQL INSERT 已成功后触发一次测试 panic，保留与父测试同步的时间窗口。
type batchPanicHook struct {
	target            string
	insert, threshold int32
	entered, release  chan struct{}
}

// BeforeProcess 不修改数据库执行与返回值，使 panic 只能发生在持久事务实际执行了写入之后。
func (*batchPanicHook) BeforeProcess(c *contexts.ContextHook) (context.Context, error) {
	return c.Ctx, nil
}

// AfterProcess 在指定前台或后台批次第 threshold 条目标 INSERT 后暂停并抛出异常，便于验证回滚、等待者和后续请求。
func (h *batchPanicHook) AfterProcess(c *contexts.ContextHook) error {
	if c.Err == nil && strings.HasPrefix(strings.TrimSpace(c.SQL), "INSERT INTO usage_events") && len(c.Args) != 0 {
		id, _ := c.Args[0].(string)
		if strings.HasPrefix(id, h.target) && atomic.AddInt32(&h.insert, 1) == h.threshold {
			close(h.entered)
			<-h.release
			panic("injected SQL writer fault")
		}
	}
	return nil
}

// TestUserUsageBatchWriterPanic 验证领写者和后台 drain 的真实 SQL 回调异常均只向对应调用返回错误，后续队列正常移交。
// 先持久化每个交付的冻结账单，在未 COMMIT 时抛出异常；检查事务回滚、未确认原文、独立新写和重新入账，私有 schema 自动回收。
func TestUserUsageBatchWriterPanic(t *testing.T) {
	for _, foreground := range []bool{true, false} {
		t.Run(fmt.Sprintf("foreground_%t", foreground), func(t *testing.T) {
			db := testDB(t)
			user, err := db.CreateUser(t.Context(), Actor{}, UserInput{Email: "panic-test@example.com", Password: "password123"})
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"front", "batch-a", "batch-b", "tail"} {
				r := usageRecord(id, user.ID, .25)
				if err := db.PrepareDelivery(t.Context(), r, time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				if err := db.ConfirmDelivery(t.Context(), r); err != nil {
					t.Fatal(err)
				}
			}
			h := &batchPanicHook{target: "batch-", threshold: 2, entered: make(chan struct{}), release: make(chan struct{})}
			if foreground {
				h.target, h.threshold = "front", 1
			}
			db.Engine.AddHook(h)
			release := holdUsageWriter(t, db)
			run := func(id string) <-chan error {
				result := make(chan error, 1)
				go func() { result <- db.FlushPendingUsageRequest(t.Context(), id) }()
				return result
			}
			head := run("front")
			waitUserQueue(t, db, user.ID, 0)
			a, b := run("batch-a"), run("batch-b")
			waitUserQueue(t, db, user.ID, 2)
			release()
			select {
			case <-h.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("未进入真实 INSERT 后的异常注入点")
			}
			next := run("tail")
			waiting := 1
			if foreground {
				waiting = 3
			}
			waitUserQueue(t, db, user.ID, waiting)
			close(h.release)
			for _, call := range []struct {
				id     string
				result <-chan error
				failed bool
			}{
				{"front", head, foreground}, {"batch-a", a, !foreground}, {"batch-b", b, !foreground}, {"tail", next, false},
			} {
				select {
				case err := <-call.result:
					if (err != nil) != call.failed || (call.failed && !errors.Is(err, errUserUsageWriterPanic)) {
						t.Fatalf("调用 %s 收到错误结果: %v", call.id, err)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("调用 %s 在 panic 后未被唤醒", call.id)
				}
				n := countEvents(t, db, call.id)
				if (call.failed && n != 0) || (!call.failed && n != 1) {
					t.Fatalf("调用 %s 未按事务回滚或提交: 账单=%d", call.id, n)
				}
			}
			waitUserBatchIdle(t, db, user.ID)
			failed := 2
			if foreground {
				failed = 1
			}
			if pendingCount(t, db) != int64(failed) || userSpend(t, db, user.ID) != float64(4-failed)*.25 {
				t.Fatal("异常后原始待办丢失或部分事务进行了扣账")
			}
			for range 2 {
				if err := db.FlushPendingUsage(t.Context(), 8); err != nil {
					t.Fatal(err)
				}
			}
			if pendingCount(t, db) != 0 || userSpend(t, db, user.ID) != 1 {
				t.Fatal("异常后的持久快照无法完整重放或发生重复扣费")
			}
		})
	}
}

// TestUserUsageBatchDataErrors 断言可拆分的单条数据错误与整库故障的边界，避免连接/取消/未知提交触发逐笔事务放大。
// 纯 Go 输入覆盖行约束、预算冲突和共享存储错误，不需要外部资源。
func TestUserUsageBatchDataErrors(t *testing.T) {
	for _, test := range []struct {
		err   error
		split bool
	}{
		{ErrConflict, true}, {ErrBudgetUnavailable, true}, {ErrInvalid, true},
		{&pgconn.PgError{Code: "22003"}, true}, {&pgconn.PgError{Code: "23514"}, true}, {&pgconn.PgError{Code: "P0001"}, true},
		{&pgconn.PgError{Code: "08006"}, false}, {&pgconn.PgError{Code: "40001"}, false}, {context.Canceled, false}, {errUserUsageWriterPanic, false},
	} {
		if got := userUsageDataError(test.err); got != test.split {
			t.Errorf("错误类型 %T：拆分=%t，期望=%t", test.err, got, test.split)
		}
	}
}

// panickingBatchContext 模拟事务外的异常来源，验证整个排空协程也有兜底；其余上下文行为由嵌入的父上下文提供。
type panickingBatchContext struct{ context.Context }

// Err 在本测试的队列筛选阶段抛出异常，绝不覆盖实际业务调用的上下文。
func (panickingBatchContext) Err() error { panic("injected worker fault") }

// TestUserUsageBatchWorkerPanic 唤醒已经取消及尚未收到回复的所有参与者，并继续排空后续的真实入账请求。
// 使用一个在筛选阶段抛出异常的上下文，确保此前已发出的取消结果不被覆盖；测试回收队列与测试库。
func TestUserUsageBatchWorkerPanic(t *testing.T) {
	db := testDB(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	done := [4]chan error{}
	for i := range done {
		done[i] = make(chan error, 1)
	}
	const user = "worker-panic-user"
	q := &userUsageQueue{pending: []userUsageWrite{{ctx: t.Context(), record: usageRecord("later", user, .125), done: done[3]}}}
	db.userWrites.queues = map[string]*userUsageQueue{user: q}
	first := []userUsageWrite{{ctx: canceled, done: done[0]}, {ctx: panickingBatchContext{t.Context()}, done: done[1]}, {ctx: t.Context(), done: done[2]}}
	go db.drainUserUsage(user, q, first)
	for i, want := range []error{context.Canceled, errUserUsageWriterPanic, errUserUsageWriterPanic, nil} {
		select {
		case err := <-done[i]:
			if !errors.Is(err, want) {
				t.Fatalf("等待项 %d 收到 %v，期望 %v", i, err, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("等待项 %d 未收到结果", i)
		}
	}
	waitUserBatchIdle(t, db, user)
	if countEvents(t, db, "later") != 1 {
		t.Fatal("后续队列在后台异常后未能单独提交")
	}
}

// TestUserUsageBatchSplitsOnlyDataFaults 验证八笔的真实批次遭遇一条约束拒绝时，其他七笔仍按子批提交；
// 共享连接类故障则不重复开七笔事务。每轮用触发器检测已尝试的 INSERT，验证精确结果后销毁私有 schema。
func TestUserUsageBatchSplitsOnlyDataFaults(t *testing.T) {
	for _, state := range []string{"23514", "08006"} {
		t.Run(state, func(t *testing.T) {
			db := testDB(t)
			if _, err := db.Engine.Exec(fmt.Sprintf(`CREATE SEQUENCE review_attempts;
				CREATE FUNCTION fail_review_bill() RETURNS TRIGGER LANGUAGE plpgsql AS $$
				BEGIN PERFORM nextval('review_attempts'); IF NEW.request_id='item-0' THEN RAISE EXCEPTION USING ERRCODE='%s',MESSAGE='injected'; END IF; RETURN NEW; END $$;
				CREATE TRIGGER check_review_bill BEFORE INSERT ON usage_events FOR EACH ROW EXECUTE FUNCTION fail_review_bill()`, state)); err != nil {
				t.Fatal(err)
			}
			var jobs []userUsageWrite
			var completions []chan error
			for i := range 8 {
				ch := make(chan error, 1)
				jobs = append(jobs, userUsageWrite{ctx: t.Context(), record: usageRecord(fmt.Sprintf("item-%d", i), "fault-owner", .125), done: ch})
				completions = append(completions, ch)
			}
			db.writeUserUsageBatch(jobs)
			for i, ch := range completions {
				if err := <-ch; (err != nil) != (i == 0 || state == "08006") {
					t.Fatalf("记录 %d 错误隔离错误: %v", i, err)
				}
			}
			rows, err := db.Engine.QueryString("SELECT count(*) AS rows,count(DISTINCT xmin::text) AS txs,(SELECT last_value FROM review_attempts) AS attempts FROM usage_events")
			if err != nil || len(rows) != 1 {
				t.Fatalf("未能读取实际事务数: %v %v", rows, err)
			}
			if state == "08006" && (rows[0]["rows"] != "0" || rows[0]["attempts"] != "1") {
				t.Fatalf("共享存储故障仍有部分账单或放大重试: %v", rows)
			}
			if state == "23514" && (rows[0]["rows"] != "7" || rows[0]["txs"] != "3") {
				t.Fatalf("一条坏单阻塞了七条好单或导致全部逐笔重放: %v", rows)
			}
		})
	}
}
