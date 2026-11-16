package catalog

import (
	"encoding/json"
	"math"
	"sort"
)

// MarketTier 保存市场公开价的一档：输入区间与输出区间采用 Modelink 的左开右闭语义。
// Rates 的 USD 为每基础单位价格；保存到部署后，不再引用会变化的市场目录。
type MarketTier struct {
	InputRange  [2]int `json:"input_range"`
	OutputRange [2]int `json:"output_range"`
	Rates       []Rate `json:"rates"`
}

// MarketTiers 将 Modelink 的输入长度分档转换成完整、可验证的价格快照。
// row 为未修改的市场模型记录，返回有序档位和原因码；仅接受覆盖完整输入窗且输出区间一致的已知 token 报价。
// 供管理员预览及确认使用；未知条件、缺价、间隙和重叠均拒绝，绝不回退首档或推测免费。
func MarketTiers(row map[string]any) ([]MarketTier, string) {
	if row["source"] != MarketURL {
		return nil, "unsupported_pricing"
	}
	raw, err := json.Marshal(row["pricing_rules_v2"])
	if err != nil {
		return nil, "unsupported_pricing"
	}
	var rules []map[string]any
	if json.Unmarshal(raw, &rules) != nil || len(rules) < 2 || len(rules) > 64 {
		return nil, "unsupported_pricing"
	}
	tiers := make([]MarketTier, 0, len(rules))
	for _, rule := range rules {
		in, ok := marketTierRange(rule["input_range"])
		if !ok {
			return nil, "unsupported_pricing"
		}
		out, ok := marketTierRange(rule["output_range"])
		if !ok || out[0] != 0 {
			return nil, "unsupported_pricing"
		}
		details, ok := rule["details_v2"].(map[string]any)
		if !ok {
			return nil, "unsupported_pricing"
		}
		// 30 分钟及 12 小时是附加缓存创建价；先单独严格检查，再保留为明确期限的费率。
		// 不把它们折入通用缓存价，否则没有实测期限时可能按错误价格收费。
		extra := []Rate{}
		for _, key := range []string{"c_30m_cache", "c_12h_cache"} {
			value, exists := details[key]
			if !exists {
				continue
			}
			unit, ok := value.(map[string]any)
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
			usd, usdOK := unit["unit_price_usd"].(float64)
			size, sizeOK := unit["unit_size"].(float64)
			if !usdOK || !sizeOK || unit["unit_name"] != "token" || math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 || size <= 0 || size != math.Trunc(size) || math.IsInf(size, 0) {
				return nil, "incomplete_pricing"
			}
			variant := "30m"
			if key == "c_12h_cache" {
				variant = "12h"
			}
			price := usd / size
			// 浮点下溢会把非零报价变为免费，必须拒绝而非写入错误快照。
			if math.IsInf(price, 0) || (usd > 0 && price == 0) {
				return nil, "incomplete_pricing"
			}
			extra = append(extra, Rate{Measure: "token", Side: "cache_write", Variant: variant, Window: "all", UnitSize: size, USD: price, SourceKey: key})
			delete(details, key)
		}
		// 单档验证器仍负责价格维度、未知字段及完整费率；区间覆盖在本函数统一验证。
		copyRule := make(map[string]any, len(rule))
		for key, value := range rule {
			copyRule[key] = value
		}
		copyRule["input_range"] = []any{float64(0), float64(99999999)}
		copyRule["output_range"] = []any{float64(0), float64(99999999)}
		copyRow := map[string]any{"pricing_rules_v2": []any{copyRule}}
		rates, reason := MarketRates(copyRow)
		if reason != "" {
			return nil, reason
		}
		tiers = append(tiers, MarketTier{InputRange: in, OutputRange: out, Rates: append(rates, extra...)})
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].InputRange[0] < tiers[j].InputRange[0] })
	if tiers[0].InputRange[0] != 0 {
		return nil, "unsupported_pricing"
	}
	for i := 1; i < len(tiers); i++ {
		if tiers[i-1].InputRange[1] != tiers[i].InputRange[0] || tiers[i].OutputRange != tiers[0].OutputRange {
			return nil, "unsupported_pricing"
		}
	}
	limit, ok := floatField(row["max_input_tokens"])
	if !ok || limit <= 0 || math.IsNaN(limit) || math.IsInf(limit, 0) || float64(tiers[len(tiers)-1].InputRange[1]) < limit {
		return nil, "unsupported_pricing"
	}
	outputLimit, ok := floatField(row["max_output_tokens"])
	if !ok || outputLimit <= 0 || math.IsNaN(outputLimit) || math.IsInf(outputLimit, 0) || float64(tiers[0].OutputRange[1]) < outputLimit {
		return nil, "unsupported_pricing"
	}
	return tiers, ""
}

// marketTierRange 读取两个非负整数边界；参数为 JSON 区间，返回正宽区间及是否有效。
// MarketTiers 调用；拒绝小数、非数和溢出，不修改输入。
func marketTierRange(value any) ([2]int, bool) {
	var out [2]int
	a, ok := value.([]any)
	if !ok || len(a) != 2 {
		return out, false
	}
	for i, value := range a {
		n, ok := value.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n != math.Trunc(n) || n > math.MaxInt32 {
			return out, false
		}
		out[i] = int(n)
	}
	return out, out[0] < out[1]
}
