package dialogue

import "testing"

// TestNativeWebSearchRequested 验证不同协议中真实搜索声明与历史、普通函数的区别。
// 使用内存请求覆盖顶层、additional_tools、显式工具选择、空数据和未知协议，不修改请求或依赖外部资源。
func TestNativeWebSearchRequested(t *testing.T) {
	for _, tc := range []struct {
		name, protocol string
		body           map[string]any
		want           bool
	}{
		{"responses", "openai-responses", map[string]any{"tools": []any{map[string]any{"type": "web_search_preview_2025_03_11"}}}, true},
		{"additional tools", "openai-responses", map[string]any{"input": []any{map[string]any{"type": "additional_tools", "tools": []any{map[string]any{"type": "web_search"}}}}}, true},
		{"forced", "openai-responses", map[string]any{"tool_choice": map[string]any{"type": "web_search"}}, true},
		{"chat", "openai-chat", map[string]any{"web_search_options": map[string]any{}}, true},
		{"messages", "anthropic-messages", map[string]any{"tools": []any{map[string]any{"type": "web_search_20250305"}}}, true},
		{"future search", "openai-responses", map[string]any{"tools": []any{map[string]any{"type": "web_search_20300101"}}}, true},
		{"history only", "openai-responses", map[string]any{"input": []any{map[string]any{"type": "web_search_call"}}}, false},
		{"user function", "openai-chat", map[string]any{"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "web_search"}}}}, false},
		{"other protocol", "gemini", map[string]any{"tools": []any{map[string]any{"type": "web_search"}}}, false},
		{"nil", "openai-responses", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NativeWebSearchRequested(tc.protocol, tc.body); got != tc.want {
				t.Fatalf("搜索识别错误：%v want %v", got, tc.want)
			}
		})
	}
}
