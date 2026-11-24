package catalog

import (
	"testing"
)

func TestNormalizeUsagePreservesExplicitZeroOverAliases(t *testing.T) {
	tests := []struct {
		name  string
		usage map[string]any
		want  Usage
	}{
		{"tokens", map[string]any{"prompt_tokens": 0, "input_tokens": 99, "completion_tokens": 0, "output_tokens": 88}, Usage{}},
		{"cache creation", map[string]any{"cache_creation_input_tokens": 0, "cache_write_tokens": 77}, Usage{}},
		{"separate cache", map[string]any{"input_tokens": 10, "cache_read_input_tokens": 0, "cache_read_tokens": 66, "cached_tokens": 55}, Usage{PromptTokens: 10}},
		{"top level cache", map[string]any{"prompt_tokens": 10, "cached_tokens": 0, "prompt_tokens_details": map[string]any{"cached_tokens": 5}}, Usage{PromptTokens: 10}},
		{"nested cache", map[string]any{"prompt_tokens": 10, "prompt_tokens_details": map[string]any{"cached_tokens": 0}, "input_tokens_details": map[string]any{"cached_tokens": 5}}, Usage{PromptTokens: 10}},
		{"null permits alias", map[string]any{"prompt_tokens": nil, "input_tokens": 9, "cache_read_input_tokens": nil, "cache_read_tokens": 3}, Usage{PromptTokens: 12, CachedTokens: 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeUsage(tc.usage); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

// TestNormalizeUsageReadsTrustedSearch 验证通用目录计价读取供应商的搜索次数，非法值与冲突保留阻断原因。
// 使用内存用量对象，无费率写入和清理步骤。
func TestNormalizeUsageReadsTrustedSearch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    map[string]any
		count  int
		reason string
	}{
		{"grok", map[string]any{"server_side_tool_usage_details": map[string]any{"web_search_calls": 1}, "num_server_side_tools_used": 3}, 1, ""},
		{"deepseek", map[string]any{"server_tool_use": map[string]any{"tool_search": 2, "tool_fetch": 4}}, 2, ""},
		{"conflict", map[string]any{"searches": 2, "server_side_tool_usage_details": map[string]any{"web_search_calls": 0}}, 0, "upstream_usage_conflict"},
		{"invalid", map[string]any{"server_side_tool_usage_details": map[string]any{"web_search_calls": "1"}}, 0, "upstream_usage_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeUsage(tc.raw); got.Searches != tc.count || got.PricingBlocked != tc.reason {
				t.Fatalf("目录实测搜索用量错误: %+v", got)
			}
		})
	}
}
