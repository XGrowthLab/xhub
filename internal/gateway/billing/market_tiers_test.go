package billing

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/config"
)

// TestModelinkTierBoundaryAndSnapshot 验证 Modelink 明示的左开右闭区间用于软硬预算并冻结费率。
// 使用独立内存目录，覆盖边界两侧、缓存读和原目录变化；无外部资源或持久数据需要清理。
func TestModelinkTierBoundaryAndSnapshot(t *testing.T) {
	low, high := tierRule(0, 50, 1), tierRule(50, 200, 2)
	d := tierDeployment(low, high)
	d.ModelInfo["pricing_source"] = "catalog"
	d.LiteLLMParams["market_tier_semantics"] = "modelink_open_closed_v1"
	hard := tierPlan(t, d)
	soft := NewSoftPricePlan(d, time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC))
	// 修改原始目录后已创建的两个计划应继续使用旧费率。
	high["details_v2"].(map[string]any)["output"] = tierUnit(99)
	for _, tc := range []struct {
		name        string
		input, read int
		want        float64
	}{
		{"below", 49, 0, .053},
		{"boundary belongs to lower tier", 50, 0, .054},
		{"above", 51, 0, .110},
		{"cached total selects upper tier", 51, 30, .056},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := catalog.Usage{PromptTokens: tc.input, CompletionTokens: 2, CachedTokens: tc.read}
			a, err := hard.Charge(u)
			if err != nil || math.Abs(a.Total-tc.want) > 1e-12 {
				t.Fatalf("硬预算分档错误: got=%+v want=%g err=%v", a, tc.want, err)
			}
			b, err := soft.Charge(map[string]any{"input_tokens": tc.input, "output_tokens": 2, "input_tokens_details": map[string]any{"cached_tokens": tc.read}})
			if err != nil || math.Abs(b.Total-tc.want) > 1e-12 {
				t.Fatalf("软预算分档错误: got=%+v want=%g err=%v", b, tc.want, err)
			}
		})
	}
}

// TestModelinkFixtureTierCharges 使用固定公开报价验证 GPT 和 Grok 的真实边界及实扣金额。
// 夹具不访问供应商或数据库；每个计划冻结独立目录，测试结束自动回收。
func TestModelinkFixtureTierCharges(t *testing.T) {
	raw, err := os.ReadFile("../../catalog/testdata/modelink-tiers.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := catalog.BuildPriceDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id    string
		input int
		want  float64
	}{
		{"openai/gpt-6.1-terra", 272000, .272010},
		{"openai/gpt-6.1-terra", 272001, .544017},
		{"openai/gpt-6.2-sol", 272000, .544020},
		{"openai/gpt-6.2-sol", 272001, 1.088034},
		{"openai/gpt-6.2-sol", 1050000, 4.200030},
		{"openai/gpt-6.2-astra", 272000, 2.176080},
		{"openai/gpt-6.2-astra", 272001, 4.352136},
		{"x-ai/grok-4.6", 200000, .400012},
		{"x-ai/grok-4.6", 200001, .800028},
	} {
		t.Run(tc.id+"/"+fmt.Sprint(tc.input), func(t *testing.T) {
			row := doc.Models[tc.id]
			dep := strictBudgetDeployment()
			dep.ModelInfo["pricing_source"] = "catalog"
			dep.LiteLLMParams = map[string]any{"model": tc.id, "pricing_rules_v2": row["pricing_rules_v2"], "market_tier_semantics": "modelink_open_closed_v1"}
			tiers, reason := catalog.MarketTiers(row)
			if reason != "" {
				t.Fatalf("公开报价未形成可确认快照: %s", reason)
			}
			dep.LiteLLMParams["market_price_tiers"] = tiers
			plan := NewSoftPricePlan(dep, time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC))
			charge, err := plan.Charge(map[string]any{"input_tokens": tc.input, "output_tokens": 2})
			if err != nil || math.Abs(charge.Total-tc.want) > 1e-10 {
				t.Fatalf("公开分档计费错误: got=%+v want=%g err=%v", charge, tc.want, err)
			}
		})
	}
}

// TestModelinkTierCacheDurationsRejectUnknown 验证额外缓存期限被完整冻结且未测得期限时不猜价格。
// 纯内存夹具覆盖正常 Token 计费与聚合缓存写入错误，不修改用户配置或数据库。
func TestModelinkTierCacheDurationsRejectUnknown(t *testing.T) {
	low, high := tierRule(0, 50, 1), tierRule(50, 200, 2)
	for _, rule := range []map[string]any{low, high} {
		details := rule["details_v2"].(map[string]any)
		details["c_30m_cache"] = tierUnit(.00125)
		details["c_12h_cache"] = tierUnit(.0025)
	}
	d := tierDeployment(low, high)
	d.ModelInfo["pricing_source"] = "catalog"
	d.LiteLLMParams["market_tier_semantics"] = "modelink_open_closed_v1"
	plan := tierPlan(t, d)
	if _, ok := plan.frozen.CacheWriteByTTL["30m"]; !ok {
		t.Fatal("30 分钟缓存价未冻结")
	}
	if _, ok := plan.frozen.CacheWriteByTTL["12h"]; !ok {
		t.Fatal("12 小时缓存价未冻结")
	}
	if _, err := plan.Charge(catalog.Usage{PromptTokens: 10, CompletionTokens: 1}); err != nil {
		t.Fatalf("无缓存写入的普通请求应可计费: %v", err)
	}
	if _, err := plan.Charge(catalog.Usage{PromptTokens: 10, CompletionTokens: 1, CacheWriteTokens: 3}); err == nil {
		t.Fatal("有多种缓存期限且缺少实测分桶时不能猜测价格")
	}
	raw := map[string]any{"input_tokens": 10, "output_tokens": 1, "cache_creation_input_tokens": 3, "cache_creation": map[string]any{"ephemeral_30m_input_tokens": 1, "ephemeral_12h_input_tokens": 2}}
	charge, err := plan.Charge(catalog.Usage{PromptTokens: 10, CompletionTokens: 1, CacheWriteTokens: 3}, raw)
	if err != nil || math.Abs(charge.Total-.01825) > 1e-12 {
		t.Fatalf("实测的 30 分钟和 12 小时缓存应分别按价计费: %+v %v", charge, err)
	}
	raw["cache_creation"] = map[string]any{"ephemeral_5m_input_tokens": 3}
	if _, err := plan.Charge(catalog.Usage{PromptTokens: 10, CompletionTokens: 1, CacheWriteTokens: 3}, raw); err == nil {
		t.Fatal("来源没有明确 5 分钟报价时不能套用 30 分钟价格")
	}
}

// TestMarketTierOpenRangeRequiresConfirmation 验证 -1 不能扩展手工预算、伪造来源或缺失/损坏的市场快照。
// 使用内存部署同时核对正常软硬计费和错误路径，不调用供应商或写入数据库，退出由 GC 回收。
func TestMarketTierOpenRangeRequiresConfirmation(t *testing.T) {
	low, high := tierRule(0, 50, 1), tierRule(50, 200, 2)
	low["output_range"], high["output_range"] = []int{0, -1}, []int{0, -1}
	high["input_range"] = []int{50, -1}
	d := tierDeployment(low, high)
	d.ModelInfo["pricing_source"] = "catalog"
	d.LiteLLMParams["market_tier_semantics"] = "modelink_open_closed_v1"
	d.LiteLLMParams["market_price_tiers"] = []catalog.MarketTier{{InputRange: [2]int{0, 50}, OutputRange: [2]int{0, 8192}}, {InputRange: [2]int{50, 100}, OutputRange: [2]int{0, 8192}}}
	hard := tierPlan(t, d)
	soft := NewSoftPricePlan(d, time.Now())
	a, err := hard.Charge(catalog.Usage{PromptTokens: 51, CompletionTokens: 2})
	b, softErr := soft.Charge(map[string]any{"input_tokens": 51, "output_tokens": 2})
	if err != nil || softErr != nil || math.Abs(a.Total-.110) > 1e-12 || math.Abs(b.Total-a.Total) > 1e-12 {
		t.Fatalf("开放上界的软硬计费不一致: hard=%+v soft=%+v err=%v/%v", a, b, err, softErr)
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any, map[string]any)
	}{
		{"manual source", func(info, params map[string]any) { info["pricing_source"] = "manual" }},
		{"unknown semantics", func(info, params map[string]any) { params["market_tier_semantics"] = "unknown" }},
		{"missing snapshot", func(info, params map[string]any) { delete(params, "market_price_tiers") }},
		{"wrong snapshot count", func(info, params map[string]any) { params["market_price_tiers"] = []any{} }},
		{"malformed snapshot", func(info, params map[string]any) { params["market_price_tiers"] = "invalid" }},
		{"conflicting lower", func(info, params map[string]any) {
			params["market_price_tiers"].([]any)[1].(map[string]any)["input_range"] = []int{49, 100}
		}},
		{"negative confirmed upper", func(info, params map[string]any) {
			params["market_price_tiers"].([]any)[1].(map[string]any)["input_range"] = []int{50, -1}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(d)
			var broken config.ModelEntry
			if err := json.Unmarshal(raw, &broken); err != nil {
				t.Fatal(err)
			}
			tc.edit(broken.ModelInfo, broken.LiteLLMParams)
			plan := NewSoftPricePlan(broken, time.Now())
			if _, err := plan.Charge(map[string]any{"input_tokens": 51, "output_tokens": 2}); err == nil {
				t.Fatal("未确认或不一致的开放上界必须拒绝计费")
			}
		})
	}
}
