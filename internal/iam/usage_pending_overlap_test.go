package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/cmd/regression/testsupport"
	"xorm.io/xorm/contexts"
)

// pendingSQLHook 只观察测试库真实 SQL，必要时在账本提交后、待办 DELETE 前提供同步点；不替换数据库结果。
type pendingSQLHook struct {
	count            atomic.Int64
	hold             atomic.Bool
	panicOnRelease   bool
	entered, release chan struct{}
}

// BeforeProcess 记录操作数，定向测试仅暂停一次真实 DELETE，异常分支模拟领写者在确认前退出；返回原请求上下文，超时与取消语义保持原样。
func (h *pendingSQLHook) BeforeProcess(c *contexts.ContextHook) (context.Context, error) {
	h.count.Add(1)
	if strings.HasPrefix(strings.TrimSpace(c.SQL), "DELETE FROM usage_pending WHERE request_id") && h.hold.CompareAndSwap(true, false) {
		close(h.entered)
		select {
		case <-h.release:
			if h.panicOnRelease {
				panic("fixture interrupted acknowledgement")
			}
		case <-c.Ctx.Done():
			return c.Ctx, c.Ctx.Err()
		}
	}
	return c.Ctx, nil
}

// AfterProcess 对测试用真实 SQL 不做改写或额外处理，原始执行错误由驱动传播。
func (*pendingSQLHook) AfterProcess(*contexts.ContextHook) error { return nil }

// observedPendingWait 在跟随者开始等候现有结算时通知测试，不改取消、截止时间或上下文值。
type observedPendingWait struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

// Done 保留原 ctx 的取消通道，仅在当前等待首次读取它时通知测试侧释放 PostgreSQL 操作。
func (c *observedPendingWait) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

// TestConfirmedDeliveryFreezesFullSnapshot 验证同步交付复用数据库已接受的快照，后续修改输入和外部删除不能导致漏扣或错误确认。
// 写入私有 PG 标记后修改原输入，再分别验证丢失待办时恢复、同 ID 冲突及零凭据；测试关闭连接并销毁 schema。
func TestConfirmedDeliveryFreezesFullSnapshot(t *testing.T) {
	db := testDB(t)
	if err := (ConfirmedDelivery{}).Settle(t.Context()); err == nil {
		t.Fatal("零凭据不能结算")
	}
	for _, conflicting := range []bool{false, true} {
		r := usageRecord(fmt.Sprintf("frozen-%t", conflicting), "frozen-owner", .125)
		cache := 3
		r.CachedTokens = &cache
		if err := db.PrepareDelivery(t.Context(), r, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		confirmed, err := db.ConfirmDeliveryForSettlement(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Engine.Exec("DELETE FROM usage_pending WHERE request_id=?", r.RequestID); err != nil {
			t.Fatal(err)
		}
		cache = 99
		if conflicting {
			if err = db.RecordUsageExact(t.Context(), []UsageRecord{r}); err != nil {
				t.Fatal(err)
			}
			if err = confirmed.Settle(t.Context()); !errors.Is(err, ErrConflict) {
				t.Fatalf("同 ID 不同缓存用量被确认为原快照: %v", err)
			}
		} else {
			if err = confirmed.Settle(t.Context()); err != nil {
				t.Fatal(err)
			}
			e, err := db.GetUsageEvent(t.Context(), UsageQuery{}, r.RequestID)
			if err != nil || e.CachedTokens == nil || *e.CachedTokens != 3 || e.Cost != .125 {
				t.Fatalf("外部删除后未恢复冻结的用量费用: %+v %v", e, err)
			}
		}
	}
}

// TestPendingObserversRespectObservationOrder 验证先观察的相同快照共用一次确认，晚观察和延后使用的凭据都必须重新核对新预占。
// 前台完成真实 PG 账本提交后阻塞删除；这时插入冲突预占并让另一调用观察旧 pending。确认后仍保留冲突占用，schema 自动清理。
func TestPendingObserversRespectObservationOrder(t *testing.T) {
	for _, timing := range []string{"early", "late-read", "late-receipt"} {
		t.Run(timing, func(t *testing.T) {
			db := testDB(t)
			r := usageRecord("shared-observation", "obs-owner", .125)
			if err := db.PrepareDelivery(t.Context(), r, time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			confirmed, err := db.ConfirmDeliveryForSettlement(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			read := func() pendingUsage {
				var p pendingUsage
				found, err := db.Engine.Table("usage_pending").Where("request_id=?", r.RequestID).Get(&p)
				if err != nil || !found {
					t.Fatalf("未读到目标持久快照: %v", err)
				}
				p.observedAt = time.Now()
				return p
			}
			observer := read()
			if _, err := db.Engine.Exec(`CREATE SEQUENCE overlap_deletes;
				CREATE FUNCTION count_overlap_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('overlap_deletes'); RETURN NULL; END $$;
				CREATE TRIGGER count_overlap AFTER DELETE ON usage_pending FOR EACH STATEMENT EXECUTE FUNCTION count_overlap_delete()`); err != nil {
				t.Fatal(err)
			}
			hook := &pendingSQLHook{entered: make(chan struct{}), release: make(chan struct{})}
			hook.hold.Store(true)
			db.Engine.AddHook(hook)
			release := sync.OnceFunc(func() { close(hook.release) })
			defer release()
			leader := make(chan error, 1)
			go func() { leader <- confirmed.Settle(t.Context()) }()
			select {
			case <-hook.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("未观察到账本提交后的确认删除")
			}
			late := timing != "early"
			if late {
				if _, err := db.Engine.Exec("INSERT INTO budget_reservations(request_id,kind,node_id,user_id,amount) VALUES(?,'user','obs-owner','obs-owner',1)", r.RequestID); err != nil {
					t.Fatal(err)
				}
				if timing == "late-read" {
					observer = read()
				}
			}
			ctx := &observedPendingWait{Context: t.Context(), entered: make(chan struct{})}
			follower := make(chan error, 1)
			go func() {
				// 凭据早于领写者创建，但前台确实在冲突预占建立后才开始调用 Settle。
				if timing == "late-receipt" {
					follower <- confirmed.Settle(ctx)
					return
				}
				follower <- db.settlePendingUsage(ctx, observer)
			}()
			select {
			case <-ctx.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("并发观察未进入在途确认")
			}
			release()
			if err := <-leader; err != nil {
				t.Fatal(err)
			}
			err = <-follower
			if late && !errors.Is(err, ErrConflict) || !late && err != nil {
				t.Fatalf("晚观察=%t 返回错误结果: %v", late, err)
			}
			rows, err := db.Engine.QueryString("SELECT last_value FROM overlap_deletes")
			if err != nil || len(rows) != 1 || rows[0]["last_value"] != "1" {
				t.Fatalf("同一观察出现重复删除: %v %v", rows, err)
			}
			holds, err := db.Engine.ID(r.RequestID).Exist(new(BudgetReservation))
			if err != nil || holds != late {
				t.Fatalf("原始预占未保留: %t %v", holds, err)
			}
		})
	}
}

// TestPendingMixedAdmission 强制并发准入对混合快照逐一给出结果；缺账本的外部删除须恢复，损坏记录返回原错误。
// 真实 PG 建立六笔已结清、一个已删除但未入账、一个损坏的观察；测试结束销毁 schema。
func TestPendingMixedAdmission(t *testing.T) {
	db := testDB(t)
	var observed []pendingUsage
	for i := 0; i < 7; i++ {
		r := usageRecord(fmt.Sprintf("observed-%d", i), "mixed-owner", .125)
		raw, _ := json.Marshal(r)
		observed = append(observed, pendingUsage{RequestID: r.RequestID, Payload: string(raw)})
		if i < 6 {
			if err := db.RecordUsageExact(t.Context(), []UsageRecord{r}); err != nil {
				t.Fatal(err)
			}
		}
	}
	observed = append(observed, pendingUsage{RequestID: "invalid-observed", Payload: "{"})
	db.pendingSweeps.Add(1)
	defer db.pendingSweeps.Add(-1)
	err := db.flushObservedPending(t.Context(), observed, time.Now())
	if err == nil || !strings.Contains(err.Error(), "invalid-observed") || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("准入未返回原记录错误: %v", err)
	}
	if countEvents(t, db, "observed-6") != 1 || countEvents(t, db, "invalid-observed") != 0 {
		t.Fatal("准入丢掉外部删除的原账单或为坏记录造账")
	}
}

// BenchmarkPendingDirectedDelivery 在独立 PostgreSQL schema 对比交付事务提交后的旧按 ID 重读与复用冻结快照。
// 每轮使用唯一 ID、实际 PG 交付及严格扣款，记录不含准备阶段的真实 SQL 数；旧路径仍可通过公开兼容 API 复验，测试清理 schema。
func BenchmarkPendingDirectedDelivery(b *testing.B) {
	for _, reuse := range []bool{false, true} {
		b.Run(fmt.Sprintf("reuse_%t", reuse), func(b *testing.B) {
			db, err := Open(b.Context(), testsupport.Postgres(b, "pending_direct"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = db.Close() })
			finals := make([]ConfirmedDelivery, b.N)
			for i := range finals {
				r := usageRecord(fmt.Sprintf("direct-%d", i), "bench", .001)
				if err := db.PrepareDelivery(b.Context(), r, time.Now().Add(time.Hour)); err != nil {
					b.Fatal(err)
				}
				d, err := db.ConfirmDeliveryForSettlement(b.Context(), r)
				if err != nil {
					b.Fatal(err)
				}
				finals[i] = d
			}
			hook := &pendingSQLHook{}
			db.Engine.AddHook(hook)
			var n atomic.Int64
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					i := int(n.Add(1)) - 1
					var err error
					if reuse {
						err = finals[i].Settle(b.Context())
					} else {
						err = db.FlushPendingUsageRequest(b.Context(), finals[i].saved.RequestID)
					}
					if err != nil {
						b.Error(err)
						return
					}
				}
			})
			b.StopTimer()
			calls := hook.count.Load()
			count, err := db.Engine.Table("usage_events").Count()
			if err != nil || count != int64(b.N) {
				b.Fatalf("唯一本数错误: %d %v", count, err)
			}
			b.ReportMetric(float64(calls)/float64(b.N), "sql/record")
		})
	}
}

// TestPendingLeaderPanicCannotAcknowledge 在私有 PG 将真实账本提交后，模拟领写者在删除待办前异常退出。
// 先观察者必须得到错误，待办可再次核对并完成，最终只有一笔账；夹具释放等待协程、连接和 schema。
func TestPendingLeaderPanicCannotAcknowledge(t *testing.T) {
	db := testDB(t)
	r := usageRecord("interrupted-pending", "interrupted-owner", .125)
	if err := db.PrepareDelivery(t.Context(), r, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	receipt, err := db.ConfirmDeliveryForSettlement(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	observer := receipt.saved
	hook := &pendingSQLHook{entered: make(chan struct{}), release: make(chan struct{}), panicOnRelease: true}
	hook.hold.Store(true)
	db.Engine.AddHook(hook)
	release := sync.OnceFunc(func() { close(hook.release) })
	defer release()
	panicked := make(chan any, 1)
	go func() { defer func() { panicked <- recover() }(); _ = receipt.Settle(t.Context()) }()
	select {
	case <-hook.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("账本未到达确认前的故障注入点")
	}
	waitCtx := &observedPendingWait{Context: t.Context(), entered: make(chan struct{})}
	follow := make(chan error, 1)
	go func() { follow <- db.settlePendingUsage(waitCtx, observer) }()
	select {
	case <-waitCtx.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("跟随者没有等待领写结算")
	}
	release()
	if got := <-panicked; got != "fixture interrupted acknowledgement" {
		t.Fatalf("未收到预期的领写异常: %v", got)
	}
	if err := <-follow; !errors.Is(err, errPendingSettlementInterrupted) {
		t.Fatalf("领写异常却让跟随者确认，得到 %v", err)
	}
	found, err := db.Engine.Table("usage_pending").Where("request_id=?", r.RequestID).Exist()
	if err != nil || !found {
		t.Fatalf("领写失败丢失待办: found=%t err=%v", found, err)
	}
	if err := receipt.Settle(t.Context()); err != nil {
		t.Fatalf("健康后继未能严格重放: %v", err)
	}
	rows, err := db.Engine.QueryString("SELECT (SELECT COUNT(*) FROM usage_events WHERE request_id=?) AS ledger, (SELECT COUNT(*) FROM usage_pending WHERE request_id=?) AS pending", r.RequestID, r.RequestID)
	if err != nil || len(rows) != 1 || rows[0]["ledger"] != "1" || rows[0]["pending"] != "0" {
		t.Fatalf("恢复后金额事实重复或待办未清: %v/%v", rows, err)
	}
}

// TestPendingRetryIsShared 在第一笔账本已提交、删除待办前让十二个独立观察者排队。
// 用真实 PostgreSQL 触发器制造一次或持续故障，核对整群至多两次尝试、单独取消和最终唯一账本；夹具清理 schema。
func TestPendingRetryIsShared(t *testing.T) {
	for _, mode := range []string{"transient", "persistent", "leader-canceled"} {
		t.Run(mode, func(t *testing.T) {
			db := testDB(t)
			r := usageRecord("shared-store-retry", "retry-owner", .125)
			if err := db.PrepareDelivery(t.Context(), r, time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			receipt, err := db.ConfirmDeliveryForSettlement(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			maxFailures := map[string]int{"transient": 1, "persistent": 100, "leader-canceled": 0}[mode]
			if _, err := db.Engine.Exec(fmt.Sprintf(`CREATE SEQUENCE ack_attempt;
				CREATE FUNCTION fail_ack() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
				IF nextval('ack_attempt') <= %d THEN RAISE EXCEPTION 'fixture retryable storage failure'; END IF;
				RETURN OLD; END $$;
				CREATE TRIGGER fail_ack BEFORE DELETE ON usage_pending FOR EACH ROW EXECUTE FUNCTION fail_ack()`, maxFailures)); err != nil {
				t.Fatal(err)
			}
			h := &pendingSQLHook{entered: make(chan struct{}), release: make(chan struct{})}
			h.hold.Store(true)
			db.Engine.AddHook(h)
			release := sync.OnceFunc(func() { close(h.release) })
			defer release()
			leadCtx, cancelLead := context.WithCancel(t.Context())
			defer cancelLead()
			leader := make(chan error, 1)
			go func() { leader <- receipt.Settle(leadCtx) }()
			select {
			case <-h.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("领写未到达实际确认阶段")
			}
			const observers = 12
			results := make([]chan error, observers)
			var cancelFirst context.CancelFunc
			for i := range results {
				ctx := t.Context()
				if i == 0 {
					ctx, cancelFirst = context.WithCancel(ctx)
					defer cancelFirst()
				}
				wait := &observedPendingWait{Context: ctx, entered: make(chan struct{})}
				results[i] = make(chan error, 1)
				go func() { results[i] <- db.settlePendingUsage(wait, receipt.saved) }()
				select {
				case <-wait.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("等待者未加入当前写入")
				}
			}
			cancelFirst()
			if err := <-results[0]; !errors.Is(err, context.Canceled) {
				t.Fatalf("单一等待者取消收到 %v", err)
			}
			if mode == "leader-canceled" {
				cancelLead()
			}
			release()
			if err := <-leader; err == nil || mode == "leader-canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("首笔未报告注入故障：%v", err)
			}
			for _, result := range results[1:] {
				err := <-result
				if (err != nil) != (mode == "persistent") {
					t.Fatalf("活跃等待者收到错误结果：%v", err)
				}
			}
			wantAttempts := 2
			if mode == "leader-canceled" {
				wantAttempts = 1
			}
			rows, err := db.Engine.QueryString(`SELECT (SELECT last_value FROM ack_attempt) AS attempts,
				(SELECT count(*) FROM usage_events WHERE request_id=?) AS billed,(SELECT count(*) FROM usage_pending WHERE request_id=?) AS pending`, r.RequestID, r.RequestID)
			wantPending := "0"
			if mode == "persistent" {
				wantPending = "1"
			}
			if err != nil || len(rows) != 1 || rows[0]["attempts"] != fmt.Sprint(wantAttempts) || rows[0]["pending"] != wantPending || rows[0]["billed"] != "1" {
				t.Fatalf("重试风暴、错误确认或账单不唯一：%v/%v", rows, err)
			}
		})
	}
}
