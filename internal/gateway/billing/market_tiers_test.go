package billing

import (
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/catalog"
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
		{"x-ai/grok-4.6", 200000, .400012},
		{"x-ai/grok-4.6", 200001, .800028},
	} {
		t.Run(tc.id+"/"+fmt.Sprint(tc.input), func(t *testing.T) {
			row := doc.Models[tc.id]
			dep := strictBudgetDeployment()
			dep.ModelInfo["pricing_source"] = "catalog"
			dep.LiteLLMParams = map[string]any{"model": tc.id, "pricing_rules_v2": row["pricing_rules_v2"], "market_tier_semantics": "modelink_open_closed_v1"}
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
