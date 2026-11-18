//go:build unix

package iam

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"xorm.io/xorm/contexts"
)

const killedBatchRequests = 3

// batchCrashGate 只暂停测试子进程中目标快照的指定持久化边界，实际 INSERT/COMMIT 仍由 PostgreSQL 执行。
type batchCrashGate struct {
	phase, path string
	inserts     atomic.Int32
	once        sync.Once
}

// pause 将到达边界的事实通知父测试，此处必须等待父进程 SIGKILL，不能执行任何正常退出清理。
func (g *batchCrashGate) pause() {
	g.once.Do(func() {
		if os.WriteFile(g.path, []byte(g.phase), 0600) != nil {
			os.Exit(75)
		}
	})
	// 有期限的看门狗让测试进程即使失去父进程也能退出。
	time.Sleep(45 * time.Second)
	os.Exit(76)
}

// BeforeProcess 仅在已提交的合批账单即将进行独立 ACK 时挂起所有目标删除，保留每一份原快照。
func (g *batchCrashGate) BeforeProcess(c *contexts.ContextHook) (context.Context, error) {
	if g.phase == "committed" && strings.HasPrefix(strings.TrimSpace(c.SQL), "DELETE FROM usage_pending WHERE request_id") {
		g.pause()
	}
	return c.Ctx, nil
}

// AfterProcess 在同用户短批次的第二条账单已实际 INSERT 后挂起；父进程还会检查 PG 事务内计数证据。
func (g *batchCrashGate) AfterProcess(c *contexts.ContextHook) error {
	if g.phase == "uncommitted" && c.Err == nil && strings.HasPrefix(strings.TrimSpace(c.SQL), "INSERT INTO usage_events") && len(c.Args) > 0 {
		id, _ := c.Args[0].(string)
		if strings.HasPrefix(id, "kill-batch-") && g.inserts.Add(1) == 2 {
			g.pause()
		}
	}
	return nil
}

// runBatchCrashWriter 用真实的交付恢复入口逐一提交请求；通过另一个零费用同用户请求确保恰好三笔排入下一事务。
// 参数取自父测试创建的私有库及临时目录，出错交给父测试；子进程正常情况下只会被 SIGKILL 终止。
func runBatchCrashWriter(t *testing.T, phase string) {
	t.Helper()
	db, err := Open(t.Context(), os.Getenv("XHUB_TEST_BATCH_CRASH_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user := os.Getenv("XHUB_TEST_BATCH_CRASH_USER")
	db.Engine.AddHook(&batchCrashGate{phase: phase, path: os.Getenv("XHUB_TEST_BATCH_CRASH_SIGNAL")})
	release := holdUsageWriter(t, db)
	done := make(chan error, killedBatchRequests+1)
	go func() {
		done <- db.RecordUsageExact(t.Context(), []UsageRecord{usageRecord("batch-sentinel", user, 0)})
	}()
	waitUserQueue(t, db, user, 0)
	for i := range killedBatchRequests {
		go func() { done <- db.FlushPendingUsageRequest(t.Context(), fmt.Sprintf("kill-batch-%d", i)) }()
	}
	waitUserQueue(t, db, user, killedBatchRequests)
	// 领写者已在等候本地锁且全部目标请求排队后，放开该批结算。
	release()
	for range killedBatchRequests + 1 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("达到真实中断边界前，所有账单已完成")
}

// seedKilledBatch 创建共享五级费用归属、三笔不同的精确费用和原始额度预占，并通过网关使用的交付 API 冻结 PG 快照。
// 返回的记录供父测试验证每笔账本和重放；所有测试对象均在独立 schema 中。
func seedKilledBatch(t *testing.T, db *DB) []UsageRecord {
	t.Helper()
	org, err := db.CreateOrg(t.Context(), Actor{}, "process-org", quotaLimit(10))
	if err != nil {
		t.Fatal(err)
	}
	team, err := db.CreateTeam(t.Context(), Actor{}, TeamInput{OrganizationID: org.ID, Name: "process-team"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(t.Context(), Actor{}, ProjectInput{TeamID: team.ID, Name: "process-project"})
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(t.Context(), Actor{}, UserInput{Email: "process@example.com", Password: "password123", TeamID: team.ID})
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := db.CreateKey(t.Context(), Actor{}, KeyInput{OwnerType: OwnerPersonal, UserID: user.ID, TeamID: team.ID, ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	var records []UsageRecord
	for i := range killedBatchRequests {
		r := usageRecord(fmt.Sprintf("kill-batch-%d", i), user.ID, float64(i+1)/8)
		r.KeyID, r.TeamID, r.ProjectID, r.OrganizationID = key.ID, team.ID, project.ID, org.ID
		hold, err := db.ReserveBudget(t.Context(), r.RequestID, "key", key.ID, r.Cost)
		if err != nil {
			t.Fatal(err)
		}
		r.BudgetReservationID = hold.RequestID
		if err = db.PrepareDelivery(t.Context(), r, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err = db.ConfirmDelivery(t.Context(), r); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	return records
}

// verifyKilledBatch 对照真实 PostgreSQL 的每笔账单与金额、五层 NUMERIC 消费、原预占、待确认数及可选的共同事务 ID。
// 参数 settled 表示账本期望状态，pending 是应保留的快照数，shared 要求三笔账单在同一事务提交；只读检查。
func verifyKilledBatch(t *testing.T, db *DB, records []UsageRecord, settled bool, pending int, shared bool) {
	t.Helper()
	total, remaining := 0., len(records)
	if settled {
		remaining = 0
	}
	for _, r := range records {
		e, err := db.GetUsageEvent(t.Context(), UsageQuery{}, r.RequestID)
		if !settled {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("未提交的事件 %s 对外可见: %v", r.RequestID, err)
			}
			continue
		}
		if err != nil || e.Cost != r.Cost || e.PromptTokens != r.PromptTokens || e.CompletionTokens != r.CompletionTokens || e.KeyID != r.KeyID || e.ProjectID != r.ProjectID {
			t.Fatalf("重启前后事件 %s 的费用、归属或用量错误: %v %+v", r.RequestID, err, e)
		}
		total += r.Cost
	}
	r := records[0]
	q, err := db.Engine.QueryString(`SELECT (SELECT spend FROM organizations WHERE id=?)=?::numeric AND
		(SELECT spend FROM teams WHERE id=?)=?::numeric AND (SELECT spend FROM projects WHERE id=?)=?::numeric AND
		(SELECT spend FROM users WHERE id=?)=?::numeric AND (SELECT spend FROM api_keys WHERE id=?)=?::numeric AS money,
		(SELECT COUNT(*) FROM usage_pending) AS pending,(SELECT COUNT(*) FROM budget_reservations) AS holds,
		COUNT(DISTINCT xmin::text) AS xs FROM usage_events WHERE request_id LIKE 'kill-batch-%'`,
		r.OrganizationID, total, r.TeamID, total, r.ProjectID, total, r.UserID, total, r.KeyID, total)
	if err != nil || len(q) != 1 || q[0]["money"] != "true" || q[0]["pending"] != fmt.Sprint(pending) || q[0]["holds"] != fmt.Sprint(remaining) || (shared && q[0]["xs"] != "1") {
		t.Fatalf("五层消费、原预占、待办或共同事务 ID 错误: %+v %v", q, err)
	}
}

// TestUserUsageBatchProcessKill 在进程隔离环境强制中断真正的同用户短批事务，并从相同 PG 原始快照重启。
// 两个中断点包括第二条 INSERT 之后但提交前，以及整批 COMMIT 之后但 ACK 前；核对逐笔、五层、预占和再次重放，清理子进程和 schema。
func TestUserUsageBatchProcessKill(t *testing.T) {
	if mode := os.Getenv("XHUB_TEST_BATCH_CRASH_MODE"); mode != "" {
		runBatchCrashWriter(t, mode)
		return
	}
	for _, phase := range []string{"uncommitted", "committed"} {
		t.Run(phase, func(t *testing.T) {
			db := testDB(t)
			records := seedKilledBatch(t, db)
			// SET LOCAL 在每次提交后自动复位，只有同一事务确实插入第二条记录才会推进这个不随回滚撤销的序列。
			if _, err := db.Engine.Exec(`CREATE SEQUENCE batch_transaction_proof;
				CREATE FUNCTION prove_multiple_rows() RETURNS trigger LANGUAGE plpgsql AS $$
				DECLARE n int := COALESCE(NULLIF(current_setting('xhub.test_batch_rows',true),''),'0')::int+1;
				BEGIN PERFORM set_config('xhub.test_batch_rows',n::text,true); IF n=2 THEN PERFORM nextval('batch_transaction_proof'); END IF; RETURN NULL; END $$;
				CREATE TRIGGER counted_batch AFTER INSERT ON usage_events FOR EACH ROW WHEN (NEW.request_id LIKE 'kill-batch-%') EXECUTE FUNCTION prove_multiple_rows()`); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			signal := filepath.Join(dir, "at-boundary")
			child := exec.Command(os.Args[0], "-test.run=^TestUserUsageBatchProcessKill$")
			child.Env = append(os.Environ(), "XHUB_TEST_BATCH_CRASH_MODE="+phase, "XHUB_TEST_BATCH_CRASH_DB="+db.Engine.DataSourceName(), "XHUB_TEST_BATCH_CRASH_USER="+records[0].UserID, "XHUB_TEST_BATCH_CRASH_SIGNAL="+signal)
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			exit := make(chan error, 1)
			go func() { exit <- child.Wait(); close(exit) }()
			t.Cleanup(func() { _ = child.Process.Kill(); <-exit })
			deadline := time.After(15 * time.Second)
			tick := time.NewTicker(5 * time.Millisecond)
			defer tick.Stop()
		wait:
			for {
				select {
				case err := <-exit:
					t.Fatalf("到达 %s 边界前子进程已退出: %v", phase, err)
				case <-deadline:
					t.Fatalf("等待 %s 边界超时", phase)
				case <-tick.C:
					if b, err := os.ReadFile(signal); err == nil && string(b) == phase {
						break wait
					}
				}
			}
			var proved bool
			if _, err := db.Engine.SQL("SELECT is_called FROM batch_transaction_proof").Get(&proved); err != nil || !proved {
				t.Fatalf("未证实第二笔 INSERT 已在同一真实短批事务内执行: %v", err)
			}
			verifyKilledBatch(t, db, records, phase == "committed", len(records), phase == "committed")
			if err := child.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			err := <-exit
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("子进程未按 SIGKILL 终止: %v", err)
			}
			fresh, err := Open(t.Context(), db.Engine.DataSourceName())
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			verifyKilledBatch(t, fresh, records, phase == "committed", len(records), phase == "committed")
			for range 2 {
				if err := fresh.FlushPendingUsage(t.Context(), 50); err != nil {
					t.Fatal(err)
				}
				verifyKilledBatch(t, fresh, records, true, 0, phase == "committed")
			}
		})
	}
}
