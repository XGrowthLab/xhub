package execution

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	dptransport "github.com/sunqirui1987/xhub/internal/dataplane/transport"
	providercontract "github.com/sunqirui1987/xhub/internal/provider/contract"
	providerregistry "github.com/sunqirui1987/xhub/internal/provider/registry"
)

// TestEmptyBypassDialogue 验证同步对话的空响应判断覆盖成功、失败、流式、计数及无关协议。
// 参数 t 为测试上下文；纯内存输入无外部服务或需清理数据，断言不会扩大到合法非对话空响应。
func TestEmptyBypassDialogue(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
		countOnly      bool
		response       dptransport.ForwardResponse
		want           bool
	}{
		{"messages empty", "bypass:anthropic-messages", false, dptransport.ForwardResponse{StatusCode: 200}, true},
		{"chat whitespace", "bypass:openai-chat", false, dptransport.ForwardResponse{StatusCode: 200, Body: []byte(" \n\t")}, true},
		{"responses no content", "bypass:openai-responses", false, dptransport.ForwardResponse{StatusCode: 204}, true},
		{"valid body", "bypass:openai-chat", false, dptransport.ForwardResponse{StatusCode: 200, Body: []byte(`{"id":"answer"}`)}, false},
		{"upstream error", "bypass:openai-chat", false, dptransport.ForwardResponse{StatusCode: 503}, false},
		{"already streamed", "bypass:openai-chat", false, dptransport.ForwardResponse{StatusCode: 200, Streamed: true}, false},
		{"count only", "bypass:anthropic-messages", true, dptransport.ForwardResponse{StatusCode: 200}, false},
		{"other operation", "bypass:openai-images", false, dptransport.ForwardResponse{StatusCode: 200}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hit := providercontract.Hit{Transport: providercontract.Transport{EndpointID: tc.endpoint}}
			if got := emptyBypassDialogue(hit, tc.countOnly, tc.response); got != tc.want {
				t.Fatalf("空对话响应判断错误: got=%v want=%v", got, tc.want)
			}
		})
	}
	for _, operation := range []string{"generateContent", "streamGenerateContent", "countTokens"} {
		hit := providercontract.Hit{Transport: providercontract.Transport{Protocol: "gemini"}, Action: providercontract.Action{UpstreamPath: "/models/{model}:" + operation}}
		if got := emptyBypassDialogue(hit, false, dptransport.ForwardResponse{StatusCode: 200}); got != (operation != "countTokens") {
			t.Fatalf("Google 空响应动作分类错误: %s got=%t", operation, got)
		}
	}
}

// TestNativeBypassEmptyDialogueNeverReplays 验证五种对话透传收到上游空 200 时返回 502，并保留真实上游事实。
// 参数 t 为测试上下文；本地上游在第二次调用时可成功以检测误重试，结束时关闭服务器，无真实凭据或持久化资源。
func TestNativeBypassEmptyDialogueNeverReplays(t *testing.T) {
	for _, spec := range []struct{ transport, path string }{
		{"bypass_openai_chat", "/bypass/openai/v1/chat/completions"},
		{"bypass_openai_responses", "/bypass/openai/v1/responses"},
		{"bypass_anthropic_messages", "/bypass/anthropic/v1/messages"},
		{"gemini_generate_content", "/bypass/gemini/v1beta/models/public:generateContent"},
		{"vertex_generate_content", "/bypass/vertex/v1/models/public:generateContent"},
	} {
		t.Run(spec.transport, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) > 1 {
					_, _ = io.WriteString(w, `{"id":"unexpected-replay"}`)
				}
			}))
			defer up.Close()
			deployment := Deployment("public", "upstream", "fixture", up.URL, spec.transport, nil)
			hit, _ := providerregistry.Match(http.MethodPost, spec.path)
			deployment.ModelInfo["endpoint_types"] = []string{hit.Transport.EndpointID}
			h := officialHost(up, deployment)
			h.settings = map[string]any{"model_routes": []any{}, "retry_policy": map[string]any{"max_attempts": 3, "timeout_seconds": 60, "failure_threshold": 3, "cooldown_seconds": 0}}
			rec := h.call(t, http.MethodPost, spec.path, `{"model":"public","messages":[{"role":"user","content":"Hello"}],"input":"Hello","max_tokens":16}`)
			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "upstream returned an empty dialogue response") ||
				!strings.Contains(rec.Body.String(), "upstream_invalid_response") || calls.Load() != 1 {
				t.Fatalf("空响应必须报错且不得重放: status=%d calls=%d body=%s", rec.Code, calls.Load(), rec.Body.String())
			}
			if len(h.spend) != 1 || h.spend[0].status != 502 || h.spend[0].usage["pricing_blocked"] != "upstream_usage_missing" {
				t.Fatalf("空响应缺少未计价事实: %+v", h.spend)
			}
			if len(h.failures) != 0 {
				t.Fatalf("唯一兼容部署的空响应不应计入冷却: failures=%v", h.failures)
			}
			found := false
			for _, note := range h.notes {
				if note.Upstream != nil && note.Upstream.StatusCode == 200 &&
					note.Upstream.Failure != nil && note.Upstream.Failure.Kind == "empty_response" &&
					note.Upstream.Outcome != nil && note.Upstream.Outcome.Code == "upstream_invalid_response" {
					found = true
				}
			}
			if !found {
				t.Fatal("日志未保留上游 200 与空响应诊断")
			}
		})
	}
}

// TestNativeBypassErrorEnvelopeWithHTTP200 验证已收到显式错误正文时，仅日志标明失败，客户端保留供应商状态和字节。
// 使用内存宿主及测试 HTTP 服务，断言一次出站、一次失败记账且无用量；结束关闭服务。
func TestNativeBypassErrorEnvelopeWithHTTP200(t *testing.T) {
	const raw = `{"id":"error-id","error":{"message":"Rejected"},"usage":{"prompt_tokens":5,"completion_tokens":2}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, raw)
	}))
	t.Cleanup(up.Close)
	h := officialHost(up, Deployment("public", "upstream", "fixture", up.URL, "bypass_openai_chat", nil))
	rec := h.call(t, http.MethodPost, "/bypass/openai/v1/chat/completions", `{"model":"public","messages":[{"role":"user","content":"Hello"}]}`)
	if rec.Code != http.StatusOK || rec.Body.String() != raw || len(h.spend) != 1 || h.spend[0].status != 502 || h.spend[0].usage != nil {
		t.Fatalf("供应商原文或失败记账错误: status=%d spend=%+v", rec.Code, h.spend)
	}
}
