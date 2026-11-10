package regression

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/gateway"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/live"
)

// TestSyncClientWriteFailure 验证同步标准及透传对话的客户端写入错误或短写按失败零费用记录。
// 使用真实网关、隔离 PG、可选磁盘队列、本机上游和注入式响应写入器；检查不重放、账单、日志及清理。
func TestSyncClientWriteFailure(t *testing.T) {
	t.Run("unprepared_error_no_cleanup", testUnpreparedFailureDoesNotCleanDelivery)
	for _, disk := range []bool{false, true} {
		for _, tc := range []struct {
			name, path        string
			short, idempotent bool
		}{
			{"standard-error", "/chat/completions", false, false}, {"standard-short", "/chat/completions", true, false},
			{"bypass-error", "/bypass/openai/v1/chat/completions", false, false}, {"bypass-short", "/bypass/openai/v1/chat/completions", true, false},
			{"standard-idempotent-error", "/chat/completions", false, true}, {"bypass-idempotent-error", "/bypass/openai/v1/chat/completions", false, true},
		} {
			t.Run(fmt.Sprintf("disk=%v/%s", disk, tc.name), func(t *testing.T) {
				t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
				dir := ""
				if disk {
					dir = filepath.Join(t.TempDir(), "spool")
				}
				t.Setenv("XHUB_USAGE_SPOOL_DIR", dir)
				var calls atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					writeJSON(w, map[string]any{"id": "chat-1", "object": "chat.completion", "model": "sync-write", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "Hello"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10}})
				}))
				t.Cleanup(up.Close)
				dep := chatDeployment("sync-write")
				dep.LiteLLMParams["api_base"] = up.URL + "/v1"
				dep.ModelInfo["transport"] = "bypass_openai_chat"
				dep.ModelInfo["endpoint_types"] = []string{"chat", "bypass:openai-chat"}
				h := newHarness(t, dep)
				t.Cleanup(func() { _ = h.gw.CloseUsageSpool() })
				admin := h.adminSession()
				owner := h.openScope(t, admin, "sync-write")
				template := routeTemplate(t, h, admin, "sync-write", routeTemplateBody([]any{}, 3, 10, 1, 60))
				bindTemplate(t, h, admin, "key", owner.keyID, template)
				req := httptest.NewRequest("POST", tc.path, strings.NewReader(`{"model":"sync-write","messages":[{"role":"user","content":"Hello"}],"stream":false}`))
				req.Header.Set("Authorization", "Bearer "+owner.key)
				req.Header.Set("Content-Type", "application/json")
				if tc.idempotent {
					req.Header.Set("Idempotency-Key", "sync-client-write-fixture")
				}
				w := &dialogueWriteProbe{header: make(http.Header), short: tc.short}
				h.gw.Handler().ServeHTTP(w, req)
				if calls.Load() != 1 {
					t.Fatalf("客户端写失败后不应重放上游: %d", calls.Load())
				}
				if tc.idempotent {
					// 原响应写入失败并记零费用后，同一幂等键不能免费重放成功正文，也不能再次调用上游。
					retry := httptest.NewRequest("POST", tc.path, strings.NewReader(`{"model":"sync-write","messages":[{"role":"user","content":"Hello"}],"stream":false}`))
					retry.Header.Set("Authorization", "Bearer "+owner.key)
					retry.Header.Set("Content-Type", "application/json")
					retry.Header.Set("Idempotency-Key", "sync-client-write-fixture")
					replay := httptest.NewRecorder()
					h.gw.Handler().ServeHTTP(replay, retry)
					if replay.Code != 409 || strings.Contains(replay.Body.String(), "Hello") || calls.Load() != 1 {
						t.Fatalf("写失败后不应重放成功正文或重复外发: status=%d calls=%d body=%q", replay.Code, calls.Load(), replay.Body.String())
					}
				}
				id := w.header.Get("x-litellm-call-id")
				if id == "" {
					t.Fatal("响应缺少调用 ID")
				}
				log := logDetail(t, h, admin, id)
				if log["status"] != "error" {
					t.Fatalf("同步写入失败被记为成功: status=%v spend=%v", log["status"], log["spend"])
				}
				if spend, ok := log["spend"].(float64); !ok || spend != 0 {
					t.Fatalf("失败请求应为零费用: %v", log["spend"])
				}
				key, err := h.db.GetKey(t.Context(), owner.keyID)
				if err != nil || key.Spend != 0 {
					t.Fatalf("写入失败不应扣减额度: key=%v err=%v", key, err)
				}
				assertUsageQueuesEmpty(t, h, dir)
				if n, err := h.db.Engine.Table("usage_delivery_pending").Count(); err != nil || n != 0 {
					t.Fatalf("写失败后待确认标记未清理: n=%d err=%v", n, err)
				}
				upstream := log["metadata"].(map[string]any)["upstream_response"].(map[string]any)
				failure := upstream["failure"].(map[string]any)
				if failure["phase"] != "client_write" || failure["kind"] != "client_write_error" {
					t.Fatalf("写入失败诊断错误: %v", failure)
				}
				recovered := h.do("POST", tc.path, owner.key, map[string]any{"model": "sync-write", "messages": []any{map[string]any{"role": "user", "content": "Recover."}}, "stream": false})
				if recovered.status != 200 || calls.Load() != 2 {
					t.Fatalf("客户端写失败导致健康上游不可用: calls=%d %s", calls.Load(), recovered.describe())
				}
			})
		}
	}
}

// TestSyncClientWriteFailureProtocols 验证 Responses 和 Messages 的普通与透传同步写失败均记零费用。
// 使用独立 PostgreSQL、真实网关及本机上游，并在真实响应写入器注入短写；测试自动清理账号、数据库和服务。
func TestSyncClientWriteFailureProtocols(t *testing.T) {
	for _, tc := range []struct{ path, transport, endpoint, provider, body string }{
		{"/v1/responses", "bypass_openai_chat", "responses", "openai", `{"model":"sync-protocol","input":"Verify write failure.","stream":false}`},
		{"/bypass/openai/v1/responses", "bypass_openai_responses", "bypass:openai-responses", "openai", `{"model":"sync-protocol","input":"Verify write failure.","stream":false}`},
		{"/v1/messages", "bypass_anthropic_messages", "messages", "anthropic", `{"model":"sync-protocol","max_tokens":32,"messages":[{"role":"user","content":"Verify write failure."}],"stream":false}`},
		{"/bypass/anthropic/v1/messages", "bypass_anthropic_messages", "bypass:anthropic-messages", "anthropic", `{"model":"sync-protocol","max_tokens":32,"messages":[{"role":"user","content":"Verify write failure."}],"stream":false}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
			t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
			dep := chatDeployment("sync-protocol")
			dep.LiteLLMParams["custom_llm_provider"] = tc.provider
			dep.ModelInfo["transport"], dep.ModelInfo["endpoint_types"] = tc.transport, []string{tc.endpoint}
			h := newHarness(t, dep)
			admin := h.adminSession()
			owner := h.openScope(t, admin, "sync-protocol")
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+owner.key)
			r.Header.Set("Content-Type", "application/json")
			w := &dialogueWriteProbe{header: make(http.Header), short: true}
			h.gw.Handler().ServeHTTP(w, r)
			id := w.Header().Get("x-litellm-call-id")
			log := logDetail(t, h, admin, id)
			if len(h.upstreamCalls()) != 1 || log["status"] != "error" || log["spend"] != float64(0) {
				t.Fatalf("协议写入失败未按零费用记录: calls=%d status=%v spend=%v", len(h.upstreamCalls()), log["status"], log["spend"])
			}
		})
	}
}

// TestSyncFailedDeliveryDatabaseDisconnect 验证上游返回前数据库断开且客户端写失败，重启后仍记失败和零费用。
// 前置隔离 PostgreSQL、独立磁盘队列和本机上游；新连接恢复后核对账单与唯一上游调用，测试数据自动清理。
func TestSyncFailedDeliveryDatabaseDisconnect(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	t.Setenv("XHUB_USAGE_SPOOL_DIR", dir)
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	h := newHarness(t, chatDeployment("sync-failed-disconnect"))
	t.Cleanup(func() { _ = h.gw.CloseUsageSpool() })
	owner := h.openScope(t, h.adminSession(), "sync-failed-disconnect")
	h.onUpstream("sync-failed-disconnect", func() { _ = h.db.Engine.Close() })
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"sync-failed-disconnect","messages":[{"role":"user","content":"Verify failed delivery."}]}`))
	req.Header.Set("Authorization", "Bearer "+owner.key)
	req.Header.Set("Content-Type", "application/json")
	w := &dialogueWriteProbe{header: make(http.Header)}
	h.gw.Handler().ServeHTTP(w, req)
	id := w.Header().Get("x-litellm-call-id")
	if id == "" || len(h.upstreamCalls()) != 1 {
		t.Fatal("缺少请求 ID 或上游调用次数不正确")
	}
	if err := h.gw.CloseUsageSpool(); err != nil {
		t.Fatal(err)
	}
	db, err := iam.Open(t.Context(), h.gw.Cfg.GeneralSettings.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fresh := gateway.New(h.gw.Cfg, h.store, db)
	t.Cleanup(func() {
		_ = fresh.CloseUsageSpool()
		if fresh.Live != nil {
			_ = fresh.Live.Close()
		}
	})
	for range 2 {
		fresh.FlushSpend()
	}
	event, err := db.GetUsageEvent(t.Context(), iam.UsageQuery{}, id)
	if err != nil || event.Status != "error" || event.Cost != 0 {
		t.Fatalf("断库写失败未恢复零费用: event=%+v err=%v", event, err)
	}
	key, err := db.GetKey(t.Context(), owner.keyID)
	if err != nil || key.Spend != 0 {
		t.Fatalf("写失败仍然扣费: err=%v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 0 {
		t.Fatalf("恢复后磁盘残留 %d", len(files))
	}
}

// disconnectOnSuccessfulWrite 在隔离回归中模拟客户端正文写出成功、随后数据库连接失败。
type disconnectOnSuccessfulWrite struct {
	*httptest.ResponseRecorder
	closeDB func()
}

// Write 返回完整响应字节，同时触发测试数据库故障；参数仅用于内存响应，返回实际写入结果。
func (w *disconnectOnSuccessfulWrite) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.closeDB()
	return n, err
}

// TestSyncSuccessWithoutSpoolDatabaseDisconnect 验证没有磁盘队列但配置 Redis 时，已确认响应在断库后仍可恢复正确费用。
// 使用隔离数据库、独立 Redis 及本机上游，关闭连接模拟故障，新实例恢复并清理测试数据。
func TestSyncSuccessWithoutSpoolDatabaseDisconnect(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
			t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
			isolatedSpendRedis(t)
			h := newHarness(t, chatDeployment("sync-success-disconnect"))
			admin := h.adminSession()
			owner := h.openScope(t, admin, "sync-success-disconnect")
			if cached {
				h.ok("POST", "/v1/chat/completions", owner.key, map[string]any{"model": "sync-success-disconnect", "messages": []any{map[string]any{"role": "user", "content": "Verify confirmed delivery recovery."}}})
				h.gw.FlushSpend()
			}
			before := h.moneyOf(t, owner)
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"sync-success-disconnect","messages":[{"role":"user","content":"Verify confirmed delivery recovery."}]}`))
			req.Header.Set("Authorization", "Bearer "+owner.key)
			req.Header.Set("Content-Type", "application/json")
			w := &disconnectOnSuccessfulWrite{ResponseRecorder: httptest.NewRecorder(), closeDB: func() { _ = h.db.Engine.Close() }}
			h.gw.Handler().ServeHTTP(w, req)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "regression-ok") {
				t.Fatal("测试需要客户端收到完整成功响应")
			}
			db, err := iam.Open(t.Context(), h.gw.Cfg.GeneralSettings.DatabaseURL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			h.db.Engine = db.Engine
			// 即使恢复时标记已到期，也必须优先处理 Redis 中已确认的成功终态。
			if _, err := db.Engine.Exec(`UPDATE usage_delivery_pending SET expires_at = now() - interval '1 second'`); err != nil {
				t.Fatal(err)
			}
			if n, err := db.RecoverExpiredDeliveries(t.Context(), time.Now(), 500); err != nil || n != 0 {
				t.Fatalf("恢复实例缺少 Redis 时不能误判已确认结果: n=%d err=%v", n, err)
			}
			// 模拟旧实现已观察到空队列、随后确认入队的时序：先运行到期恢复，也不能抢写零费用。
			if n, err := db.RecoverExpiredDeliveriesGuarded(t.Context(), time.Now(), 500, h.gw.Live.ReserveDeliveryRecovery); err != nil || n != 0 {
				t.Fatalf("Redis 中已确认的真实 HTTP 响应被未知恢复抢先结算: n=%d err=%v", n, err)
			}
			for range 2 {
				h.gw.FlushSpend()
			}
			wantCost := expectedCost()
			if cached {
				wantCost = 0
				if len(h.upstreamCalls()) != 1 {
					t.Fatalf("缓存请求不应再次调用上游: calls=%v", h.upstreamCalls())
				}
			}
			log := h.ok(http.MethodGet, "/spend/logs/ui/"+w.Header().Get("x-litellm-call-id"), admin, nil).json()
			if log["status"] != "success" || !nearlyEqual(log["spend"].(float64), wantCost) {
				t.Fatalf("HTTP 日志必须显示已确认费用: %+v", log)
			}
			if after := h.moneyOf(t, owner); !after.grewBy(before, wantCost) {
				t.Fatalf("已确认成功但断库后费用未恢复: before=%+v after=%+v", before, after)
			}
		})
	}
}

// TestSyncUnknownRecoveryWaitsForCorruptQueue 验证 Redis 队列损坏时不能将到期待确认账单误判为零费用。
// 前置私有 PostgreSQL 和独立 Redis，修复队列后才允许未知状态恢复；测试结束自动清理资源。
func TestSyncUnknownRecoveryWaitsForCorruptQueue(t *testing.T) {
	t.Run("recovery_transaction_rollback", testSyncRecoveryRollbackAndRetry)
	t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	rdb := isolatedSpendRedis(t)
	h := newHarness(t, chatDeployment("sync-unknown-queue"))
	rec := iam.UsageRecord{RequestID: "unknown-queue", TS: time.Now().UTC(), Model: "sync-unknown-queue", CallType: "chat", Status: "success", Cost: 0.1}
	if err := h.db.PrepareDelivery(t.Context(), rec, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Engine.Exec(`UPDATE usage_delivery_pending SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(t.Context(), "xhub:spendlog", "broken", 0).Err(); err != nil {
		t.Fatal(err)
	}
	h.gw.FlushSpend()
	if n, err := h.db.Engine.Table("usage_delivery_pending").Count(); err != nil || n != 1 {
		t.Fatalf("队列损坏时不能恢复未知结果: n=%d err=%v", n, err)
	}
	if err := rdb.Del(t.Context(), "xhub:spendlog").Err(); err != nil {
		t.Fatal(err)
	}
	h.gw.FlushSpend()
	event, err := h.db.GetUsageEvent(t.Context(), iam.UsageQuery{}, rec.RequestID)
	if err != nil || event.Status != "error" || event.Cost != 0 {
		t.Fatalf("队列恢复后应记录零费用未知结果: %+v %v", event, err)
	}
}

// TestSyncDeliveryConfirmedSpoolRecovery 验证响应已经写出而账单队列暂不可写时，磁盘快照重启后只结算一次。
// 使用隔离数据库、真实 HTTP 网关及本机上游；临时故障约束与独立磁盘目录由测试清理。
func TestSyncDeliveryConfirmedSpoolRecovery(t *testing.T) {
	t.Run("confirmed_pending_clears_disk", testSyncPendingConfirmationClearsDisk)
	dir := filepath.Join(t.TempDir(), "spool")
	t.Setenv("XHUB_USAGE_SPOOL_DIR", dir)
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	h := newHarness(t, chatDeployment("sync-recover"))
	t.Cleanup(func() { _ = h.gw.CloseUsageSpool() })
	admin := h.adminSession()
	owner := h.openScope(t, admin, "sync-recover")
	before := h.moneyOf(t, owner)
	h.onUpstream("sync-recover", func() {
		if _, err := h.db.Engine.Exec("ALTER TABLE usage_pending ADD CONSTRAINT test_delivery_reject CHECK (false)"); err != nil {
			t.Error(err)
		}
	})
	response := h.ok(http.MethodPost, "/v1/chat/completions", owner.key, chatRequest("sync-recover", "Verify delivery recovery."))
	callID := response.header("x-litellm-call-id")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("已确认响应未保存在磁盘: files=%d err=%v", len(files), err)
	}
	snapshot, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if after := h.moneyOf(t, owner); after.key != before.key {
		t.Fatalf("数据库故障时提前扣费: before=%v after=%v", before.key, after.key)
	}
	if _, err := h.db.Engine.Exec("ALTER TABLE usage_pending DROP CONSTRAINT test_delivery_reject"); err != nil {
		t.Fatal(err)
	}
	if err := h.gw.CloseUsageSpool(); err != nil {
		t.Fatal(err)
	}
	fresh := gateway.New(h.gw.Cfg, h.store, h.db)
	t.Cleanup(func() {
		_ = fresh.CloseUsageSpool()
		if fresh.Live != nil {
			_ = fresh.Live.Close()
		}
	})
	if err := os.WriteFile(files[0], snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		fresh.FlushSpend()
	}
	if after := h.moneyOf(t, owner); !after.grewBy(before, expectedCost()) {
		t.Fatalf("恢复未唯一结算: before=%+v after=%+v", before, after)
	}
	if n, err := h.db.Engine.Table("usage_events").Where("request_id = ?", callID).Count(); err != nil || n != 1 {
		t.Fatalf("恢复账单数量错误: n=%d err=%v", n, err)
	}
	if len(h.upstreamCalls()) != 1 {
		t.Fatal("恢复时重复调用上游")
	}
	assertUsageQueuesEmpty(t, h, dir)
	if n, err := h.db.Engine.Table("usage_delivery_pending").Count(); err != nil || n != 0 {
		t.Fatalf("确认后标记未清理: n=%d err=%v", n, err)
	}
}

// TestSyncConfirmedSpoolRecoveryAcrossInstances 验证其他实例不能先把已保存到原实例磁盘的成功响应恢复成零费用。
// 前置私有 PostgreSQL、两个独立磁盘队列及本机上游，注入数据库暂时不可写；测试自动回收所有资源。
func TestSyncConfirmedSpoolRecoveryAcrossInstances(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "first")
	t.Setenv("XHUB_USAGE_SPOOL_DIR", dir)
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	h := newHarness(t, chatDeployment("sync-spool-multi"))
	t.Cleanup(func() { _ = h.gw.CloseUsageSpool() })
	owner := h.openScope(t, h.adminSession(), "sync-spool-multi")
	before := h.moneyOf(t, owner)
	h.onUpstream("sync-spool-multi", func() {
		if _, err := h.db.Engine.Exec("ALTER TABLE usage_pending ADD CONSTRAINT test_delivery_cross_reject CHECK (false)"); err != nil {
			t.Error(err)
		}
	})
	r := h.ok("POST", "/v1/chat/completions", owner.key, chatRequest("sync-spool-multi", "Verify cross-instance recovery."))
	id := r.header("x-litellm-call-id")
	if _, err := h.db.Engine.Exec("ALTER TABLE usage_pending DROP CONSTRAINT test_delivery_cross_reject"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Engine.Exec(`UPDATE usage_delivery_pending SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XHUB_USAGE_SPOOL_DIR", filepath.Join(t.TempDir(), "second"))
	other := gateway.New(h.gw.Cfg, h.store, h.db)
	t.Cleanup(func() {
		_ = other.CloseUsageSpool()
		if other.Live != nil {
			_ = other.Live.Close()
		}
	})
	other.FlushSpend()
	// 原实例重启后持有同一磁盘卷，恢复自己的已确认终态。
	if err := h.gw.CloseUsageSpool(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XHUB_USAGE_SPOOL_DIR", dir)
	fresh := gateway.New(h.gw.Cfg, h.store, h.db)
	t.Cleanup(func() {
		_ = fresh.CloseUsageSpool()
		if fresh.Live != nil {
			_ = fresh.Live.Close()
		}
	})
	fresh.FlushSpend()
	after := h.moneyOf(t, owner)
	if !after.grewBy(before, expectedCost()) {
		t.Fatalf("其他实例提前恢复导致成功账单丢失: id=%s before=%+v after=%+v", id, before, after)
	}
}

// TestSyncClientWriteNetworkErrorPrivacy 验证真实网关写入错误日志不会泄露 TCP 连接双方地址。
// 使用隔离 PostgreSQL 和本地上游，注入带虚拟地址的写失败，检查错误分类与零费用；夹具清理所有资源。
func TestSyncClientWriteNetworkErrorPrivacy(t *testing.T) {
	t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	h := newHarness(t, chatDeployment("sync-network-privacy"))
	admin := h.adminSession()
	owner := h.openScope(t, admin, "sync-network-privacy")
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"sync-network-privacy","messages":[{"role":"user","content":"Hello"}],"stream":false}`))
	req.Header.Set("Authorization", "Bearer "+owner.key)
	req.Header.Set("Content-Type", "application/json")
	failure := &net.OpError{Op: "write", Net: "tcp", Source: &net.TCPAddr{IP: net.ParseIP("10.20.30.40"), Port: 1234}, Addr: &net.TCPAddr{IP: net.ParseIP("198.51.100.123"), Port: 5678}, Err: errors.New("broken pipe")}
	w := &dialogueWriteProbe{header: make(http.Header), err: failure}
	h.gw.Handler().ServeHTTP(w, req)
	log := logDetail(t, h, admin, w.header.Get("x-litellm-call-id"))
	if log["status"] != "error" || parseFloatOrZero(fmt.Sprint(log["spend"])) != 0 {
		t.Fatalf("失败响应不应收费: %v", log)
	}
	detail := fmt.Sprint(log["metadata"], log["error"])
	if strings.Contains(detail, "10.20.30.40") || strings.Contains(detail, "198.51.100.123") || !strings.Contains(detail, "client_write_error") {
		t.Fatalf("写失败诊断泄露地址或分类缺失: %s", detail)
	}
}

// TestSyncMissingDeliveryDoesNotBlockBilling 验证缺失交付标记的冲突快照不阻塞后续真实请求的账单。
// 使用隔离 PostgreSQL、Redis、网关及本地上游，保留故障快照并验证正常请求准确结算；夹具自动清理全部资源。
func TestSyncMissingDeliveryDoesNotBlockBilling(t *testing.T) {
	t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
	isolatedSpendRedis(t)
	const model = "sync-missing-delivery"
	h := newHarness(t, chatDeployment(model))
	admin := h.adminSession()
	owner := h.openScope(t, admin, model)
	before := h.moneyOf(t, owner)
	missing := iam.UsageRecord{RequestID: "missing-delivery-fixture", UserID: owner.userID, OwnerType: iam.OwnerPersonal, TS: time.Now().UTC(), Model: model, CallType: "chat", Status: "success", HTTPStatus: 200, Cost: 0.25}
	payload, err := json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	row := live.SpendLog{RequestID: missing.RequestID, UserID: missing.UserID, Spend: missing.Cost, SpendValid: true, Start: missing.TS.Format(time.RFC3339Nano), DeliverySnapshot: string(payload)}
	if err := h.gw.Live.EnqueueConfirmedDelivery(row); err != nil {
		t.Fatal(err)
	}
	response := h.ok(http.MethodPost, "/v1/chat/completions", owner.key, chatRequest(model, "Verify billing after a conflicting snapshot."))
	for range 5 {
		h.gw.FlushSpend()
	}
	event, err := h.db.GetUsageEvent(t.Context(), iam.UsageQuery{}, response.header("x-litellm-call-id"))
	if err != nil || event.Status != "success" || event.Cost <= 0 {
		t.Fatalf("正常请求账单被冲突快照阻塞: event=%+v err=%v", event, err)
	}
	if after := h.moneyOf(t, owner); !after.grewBy(before, event.Cost) {
		t.Fatalf("正常请求结算错误: before=%+v after=%+v cost=%v", before, after, event.Cost)
	}
	rows, _ := h.gw.Live.PeekLogs(10)
	if len(rows) != 1 || rows[0].RequestID != missing.RequestID {
		t.Fatalf("冲突证据应保留: %+v", rows)
	}
}

// testSyncRecoveryRollbackAndRetry 验证恢复权已写 Redis 后 PostgreSQL 失败不会发布零费用账单，重试仍能唯一恢复。
// 使用隔离数据库、Redis、真实日志 HTTP 接口和本机供应商；测试框架清理 schema、服务和缓存。
func testSyncRecoveryRollbackAndRetry(t *testing.T) {
	t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
	isolatedSpendRedis(t)
	h := newHarness(t, chatDeployment("recovery-rollback"))
	admin := h.adminSession()
	record := iam.UsageRecord{RequestID: "recovery-rollback", TS: time.Now().UTC(), Model: "recovery-rollback", CallType: "chat", Status: "success", HTTPStatus: 200, Cost: 0.25, DeliveryRedisGuard: true}
	if err := h.db.PrepareDelivery(t.Context(), record, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Engine.Exec(`UPDATE usage_delivery_pending SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Engine.Exec(`ALTER TABLE usage_pending ADD CONSTRAINT reject_recovery CHECK (false)`); err != nil {
		t.Fatal(err)
	}
	if n, err := h.db.RecoverExpiredDeliveriesGuarded(t.Context(), time.Now(), 10, h.gw.Live.ReserveDeliveryRecovery); err == nil || n != 0 {
		t.Fatalf("数据库失败不能报告已恢复: %d %v", n, err)
	}
	if n, err := h.db.Engine.Table("usage_events").Count(); err != nil || n != 0 {
		t.Fatalf("事务失败不得发布账单: %d %v", n, err)
	}
	if n, err := h.db.Engine.Table("usage_delivery_pending").Count(); err != nil || n != 1 {
		t.Fatalf("事务回滚必须保留恢复标记: %d %v", n, err)
	}
	if _, err := h.db.Engine.Exec(`ALTER TABLE usage_pending DROP CONSTRAINT reject_recovery`); err != nil {
		t.Fatal(err)
	}
	h.gw.FlushSpend()
	h.gw.FlushSpend()
	log := h.ok(http.MethodGet, "/spend/logs/ui/"+record.RequestID, admin, nil).json()
	if log["status"] != "error" || log["spend"] != float64(0) {
		t.Fatalf("重试后未知结果必须唯一恢复为失败零费用: %+v", log)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.gw.Live.EnqueueConfirmedDelivery(live.SpendLog{RequestID: record.RequestID, Spend: record.Cost, SpendValid: true, DeliverySnapshot: string(raw)}); err == nil {
		t.Fatal("未知结果已完成恢复后，迟到快照不能被声称已确认")
	}
	if n, err := h.db.Engine.Table("usage_events").Count(); err != nil || n != 1 {
		t.Fatalf("不得重复恢复或重复计费: %d %v", n, err)
	}
}

// testUnpreparedFailureDoesNotCleanDelivery 验证未进入同步交付的 HTTP 错误不会额外写交付表。
// 前置私有 PG 和真实路由，以语句级触发器观察 DELETE（即便没有命中行），schema 删除时回收全部探针。
func testUnpreparedFailureDoesNotCleanDelivery(t *testing.T) {
	h := newHarness(t, chatDeployment("unprepared-error"))
	for _, sql := range []string{
		`CREATE TABLE delivery_cleanup_probe (calls int)`,
		`CREATE FUNCTION observe_delivery_cleanup() RETURNS trigger AS $$ BEGIN INSERT INTO delivery_cleanup_probe VALUES (1); RETURN NULL; END; $$ LANGUAGE plpgsql`,
		`CREATE TRIGGER observe_delivery_cleanup AFTER DELETE ON usage_delivery_pending FOR EACH STATEMENT EXECUTE FUNCTION observe_delivery_cleanup()`,
	} {
		if _, err := h.db.Engine.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	response := h.do(http.MethodPost, "/v1/chat/completions", "", chatRequest("unprepared-error", "Verify authentication failure."))
	if response.status != 401 {
		t.Fatalf("预期未鉴权失败: %d %s", response.status, response.text())
	}
	if n, err := h.db.Engine.Table("delivery_cleanup_probe").Count(); err != nil || n != 0 {
		t.Fatalf("未准备交付的错误不应执行清理写入: n=%d err=%v", n, err)
	}
}

// testSyncPendingConfirmationClearsDisk 验证真实同步调用已在 PG 确认后，磁盘超限及最终入账失败不会遗留未知标记。
// 隔离磁盘限制小于完整费用快照，故障仅作用于私有表；恢复后通过日志 HTTP 接口和各层消费核对唯一费用，夹具自动清理。
func testSyncPendingConfirmationClearsDisk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	t.Setenv("XHUB_USAGE_SPOOL_DIR", dir)
	t.Setenv("XHUB_USAGE_SPOOL_RECORD_BYTES", "2048")
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	h := newHarness(t, chatDeployment("sync-pending-confirmed"))
	t.Cleanup(func() { _ = h.gw.CloseUsageSpool() })
	admin := h.adminSession()
	owner := h.openScope(t, admin, "sync-pending-confirmed")
	before := h.moneyOf(t, owner)
	h.onUpstream("sync-pending-confirmed", func() {
		if _, err := h.db.Engine.Exec(`ALTER TABLE usage_events ADD CONSTRAINT reject_sync_event CHECK (cost=0)`); err != nil {
			t.Error(err)
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"sync-pending-confirmed","messages":[{"role":"user","content":"Verify pending confirmed charge."}]}`))
	req.Header.Set("Authorization", "Bearer "+owner.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Xhub-Label", strings.Repeat("x", 2048))
	w := httptest.NewRecorder()
	h.gw.Handler().ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "regression-ok") {
		t.Fatalf("请求必须真实成功: %d %s", w.Code, w.Body.String())
	}
	id := w.Header().Get("x-litellm-call-id")
	if n, err := h.db.Engine.Table("usage_pending").Where("request_id=?", id).Count(); err != nil || n != 1 {
		t.Fatalf("确认费用必须在 PG 保留: %d %v", n, err)
	}
	if files, err := filepath.Glob(filepath.Join(dir, "*.json")); err != nil || len(files) != 0 {
		t.Fatalf("已确认后不能残留磁盘未知标记: %v %v", files, err)
	}
	if _, err := h.db.Engine.Exec(`ALTER TABLE usage_events DROP CONSTRAINT reject_sync_event`); err != nil {
		t.Fatal(err)
	}
	h.gw.FlushSpend()
	h.gw.FlushSpend()
	log := h.ok(http.MethodGet, "/spend/logs/ui/"+id, admin, nil).json()
	if log["status"] != "success" || !nearlyEqual(log["spend"].(float64), expectedCost()) {
		t.Fatalf("已确认费用在恢复后错误: %+v", log)
	}
	if after := h.moneyOf(t, owner); !after.grewBy(before, expectedCost()) {
		t.Fatalf("各层消费应且仅应增加一次: before=%+v after=%+v", before, after)
	}
}
