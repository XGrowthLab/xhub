package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/cmd/regression/testsupport"
	"xorm.io/xorm"
)

// TestStalePendingReceiptsGuards 制造同 ID 不同快照、外部删除、原预占、已核销和同内容重新入队。
// 真实事务只能认证唯一无歧义快照。其他项交回原结算器；测试只用独立 schema，退出自动删除。
func TestStalePendingReceiptsGuards(t *testing.T) {
	db := testDB(t)
	var rows []pendingUsage
	appendRecord := func(r UsageRecord) {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, pendingUsage{RequestID: r.RequestID, Payload: string(raw)})
	}
	for _, id := range []string{"verified", "externally-deleted", "different-ledger", "still-held", "manual-call", "manual-hold", "same-pending", "legacy"} {
		r := usageRecord(id, "user", .25)
		if id == "manual-hold" {
			r.BudgetReservationID = "attempt-other"
		}
		if id != "externally-deleted" {
			if err := db.RecordUsageExact(t.Context(), []UsageRecord{r}); err != nil {
				t.Fatal(err)
			}
		}
		if id == "different-ledger" {
			r.RequestBody = "changed"
		}
		appendRecord(r)
		if id == "verified" {
			r.ResponseBody = "same id but distinct observed snapshot"
			appendRecord(r)
		}
		if id == "same-pending" {
			if _, err := db.Engine.Exec("INSERT INTO usage_pending(request_id,payload) VALUES(?,?::jsonb)", id, rows[len(rows)-1].Payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Engine.Exec(`INSERT INTO budget_reservations(request_id,kind,node_id,user_id,amount) VALUES('still-held','user','user','user',1);
	INSERT INTO budget_reconciliations(reservation_id,call_id,payload) VALUES('other-attempt','manual-call','{}'),('attempt-other','different-call','{}');
	UPDATE usage_events SET usage_fingerprint='' WHERE request_id='legacy'`); err != nil {
		t.Fatal(err)
	}
	verified, err := verifiedPendingReceipts(t.Context(), db, rows)
	if err != nil || len(verified) != 1 || !verified[0] {
		t.Fatalf("有冲突/缺证据项错误通过批量认证: %v %v", verified, err)
	}
}

// staleObservationFixture 用实际创建的用户和冻结的数据库读结果重放并发观察，费用始终为每笔 0.125 美元。
type staleObservationFixture struct {
	db           *DB
	userID       string
	observations [][]pendingUsage
	observedAt   time.Time
}

// newStaleObservationFixture 在私有 PG 中创建真实用户和多笔持久待办，等每位调用者都读完后才首次结清。
// identities 是唯一账单数，waiters 是分别读取 PG 的调用者数；账本和连接在测试或基准结束时一并回收。
func newStaleObservationFixture(t testing.TB, identities, waiters int) staleObservationFixture {
	t.Helper()
	db, err := Open(t.Context(), testsupport.Postgres(t, "stale_receipts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user, err := db.CreateUser(t.Context(), Actor{}, UserInput{Email: "study-user@example.com", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range identities {
		r := usageRecord(fmt.Sprintf("stale-%d", i), user.ID, .125)
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Engine.Exec("INSERT INTO usage_pending(request_id,payload) VALUES(?,?::jsonb)", r.RequestID, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	f := staleObservationFixture{db: db, userID: user.ID, observations: make([][]pendingUsage, waiters)}
	for i := range f.observations {
		err := db.Engine.Table("usage_pending").Asc("created_at", "request_id").Limit(100).Find(&f.observations[i])
		if err != nil || len(f.observations[i]) != identities {
			t.Fatalf("调用者 %d 实际观察到 %d 条待办，期望 %d: %v", i, len(f.observations[i]), identities, err)
		}
	}
	f.observedAt = time.Now()
	if err := db.FlushPendingUsage(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	f.assertSettled(t)
	return f
}

// assertSettled 对照 PG 精确 NUMERIC 费用、真实用户消费、事件总数和原始待办；在首次结算和重复重放后均验证。
// 使用同一份期望值可发现重复扣费、漏记、记录落给其他用户或确认后仍留待办。
func (f staleObservationFixture) assertSettled(t testing.TB) {
	t.Helper()
	n := len(f.observations[0])
	var got struct {
		Events, Owned, Pending int
		Exact                  bool
	}
	found, err := f.db.Engine.SQL(`SELECT COUNT(*) AS events, COUNT(*) FILTER (WHERE user_id=? AND cost=0.125) AS owned,
		(SELECT COUNT(*) FROM usage_pending) AS pending,
		SUM(cost) = ?::numeric * 0.125 AND (SELECT spend FROM users WHERE id=?) = ?::numeric * 0.125 AS exact
		FROM usage_events`, f.userID, n, f.userID, n).Get(&got)
	if err != nil || !found || got.Events != n || got.Owned != n || got.Pending != 0 || !got.Exact {
		t.Fatalf("应有 %d 笔每笔 $0.125 的本用户账单、精确总消费 $%g、0 条待办，实际 %+v: %v", n, float64(n)*.125, got, err)
	}
}

// replayStaleObservations 同时处理每位调用者原先从 PG 观察到的快照；返回后所有调用都已执行完成。
// mode 指定原逐笔结算、只读凭据核验或当前产品路径，任何单笔错误均写入相应测试或基准。
func (f staleObservationFixture) replayStaleObservations(t testing.TB, mode string) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, rows := range f.observations {
		wg.Go(func() {
			<-start
			switch mode {
			case "baseline":
				for _, row := range rows {
					var record UsageRecord
					if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
						t.Error(err)
						return
					}
					if err := f.db.settleFrozenPending(t.Context(), record, row); err != nil {
						t.Errorf("原恢复失败: %v", err)
						return
					}
				}
			case "receipt":
				got, err := verifiedPendingReceipts(t.Context(), f.db, rows)
				if err != nil || len(got) != len(rows) {
					t.Errorf("批量认证 %d/%d: %v", len(got), len(rows), err)
				}
			case "product":
				if err := f.db.flushObservedPending(t.Context(), rows, f.observedAt); err != nil {
					t.Error(err)
				}
			default:
				t.Errorf("未知研究模式 %q", mode)
			}
		})
	}
	close(start)
	wg.Wait()
}

// TestStalePendingObserversPreserveLedger 用两位调用者各自取得的八笔快照覆盖产品的并发重放；初次结清后每笔只应扣一次。
// 暂停真实 PG 写入直到两份扫描重叠，随后核对用户实际消费、准确账单数和空待办，结束时删除私有 schema。
func TestStalePendingObserversPreserveLedger(t *testing.T) {
	const waiters = 2
	f := newStaleObservationFixture(t, 8, waiters)
	release := holdUsageWriter(t, f.db)
	done := make(chan struct{})
	go func() { defer close(done); f.replayStaleObservations(t, "product") }()
	end := time.Now().Add(5 * time.Second)
	for f.db.pendingSweeps.Load() != waiters && time.Now().Before(end) {
		time.Sleep(time.Millisecond)
	}
	overlap := f.db.pendingSweeps.Load()
	release()
	<-done
	if overlap != waiters {
		t.Fatalf("未观察到预期的 %d 个并发恢复调用，实际 %d", waiters, overlap)
	}
	f.assertSettled(t)
}

// BenchmarkStalePendingObservations 显式比较 64 个调用各自重放相同 32 笔已结清快照的原路径、批查及产品路径。
// 每模式单独创建真实用户与 PG schema，计时仅包括并发重放，另用语句触发器报告每轮 DELETE 尝试数。
// 基准完成后核对恰好 32 个事件、用户消费 4 美元、待办清空；独立调用本基准时才会运行大规模研究。
func BenchmarkStalePendingObservations(b *testing.B) {
	for _, mode := range []string{"baseline", "receipt", "product"} {
		b.Run(mode, func(b *testing.B) {
			f := newStaleObservationFixture(b, 32, 64)
			if _, err := f.db.Engine.Exec(`CREATE SEQUENCE study_deletes;
				CREATE FUNCTION count_stale_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('study_deletes');RETURN NULL;END $$;
				CREATE TRIGGER count_stale_delete AFTER DELETE ON usage_pending FOR EACH STATEMENT EXECUTE FUNCTION count_stale_delete()`); err != nil {
				b.Fatal(err)
			}
			for b.Loop() {
				f.replayStaleObservations(b, mode)
			}
			f.assertSettled(b)
			var count int64
			if _, err := f.db.Engine.SQL("SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM study_deletes").Get(&count); err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(count)/float64(b.N), "deletes/op")
		})
	}
}

// TestPendingReceiptWaitsForConcurrentBudgetFences 用同一私有 schema 的两个 IAM 实例，重现未提交的预占和人工核销。
// 普通 SELECT 可读到陈旧的无围栏状态，正式收据必须等写锁并在提交后拒绝这两笔；另核对到期双层预算重置且旧账不重扣。清理连接和 schema。
func TestPendingReceiptWaitsForConcurrentBudgetFences(t *testing.T) {
	dsn := testsupport.Postgres(t, "receipt_fence")
	db, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal("打开写入实例失败")
	}
	t.Cleanup(func() { _ = db.Close() })
	reader, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal("打开验证实例失败")
	}
	t.Cleanup(func() { _ = reader.Close() })
	org, err := db.CreateOrgWithConfig(t.Context(), Actor{}, "receipt-period", nil, OrgConfig{BudgetDuration: "24h"})
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(t.Context(), Actor{}, UserInput{Email: "receipt-period@example.com", Password: "receipt-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	var observed []pendingUsage
	for _, id := range []string{"open-hold", "manual-resolution", "settled-earlier"} {
		r := usageRecord(id, user.ID, .25)
		r.OrganizationID = org.ID
		r.TS = time.Now().UTC().Add(-48 * time.Hour)
		if err := db.RecordUsageExact(t.Context(), []UsageRecord{r}); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		observed = append(observed, pendingUsage{RequestID: id, Payload: string(raw)})
	}
	ready, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	defer finish()
	writer := make(chan error, 1)
	go func() {
		writer <- db.tx(t.Context(), func(s *xorm.Session) error {
			if _, err := s.Exec(`INSERT INTO budget_reservations(request_id,kind,node_id,user_id,amount) VALUES('open-hold','user',?,?,1)`, user.ID, user.ID); err != nil {
				return err
			}
			if _, err := s.Exec(`INSERT INTO budget_reconciliations(reservation_id,call_id,payload) VALUES('manual-attempt','manual-resolution','{}')`); err != nil {
				return err
			}
			if _, err := s.Exec(`UPDATE users SET spend=5, budget_reset_day=1,budget_reset_at=now()-interval '1 hour' WHERE id=?`, user.ID); err != nil {
				return err
			}
			if _, err := s.Exec(`UPDATE organizations SET spend=5,budget_reset_at=now()-interval '1 hour' WHERE id=?`, org.ID); err != nil {
				return err
			}
			close(ready)
			select {
			case <-release:
				return nil
			case <-t.Context().Done():
				return t.Context().Err()
			}
		})
	}()
	select {
	case <-ready:
	case err := <-writer:
		t.Fatalf("未建立在途预算围栏: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("未观察到未提交围栏")
	}
	rows, err := reader.Engine.QueryString(`SELECT count(*) AS stale FROM usage_events e
		WHERE NOT EXISTS (SELECT 1 FROM budget_reservations h WHERE h.request_id=e.request_id)
		AND NOT EXISTS (SELECT 1 FROM budget_reconciliations f WHERE f.call_id=e.request_id)`)
	if err != nil || len(rows) != 1 || rows[0]["stale"] != "3" {
		t.Fatalf("普通查询未读到预期旧版本：%v/%v", rows, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	verified, verifyErr := verifiedPendingReceipts(ctx, reader, observed)
	deadlineErr := ctx.Err()
	cancel()
	if verifyErr == nil || len(verified) != 0 || !errors.Is(deadlineErr, context.DeadlineExceeded) {
		t.Fatalf("跨实例确认在事务提交前越过围栏：%v/%v/%v", verified, verifyErr, deadlineErr)
	}
	finish()
	if err := <-writer; err != nil {
		t.Fatal(err)
	}
	verified, err = verifiedPendingReceipts(t.Context(), reader, observed)
	if err != nil || len(verified) != 1 || !verified[2] {
		t.Fatalf("预占或人工核销错误通过，或旧账本被重扣：%v/%v", verified, err)
	}
	rows, err = reader.Engine.QueryString(`SELECT u.spend+o.spend AS spend,(u.budget_reset_at>now() AND o.budget_reset_at>now()) AS advanced FROM users u,organizations o WHERE u.id=? AND o.id=?`, user.ID, org.ID)
	if err != nil || len(rows) != 1 || rows[0]["spend"] != "0" || rows[0]["advanced"] != "true" {
		t.Fatalf("确认已入账快照前未刷新到期预算：%v/%v", rows, err)
	}
}
