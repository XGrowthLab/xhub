package regression

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/gateway"
	"github.com/sunqirui1987/xhub/internal/gateway/usagespool"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/store"
)

const crashGroupRequests = 8

// groupCrashReceipt 记录测试客户端已经完整读出的调用 ID 和流式标志，供父进程逐笔验账。
type groupCrashReceipt struct {
	ID     string
	Stream bool
}

// TestUsageGroupProcessRecovery 通过同一用户的八条并发真实 HTTP 请求检查三次进程硬中断。
// 父进程保留私有 PG 和模拟上游，在八份终态仅存磁盘、PG 已插入但事务尚未提交、PG 已提交但本机尚未确认处依次杀死进程。
// 一笔已确认账单用文件同步后、发布前的临时文件名留存。全新网关必须恢复每笔成功用量和五层费用，重复启动不得重扣或重发上游。测试框架回收进程、schema、目录和一次性密钥。
func TestUsageGroupProcessRecovery(t *testing.T) {
	const model = "group-crash"
	t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	h := newHarness(t, chatDeployment(model))
	owner := h.openScope(t, h.adminSession(), model)
	before := h.moneyOf(t, owner)
	dir := t.TempDir()
	cfg, err := json.Marshal(h.gw.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0600); err != nil {
		t.Fatal(err)
	}

	arrivals := make(chan struct{}, crashGroupRequests)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	h.onUpstream(model, func() { arrivals <- struct{}{}; <-release })
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	checkpoints := make(chan []groupCrashReceipt, 1)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/all-at-upstream":
			for range crashGroupRequests {
				select {
				case <-arrivals:
				case <-ctx.Done():
					return
				case <-r.Context().Done():
					return
				}
			}
		case "/database-offline":
			unblock()
		case "/checkpoint":
			var receipts []groupCrashReceipt
			if err := json.NewDecoder(r.Body).Decode(&receipts); err != nil {
				http.Error(w, "invalid checkpoint", 400)
				return
			}
			select {
			case checkpoints <- receipts:
			case <-ctx.Done():
				return
			}
			// 父进程收到事件后直接终止发出通知的子进程，确保 callback 没有返回去删除持久快照。
			select {
			case <-r.Context().Done():
			case <-ctx.Done():
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(control.Close)
	killAtCheckpoint := func(stage string) []groupCrashReceipt {
		t.Helper()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUsageGroupProcessChild$")
		cmd.Env = append(os.Environ(), "XHUB_TEST_GROUP_STAGE="+stage, "XHUB_TEST_GROUP_DIR="+dir,
			"XHUB_TEST_GROUP_CONTROL="+control.URL, "XHUB_TEST_GROUP_KEY="+owner.key)
		var diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &diagnostic, &diagnostic
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case receipts := <-checkpoints:
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			var exit *exec.ExitError
			if err := <-done; !errors.As(err, &exit) || exit.ExitCode() == 0 {
				t.Fatalf("阶段 %s 未发生进程硬中断: %v", stage, err)
			}
			return receipts
		case err := <-done:
			t.Fatalf("阶段 %s 在持久化检查点前退出: %v\n%s", stage, err, diagnostic.Bytes())
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
			t.Fatalf("等待阶段 %s 超时", stage)
		}
		return nil
	}
	receipts := killAtCheckpoint("serve")
	if len(receipts) != crashGroupRequests {
		t.Fatalf("已完整交付 %d 笔，期望 %d", len(receipts), crashGroupRequests)
	}
	if n, err := h.db.Engine.Table("usage_events").Where("key_id = ?", owner.keyID).Count(); err != nil || n != 0 {
		t.Fatalf("断库期间账本应为空: n=%d err=%v", n, err)
	}
	// 服务子进程已经停止。用同一个已同步文件重建锁外暂存后的真实目录布局，让后续恢复只有这一份正金额证据。
	spoolDir := filepath.Join(dir, "spool")
	digest := sha256.Sum256([]byte(receipts[0].ID))
	finalPath := filepath.Join(spoolDir, fmt.Sprintf("%x.json", digest))
	pendingPath := filepath.Join(spoolDir, ".pending-crash-group.tmp")
	if err := os.Rename(finalPath, pendingPath); err != nil {
		t.Fatalf("无法准备实际交付账单的暂存证据: %v", err)
	}
	parent, err := os.Open(spoolDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
		t.Fatalf("同步进程退出后的账单目录失败: %v", err)
	}
	// 在专属表已插入行但事务尚未提交时终止整个重放进程，验证事务回滚且磁盘证据没有被提前确认。
	if _, err := h.db.Engine.Exec(`CREATE FUNCTION pause_charge_before_commit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(TG_RELID::bigint); RETURN NEW; END $$;
		CREATE TRIGGER pause_charge AFTER INSERT ON usage_events FOR EACH ROW EXECUTE FUNCTION pause_charge_before_commit()`); err != nil {
		t.Fatal(err)
	}
	gate := h.db.Engine.NewSession().Context(ctx)
	defer gate.Close()
	if err := gate.Begin(); err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	if _, err := gate.Exec(`SELECT pg_advisory_xact_lock('usage_events'::regclass::oid::bigint)`); err != nil {
		t.Fatal(err)
	}
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var blocked bool
				_, err := h.db.Engine.Context(ctx).SQL(`SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=0 AND objid='usage_events'::regclass::oid AND NOT granted)`).Get(&blocked)
				if err == nil && blocked {
					select {
					case checkpoints <- nil:
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()
	if received := killAtCheckpoint("during-transaction"); len(received) != 0 {
		t.Fatal("事务尚未提交，子进程却报告了提交成功")
	}
	if err := gate.Rollback(); err != nil {
		t.Fatal(err)
	}
	// DDL 等待被杀进程的事务结束，随后读取计数能可靠区分数据库提交和回滚。
	if _, err := h.db.Engine.Exec("DROP TRIGGER pause_charge ON usage_events"); err != nil {
		t.Fatal(err)
	}
	if n, err := h.db.Engine.Table("usage_events").Where("key_id = ?", owner.keyID).Count(); err != nil || n != 0 || !h.readMoneyOf(t, owner).grewBy(before, 0) {
		t.Fatalf("进程退出后未提交账单或部分费用仍可见: rows=%d err=%v", n, err)
	}
	committed := killAtCheckpoint("replay")
	if len(committed) != 1 {
		t.Fatal("提交后的检查点缺少请求 ID")
	}
	if _, err := h.db.GetUsageEvent(t.Context(), iam.UsageQuery{}, committed[0].ID); err != nil {
		t.Fatalf("最后一次中断前 PG 尚未提交: %v", err)
	}
	t.Setenv("XHUB_USAGE_SPOOL_DIR", filepath.Join(dir, "spool"))
	for range 2 {
		fresh := gateway.New(h.gw.Cfg, h.store, h.db)
		err := fresh.FlushLocalUsage(t.Context())
		pending, checkErr := fresh.HasPendingLocalUsage(t.Context())
		closeErr := fresh.CloseUsageSpool()
		if err != nil || checkErr != nil || closeErr != nil || pending {
			t.Fatalf("重启后仍有未结算证据: pending=%v replay=%v check=%v close=%v", pending, err, checkErr, closeErr)
		}
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("多次重放后暂存证据仍未核销: %v", err)
	}
	seen := make(map[string]bool)
	for _, receipt := range receipts {
		e, err := h.db.GetUsageEvent(t.Context(), iam.UsageQuery{}, receipt.ID)
		if err != nil || seen[receipt.ID] || e.Status != "success" || !nearlyEqual(e.Cost, expectedCost()) ||
			e.Streamed != receipt.Stream || e.PromptTokens != defaultReply.PromptTokens || e.CompletionTokens != defaultReply.CompletionTokens {
			t.Fatalf("调用 %s 的终态、用量或费用不符（流式=%v）: %v", receipt.ID, receipt.Stream, err)
		}
		seen[receipt.ID] = true
	}
	if n, err := h.db.Engine.Table("usage_events").Where("key_id = ?", owner.keyID).Count(); err != nil || n != crashGroupRequests || len(h.upstreamCalls()) != crashGroupRequests || !seen[committed[0].ID] || !h.moneyOf(t, owner).grewBy(before, crashGroupRequests*expectedCost()) {
		t.Fatalf("多次重启后账本、上游次数或五层扣费不一致: rows=%d upstream=%d err=%v", n, len(h.upstreamCalls()), err)
	}
}

// TestUsageGroupProcessChild 是上述父用例启动的实际网关或磁盘重放进程。仅在专属环境中运行。
// 测试控制端只接收临时调用 ID；回放使用生产结算方法，在提交完成但未 ACK 处停住，由父进程终止并回收。
func TestUsageGroupProcessChild(t *testing.T) {
	stage := os.Getenv("XHUB_TEST_GROUP_STAGE")
	if stage == "" {
		return
	}
	dir := os.Getenv("XHUB_TEST_GROUP_DIR")
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	db, err := iam.Open(t.Context(), cfg.GeneralSettings.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	control := func(path string, receipts []groupCrashReceipt) {
		t.Helper()
		raw, err := json.Marshal(receipts)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(os.Getenv("XHUB_TEST_GROUP_CONTROL")+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("进程测试控制点 %s 返回 %d", path, resp.StatusCode)
		}
	}
	spoolDir := filepath.Join(dir, "spool")
	if stage == "replay" || stage == "during-transaction" {
		sp, err := usagespool.Open(usagespool.Config{Dir: spoolDir})
		if err != nil {
			t.Fatal(err)
		}
		_, err = sp.Replay(t.Context(), crashGroupRequests, func(ctx context.Context, r iam.UsageRecord) error {
			if r.DeliveryUnconfirmed {
				return errors.New("客户端已收到完整答复，回放却只有未确认标记")
			}
			var err error
			if r.DeliveryPending {
				err = db.SettleConfirmedDelivery(ctx, r)
			} else {
				err = db.RecordUsageExact(ctx, []iam.UsageRecord{r})
			}
			if err == nil {
				control("/checkpoint", []groupCrashReceipt{{ID: r.RequestID, Stream: r.Streamed}})
			}
			return err
		})
		t.Fatalf("预期在提交后中断: %v", err)
	}
	if stage != "serve" {
		t.Fatal("未知子进程模式")
	}
	t.Setenv("XHUB_USAGE_SPOOL_DIR", spoolDir)
	st, err := store.Open(cfg.GeneralSettings.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gateway.New(&cfg, st, db).Handler())
	client := &harness{server: srv, t: t}
	results := make(chan groupCrashReceipt, crashGroupRequests)
	for i := range crashGroupRequests {
		go func() {
			stream := i%2 == 0
			request := chatRequest("group-crash", "Verify concurrent crash recovery.")
			request["stream"] = stream
			response := client.do(http.MethodPost, "/v1/chat/completions", os.Getenv("XHUB_TEST_GROUP_KEY"), request)
			id := response.header("x-litellm-call-id")
			if response.status != 200 || id == "" || !strings.Contains(response.text(), defaultReply.Content) || (stream && !strings.Contains(response.text(), "[DONE]")) {
				t.Errorf("完整 HTTP 响应缺失: stream=%v http=%d id=%s", stream, response.status, id)
			}
			results <- groupCrashReceipt{ID: id, Stream: stream}
		}()
	}
	control("/all-at-upstream", nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	control("/database-offline", nil)
	var receipts []groupCrashReceipt
	for range crashGroupRequests {
		receipts = append(receipts, <-results)
	}
	srv.Close()
	if t.Failed() {
		return
	}
	control("/checkpoint", receipts)
	t.Fatal("预期在账单落盘、数据库不可用时硬中断")
}
