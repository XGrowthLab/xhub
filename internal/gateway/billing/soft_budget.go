package billing

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/config"
)

// SoftPricePlan 冻结实际出站部署和目录价格，结算时使用实际token，不需要输入窗口或预占金额。
// 内部快照独立于可变模型配置；仅供网关单次尝试使用，不写数据库。
type SoftPricePlan struct {
	dep             config.ModelEntry
	row             map[string]any
	started         time.Time
	err             error
	tokenOnly       bool
	searchRequested bool
}

// WithSearchRequest 冻结本次实际出站请求是否声明联网搜索；requested 为已解析标记，返回独立计费计划。
// 预算准入调用；不修改部署或请求正文，搜索用量缺失时结算必须标为未知而不是猜零。
func (p SoftPricePlan) WithSearchRequest(requested bool) SoftPricePlan {
	p.searchRequested = requested
	return p
}

// IncludesSearch 判断本次计划是否可能按搜索计费；返回标志供响应头和账单说明使用，不改变价格或额度。
// 检查实际请求及冻结模型的明确搜索报价；非对话计价仍沿用既有模型用量说明。
func (p SoftPricePlan) IncludesSearch() bool {
	if p.searchRequested {
		return true
	}
	source := budgetPriceSource(p.dep, p.row)
	if source["search_context_cost_per_query"] != nil {
		return true
	}
	if rates, ok := catalog.DecodeRates(source["rates"]); ok {
		for _, r := range rates {
			if r.Measure == "query" && (r.Variant == "search" || r.SourceKey == "search_context_cost_per_query" || r.SourceKey == "web_search_req") {
				return true
			}
		}
	}
	raw, err := json.Marshal(source["pricing_rules_v2"])
	var rules []struct {
		Details map[string]any `json:"details_v2"`
	}
	if err == nil && json.Unmarshal(raw, &rules) == nil {
		for _, rule := range rules {
			if rule.Details["web_search_req"] != nil {
				return true
			}
		}
	}
	return false
}

// NewSoftPricePlan 在出站前复制价格源；参数是实际部署、开始时间及可选的仅Token标志（默认true），返回只读计价计划。
// 缺少价格不阻止对话，结算返回明确错误供日志标记未定价，绝不冒充免费。
func NewSoftPricePlan(dep config.ModelEntry, started time.Time, tokenOnly ...bool) SoftPricePlan {
	p := SoftPricePlan{started: started, tokenOnly: true}
	if len(tokenOnly) > 0 {
		p.tokenOnly = tokenOnly[0]
	}
	raw, err := json.Marshal(dep)
	if err == nil {
		err = json.Unmarshal(raw, &p.dep)
	}
	if err == nil {
		raw, err = json.Marshal(budgetCatalogRow(dep))
	}
	if err == nil {
		err = json.Unmarshal(raw, &p.row)
	}
	p.err = err
	return p
}

// Charge 按实际用量和冻结价格计算软预算费用；对话保留缓存TTL、分档、缺失/冲突检查，其他模型保留各维度定价。
// 参数为上游原始usage，返回分项账单或未定价错误；搜索次数按冻结单价计费，其他工具不猜测价格。
func (p SoftPricePlan) Charge(raw map[string]any) (catalog.Charge, error) {
	if p.err != nil {
		return catalog.Charge{}, p.err
	}
	if !p.tokenOnly {
		u := catalog.NormalizeUsage(raw)
		if c, ok := deploymentCost(p.dep, u, p.started); ok {
			return c, nil
		}
		if _, has := deploymentRates(p.dep.LiteLLMParams); has || p.dep.ModelInfo["pricing_source"] == "manual" {
			return catalog.Charge{}, fmt.Errorf("model pricing is incomplete")
		}
		if rates, ok := catalog.DecodeRates(p.row["rates"]); ok && len(rates) > 0 {
			if c, ok := catalog.CostFromRates(rates, u, p.started); ok {
				return c, nil
			}
		} else if c, ok := catalog.CostFromFlatOrRates(func(field string) (float64, bool) { return floatParam(p.row, field) }, u, p.started); ok {
			return c, nil
		}
		return catalog.Charge{}, fmt.Errorf("model pricing is unavailable")
	}
	u := SearchTokenUsage(raw)
	searches, searchReported, searchErr := SearchUsage(raw)
	if searchErr != nil {
		return catalog.Charge{}, searchErr
	}
	if p.searchRequested && !searchReported {
		return catalog.Charge{}, fmt.Errorf("measured search usage is missing")
	}
	input := u.PromptTokens + u.CacheWriteTokens
	snapshot := budgetPriceSnapshot{InputImagesIncluded: true, InputBound: input, OutputCap: u.CompletionTokens, Amount: math.MaxFloat64, Window: catalog.WindowAt(p.started), Model: p.dep.ParamString("model", "")}
	tiers, tiered, err := budgetTiers(p.dep, p.row, input, u.CompletionTokens, snapshot.Window, "")
	if err != nil {
		return catalog.Charge{}, err
	}
	var searchRate *catalog.Rate
	if tiered {
		snapshot.Tiers = tiers
		if p.dep.LiteLLMParams["market_tier_semantics"] == "modelink_open_closed_v1" && p.dep.ModelInfo["pricing_source"] == "catalog" {
			// 只对已确认的 Modelink 快照使用来源明确的边界；未知来源仍拒绝歧义。
			snapshot.IntervalSemantics = "modelink_open_closed_v1"
		}
		snapshot.Input, snapshot.Output, snapshot.CacheRead, snapshot.CacheWrite = budgetTierMaximum(tiers)
		snapshot.CacheWriteByTTL = budgetMaximumCacheTTL(tiers)
		selected, selectErr := budgetSelectTier(tiers, u, snapshot.IntervalSemantics)
		if selectErr != nil {
			return catalog.Charge{}, selectErr
		}
		searchRate = selected.Search
	} else {
		rates, e := budgetRates(p.dep, p.row)
		if e != nil {
			return catalog.Charge{}, e
		}
		prices, e := budgetSelectPrices(rates, snapshot.Window, "")
		if e != nil {
			return catalog.Charge{}, e
		}
		snapshot.Input, snapshot.Output, snapshot.CacheRead, snapshot.CacheWrite = prices.Input, prices.Output, prices.CacheRead, prices.CacheWrite
		snapshot.CacheWriteByTTL = prices.CacheWriteByTTL
		searchRate = prices.Search
	}
	plan := BudgetPlan{frozen: &snapshot, soft: true}
	charge, err := plan.Charge(u, raw)
	// 冻结费率完全缺少缓存写入项时，期限校验先返回通用错误；只改善日志分类，不改变原计费结果。
	if err != nil && err.Error() == "measured cache creation TTL differs from the reserved price" && snapshot.CacheWrite == nil && u.CacheWriteTokens > 0 {
		return charge, fmt.Errorf("measured cache writes have no reserved price")
	}
	if err != nil {
		return charge, err
	}
	if searches > 0 {
		if searchRate == nil {
			return catalog.Charge{}, fmt.Errorf("measured searches have no confirmed price")
		}
		budgetAddCharge(&charge, *searchRate, searches)
		charge.Total = charge.Input + charge.Output + charge.Cache
		if math.IsNaN(charge.Total) || math.IsInf(charge.Total, 0) {
			return catalog.Charge{}, fmt.Errorf("measured search cost is invalid")
		}
	}
	return charge, err
}

// SoftTokenOnly 判断对话是否采用仅Token计费；参数为部署和出站协议，返回是否排除托管工具次数。
// 图片/音视频接口及仅按秒、张、查询收费的部署保留已有计费维度，准入仍统一软预算，不把它们改成免费。
func SoftTokenOnly(dep config.ModelEntry, protocol string) bool {
	switch protocol {
	case "openai-chat", "openai-responses", "anthropic", "anthropic-messages", "gemini", "vertex":
	default:
		return false
	}
	source := budgetPriceSource(dep, budgetCatalogRow(dep))
	if rates, ok := catalog.DecodeRates(source["rates"]); ok && len(rates) > 0 {
		for _, r := range rates {
			if r.Measure == "token" {
				return true
			}
		}
		return false
	}
	_, input := floatParam(source, "input_cost_per_token")
	_, output := floatParam(source, "output_cost_per_token")
	if input || output {
		return true
	}
	for _, field := range []string{"input_cost_per_second", "output_cost_per_second", "input_cost_per_image", "output_cost_per_image", "search_context_cost_per_query"} {
		if _, ok := floatParam(source, field); ok {
			return false
		}
	}
	return true
}
