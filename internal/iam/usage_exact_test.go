package iam

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"xorm.io/xorm"
)

// TestUsageExactSnapshotConfirmation 验证精确恢复接受相同快照、拒绝同ID金额/身份/正文冲突，并保留待结算原文。
// 前置私有PG，覆盖PG pending及既有账单两处证据；断言ErrConflict而非任意数据库错误，schema自动清理。
func TestUsageExactSnapshotConfirmation(t *testing.T) {
	for _, kind := range []string{"matching", "cost", "owner", "body", "pending", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			db := testDB(t)
			original := UsageRecord{RequestID: "exact-" + kind, TS: time.Now().UTC(), Model: "synthetic", Cost: 1, RequestBody: `{"input":"Synthetic input"}`}
			submitted := original
			switch kind {
			case "cost", "pending":
				submitted.Cost = 2
			case "owner":
				submitted.UserID = "another-user"
			case "body":
				submitted.RequestBody = `{"input":"Different evidence"}`
			}
			if kind == "pending" {
				raw, _ := json.Marshal(original)
				if _, err := db.Engine.Exec("INSERT INTO usage_pending(request_id,payload) VALUES (?,?::jsonb)", original.RequestID, string(raw)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.RecordUsage(t.Context(), []UsageRecord{original}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "legacy" {
				if _, err := db.Engine.Exec("UPDATE usage_events SET usage_fingerprint='' WHERE request_id=?", original.RequestID); err != nil {
					t.Fatal(err)
				}
			}
			err := db.RecordUsageExact(t.Context(), []UsageRecord{submitted})
			if kind == "matching" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrConflict) {
				t.Fatalf("不同快照未精确拒绝: %v", err)
			}
			if kind == "pending" {
				durable, err := db.RecordUsageDurably(t.Context(), submitted)
				if durable || !errors.Is(err, ErrConflict) {
					t.Fatalf("另一份PG快照冒充当前快照持久化: durable=%v err=%v", durable, err)
				}
				if countEvents(t, db, original.RequestID) != 0 || pendingCount(t, db) != 1 {
					t.Fatal("冲突改变原账单证据")
				}
			}
		})
	}
}

// TestUsageFingerprintStartupMigration 验证既有表启动补列且重复启动幂等，历史事实保持原样、未知指纹不伪造。
// 前置私有PG先建一条真实账单再删除新列模拟旧库，重跑迁移两次；全部数据由私有schema清理。
func TestUsageFingerprintStartupMigration(t *testing.T) {
	db := testDB(t)
	record := UsageRecord{RequestID: "legacy-fingerprint", TS: time.Now().UTC(), Model: "synthetic", Cost: .25}
	if err := db.RecordUsage(t.Context(), []UsageRecord{record}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec("ALTER TABLE usage_events DROP COLUMN usage_fingerprint"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var event UsageEvent
	found, err := db.Engine.Where("request_id=?", record.RequestID).Get(&event)
	if err != nil || !found || event.Cost != .25 || event.UsageFingerprint != "" {
		t.Fatalf("迁移改写历史: found=%v cost=%v fingerprint=%q err=%v", found, event.Cost, event.UsageFingerprint, err)
	}
	if err := db.RecordUsageExact(t.Context(), []UsageRecord{record}); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧无指纹账单误确认: %v", err)
	}
}

// TestLocalWriteWaitDoesNotBorrowPostgresConnection 在独占测试库仅开放一条连接，先令事务停在业务回调，
// 然后取消等待写入的请求。等待开销应留在进程内；归还写闸后下一笔必须能提交。schema 和连接由 testDB 回收。
func TestLocalWriteWaitDoesNotBorrowPostgresConnection(t *testing.T) {
	db := testDB(t)
	db.Engine.SetMaxOpenConns(1)
	entered, release := make(chan struct{}), make(chan struct{})
	complete := make(chan error, 1)
	go func() {
		complete <- db.tx(t.Context(), func(*xorm.Session) error { close(entered); <-release; return nil })
	}()
	<-entered
	pool := db.Engine.DB().DB
	before := pool.Stats().WaitCount
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	err := db.tx(ctx, func(*xorm.Session) error { return errors.New("canceled callback invoked") })
	after := pool.Stats().WaitCount
	close(release)
	if heldErr := <-complete; heldErr != nil {
		t.Fatal(heldErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) || after != before {
		t.Fatalf("等待者借用了数据库连接或忽略取消: %v; pool waits %d -> %d", err, before, after)
	}
	if err := db.RecordUsageExact(t.Context(), []UsageRecord{usageRecord("after-cancel", "", 0)}); err != nil {
		t.Fatalf("取消请求后未归还写入机会: %v", err)
	}
}

// exactWaitContext 让并发结算测试观察到请求已注册写入或加入已有快照，再精确触发取消。
type exactWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

// Done 保留原上下文的取消语义；首次取通知通道时报告已进入等待，不依赖定时睡眠。
func (c *exactWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

// TestExactUsageConcurrentCancellation 验证同一入账快照的持有者或等待者取消时仅各自停止，
// 另一个仍活动的请求能完成唯一结算；不同金额的同 ID 请求分别校验并报冲突。测试库按 schema 回收。
func TestExactUsageConcurrentCancellation(t *testing.T) {
	for _, scenario := range []string{"cancel-first", "cancel-second", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			db := testDB(t)
			// 暂占写闸让两份同 ID 的快照都先注册，再确定性触发取消或释放。
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				finished <- db.tx(t.Context(), func(*xorm.Session) error { close(entered); <-release; return nil })
			}()
			<-entered
			record := usageRecord("concurrent-"+scenario, "", 0.25)
			record.TS = time.Now().UTC()
			start := func(r UsageRecord) (context.CancelFunc, <-chan error) {
				base, cancel := context.WithCancel(t.Context())
				w := &exactWaitContext{Context: base, waiting: make(chan struct{})}
				result := make(chan error, 1)
				go func() { result <- db.RecordUsageExact(w, []UsageRecord{r}) }()
				<-w.waiting
				return cancel, result
			}
			cancelFirst, first := start(record)
			defer cancelFirst()
			if scenario == "conflict" {
				record.Cost *= 2
			}
			cancelSecond, second := start(record)
			defer cancelSecond()
			var a, b error
			switch scenario {
			case "cancel-first":
				cancelFirst()
				a = <-first
			case "cancel-second":
				cancelSecond()
				b = <-second
			}
			close(release)
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if scenario != "cancel-first" {
				a = <-first
			}
			if scenario != "cancel-second" {
				b = <-second
			}
			switch scenario {
			case "cancel-first":
				if !errors.Is(a, context.Canceled) || b != nil {
					t.Fatalf("先到请求取消后，另一个请求未完成结算: %v / %v", a, b)
				}
			case "cancel-second":
				if a != nil || !errors.Is(b, context.Canceled) {
					t.Fatalf("等待者取消影响正在进行的结算: %v / %v", a, b)
				}
			case "conflict":
				if !(a == nil && errors.Is(b, ErrConflict) || b == nil && errors.Is(a, ErrConflict)) {
					t.Fatalf("不同内容错误地共享了结算结果: %v / %v", a, b)
				}
			}
			if countEvents(t, db, record.RequestID) != 1 {
				t.Fatal("并发结算未保留唯一账单")
			}
		})
	}
}
