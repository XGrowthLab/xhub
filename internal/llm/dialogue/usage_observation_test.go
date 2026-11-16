package dialogue

import (
	"encoding/json"
	"math"
	"testing"
)

// TestMergeDialogueUsage 验证完整/部分/空帧、等价别名、矛盾别名及非法计数；纯内存，无需外部清理。
// 后续合法终态及显式空标记不能消除先前发现的矛盾；输入 map 保持不变，正常部分帧仍允许补齐。
func TestMergeDialogueUsage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		usage   map[string]any
		blocked bool
	}{
		{"nil", nil, false},
		{"partial", map[string]any{"input_tokens": 11}, false},
		{"equal_aliases", map[string]any{"prompt_tokens": 11, "input_tokens": json.Number("11")}, false},
		{"input_conflict", map[string]any{"prompt_tokens": 1024, "input_tokens": 11}, true},
		{"output_conflict", map[string]any{"completion_tokens": 1024, "output_tokens": 5}, true},
		{"string", map[string]any{"input_tokens": "11"}, true},
		{"fraction", map[string]any{"input_tokens": 1.5}, true},
		{"negative", map[string]any{"input_tokens": -1}, true},
		{"nan", map[string]any{"input_tokens": math.NaN()}, true},
		{"infinite", map[string]any{"input_tokens": math.Inf(1)}, true},
		{"null_partial", map[string]any{"input_tokens": nil}, false},
		{"nested", map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": "3"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeDialogueUsage(nil, tc.usage)
			got = MergeDialogueUsage(got, map[string]any{"prompt_tokens": 11, "input_tokens": 11, "completion_tokens": 5, "output_tokens": 5, "pricing_blocked": ""})
			got = NormalizeDialogueUsage(got)
			blocked, _ := got["pricing_blocked"].(string)
			if (blocked != "") != tc.blocked {
				t.Fatalf("先前用量信任状态被覆盖: %+v", got)
			}
			if _, changed := tc.usage["pricing_blocked"]; changed {
				t.Fatal("修改了供应商原始用量对象")
			}
		})
	}
}

// TestNormalizeDialogueUsageBeforeAliasReplacement 验证同步归一化先识别原始别名冲突，不能覆盖后再假装完整；纯内存自动清理。
func TestNormalizeDialogueUsageBeforeAliasReplacement(t *testing.T) {
	got := NormalizeDialogueUsage(map[string]any{"prompt_tokens": 1024, "input_tokens": 11, "completion_tokens": 5})
	if got["pricing_blocked"] != "upstream_usage_conflict" {
		t.Fatalf("归一化丢失原始别名冲突: %+v", got)
	}
}

// TestMergeDialogueUsageCumulativeAliases 验证跨帧累计增长与同帧矛盾分开判断；纯内存，无外部清理。
// 首帧两个别名相等，后帧只更新任一名称，最终应使用新值；同帧真矛盾仍由原矩阵验证为永久阻断。
func TestMergeDialogueUsageCumulativeAliases(t *testing.T) {
	for _, field := range []string{"prompt_tokens", "input_tokens", "completion_tokens", "output_tokens"} {
		t.Run(field, func(t *testing.T) {
			usage := MergeDialogueUsage(nil, map[string]any{"prompt_tokens": 8, "input_tokens": 8, "completion_tokens": 0, "output_tokens": 0})
			next := map[string]any{field: 9}
			usage = NormalizeDialogueUsage(MergeDialogueUsage(usage, next))
			if usage["pricing_blocked"] != nil {
				t.Fatalf("合法累计增长被误认为同帧冲突: %+v", usage)
			}
			target := "completion_tokens"
			if field == "prompt_tokens" || field == "input_tokens" {
				target = "prompt_tokens"
			}
			if usage[target] != 9 || len(next) != 1 {
				t.Fatalf("累计值或原始帧被错误修改: usage=%+v incoming=%+v", usage, next)
			}
		})
	}
}

// TestSearchUsageObservation 验证搜索累计次数可跨帧增长，倒退和矛盾无法被后续帧掩盖。
// 仅处理内存用量；不访问供应商，输入对象不被修改且无需清理。
func TestSearchUsageObservation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first, next map[string]any
		blocked     bool
	}{
		{"growth", map[string]any{"tool_usage": map[string]any{"web_search": 1}}, map[string]any{"searches": 2}, false},
		{"decrease", map[string]any{"searches": 2}, map[string]any{"tool_usage": map[string]any{"web_search": 1}}, true},
		{"conflicting aliases", map[string]any{"searches": 1, "tool_usage": map[string]any{"web_search": 2}}, map[string]any{"searches": 2}, true},
		{"invalid count", map[string]any{"tool_usage": map[string]any{"web_search": -1}}, map[string]any{"searches": 2}, true},
		{"breakdown is not a count", map[string]any{"tool_usage_details": map[string]any{"web_search": map[string]any{"search_engine": 2}}}, map[string]any{"tool_usage": map[string]any{"web_search": 2}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeDialogueUsage(nil, tc.first)
			got = MergeDialogueUsage(got, tc.next)
			reason, _ := got["pricing_blocked"].(string)
			if (reason != "") != tc.blocked {
				t.Fatalf("搜索用量可信状态错误: %+v", got)
			}
			if _, mutated := tc.first["pricing_blocked"]; mutated {
				t.Fatal("修改上游原始帧")
			}
		})
	}
}
