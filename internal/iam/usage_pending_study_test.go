package iam

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
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

// TestStalePendingObservationStudy 复现 64 个调用都已读取相同 32 份快照，随后前台先完成结算的真实 PG 情形。
// 计时记录当前逐项结算/确认和每调用一次的批量已入账核对；DB 触发器统计所有 DELETE 尝试，不对真实网关吞吐作推断。
func TestStalePendingObservationStudy(t *testing.T) {
	const identities, waiters = 32, 64
	db := testDB(t)
	for i := 0; i < identities; i++ {
		r := usageRecord(fmt.Sprintf("stale-%d", i), "study-user", .125)
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Engine.Exec("INSERT INTO usage_pending(request_id,payload) VALUES(?,?::jsonb)", r.RequestID, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	observations := make([][]pendingUsage, waiters)
	for i := range observations {
		if err := db.Engine.Table("usage_pending").Asc("created_at", "request_id").Limit(100).Find(&observations[i]); err != nil || len(observations[i]) != identities {
			t.Fatalf("私有测试快照读取失败: %v", err)
		}
	}
	if err := db.FlushPendingUsage(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`CREATE SEQUENCE study_deletes;
	CREATE FUNCTION count_stale_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('study_deletes'); RETURN NULL; END $$;
	CREATE TRIGGER count_stale_delete AFTER DELETE ON usage_pending FOR EACH STATEMENT EXECUTE FUNCTION count_stale_delete()`); err != nil {
		t.Fatal(err)
	}
	observedAt := time.Now()
	for _, mode := range []string{"baseline", "receipt", "product"} {
		before := time.Now()
		var wg sync.WaitGroup
		closeStart := make(chan struct{})
		for _, rows := range observations {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-closeStart
				if mode == "baseline" {
					for _, row := range rows {
						var record UsageRecord
						if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
							t.Error(err)
							return
						}
						if err := db.settleFrozenPending(t.Context(), record, row); err != nil {
							t.Errorf("原恢复失败: %v", err)
							return
						}
					}
				} else if mode == "product" {
					if err := db.flushObservedPending(t.Context(), rows, observedAt); err != nil {
						t.Error(err)
					}
				} else {
					got, err := verifiedPendingReceipts(t.Context(), db, rows)
					if err != nil || len(got) != identities {
						t.Errorf("批量认证失败 %d: %v", len(got), err)
					}
				}
			}()
		}
		close(closeStart)
		wg.Wait()
		n, err := db.Engine.QueryString(`SELECT last_value FROM study_deletes`)
		if err != nil || len(n) != 1 || (mode != "product" && n[0]["last_value"] != "2048") {
			t.Fatalf("陈旧快照的删除尝试与预期不符: %v %v", n, err)
		}
		t.Logf("mode=%s callers=%d observed_per_caller=%d elapsed_ms=%d cumulative_delete_attempts=%s", mode, waiters, identities, time.Since(before).Milliseconds(), n[0]["last_value"])
	}
}
