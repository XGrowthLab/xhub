package catalog

import (
	"encoding/json"
	"math"
)

// MarketRates 为管理员调价提取可完整执行的市场费率。
// row 是市场完整记录，返回费率与稳定原因码；仅接受现有计费器可表达的单档 token 价格。
// 多档、未知单位/计价项、缺美元价或范围不完整都要求人工配置，禁止偷偷取第一档或当作免费。
func MarketRates(row map[string]any) ([]Rate, string) {
	raw, err := json.Marshal(row["pricing_rules_v2"])
	if err != nil {
		return nil, "unsupported_pricing"
	}
	var rules []feedPricingRule
	if json.Unmarshal(raw, &rules) != nil || len(rules) != 1 || len(rules[0].DetailsV2) == 0 {
		return nil, "unsupported_pricing"
	}
	var rawRules []map[string]any
	if json.Unmarshal(raw, &rawRules) != nil {
		return nil, "unsupported_pricing"
	}
	// 新增条件不能静默丢弃；显示顺序不参与计费，其他未知字段等待显式支持。
	for key := range rawRules[0] {
		switch key {
		case "input_range", "output_range", "input_item_type", "output_item_type", "details_v2", "item_order":
		case "details":
			legacy, ok := rawRules[0][key].(map[string]any)
			if !ok || len(legacy) != 0 {
				return nil, "unsupported_pricing"
			}
		default:
			return nil, "unsupported_pricing"
		}
	}
	details, _ := rawRules[0]["details_v2"].(map[string]any)
	for _, rawUnit := range details {
		unit, ok := rawUnit.(map[string]any)
		if !ok {
			return nil, "unsupported_pricing"
		}
		for field := range unit {
			switch field {
			case "name", "unit_name", "unit_size", "unit_price", "unit_price_usd":
			default:
				return nil, "unsupported_pricing"
			}
		}
		value, present := unit["unit_price_usd"].(float64)
		if !present || value < 0 {
			return nil, "incomplete_pricing"
		}
	}
	rule := rules[0]
	if rule.InputItemType != "" || rule.OutputItemType != "" {
		return nil, "unsupported_pricing"
	}
	for i, r := range [][]float64{rule.InputRange, rule.OutputRange} {
		if len(r) == 0 {
			continue
		}
		limitRaw, _ := json.Marshal(row[[]string{"max_input_tokens", "max_output_tokens"}[i]])
		var limit float64
		_ = json.Unmarshal(limitRaw, &limit)
		if len(r) == 2 && r[0] == 0 && r[1] == -1 {
			continue
		}
		if len(r) != 2 || r[0] != 0 || r[1] <= 0 || (limit <= 0 && r[1] < 99999999) || r[1] < limit {
			return nil, "unsupported_pricing"
		}
	}
	for key, unit := range rule.DetailsV2 {
		// 来源把搜索按次标为 time；明确映射 query，不能误记为每秒费用。
		search := key == "web_search_req" && (unit.UnitName == "time" || unit.UnitName == "query" || unit.UnitName == "request")
		spec, known := knownKeys[key]
		// 这里只开放普通 token、缓存及完整分时费率；推理/模态限定价需要额外用量事实。
		if !known || (spec.side == "input" && spec.variant != "uncached") || (spec.side == "output" && spec.variant != "" && !search) || spec.side == "batch_input" || spec.side == "batch_output" {
			return nil, "unsupported_pricing"
		}
		usd, ok := perUnit(unit)
		if (unit.UnitName != "token" && !search) || unit.UnitSize <= 0 || unit.UnitSize != math.Trunc(unit.UnitSize) || !ok || usd < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
			return nil, "unsupported_pricing"
		}
	}
	rates := buildRates(rule.DetailsV2)
	for i := range rates {
		if rates[i].SourceKey == "web_search_req" {
			rates[i].Measure = "query"
		}
	}
	seen := map[string]bool{}
	for _, rate := range rates {
		key := rate.Side + "/" + rate.Variant + "/" + rate.Window
		if seen[key] {
			return nil, "unsupported_pricing"
		}
		seen[key] = true
		if rate.Window != "all" {
			other := "peak"
			if rate.Window == "peak" {
				other = "offpeak"
			}
			found := false
			for _, candidate := range rates {
				found = found || candidate.Side == rate.Side && candidate.Variant == rate.Variant && candidate.Window == other
			}
			if !found {
				return nil, "incomplete_pricing"
			}
		}
	}
	input, output := false, false
	for _, r := range rates {
		input = input || r.Side == "input"
		output = output || r.Side == "output"
	}
	if !input || !output {
		return nil, "incomplete_pricing"
	}
	return rates, ""
}
