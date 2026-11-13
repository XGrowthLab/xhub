package catalog

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// TestMarketRatesComplete 验证美元单位转换及快捷调价的安全边界。
// 前置仅内存来源记录，覆盖零价、多档、缺价、未知项及不完整范围；无外部依赖和持久数据。
func TestMarketRatesComplete(t *testing.T) {
	base := `{"max_input_tokens":128000,"pricing_rules_v2":[{"input_range":[0,128000],"details_v2":{"input":{"unit_name":"token","unit_size":1000000,"unit_price_usd":2},"output":{"unit_name":"token","unit_size":1000000,"unit_price_usd":6}}}]}`
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		ok     bool
	}{
		{"normal", func(m map[string]any) {}, true},
		{"free", func(m map[string]any) {
			m["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)["input"].(map[string]any)["unit_price_usd"] = float64(0)
		}, true},
		{"multiple tiers", func(m map[string]any) { r := m["pricing_rules_v2"].([]any); m["pricing_rules_v2"] = append(r, r[0]) }, false},
		{"missing USD", func(m map[string]any) {
			delete(m["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)["input"].(map[string]any), "unit_price_usd")
		}, false},
		{"unknown item", func(m map[string]any) {
			d := m["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)
			d["unknown"] = d["input"]
		}, false},
		{"integer limit", func(m map[string]any) { m["max_input_tokens"] = 128000 }, true},
		{"unlimited", func(m map[string]any) {
			rule := m["pricing_rules_v2"].([]any)[0].(map[string]any)
			rule["input_range"] = []int{0, -1}
			rule["details"] = map[string]any{}
		}, true},
		{"unknown condition", func(m map[string]any) { m["pricing_rules_v2"].([]any)[0].(map[string]any)["condition"] = "batch" }, false},
		{"unknown unit condition", func(m map[string]any) {
			m["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)["input"].(map[string]any)["condition"] = "batch"
		}, false},
		{"inferred variant", func(m map[string]any) {
			d := m["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)
			d["text_input"] = d["input"]
			delete(d, "input")
		}, false},
		{"duplicate input", func(m map[string]any) {
			d := m["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)
			d["ncache"] = d["input"]
		}, false},
		{"half window", func(m map[string]any) {
			d := m["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)
			d["ncache_peak"] = d["input"]
			delete(d, "input")
		}, false},
		{"full windows", func(m map[string]any) {
			d := m["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)
			d["ncache_peak"] = d["input"]
			d["ncache_offpeak"] = d["input"]
			delete(d, "input")
		}, true},
		{"search", func(m map[string]any) {
			d := m["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)
			d["web_search_req"] = map[string]any{"unit_name": "time", "unit_size": 1, "unit_price_usd": .01}
		}, true},
		{"range gap", func(m map[string]any) { m["max_input_tokens"] = float64(256000) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var row map[string]any
			_ = json.Unmarshal([]byte(base), &row)
			tc.mutate(row)
			rates, reason := MarketRates(row)
			if (reason == "") != tc.ok {
				t.Fatalf("快捷定价支持状态错误: rates=%v reason=%s", rates, reason)
			}
			if tc.ok {
				usage := Usage{PromptTokens: 10, CompletionTokens: 2}
				want := .000032
				if tc.name == "free" {
					want = .000012
				}
				if tc.name == "search" {
					usage.Searches = 2
					want += .02
				}
				charge, ok := CostFromRates(rates, usage, time.Now())
				if !ok || math.Abs(charge.Total-want) > 1e-12 {
					t.Fatalf("可应用价格不能完整计费: %+v want=%v", charge, want)
				}
			}
			if tc.name == "normal" && rates[0].USD != .000002 {
				t.Fatalf("价格不是每基础单位: %v", rates)
			}
		})
	}
}
