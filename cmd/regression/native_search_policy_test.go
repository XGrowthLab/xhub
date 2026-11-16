package regression

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestModelContractNativeSearchPolicy 验证管理配置禁用搜索时，三种透传和普通入口不能调用上游或消耗预算。
// 使用独立 PostgreSQL、本地供应商与个人密钥；覆盖原生顶层/嵌套声明、普通工具及只读计数，测试退出自动清理。
func TestModelContractNativeSearchPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, path, transport, endpoint string
		search                          map[string]any
	}{
		{"responses", "/bypass/openai/v1/responses", "bypass_openai_responses", "bypass:openai-responses", map[string]any{"tools": []any{map[string]any{"type": "web_search"}}}},
		{"responses additional tools", "/bypass/openai/v1/responses", "bypass_openai_responses", "bypass:openai-responses", map[string]any{"input": []any{map[string]any{"type": "additional_tools", "tools": []any{map[string]any{"type": "web_search_preview"}}}}}},
		{"responses forced", "/bypass/openai/v1/responses", "bypass_openai_responses", "bypass:openai-responses", map[string]any{"tool_choice": map[string]any{"type": "web_search"}}},
		{"chat", "/bypass/openai/v1/chat/completions", "bypass_openai_chat", "bypass:openai-chat", map[string]any{"web_search_options": map[string]any{}}},
		{"messages", "/bypass/anthropic/v1/messages", "bypass_anthropic_messages", "bypass:anthropic-messages", map[string]any{"tools": []any{map[string]any{"type": "web_search_20250305"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/count_tokens") {
					io.WriteString(w, `{"input_tokens":8}`)
					return
				}
				io.WriteString(w, `{"id":"resp","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":1}}`)
			}))
			t.Cleanup(up.Close)
			dep := hardBudgetDeployment("disabled-native-search")
			dep.LiteLLMParams["api_base"] = up.URL
			dep.ModelInfo["transport"] = tc.transport
			dep.ModelInfo["endpoint_types"] = []string{tc.endpoint, "responses"}
			dep.ModelInfo["web_search_support"] = "unsupported"
			dep.ModelInfo["web_search_fallback"] = "reject"
			h := newHarness(t, dep)
			admin := h.adminSession()
			owner := h.openScope(t, admin, dep.ModelName)
			h.setKeyBudget(t, admin, owner.key, 1)
			body := map[string]any{"model": dep.ModelName, "input": "Search for Go.", "messages": []any{map[string]any{"role": "user", "content": "Search for Go."}}, "max_tokens": 32}
			for k, v := range tc.search {
				body[k] = v
			}
			before, err := h.db.GetKey(t.Context(), owner.keyID)
			if err != nil {
				t.Fatal(err)
			}
			blocked := h.do("POST", tc.path, owner.key, body)
			after, err := h.db.GetKey(t.Context(), owner.keyID)
			if err != nil || blocked.status != 400 || !strings.Contains(blocked.text(), "unsupported_capability") || calls.Load() != 0 || after.Spend != before.Spend {
				t.Fatalf("禁用搜索应明确拒绝且不调用上游或扣费: status=%d calls=%d err=%v", blocked.status, calls.Load(), err)
			}
			// 普通同名客户端函数不属于托管搜索，必须原样允许透传。
			ordinary := map[string]any{"model": dep.ModelName, "messages": []any{map[string]any{"role": "user", "content": "Hello"}}, "input": "Hello", "max_tokens": 32, "tools": []any{map[string]any{"type": "function", "name": "web_search", "function": map[string]any{"name": "web_search"}}}}
			allowed := h.do("POST", tc.path, owner.key, ordinary)
			if allowed.status != 200 || calls.Load() != 1 {
				t.Fatalf("普通函数被误拒绝: status=%d calls=%d", allowed.status, calls.Load())
			}
			if tc.name == "messages" {
				count := h.do("POST", "/bypass/anthropic/v1/messages/count_tokens", owner.key, body)
				if count.status != 200 || calls.Load() != 2 {
					t.Fatalf("只读 Token 计数被误拒绝: status=%d calls=%d", count.status, calls.Load())
				}
			}
		})
	}
}
