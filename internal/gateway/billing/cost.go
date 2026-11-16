package billing

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/config"
)

// DeploymentFinder 是结算消费方的最小配置接口；按 ID 查找部署，不修改配置。
type DeploymentFinder interface {
	FindDeployment(string) (config.ModelEntry, bool)
}

// CallCost 按请求开始时间选择部署费率或目录费率。参数为部署查找接口、别名、部署 ID、用量和开始时间；返回费用及费率快照。
// 调用：网关结算；不写数据库，缺少明确费率时返回 ok=false。
//
// A rate typed on the deployment wins. Otherwise the price map is read by the
// upstream model id, then by the public name.
//
// The instant matters: a window-priced model costs twice as much inside its
// peak hours, so pricing a call without its start time undercharges every peak
// request by half. start is the request's own start, which is also what the
// usage row records, so a log row can be re-checked later.
//
// 参数 alias（string）：对外模型名；depID（string）：部署 id。空串表示当前没有钉住的部署；usage（catalog.Usage）：这一次调用报出来的用量，含缓存命中和按秒按张的数量；start（time.Time）：调用开始的时刻，时段由它决定。
// 返回 total（float64）：这一次的总费用；input（float64）：输入侧费用；output（float64）：输出侧费用；ok（bool）：真表示找到了可用结果；charge（catalog.Charge）：这次实际用到的费率，用来落 price_snapshot。
// 调用：recordSpend。
// 测试：billing/cost_test.go
func CallCost(s DeploymentFinder, alias, depID string, usage catalog.Usage, start time.Time) (total, input, output float64, ok bool, charge catalog.Charge) {
	if dep, found := s.FindDeployment(depID); found {
		if dep.LiteLLMParams["market_tier_semantics"] == "modelink_open_closed_v1" {
			// 少数没有尝试快照的旧结算入口仍须使用冻结部署分档，绝不能回落目录首档。
			c, valid := marketTierCost(dep, usage, start)
			return c.Total, c.Input, c.Output, valid, c
		}
		if c, ok := deploymentCost(dep, usage, start); ok {
			return c.Total, c.Input, c.Output, true, c
		}
		if _, hasRates := deploymentRates(dep.LiteLLMParams); hasRates || dep.ModelInfo["pricing_source"] == "manual" {
			return 0, 0, 0, false, catalog.Charge{}
		}
		if id, _ := dep.ModelInfo["base_model"].(string); id != "" {
			c, ok := catalog.CostAt(id, usage, start)
			return c.Total, c.Input, c.Output, ok, c
		}
		if usage.PricingModel != "" {
			c, ok := catalog.CostAt(usage.PricingModel, usage, start)
			return c.Total, c.Input, c.Output, ok, c
		}
		if id := dep.ParamString("model", ""); id != "" && id != alias {
			if c, ok := catalog.CostAt(id, usage, start); ok {
				return c.Total, c.Input, c.Output, true, c
			}
		}
	}
	c, ok := catalog.CostAt(alias, usage, start)
	if !ok {
		return 0, 0, 0, false, catalog.Charge{}
	}
	return c.Total, c.Input, c.Output, true, c
}

// marketTierCost 按已确认的 Modelink 分档和已归一化用量计算一次费用。
// dep 为实际部署、usage 为实测用量、start 为请求开始时间；返回账单及是否完整计价。
// 旧结算入口缺少缓存 TTL 原始分桶时必须拒绝不确定账单；正常请求优先使用出站时冻结的计划。
func marketTierCost(dep config.ModelEntry, usage catalog.Usage, start time.Time) (catalog.Charge, bool) {
	if dep.ModelInfo["pricing_source"] != "catalog" || usage.PromptTokens < 0 || usage.CacheWriteTokens < 0 || usage.CompletionTokens < 0 {
		return catalog.Charge{}, false
	}
	if int64(usage.PromptTokens)+int64(usage.CacheWriteTokens) > math.MaxInt32 {
		return catalog.Charge{}, false
	}
	input := usage.PromptTokens + usage.CacheWriteTokens
	tiers, tiered, err := budgetTiers(dep, nil, input, usage.CompletionTokens, catalog.WindowAt(start), "")
	if err != nil || !tiered || len(tiers) == 0 {
		return catalog.Charge{}, false
	}
	snapshot := budgetPriceSnapshot{InputImagesIncluded: true, InputBound: input, OutputCap: usage.CompletionTokens, Amount: math.MaxFloat64, Window: catalog.WindowAt(start), Model: dep.ParamString("model", ""), Tiers: tiers, IntervalSemantics: "modelink_open_closed_v1"}
	snapshot.Input, snapshot.Output, snapshot.CacheRead, snapshot.CacheWrite = budgetTierMaximum(tiers)
	snapshot.CacheWriteByTTL = budgetMaximumCacheTTL(tiers)
	plan := BudgetPlan{frozen: &snapshot, soft: true}
	charge, err := plan.Charge(usage)
	return charge, err == nil
}

// deploymentCost prices a call from the prices typed on the deployment itself.
//
// A deployment may carry its own rate table, and it has the same shape as the
// catalog's, so a deployment can express an off-peak price and a peak price
// separately. It is tried before the catalog because an operator who typed a
// price meant it.
//
// The console submits flat unit-price fields: the unit-price form submits
// one field per side rather than a table. They go through catalog.RatesFromFlat
// and then the same arithmetic as a rate table, rather than through a second
// copy of the billing rules here. That second copy is what used to happen, and
// it dropped three things an operator had already typed: the peak price, the
// cache-write price, and every measure that is not a token - so a video
// deployment with a per-second price recorded a row of zero spend.
//
// 参数 dep（config.ModelEntry）：这条部署；usage（catalog.Usage）：这一次调用报出来的用量；start（time.Time）：调用开始的时刻。
// 返回 catalog.Charge（catalog.Charge）：按部署自己的费率算出的账单；bool（bool）：部署上写了价时为真。
// 调用：callCost。
// 测试：billing/cost_test.go
func deploymentCost(dep config.ModelEntry, usage catalog.Usage, start time.Time) (catalog.Charge, bool) {
	params := dep.LiteLLMParams
	if params == nil {
		return catalog.Charge{}, false
	}
	// A rate table on the deployment is the full form and can carry windows and
	// variants the flat fields have no name for.
	if rates, ok := deploymentRates(params); ok {
		return catalog.CostFromRates(rates, usage, start)
	}
	// Otherwise the flat fields, read through the same biller. A field that is
	// absent stays absent: the difference between "no price" and "a price of
	// zero" decides whether this call is billed at all.
	return catalog.CostFromFlatOrRates(func(field string) (float64, bool) {
		return floatParam(params, field)
	}, usage, start)
}

// deploymentRates reads a rate table typed on a deployment. The shape is the
// same as the catalog's rates array, so one renderer and one billing path
// handle both.
// 参数 params（map[string]any）：部署上的 litellm_params。
// 返回 []catalog.Rate（[]catalog.Rate）：部署自己写的费率表；bool（bool）：写了解析得出来时为真。
// 调用：deploymentCost。
// 测试：billing/cost_test.go
func deploymentRates(params map[string]any) ([]catalog.Rate, bool) {
	raw, ok := params["rates"]
	if !ok || raw == nil {
		return nil, false
	}
	return catalog.DecodeRates(raw)
}

// 从 litellm_params 读取一个小数。缺键或类型不符时 ok 为假。
// 参数 params（map[string]any）：部署上的 litellm_params。常见键是 model、api_base、api_key；key（string）：上游或调用方的密钥。空串表示还不能转发或还没有密钥。
// 返回 float64（float64）：小数参数。缺失时为 0，不要把它理解成免费除非调用方另有约定；bool（bool）：litellm_params 里有这个小数键且类型正确时返回真。
// 调用：仅在 spend.go 内使用
// 测试：无直接单测
func floatParam(params map[string]any, key string) (float64, bool) {
	if params == nil {
		return 0, false
	}
	v, ok := params[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
