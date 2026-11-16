package execution

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	dptransport "github.com/sunqirui1987/xhub/internal/dataplane/transport"
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
			if got := emptyBypassDialogue(tc.endpoint, tc.countOnly, tc.response); got != tc.want {
				t.Fatalf("空对话响应判断错误: got=%v want=%v", got, tc.want)
			}
		})
	}
}

// TestNativeBypassEmptyDialogueNeverReplays 验证三种对话透传收到上游空 200 时返回 502，并保留真实上游事实。
// 参数 t 为测试上下文；本地上游在第二次调用时可成功以检测误重试，结束时关闭服务器，无真实凭据或持久化资源。
func TestNativeBypassEmptyDialogueNeverReplays(t *testing.T) {
	for _, spec := range []struct{ transport, path string }{
		{"bypass_openai_chat", "/bypass/openai/v1/chat/completions"},
		{"bypass_openai_responses", "/bypass/openai/v1/responses"},
		{"bypass_anthropic_messages", "/bypass/anthropic/v1/messages"},
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
			h := officialHost(up, Deployment("public", "upstream", "fixture", up.URL, spec.transport, nil))
			h.settings = map[string]any{"model_routes": []any{}, "retry_policy": map[string]any{"max_attempts": 3, "timeout_seconds": 60, "failure_threshold": 3, "cooldown_seconds": 0}}
			rec := h.call(t, http.MethodPost, spec.path, `{"model":"public","messages":[{"role":"user","content":"Hello"}],"input":"Hello","max_tokens":16}`)
			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "upstream request failed") || calls.Load() != 1 {
				t.Fatalf("空响应必须报错且不得重放: status=%d calls=%d body=%s", rec.Code, calls.Load(), rec.Body.String())
			}
			if len(h.spend) != 1 || h.spend[0].usage["pricing_blocked"] != "upstream_usage_missing" {
				t.Fatalf("空响应缺少未计价事实: %+v", h.spend)
			}
			found := false
			for _, note := range h.notes {
				if note.Upstream != nil && note.Upstream.StatusCode == 200 && note.Upstream.Failure != nil && note.Upstream.Failure.Kind == "empty_response" {
					found = true
				}
			}
			if !found {
				t.Fatal("日志未保留上游 200 与空响应诊断")
			}
		})
	}
}
