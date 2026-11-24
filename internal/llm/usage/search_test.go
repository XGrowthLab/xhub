package usage

import (
	"encoding/json"
	"math"
	"testing"
)

// TestParseSearchCount 验证不同供应商的真实用量形状、零和缺失、别名冲突与非法边界。
// 用内存用量对象模拟同步或 SSE 快照，断言只读取明确计数，无外部依赖或清理。
func TestParseSearchCount(t *testing.T) {
	for _, tc := range []struct {
		name     string
		raw      map[string]any
		count    int
		reported bool
		err      error
	}{
		{name: "fenno deepseek", raw: map[string]any{"tool_usage": map[string]any{"web_search": 2}}, count: 2, reported: true},
		{name: "deepseek official", raw: map[string]any{"server_tool_use": map[string]any{"tool_search": 2, "tool_fetch": 4}}, count: 2, reported: true},
		{name: "grok", raw: map[string]any{"server_side_tool_usage_details": map[string]any{"web_search_calls": 2, "x_search_calls": 1}, "num_server_side_tools_used": 3}, count: 2, reported: true},
		{name: "zero", raw: map[string]any{"server_side_tool_usage_details": map[string]any{"web_search_calls": 0}}, reported: true},
		{name: "equal aliases", raw: map[string]any{"search_count": json.Number("2"), "web_search_requests": 2, "server_tool_use": map[string]any{"web_search_requests": 2}, "server_side_tool_usage_details": map[string]any{"web_search_calls": 2}}, count: 2, reported: true},
		{name: "absent", raw: map[string]any{"input_tokens": 1, "server_tool_use": nil}},
		{name: "only other tools", raw: map[string]any{"num_server_side_tools_used": 5, "web_search_calls": 2, "server_side_tool_usage_details": map[string]any{"x_search_calls": 5}, "tool_usage_details": map[string]any{"web_search": map[string]any{"search_engine": 2}}}},
		{name: "conflict", raw: map[string]any{"searches": 1, "server_side_tool_usage_details": map[string]any{"web_search_calls": 2}}, err: ErrSearchUsageConflict},
		{name: "invalid bucket", raw: map[string]any{"server_side_tool_usage_details": "4"}, err: ErrSearchUsageInvalid},
		{name: "null count", raw: map[string]any{"server_tool_use": map[string]any{"tool_search": nil}}, err: ErrSearchUsageInvalid},
		{name: "negative", raw: map[string]any{"search_count": -1}, err: ErrSearchUsageInvalid},
		{name: "fraction", raw: map[string]any{"search_count": .5}, err: ErrSearchUsageInvalid},
		{name: "string", raw: map[string]any{"search_count": "2"}, err: ErrSearchUsageInvalid},
		{name: "nan", raw: map[string]any{"search_count": math.NaN()}, err: ErrSearchUsageInvalid},
		{name: "infinite", raw: map[string]any{"search_count": math.Inf(1)}, err: ErrSearchUsageInvalid},
		{name: "largest permitted count", raw: map[string]any{"search_count": int64(math.MaxInt32)}, count: math.MaxInt32, reported: true},
		{name: "overflow", raw: map[string]any{"search_count": int64(math.MaxInt32) + 1}, err: ErrSearchUsageInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, reported, err := ParseSearchCount(tc.raw)
			if count != tc.count || reported != tc.reported || err != tc.err {
				t.Fatalf("搜索数错误: count=%d reported=%v err=%v, want %d/%v/%v", count, reported, err, tc.count, tc.reported, tc.err)
			}
		})
	}
}
