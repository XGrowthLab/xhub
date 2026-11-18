package regression

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/benchmarks/load"
	"github.com/sunqirui1987/xhub/internal/gateway"
	"github.com/sunqirui1987/xhub/internal/gateway/usagespool"
	"github.com/sunqirui1987/xhub/internal/iam"
)

// TestUsageSpoolConcurrentChatBilling 使用工单中的压测客户端，验证启用磁盘队列时普通 Chat 与 SSE 均能并发完成。
// 前置私有 PostgreSQL schema、Redis、测试密钥和本机上游；核对每笔用量、五层费用及重放幂等。
// t 托管服务、数据库、进程和目录，成功或失败均回收；测试不使用外部供应商凭据。
func TestUsageSpoolConcurrentChatBilling(t *testing.T) {
	for _, size := range []string{"1", "16"} {
		t.Run("group="+size, func(t *testing.T) {
			t.Setenv("XHUB_USAGE_SPOOL_GROUP_SIZE", size)
			verifyConcurrentChatBills(t)
		})
	}
}

// verifyConcurrentChatBills 在调用方指定的磁盘写入模式下发起真实的普通及流式并发请求。
// 独立上游、PG schema 与 Redis 由测试清理；以计费触发器及五层账本证明没有漏记或重复提交。
func verifyConcurrentChatBills(t *testing.T) {
	const model = "spool-concurrent-chat"
	t.Setenv("XHUB_USAGE_SPOOL_DIR", filepath.Join(t.TempDir(), "spool"))
	isolatedSpendRedis(t)
	h := newHarness(t, chatDeployment(model))
	if h.gw.Live == nil {
		t.Fatal("并发账单回归需要连接真实 Redis")
	}
	scope := h.openScope(t, h.adminSession(), model)
	before := h.moneyOf(t, scope)
	// BEFORE INSERT 会在 ON CONFLICT 去重之前执行。私有 schema 的序列因回滚也不会减小，
	// 因此可区分「最终账单数正确」和「每个成功调用只尝试一次写入」。
	if _, err := h.db.Engine.Exec(`CREATE SEQUENCE billed_attempts START 1;
        CREATE FUNCTION count_billed_attempts() RETURNS trigger LANGUAGE plpgsql AS $$
          BEGIN PERFORM nextval('billed_attempts'); RETURN NEW; END $$;
        CREATE TRIGGER count_billed BEFORE INSERT ON usage_events FOR EACH ROW
          WHEN (NEW.model = 'spool-concurrent-chat') EXECUTE FUNCTION count_billed_attempts()`); err != nil {
		t.Fatal(err)
	}
	// 在上游作答前检查真实队列的在途状态，防止因关闭队列绕过并发故障而误通过。
	h.onUpstream(model, sync.OnceFunc(func() {
		if pending, err := h.gw.HasPendingLocalUsage(t.Context()); err != nil || !pending {
			t.Errorf("首个上游调用缺少本机账单预留: pending=%v err=%v", pending, err)
		}
	}))
	succeeded, streamed := 0, 0
	for _, scenario := range []string{"chat", "stream"} {
		for _, concurrency := range []int{4, 16} {
			for _, requests := range []int{3, 30} {
				cfg := load.Config{BaseURL: h.server.URL, Model: model, Key: scope.key, Scenario: scenario,
					Concurrency: concurrency, Requests: requests, Duration: 6 * time.Second, Timeout: 30 * time.Second, PromptBytes: 128}
				res, err := load.Run(t.Context(), cfg)
				if err != nil {
					t.Fatalf("%s 并发 %d 启动失败: %v", scenario, concurrency, err)
				}
				t.Logf("%s 并发 %d 上限 %d: 完成 %d 成功 %d 状态 %v", scenario, concurrency, requests, res.Attempted, res.Succeeded, res.Statuses)
				// 六秒是发压窗口；较慢机器也必须发满首批并发，且每个实际发出的请求都须返回完整回答。
				if res.Attempted < min(requests, concurrency) || res.Failed != 0 || res.Statuses[http.StatusOK] != res.Attempted {
					t.Errorf("%s 并发 %d 返回异常: 发出=%d 成功=%d 状态=%v 错误=%v", scenario, concurrency, res.Attempted, res.Succeeded, res.Statuses, res.Errors)
				}
				succeeded += res.Succeeded
				if scenario == "stream" {
					streamed += res.Succeeded
				}
			}
		}
	}
	// SSE 客户端可先收到结束帧；等所有处理器退出再连续刷账，确保检查的是持久终态。
	h.server.Close()
	h.flushSpend()
	after := h.moneyOf(t, scope)
	if got := len(h.upstreamCalls()); got != succeeded || !after.grewBy(before, float64(succeeded)*expectedCost()) {
		t.Errorf("上游调用或五层入账不一致: 成功=%d 上游=%d 之前=%+v 之后=%+v", succeeded, got, before, after)
	}
	var events []iam.UsageEvent
	if err := h.db.Engine.Where("key_id = ?", scope.keyID).Find(&events); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool, len(events))
	streamBills := 0
	for _, event := range events {
		if event.RequestID == "" || ids[event.RequestID] || event.Status != "success" || event.HTTPStatus != http.StatusOK ||
			event.PromptTokens != defaultReply.PromptTokens || event.CompletionTokens != defaultReply.CompletionTokens || !nearlyEqual(event.Cost, expectedCost()) {
			t.Fatalf("账单重复、费用或用量不符: id=%q 状态=%s HTTP=%d 输入=%d 输出=%d 费用=%g", event.RequestID, event.Status, event.HTTPStatus, event.PromptTokens, event.CompletionTokens, event.Cost)
		}
		ids[event.RequestID] = true
		if event.Streamed {
			streamBills++
		}
	}
	var inserts int
	if _, err := h.db.Engine.SQL("SELECT last_value FROM billed_attempts").Get(&inserts); err != nil || inserts != succeeded {
		t.Errorf("结算发生冗余写入：成功=%d 实际INSERT尝试=%d err=%v", succeeded, inserts, err)
	}
	if len(events) != succeeded || streamBills != streamed {
		t.Errorf("成功请求与账单条数不一致: 成功=%d 账单=%d 流式成功=%d 流式账单=%d", succeeded, len(events), streamed, streamBills)
	}
	if pending, err := h.gw.HasPendingLocalUsage(t.Context()); err != nil || pending {
		t.Errorf("成功结算后磁盘仍有待处理账单: pending=%v err=%v", pending, err)
	}
}

// TestUsageSpoolDatabaseDisconnect 验证上游已接收后数据库断开仍可恢复完整账单，正文存储失败则明确标记。
// 前置私有PG和本地供应商，覆盖同步/流式及有无硬额度；重复Flush不重复费用或上游，最后清理schema/磁盘/连接。
func TestUsageSpoolDatabaseDisconnect(t *testing.T) {
	for _, protocol := range []string{"chat/completions", "responses", "messages"} {
		for _, useRedis := range []bool{false, true} {
			for _, hard := range []bool{false, true} {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/Redis=%v/hard=%v/stream=%v", protocol, useRedis, hard, stream), func(t *testing.T) {
						dir := filepath.Join(t.TempDir(), "spool")
						t.Setenv("XHUB_USAGE_SPOOL_DIR", dir)
						t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
						var breakRedis func()
						if useRedis {
							rdb := isolatedSpendRedis(t)
							breakRedis = func() {
								if err := rdb.Set(t.Context(), "xhub:spendlog", "broken-queue", 0).Err(); err != nil {
									t.Error(err)
								}
							}
						}
						const model = "regression-disk-recovery"
						dep := chatDeployment(model)
						dep.ModelInfo["endpoint_types"] = []string{"chat", "responses", "messages"}
						h := newHarness(t, dep)
						t.Cleanup(func() { _ = h.gw.CloseUsageSpool() })
						admin := h.adminSession()
						scope := h.openScope(t, admin, "disk-recovery")
						if hard {
							h.setKeyBudget(t, admin, scope.key, 10)
						}
						before := h.moneyOf(t, scope)
						h.onUpstream(model, func() {
							if breakRedis != nil {
								breakRedis()
							}
							_ = h.db.Engine.Close()
						})
						request := chatRequest(model, "Verify independent durable billing recovery.")
						if protocol == "responses" {
							request = map[string]any{"model": model, "input": "Verify independent durable billing recovery."}
						}
						if protocol == "messages" {
							request["max_tokens"] = 128
						}
						request["stream"] = stream
						response := h.ok(http.MethodPost, "/v1/"+protocol, scope.key, request)
						callID := response.header("x-litellm-call-id")
						if callID == "" || len(h.upstreamCalls()) != 1 {
							t.Fatal("真实推理未完成唯一调用")
						}
						h.server.Close() // 等待SSE处理器完成本机落盘，再重建实例。
						files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
						if len(files) != 1 {
							t.Fatalf("PG完全不可写时磁盘账单数=%d", len(files))
						}
						snapshot, err := os.ReadFile(files[0])
						if err != nil {
							t.Fatal(err)
						}
						if strings.Contains(string(snapshot), "Verify independent durable billing recovery.") || strings.Contains(string(snapshot), "regression-ok") {
							t.Fatal("磁盘账单快照不应携带用户正文")
						}
						_ = h.gw.CloseUsageSpool()
						reopened, err := iam.Open(t.Context(), h.gw.Cfg.GeneralSettings.DatabaseURL)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = reopened.Close() })
						n, err := reopened.Engine.Table("usage_pending").Count()
						if err != nil || n != 0 {
							t.Fatalf("本场景必须证明PG pending也不可写: n=%d err=%v", n, err)
						}
						holds, err := reopened.PendingKeyBudget(t.Context(), scope.keyID)
						if err != nil || len(holds) != 0 {
							t.Fatalf("断库期间预占证据错误: holds=%d err=%v", len(holds), err)
						}
						fresh := gateway.New(h.gw.Cfg, h.store, reopened)
						if fresh.Live != nil {
							t.Cleanup(func() { _ = fresh.Live.Close() })
						}
						t.Cleanup(func() { _ = fresh.CloseUsageSpool() })
						// 模拟数据库提交完成、删除磁盘文件前进程退出，重放相同快照必须由账单幂等键去重。
						if err = os.WriteFile(files[0], snapshot, 0600); err != nil {
							t.Fatal(err)
						}
						for range 3 {
							fresh.FlushSpend()
						}
						files, _ = filepath.Glob(filepath.Join(dir, "*.json"))
						if len(files) != 0 {
							t.Fatalf("恢复未确认磁盘快照: %d", len(files))
						}
						holds, err = reopened.PendingKeyBudget(t.Context(), scope.keyID)
						if err != nil || len(holds) != 0 {
							t.Fatalf("完整账单恢复未释放已结算占用: holds=%d err=%v", len(holds), err)
						}
						h.db.Engine = reopened.Engine // 原HTTP鉴权持有相同DB对象，恢复其连接以验证用户可见查询。
						if after := h.moneyOf(t, scope); !after.grewBy(before, expectedCost()) {
							t.Fatalf("五层费用未唯一结算: before=%+v after=%+v", before, after)
						}
						rows, err := reopened.Engine.Table("usage_events").Where("request_id = ?", callID).Count()
						if err != nil || rows != 1 {
							t.Fatalf("账单非唯一: n=%d err=%v", rows, err)
						}
						log, err := reopened.GetRequestLog(t.Context(), callID)
						if err != nil || log.BodyCaptureStatus != "storage_failed" || log.RequestBody != "" || log.ResponseBody != "" {
							t.Fatalf("断库时正文应标记存储失败，账单重放不能恢复正文: log=%+v err=%v", log, err)
						}
						if len(h.upstreamCalls()) != 1 {
							t.Fatal("重放错误地重试上游")
						}
					})
				}
			}
		}
	}
}

// TestUsageSpoolUnavailableAdmission 验证不可写磁盘在真实HTTP准入时返回503且不调用上游；恢复权限后可继续对话。
// 使用临时目录及私有PG，硬额度与无限额账号均走相同存储准入；最后清理所有测试数据。
func TestUsageSpoolUnavailableAdmission(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	t.Setenv("XHUB_USAGE_SPOOL_DIR", dir)
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	h := newHarness(t, chatDeployment("spool-admission"))
	t.Cleanup(func() { _ = h.gw.CloseUsageSpool() })
	admin := h.adminSession()
	scope := h.openScope(t, admin, "spool-admission")
	h.setKeyBudget(t, admin, scope.key, 10)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	for _, key := range []string{scope.key, scope.session} {
		r := h.do(http.MethodPost, "/v1/chat/completions", key, chatRequest("spool-admission", "Verify durable admission."))
		if r.status != 503 || !strings.Contains(string(r.body), "billing_storage_unavailable") {
			t.Fatalf("存储故障未明确拒绝: %s", r.describe())
		}
	}
	if len(h.upstreamCalls()) != 0 {
		t.Fatal("存储不可用仍调用付费上游")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	h.ok(http.MethodPost, "/v1/chat/completions", scope.key, chatRequest("spool-admission", "Verify restored admission."))
}

// TestUsageSpoolUnsettledEvidenceBlocksUpstream 在真实 PostgreSQL 账本与持久目录构造同一请求的冲突快照。
// 后台已进入退避时，密钥和会话的新请求均须明确返回 503，且无上游调用或费用变化。夹具回收凭据、数据库和磁盘。
func TestUsageSpoolUnsettledEvidenceBlocksUpstream(t *testing.T) {
	const model = "spool-admission-conflict"
	dir := filepath.Join(t.TempDir(), "spool")
	t.Setenv("XHUB_USAGE_SPOOL_DIR", dir)
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	h := newHarness(t, chatDeployment(model))
	t.Cleanup(func() { _ = h.gw.CloseUsageSpool() })
	scope := h.openScope(t, h.adminSession(), model)
	h.ok(http.MethodPost, "/v1/chat/completions", scope.key, chatRequest(model, "Verify normal admission."))
	h.flushSpend()
	before := h.moneyOf(t, scope)
	existing := iam.UsageRecord{RequestID: "durable-conflict", TS: time.Now().UTC(), Model: model, Status: "success", Cost: 0}
	if err := h.db.RecordUsageExact(t.Context(), []iam.UsageRecord{existing}); err != nil {
		t.Fatal(err)
	}
	existing.Cost = 1
	// 另一私有目录用产品序列化器创建完整快照；关闭后原子移入当前队列，模拟此前留下的耐久证据。
	source := filepath.Join(t.TempDir(), "recovery")
	sp, err := usagespool.Open(usagespool.Config{Dir: source})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sp.Close() })
	if err = sp.Put(existing); err != nil {
		t.Fatal(err)
	}
	if err = sp.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(source, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("缺少持久化快照: %v %v", files, err)
	}
	name := filepath.Join(dir, filepath.Base(files[0]))
	if err := os.Rename(files[0], name); err != nil {
		t.Fatal(err)
	}
	if err := h.gw.FlushLocalUsage(t.Context()); !errors.Is(err, iam.ErrConflict) {
		t.Fatalf("后台未检出账本冲突: %v", err)
	}
	if err := h.gw.FlushLocalUsage(t.Context()); err != nil {
		t.Fatalf("后台未进入退避: %v", err)
	}
	for _, token := range []string{scope.key, scope.session} {
		r := h.do(http.MethodPost, "/v1/chat/completions", token, chatRequest(model, "Check unresolved billing admission."))
		if r.status != http.StatusServiceUnavailable || !strings.Contains(string(r.body), "billing_storage_pending") {
			t.Fatalf("未结清账单时未明确拒绝: %s", r.describe())
		}
	}
	if got := len(h.upstreamCalls()); got != 1 {
		t.Fatalf("冲突后重复访问上游: 总调用=%d 正常对照=1", got)
	}
	if !h.moneyOf(t, scope).grewBy(before, 0) {
		t.Fatal("拒绝准入时仍产生使用费用")
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatalf("冲突持久证据丢失: %v", err)
	}
}
