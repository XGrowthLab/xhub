// 本文件验证 execution 的相关执行契约，阅读顺序为测试宿主/辅助方法，再到各个 Test 场景。
// 具体前置、期望、边界和清理方式见每个测试方法；夹具的空实现仅用于隔离当前测试。
package execution

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/auth"
	"github.com/sunqirui1987/xhub/internal/config"
	dptransport "github.com/sunqirui1987/xhub/internal/dataplane/transport"
	providercontract "github.com/sunqirui1987/xhub/internal/provider/contract"
	"github.com/sunqirui1987/xhub/internal/router"
)

// officialHost 初始化适用于原生任务测试的逻辑宿主。
// 参数 up 为本地 HTTP 服务，models 为显式测试部署；返回使用 up.Client 的宿主并初始化任务归属表。
// 仅初始化内存，不启动或关闭服务器；所属测试负责 Close，不访问真实供应商。
func officialHost(up *httptest.Server, models ...config.ModelEntry) *logicHost {
	return &logicHost{cfg: &config.Config{}, client: up.Client(), models: models, pins: map[string]string{}}
}

// TestOfficialModelDefaultWeightsAndCredentialPin 验证模型默认权重选中凭据并在任务查询时保持该凭据。
// 参数 t：测试上下文；返回：无。前置本地上游与两条部署；断言创建和查询一致，服务关闭清理，无外部调用。
func TestOfficialModelDefaultWeightsAndCredentialPin(t *testing.T) {
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		io.WriteString(w, `{"id":"task-1"}`)
	}))
	defer up.Close()
	a := Deployment("video", "volcengine/model", "key-a", up.URL, "ark_contents_generation", map[string]any{"litellm_credential_name": "a", "weight": 0.0, "deployment_id": "a"})
	b := Deployment("video", "volcengine/model", "key-b", up.URL, "ark_contents_generation", map[string]any{"litellm_credential_name": "b", "weight": 1.0, "deployment_id": "b"})
	h := officialHost(up, a, b)
	h.modelDefaults = map[string]any{"video": map[string]any{"allocations": []any{
		map[string]any{"deployment_id": router.DeploymentID(a), "weight": 0.0},
		map[string]any{"deployment_id": router.DeploymentID(b), "weight": 100.0},
	}}}

	if rec := h.call(t, http.MethodPost, "/api/v3/contents/generations/tasks", `{"model":"video"}`); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if h.spend[0].depID != router.CooldownID(b) {
		t.Fatalf("wrong credential recorded: %+v", h.spend)
	}
	h.modelDefaults = nil
	if rec := h.call(t, http.MethodGet, "/api/v3/contents/generations/tasks/task-1", ""); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if len(seen) != 2 || seen[0] != "Bearer key-b" || seen[1] != "Bearer key-b" {
		t.Fatalf("task credential changed: %v", seen)
	}
}

// TestOfficialTemplateTrafficSplitUsesLiveModelWeights 验证请求模板只选调度策略，分流采用当前模型默认权重，零权重部署不会接收请求。
// 参数 t 为 Go 测试上下文；返回值无，断言失败定位具体协议、路由或日志行为。
// 前置使用独立测试配置及本地 HTTP 假上游，不依赖外部供应商凭据。
// 服务器通过 defer/Cleanup 关闭，测试状态只存在本例内存，不产生持久化数据。
func TestOfficialTemplateTrafficSplitUsesLiveModelWeights(t *testing.T) {
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		io.WriteString(w, "{\"id\":\"task-model-policy\"}")
	}))
	defer up.Close()
	a := Deployment("video", "provider/model-a", "key-a", up.URL, "ark_contents_generation", map[string]any{"weight": 9.0, "deployment_id": "model-a"})
	b := Deployment("video", "provider/model-b", "key-b", up.URL, "ark_contents_generation", map[string]any{"weight": 1.0, "deployment_id": "model-b"})
	h := officialHost(up, a, b)
	h.settings = map[string]any{
		"model_routes": []any{map[string]any{"model": "video", "strategy": "traffic-split"}},
		"retry_policy": map[string]any{"max_attempts": 1, "timeout_seconds": 60, "failure_threshold": 3, "cooldown_seconds": 0},
	}
	h.modelDefaults = map[string]any{"video": map[string]any{"allocations": []any{
		map[string]any{"deployment_id": "model-a", "weight": 0.0},
		map[string]any{"deployment_id": "model-b", "weight": 100.0},
	}}}

	rec := h.call(t, http.MethodPost, "/api/v3/contents/generations/tasks", "{\"model\":\"video\"}")
	if rec.Code != http.StatusOK || len(seen) != 1 || seen[0] != "Bearer key-b" {
		t.Fatalf("official model policy was not applied: status=%d seen=%v body=%s", rec.Code, seen, rec.Body.String())
	}
}

// TestOfficialTemplateRetriesHTTPFailure 验证异步创建遇到明确 429 时按模板重试，成功后记录一次失败且上游只调用两次。
// 参数 t 为 Go 测试上下文；返回值无，断言失败定位具体协议、路由或日志行为。
// 前置使用独立测试配置及本地 HTTP 假上游，不依赖外部供应商凭据。
// 服务器通过 defer/Cleanup 关闭，测试状态只存在本例内存，不产生持久化数据。
func TestOfficialTemplateRetriesHTTPFailure(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			return
		}
		io.WriteString(w, `{"id":"task-1"}`)
	}))
	defer up.Close()
	h := officialHost(up, Deployment("video", "volcengine/model", "key", up.URL, "ark_contents_generation", nil))
	h.settings = map[string]any{
		"model_routes": []any{},
		"retry_policy": map[string]any{"max_attempts": 2, "timeout_seconds": 60, "failure_threshold": 3, "cooldown_seconds": 0},
	}
	rec := h.call(t, http.MethodPost, "/api/v3/contents/generations/tasks", `{"model":"video"}`)
	if rec.Code != 200 || calls.Load() != 2 || len(h.failures) != 1 {
		t.Fatalf("status=%d calls=%d failures=%v", rec.Code, calls.Load(), h.failures)
	}
}

// TestOfficialTimeoutDoesNotReplayAmbiguousCreate 验证异步创建超时即终止，即使模板允许多次尝试也只创建一次，避免重复付费任务。
// 参数 t 为 Go 测试上下文；返回值无，断言失败定位具体协议、路由或日志行为。
// 前置使用独立测试配置及本地 HTTP 假上游，不依赖外部供应商凭据。
// 服务器通过 defer/Cleanup 关闭，测试状态只存在本例内存，不产生持久化数据。
func TestOfficialTimeoutDoesNotReplayAmbiguousCreate(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(150 * time.Millisecond):
		}
	}))
	defer up.Close()
	h := officialHost(up, Deployment("video", "volcengine/model", "key", up.URL, "ark_contents_generation", nil))
	h.settings = map[string]any{
		"model_routes": []any{},
		"retry_policy": map[string]any{"max_attempts": 3, "timeout_seconds": 0.03, "failure_threshold": 3, "cooldown_seconds": 0},
	}
	rec := h.call(t, http.MethodPost, "/api/v3/contents/generations/tasks", `{"model":"video"}`)
	if rec.Code != 502 || calls.Load() != 1 {
		t.Fatalf("ambiguous create replayed: status=%d calls=%d", rec.Code, calls.Load())
	}
}

// TestOfficialTaskScopesAndPendingUsage 验证任务归属键隔离调用方与传输，进行中响应携带进度用量仍不能扣最终费用。
// 参数 t 为 Go 测试上下文；返回值无，断言失败定位具体协议、路由或日志行为。
// 前置使用独立测试配置及本地 HTTP 假上游，不依赖外部供应商凭据。
// 服务器通过 defer/Cleanup 关闭，测试状态只存在本例内存，不产生持久化数据。
func TestOfficialTaskScopesAndPendingUsage(t *testing.T) {
	p := &auth.Principal{Hash: "a"}
	key := officialTaskScope(p, "ark", "same")
	if key == officialTaskScope(&auth.Principal{Hash: "b"}, "ark", "same") || key == officialTaskScope(p, "qiniu", "same") {
		t.Fatal("task scope collision")
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"status":"running","usage":{"completion_tokens":12}}`)
	}))
	defer up.Close()
	dep := Deployment("video", "volcengine/model", "key", up.URL, "ark_contents_generation", nil)
	h := officialHost(up, dep)
	h.pins[officialTaskScope(&auth.Principal{UserID: "test-user"}, "ark_contents_generation", "task")] = router.CooldownID(dep)
	h.call(t, http.MethodGet, "/api/v3/contents/generations/tasks/task", "")
	if len(h.spend) != 1 || h.spend[0].usage != nil {
		t.Fatalf("in-progress task billed: %+v", h.spend)
	}
}

// TestOfficialForwardDoesNotLeakGatewayCredentials 验证原生请求剥离 Cookie、调用方 API Key 和 Connection 指定头，仅发送部署提供的供应商鉴权。
// 参数 t 为 Go 测试上下文；返回值无，断言失败定位具体协议、路由或日志行为。
// 前置使用独立测试配置及本地 HTTP 假上游，不依赖外部供应商凭据。
// 服务器通过 defer/Cleanup 关闭，测试状态只存在本例内存，不产生持久化数据。
func TestOfficialForwardDoesNotLeakGatewayCredentials(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Cookie", "X-Api-Key", "Api-Key", "X-Litellm-Api-Key", "X-Private"} {
			if r.Header.Get(name) != "" {
				t.Errorf("leaked header %s", name)
			}
		}
		if r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Error("missing provider credential")
		}
		io.WriteString(w, `{}`)
	}))
	defer up.Close()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, name := range []string{"Cookie", "X-Api-Key", "Api-Key", "X-Litellm-Api-Key", "X-Private"} {
		r.Header.Set(name, "gateway-secret")
	}
	r.Header.Set("Connection", "X-Private")
	response, err := dptransport.ForwardOfficial(officialHost(up).HTTPClient(), r, http.MethodGet, up.URL, "provider-key", "fixture-call", nil, r.Header, providercontract.Transport{Auth: providercontract.AuthConfig{Header: "Authorization", Prefix: "Bearer"}}, nil)
	status := response.StatusCode
	if err != nil || status != 200 {
		t.Fatal(status, err)
	}
}
