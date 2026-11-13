package regression

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/provider/probe"
	"github.com/sunqirui1987/xhub/internal/testutil/protocolfixture"
)

// TestProtocolProbeLifecycle 使用真实网关、隔离 PostgreSQL 和三协议上游验证保存后自动探测、手动探测、权限、失效及透传数据面。
// 不依赖商业密钥；harness 清理账号/数据库，defer 关闭独占上游；不改变预算规则或普通路由。
func TestProtocolProbeLifecycle(t *testing.T) {
	var calls atomic.Int32
	var failure atomic.Bool
	var limited atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if failure.Load() {
			w.WriteHeader(503)
			return
		}
		if limited.Load() {
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			protocol := map[string]string{"/v1/chat/completions": "openai-chat", "/v1/responses": "openai-responses", "/v1/messages": "anthropic-messages"}[r.URL.Path]
			d := protocolfixture.Reply(protocol, b)
			switch protocol {
			case "openai-chat":
				d["choices"].([]any)[0].(map[string]any)["finish_reason"] = "length"
			case "openai-responses":
				d["status"] = "incomplete"
				d["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
			default:
				d["stop_reason"] = "max_tokens"
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(d)
			return
		}
		protocolfixture.Serve(w, r)
	}))
	defer up.Close()
	c := newModelContractClient(t)
	h := c.h
	admin := c.admin
	tenant := h.newTenant(t, admin, "protocol-probe-member")
	member := h.signIn(t, tenant)
	name := "protocol-probe-regression"
	id := "probe-deployment"
	body := map[string]any{"model_name": name, "litellm_params": map[string]any{"model": "test", "deployment_id": "separate-router-id", "api_base": up.URL, "api_key": "fixture-protocol-key", "input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002}, "model_info": map[string]any{"id": id, "transport": "bypass_openai_responses", "auto_probe": true, "protocol_probe": map[string]any{"verified": map[string]any{"openai-chat": true}}}}
	created := h.ok("POST", "/model/new", admin, body).json()
	if created["model_info"].(map[string]any)["protocol_probe"] != nil {
		t.Fatal("客户端伪造结果被保存")
	}
	for _, key := range []string{"", member} {
		res := h.do("POST", "/model/protocol_probe", key, map[string]any{"id": id})
		if res.status != 401 && res.status != 403 {
			t.Fatalf("越权探测: %s", res.describe())
		}
	}
	if got := h.do("POST", "/model/protocol_probe", admin, map[string]any{"id": "missing"}); got.status != 404 {
		t.Fatal(got.describe())
	}
	h.gw.ProbeChangedModels(context.Background())
	if got := calls.Load(); got != 12 {
		t.Fatalf("保存变更没有完成三协议四步验证: %d", got)
	}
	h.gw.ProbeChangedModels(context.Background())
	if calls.Load() != 12 {
		t.Fatal("配置未变化仍重复付费探测")
	}
	info := h.ok("GET", "/v2/model/info?modelId="+id, admin, nil)
	if strings.Contains(string(info.body), "signature") || strings.Contains(string(info.body), "fixture-protocol-key") {
		t.Fatal("探测证明或密钥暴露")
	}
	// 用普通个人会话实际穿过预算、选路和结算链；三个原生入口不能只在目录中出现。
	payer := h.personalBillingSession()
	for _, path := range []string{"/bypass/openai/v1/chat/completions", "/bypass/openai/v1/responses", "/bypass/anthropic/v1/messages"} {
		for _, streaming := range []bool{false, true} {
			request := map[string]any{"model": name, "stream": streaming, "messages": []any{map[string]any{"role": "user", "content": "Reply with PROBE_OK."}}, "max_tokens": 256}
			if strings.HasSuffix(path, "/responses") {
				delete(request, "messages")
				request["input"] = []any{map[string]any{"role": "user", "content": "Reply with PROBE_OK."}}
				request["max_output_tokens"] = 256
				delete(request, "max_tokens")
			}
			reply := h.ok("POST", path, payer, request)
			if !strings.Contains(string(reply.body), "PROBE_OK") {
				t.Fatalf("原生入口没有真实回复: %s", reply.describe())
			}
		}
	}
	// 探测器作为真实协议客户端再经过新增透传入口，覆盖工具调用、关联 ID 和结果回传。
	for _, protocol := range probe.Protocols() {
		base := h.server.URL + "/bypass/openai/v1"
		if protocol == "anthropic-messages" {
			base = h.server.URL + "/bypass/anthropic/v1"
		}
		result := probe.Run(t.Context(), http.DefaultClient, config.ModelEntry{LiteLLMParams: map[string]any{"model": name, "api_base": base, "api_key": payer}}, protocol)
		if result.Status != "verified" {
			t.Fatalf("网关透传完整工具协议失败: %+v", result)
		}
	}
	if rows := logsFor(h.spendLogs(t, payer), name); len(rows) != 18 {
		t.Fatalf("原生入口未生成十八条真实账单: %d", len(rows))
	}
	failure.Store(true)
	result := c.call("POST", "/model/protocol_probe", admin, map[string]any{"id": id}, 200, true).json()
	proof := result["model_info"].(map[string]any)["protocol_probe"].(map[string]any)
	if proof["verified"].(map[string]any)["openai-chat"] != true {
		t.Fatal("暂时故障撤销已验证能力")
	}
	failure.Store(false)
	limited.Store(true)
	uncertain := h.ok("POST", "/model/protocol_probe", admin, map[string]any{"id": id}).json()["model_info"].(map[string]any)["protocol_probe"].(map[string]any)
	if len(uncertain["verified"].(map[string]any)) != 3 {
		t.Fatal("测试输出不足撤销了此前成功验证")
	}
	for _, r := range uncertain["results"].([]any) {
		if r.(map[string]any)["status"] != "inconclusive" {
			t.Fatal("测试上限未标记为不确定")
		}
	}
	limited.Store(false)
	// 模型管理字段必须留在服务器生成；只改价格不会重复验证。
	h.ok("POST", "/model/update", admin, map[string]any{"id": id, "litellm_params": map[string]any{"input_cost_per_token": 0.000003}, "model_info": map[string]any{"protocol_probe": nil}})
	before := calls.Load()
	h.gw.ProbeChangedModels(context.Background())
	if calls.Load() != before {
		t.Fatal("价格修改触发重复探测")
	}
	h.ok("POST", "/model/update", admin, map[string]any{"id": id, "litellm_params": map[string]any{"api_key": "rotated"}})
	h.gw.ProbeChangedModels(context.Background())
	result = h.ok("GET", "/v2/model/info?modelId="+id, admin, nil).json()
	rows := result["data"].([]any)
	proof = rows[0].(map[string]any)["model_info"].(map[string]any)["protocol_probe"].(map[string]any)
	if len(proof["verified"].(map[string]any)) != 0 {
		t.Fatal("新密钥未通过仍保留旧连接能力")
	}
	h.ok("POST", "/model/delete", admin, map[string]any{"id": id})
	if got := h.do("POST", "/model/protocol_probe", admin, map[string]any{"id": id}); got.status != 404 {
		t.Fatal("删除后仍能探测")
	}
}

// TestProtocolProbeConcurrentEditAndStorageFailure 在真实路由及 PostgreSQL 上阻塞上游，验证重复探测互斥、配置变化丢弃结果。
// 另注入数据库写入故障，验证不发布能力且自动循环不重复付费，手动可恢复；harness 回收数据库和服务。
func TestProtocolProbeConcurrentEditAndStorageFailure(t *testing.T) {
	h := newHarness(t)
	admin := h.adminSession()
	var calls atomic.Int32
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var unblock sync.Once
	var block atomic.Bool
	block.Store(true)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if block.Load() {
			entered <- struct{}{}
			<-release
		}
		protocolfixture.Serve(w, r)
	}))
	defer up.Close()
	defer unblock.Do(func() { close(release) })
	id := "probe-race"
	h.ok("POST", "/model/new", admin, map[string]any{"model_name": "probe-race", "litellm_params": map[string]any{"model": "test", "api_base": up.URL, "api_key": "fixture-protocol-key", "input_cost_per_token": 0.000001}, "model_info": map[string]any{"id": id, "transport": "bypass_openai_responses", "auto_probe": true}})
	done := make(chan int, 1)
	go func() { done <- h.do("POST", "/model/protocol_probe", admin, map[string]any{"id": id}).status }()
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("上游探测未到达")
		}
	}
	if got := h.do("POST", "/model/protocol_probe", admin, map[string]any{"id": id}); got.status != 409 {
		t.Fatal("重复探测未阻断")
	}
	h.ok("POST", "/model/update", admin, map[string]any{"id": id, "litellm_params": map[string]any{"model": "test-updated"}})
	block.Store(false)
	unblock.Do(func() { close(release) })
	if status := <-done; status != 409 {
		t.Fatalf("旧配置结果未丢弃: %d", status)
	}
	if strings.Contains(string(h.ok("GET", "/v2/model/info?modelId="+id, admin, nil).body), "verified") {
		t.Fatal("旧证明被发布")
	}
	// 独立 schema 中注入失败，只阻止探测结果写入。
	_, err := h.gw.Store.DB.Exec(`CREATE FUNCTION reject_probe() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe storage failure'; END $$; CREATE TRIGGER reject_probe BEFORE UPDATE ON proxy_models FOR EACH ROW EXECUTE FUNCTION reject_probe()`)
	if err != nil {
		t.Fatal(err)
	}
	h.gw.ProbeChangedModels(context.Background())
	before := calls.Load()
	h.gw.ProbeChangedModels(context.Background())
	if calls.Load() != before {
		t.Fatal("存储失败导致持续上游探测")
	}
	if strings.Contains(string(h.ok("GET", "/v2/model/info?modelId="+id, admin, nil).body), "verified") {
		t.Fatal("写入失败发布能力")
	}
	if _, err = h.gw.Store.DB.Exec(`DROP TRIGGER reject_probe ON proxy_models; DROP FUNCTION reject_probe()`); err != nil {
		t.Fatal(err)
	}
	h.ok("POST", "/model/protocol_probe", admin, map[string]any{"id": id})
	if calls.Load() != before+12 {
		t.Fatal("手动恢复未重新验证")
	}
	h.ok("POST", "/model/delete", admin, map[string]any{"id": id})
}

// TestProtocolProbeCredentialRotation 前置真实数据库凭据、关闭自动探测的部署及三协议上游；验证关闭不发请求、手动可用、凭据轮换立即撤销旧证明。
// 随后开启自动探测验证新连接，且错误不重复计费探测；测试结束删除部署及凭据，harness 回收 schema。
func TestProtocolProbeCredentialRotation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminSession()
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); protocolfixture.Serve(w, r) }))
	defer up.Close()
	h.ok("POST", "/credentials", admin, map[string]any{"credential_name": "probe-rotation", "credential_info": map[string]any{"custom_llm_provider": "openai"}, "credential_values": map[string]any{"api_base": up.URL, "api_key": "fixture-protocol-key"}})
	id := "probe-rotation"
	h.ok("POST", "/model/new", admin, map[string]any{"model_name": id, "litellm_params": map[string]any{"model": "test", "litellm_credential_name": id, "custom_llm_provider": "openai", "input_cost_per_token": 0.000001}, "model_info": map[string]any{"id": id, "transport": "bypass_openai_responses"}})
	h.gw.ProbeChangedModels(t.Context())
	if calls.Load() != 0 {
		t.Fatal("未开启也发出了探测")
	}
	if r := h.do("POST", "/model/update", admin, map[string]any{"id": id, "model_info": map[string]any{"auto_probe": "true"}}); r.status != 400 {
		t.Fatal("自动探测开关接受非布尔值")
	}
	oldLimit := h.gw.Store.DB.Stats().MaxOpenConnections
	h.gw.Store.DB.SetMaxOpenConns(1)
	h.ok("POST", "/model/protocol_probe", admin, map[string]any{"id": id})
	h.gw.Store.DB.SetMaxOpenConns(oldLimit)
	if !strings.Contains(string(h.ok("GET", "/model/available", admin, nil).body), "/bypass/anthropic/v1/messages") {
		t.Fatal("成功协议未加入可用入口")
	}
	h.ok("PATCH", "/credentials/"+id, admin, map[string]any{"credential_values": map[string]any{"api_base": up.URL, "api_key": "rotated-key"}})
	if strings.Contains(string(h.ok("GET", "/v2/model/info?modelId="+id, admin, nil).body), "verified") {
		t.Fatal("已轮换连接仍展示旧证明")
	}
	if strings.Contains(string(h.ok("GET", "/model_group/info", admin, nil).body), "/bypass/anthropic/v1/messages") {
		t.Fatal("调试台显示轮换后的旧验证入口")
	}
	payer := h.personalBillingSession()
	rejected := h.do("POST", "/bypass/anthropic/v1/messages", payer, map[string]any{"model": id, "max_tokens": 20, "messages": []any{map[string]any{"role": "user", "content": "Hello"}}})
	if rejected.status < 400 {
		t.Fatal("凭据轮换后旧证明仍允许调用")
	}
	before := calls.Load()
	h.ok("POST", "/model/update", admin, map[string]any{"id": id, "model_info": map[string]any{"auto_probe": true}})
	h.gw.ProbeChangedModels(t.Context())
	if calls.Load() != before+3 {
		t.Fatal("新连接未重新验证三种协议")
	}
	h.gw.ProbeChangedModels(t.Context())
	if calls.Load() != before+3 {
		t.Fatal("相同失败连接重复自动探测")
	}
	h.ok("POST", "/model/delete", admin, map[string]any{"id": id})
	h.ok("DELETE", "/credentials/"+id, admin, nil)
}

// TestProtocolProbeSlowStorageDoesNotLockModels 在真实 PostgreSQL 锁住模型行，确认探测等待写库时正常模型目录仍可读取。
// 使用本地协议上游；事务回滚、租约释放与 harness 清理确保无残留，不依赖外部凭据。
func TestProtocolProbeSlowStorageDoesNotLockModels(t *testing.T) {
	h := newHarness(t)
	admin := h.adminSession()
	up := httptest.NewServer(http.HandlerFunc(protocolfixture.Serve))
	defer up.Close()
	id := "probe-slow-storage"
	h.ok("POST", "/model/new", admin, map[string]any{"model_name": id, "litellm_params": map[string]any{"model": "test", "api_base": up.URL, "api_key": "fixture-protocol-key"}, "model_info": map[string]any{"id": id, "transport": "bypass_openai_responses"}})
	tx, err := h.gw.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT id FROM proxy_models WHERE id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- h.do("POST", "/model/protocol_probe", admin, map[string]any{"id": id}).status }()
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		err = h.gw.Store.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'UPDATE proxy_models SET model_info_json=jsonb_set%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("未观察到探测结果等待数据库行锁")
	}
	readable := make(chan struct{})
	go func() { h.gw.Models(); close(readable) }()
	select {
	case <-readable:
	case <-time.After(time.Second):
		t.Fatal("探测写库持有全局模型锁")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 200 {
		t.Fatalf("解锁后探测未完成: %d", code)
	}
	var leases int
	if err = h.gw.Store.DB.QueryRow(`SELECT count(*) FROM kv WHERE kind='protocol_probe_lease'`).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("探测租约未清理: %d %v", leases, err)
	}
	h.ok("POST", "/model/delete", admin, map[string]any{"id": id})
}
