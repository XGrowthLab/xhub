package catalog

import (
	"encoding/json"
	"os"
	"testing"
)

// tierMarketFixture 构造与 Modelink 一致的两档公开报价；无外部依赖，测试结束由 GC 回收。
// 参数 t 为测试上下文；返回独立目录行，失败时立即终止，供正常和错误边界测试复用。
func tierMarketFixture(t *testing.T) map[string]any {
	t.Helper()
	var row map[string]any
	raw := `{"source":"https://api.modelink.ai/v1/market/models","max_input_tokens":1050000,"max_output_tokens":128000,"pricing_rules_v2":[{"input_range":[0,272000],"output_range":[0,99999999],"details":{},"details_v2":{"ncache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.001},"output":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.005},"cache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.00005},"c_cache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.00125},"c_30m_cache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.00125},"c_12h_cache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.0025}}},{"input_range":[272000,99999999],"output_range":[0,99999999],"details":{},"details_v2":{"ncache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.002},"output":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.0075},"cache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.0001},"c_cache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.0025},"c_30m_cache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.0025},"c_12h_cache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.005}}}]}`
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		t.Fatal(err)
	}
	return row
}

// TestMarketTiersModelinkFixture 验证真实来源报价结构可完整转换，包含 GPT 缓存期限及 Grok 两档。
// 使用脱敏、固定的公开报价 fixture，测试不访问网络、不修改目录；文件由仓库管理无需清理。
func TestMarketTiersModelinkFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/modelink-tiers.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := BuildPriceDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"openai/gpt-6.1-terra", "x-ai/grok-4.5", "x-ai/grok-4.6", "x-ai/grok-4.7"} {
		t.Run(id, func(t *testing.T) {
			tiers, reason := MarketTiers(doc.Models[id])
			if reason != "" || len(tiers) != 2 || tiers[0].InputRange[1] != tiers[1].InputRange[0] {
				t.Fatalf("公开价未正确形成连续两档: %+v %q", tiers, reason)
			}
		})
	}
}

// TestMarketTiers 验证市场报价的完整转换、不可变输入及错误边界；独立内存 fixture，无数据清理。
// 覆盖正确档位、缓存期限、覆盖缺口、未知字段和缺价，确保异常不落回首档或免费。
func TestMarketTiers(t *testing.T) {
	row := tierMarketFixture(t)
	before, _ := json.Marshal(row)
	tiers, reason := MarketTiers(row)
	if reason != "" || len(tiers) != 2 || tiers[0].InputRange != [2]int{0, 272000} || tiers[1].InputRange != [2]int{272000, 99999999} {
		t.Fatalf("分档报价解析错误: tiers=%+v reason=%q", tiers, reason)
	}
	seen := map[string]bool{}
	for _, rate := range tiers[0].Rates {
		seen[rate.Side+"/"+rate.Variant] = true
	}
	if !seen["cache_write/30m"] || !seen["cache_write/12h"] || !seen["input/uncached"] || !seen["output/"] {
		t.Fatalf("缓存期限或输入输出价格丢失: %+v", tiers[0].Rates)
	}
	after, _ := json.Marshal(row)
	if string(before) != string(after) {
		t.Fatal("解析报价不应修改市场目录")
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"unknown source", func(r map[string]any) { r["source"] = "other" }},
		{"gap", func(r map[string]any) {
			r["pricing_rules_v2"].([]any)[1].(map[string]any)["input_range"] = []any{float64(272001), float64(99999999)}
		}},
		{"overlap", func(r map[string]any) {
			r["pricing_rules_v2"].([]any)[1].(map[string]any)["input_range"] = []any{float64(271999), float64(99999999)}
		}},
		{"missing usd", func(r map[string]any) {
			delete(r["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)["c_12h_cache"].(map[string]any), "unit_price_usd")
		}},
		{"conditional", func(r map[string]any) { r["pricing_rules_v2"].([]any)[0].(map[string]any)["condition"] = "batch" }},
		{"short output", func(r map[string]any) { r["max_output_tokens"] = float64(100000000) }},
		{"nonfinite output", func(r map[string]any) { r["max_output_tokens"] = "NaN" }},
		{"cache price underflows", func(r map[string]any) {
			unit := r["pricing_rules_v2"].([]any)[0].(map[string]any)["details_v2"].(map[string]any)["c_30m_cache"].(map[string]any)
			unit["unit_price_usd"] = 5e-324
			unit["unit_size"] = 1000.0
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := tierMarketFixture(t)
			tc.edit(fixture)
			if got, reason := MarketTiers(fixture); reason == "" || len(got) != 0 {
				t.Fatalf("错误市场报价必须拒绝: %+v %q", got, reason)
			}
		})
	}
}
