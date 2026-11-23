package models

import (
	"encoding/json"
	"math"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/config"
)

// availableRate 仅公开计价所需的维度和美元单价；不携带供应商凭据、原始配置或内部标签。
type availableRate struct {
	Measure string  `json:"measure"`
	Side    string  `json:"side"`
	Variant string  `json:"variant,omitempty"`
	Window  string  `json:"window"`
	USD     float64 `json:"usd"`
}

// availablePriceTier 描述已确认部署的输入档位；边界沿用 Modelink 左开右闭语义，不转换计费规则。
type availablePriceTier struct {
	InputRange  [2]int          `json:"input_range"`
	OutputRange [2]int          `json:"output_range"`
	Rates       []availableRate `json:"rates"`
}

// publicRates 复制可公开的价格字段；参数为已解码费率，返回完整安全快照及有效标记。
// 模型卡片展示调用；搜索单位与调价预览一致，非法金额或缺失单位使整组无效，无外部副作用。
func publicRates(rates []catalog.Rate) ([]availableRate, bool) {
	if len(rates) == 0 {
		return nil, false
	}
	result := make([]availableRate, 0, len(rates))
	for _, rate := range rates {
		if rate.Measure == "" || rate.Side == "" || rate.Window == "" || rate.USD < 0 || math.IsNaN(rate.USD) || math.IsInf(rate.USD, 0) {
			return nil, false
		}
		rate = displayDeploymentRate(rate)
		result = append(result, availableRate{Measure: rate.Measure, Side: rate.Side, Variant: rate.Variant, Window: rate.Window, USD: rate.USD})
	}
	return result, true
}

// addAvailablePrices 给卡片补充当前部署的完整费率；entry 为授权后的一条部署，card 为独立展示结果。
// 只读取管理员已接受的 rates/market_price_tiers；绝不以最新市场候选替换部署价，也不改权限、预算或结算。
// 分档配置即使无效也不回退平面首档，避免把部分价格错误地显示成所有上下文的统一价格。
func addAvailablePrices(entry config.ModelEntry, card map[string]any) {
	if entry.LiteLLMParams["market_tier_semantics"] != nil {
		for _, field := range []string{"input_price", "output_price", "cache_read_price", "cache_write_price"} {
			card[field] = nil
		}
		if entry.ModelInfo["pricing_source"] != "catalog" || entry.LiteLLMParams["market_tier_semantics"] != "modelink_open_closed_v1" {
			return
		}
		raw, err := json.Marshal(entry.LiteLLMParams["market_price_tiers"])
		var tiers []catalog.MarketTier
		if err != nil || json.Unmarshal(raw, &tiers) != nil || len(tiers) == 0 || len(tiers) > 64 {
			return
		}
		result := make([]availablePriceTier, 0, len(tiers))
		for index, tier := range tiers {
			rates, ok := publicRates(tier.Rates)
			if !ok || tier.InputRange[0] < 0 || tier.InputRange[1] <= tier.InputRange[0] || tier.OutputRange[0] != 0 || tier.OutputRange[1] <= 0 {
				return
			}
			if index == 0 && tier.InputRange[0] != 0 || index > 0 && (tiers[index-1].InputRange[1] != tier.InputRange[0] || tiers[0].OutputRange != tier.OutputRange) {
				return
			}
			result = append(result, availablePriceTier{InputRange: tier.InputRange, OutputRange: tier.OutputRange, Rates: rates})
		}
		card["price_tiers"] = result
		return
	}
	// 显式费率表是实际计费的优先来源；只在存在有效快照时覆盖旧版卡片的平面价格。
	if rates, ok := catalog.DecodeRates(entry.LiteLLMParams["rates"]); ok {
		if visible, valid := publicRates(rates); valid {
			card["rates"] = visible
		}
	}
}
