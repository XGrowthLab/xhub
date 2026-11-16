package models

import (
	"encoding/json"
	"testing"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/config"
)

// marketTierPreviewFixture 创建独立公开分档报价；参数为测试上下文，返回内存行，不接触生产目录。
func marketTierPreviewFixture(t *testing.T) map[string]any {
	t.Helper()
	var row map[string]any
	raw := `{"source":"https://api.modelink.ai/v1/market/models","max_input_tokens":100,"max_output_tokens":20,"pricing_rules_v2":[{"input_range":[0,50],"output_range":[0,99999999],"details_v2":{"ncache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.001},"output":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.005}}},{"input_range":[50,99999999],"output_range":[0,99999999],"details_v2":{"ncache":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.002},"output":{"unit_name":"token","unit_size":1000,"unit_price_usd":0.0075}}}]}`
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		t.Fatal(err)
	}
	return row
}

// TestDeploymentTierPreview 验证完整分档预览、版本变化和已确认价格比较；不调用网络或数据库。
// 篡改第二档或产生区间缺口时不得静默保持旧版本或显示可更新，fixture 随测试结束回收。
func TestDeploymentTierPreview(t *testing.T) {
	row := marketTierPreviewFixture(t)
	dep := config.ModelEntry{ModelName: "tier-model", LiteLLMParams: map[string]any{"input_cost_per_token": .000001, "output_cost_per_token": .000005}, ModelInfo: map[string]any{"id": "tier-id", "db_model": true, "base_model": "tier-base", "pricing_source": "manual"}}
	doc := catalog.PriceDocument{Models: map[string]map[string]any{"tier-base": row}}
	p := pricePreview(dep, doc)
	if p.Reason != "" || !p.Changed || len(p.ProposedTiers) != 2 || len(p.Proposed) != 0 {
		t.Fatalf("未返回完整可确认分档: %+v", p)
	}
	version := p.Version
	rules := row["pricing_rules_v2"].([]any)
	high := rules[1].(map[string]any)["details_v2"].(map[string]any)["output"].(map[string]any)
	high["unit_price_usd"] = float64(.008)
	if changed := pricePreview(dep, doc); changed.Version == version || changed.Reason != "" {
		t.Fatalf("第二档价格变化未改变版本: %+v", changed)
	}
	high["unit_price_usd"] = float64(.0075)
	dep.LiteLLMParams = map[string]any{"market_price_tiers": p.ProposedTiers, "pricing_rules_v2": rules, "market_tier_semantics": "modelink_open_closed_v1"}
	dep.ModelInfo["pricing_source"] = "catalog"
	if current := pricePreview(dep, doc); current.Changed || len(current.CurrentTiers) != 2 {
		t.Fatalf("相同已确认分档不应报差异: %+v", current)
	}
	rules[1].(map[string]any)["input_range"] = []any{float64(51), float64(99999999)}
	if bad := pricePreview(dep, doc); bad.Reason == "" || bad.Changed {
		t.Fatalf("有区间缺口的报价不能确认: %+v", bad)
	}
}
