package billing

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/catalog"
)

// TestWebSearchRequested 验证三种协议的搜索声明和普通客户端工具区分；仅解析合成正文，不访问外部服务。
func TestWebSearchRequested(t *testing.T) {
	for _, tc := range []struct {
		protocol, raw string
		want          bool
	}{
		{"openai-responses", `{"tools":[{"type":"web_search"}]}`, true},
		{"openai-chat", `{"web_search_options":{}}`, true},
		{"anthropic-messages", `{"tools":[{"type":"web_search_20250305"}]}`, true},
		{"openai-responses", `{"tools":[{"type":"function","name":"web_search"}]}`, false},
		{"openai-responses", `{"input":[{"type":"web_search_call"}]}`, false},
		{"other", `{"tools":[{"type":"web_search"}]}`, false},
		{"openai-responses", `{`, false},
	} {
		if got := WebSearchRequested(tc.protocol, []byte(tc.raw)); got != tc.want {
			t.Fatalf("%s 搜索判定错误: %v", tc.protocol, got)
		}
	}
}

// TestSearchUsage 验证上游明确报告的总数、等价别名、缺失、非法值及冲突；输入为内存数据，不计算或推测用量。
func TestSearchUsage(t *testing.T) {
	for _, tc := range []struct {
		name         string
		raw          map[string]any
		count        int
		present, bad bool
	}{
		{"fenno", map[string]any{"tool_usage": map[string]any{"web_search": 2}, "tool_usage_details": map[string]any{"web_search": map[string]any{"search_engine": 2}}}, 2, true, false},
		{"zero", map[string]any{"searches": 0}, 0, true, false},
		{"same aliases", map[string]any{"search_count": 2, "server_tool_use": map[string]any{"web_search_requests": 2}}, 2, true, false},
		{"not reported", map[string]any{"input_tokens": 1}, 0, false, false},
		{"only breakdown", map[string]any{"tool_usage_details": map[string]any{"web_search": map[string]any{"search_engine": 2}}}, 0, false, false},
		{"conflict", map[string]any{"searches": 1, "tool_usage": map[string]any{"web_search": 2}}, 0, false, true},
		{"negative", map[string]any{"search_count": -1}, 0, false, true},
		{"fraction", map[string]any{"search_count": .5}, 0, false, true},
		{"string", map[string]any{"search_count": "2"}, 0, false, true},
		{"container", map[string]any{"tool_usage": "invalid"}, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, present, err := SearchUsage(tc.raw)
			if n != tc.count || present != tc.present || (err != nil) != tc.bad {
				t.Fatalf("搜索计数错误 count=%d present=%v err=%v", n, present, err)
			}
		})
	}
}

// TestSoftSearchPricing 验证按实际搜索总数与冻结报价计费，而非按响应工具项数或最新目录猜价。
// 分别覆盖计费、零次、无报价、缺失和矛盾；独立内存部署，无资源或生产数据需清理。
func TestSoftSearchPricing(t *testing.T) {
	dep := strictBudgetDeployment()
	dep.ModelInfo["pricing_source"] = "manual"
	dep.LiteLLMParams["search_context_cost_per_query"] = .01
	plan := NewSoftPricePlan(dep, time.Now()).WithSearchRequest(true)
	if !plan.IncludesSearch() {
		t.Fatal("明确搜索价格未反映到计费范围")
	}
	dep.LiteLLMParams["search_context_cost_per_query"] = 99
	for _, tc := range []struct {
		raw  map[string]any
		want float64
		bad  bool
	}{
		{map[string]any{"input_tokens": 10, "output_tokens": 2, "tool_usage": map[string]any{"web_search": 2}}, .020014, false},
		{map[string]any{"input_tokens": 10, "output_tokens": 2, "searches": 0}, .000014, false},
		{map[string]any{"input_tokens": 10, "output_tokens": 2}, 0, true},
		{map[string]any{"input_tokens": 10, "output_tokens": 2, "searches": 1, "tool_usage": map[string]any{"web_search": 2}}, 0, true},
	} {
		charge, err := plan.Charge(tc.raw)
		if (err != nil) != tc.bad || !tc.bad && math.Abs(charge.Total-tc.want) > 1e-12 {
			t.Fatalf("搜索费用错误: %+v want=%g err=%v", charge, tc.want, err)
		}
	}
	delete(dep.LiteLLMParams, "search_context_cost_per_query")
	if _, err := NewSoftPricePlan(dep, time.Now()).WithSearchRequest(true).Charge(map[string]any{"input_tokens": 1, "output_tokens": 1, "searches": 1}); err == nil {
		t.Fatal("实测搜索缺价不能记为免费")
	}
}

// TestTieredSearchUsesFrozenBand 验证 Modelink 分档的每次搜索价与上下文阈值一起冻结，未知单位或缺价不猜测。
// 仅使用内存报价，分别覆盖两档、价格修改、单价缺失与不支持单位，无生产数据需要清理。
func TestTieredSearchUsesFrozenBand(t *testing.T) {
	low, high := tierRule(0, 50, 1), tierRule(50, 200, 2)
	low["details_v2"].(map[string]any)["web_search_req"] = map[string]any{"unit_name": "time", "unit_size": 1, "unit_price_usd": .01}
	high["details_v2"].(map[string]any)["web_search_req"] = map[string]any{"unit_name": "time", "unit_size": 1, "unit_price_usd": .02}
	dep := tierDeployment(low, high)
	dep.ModelInfo["pricing_source"] = "catalog"
	dep.LiteLLMParams["market_tier_semantics"] = "modelink_open_closed_v1"
	plan := NewSoftPricePlan(dep, time.Now()).WithSearchRequest(true)
	high["details_v2"].(map[string]any)["web_search_req"].(map[string]any)["unit_price_usd"] = 99
	for _, tc := range []struct {
		in   int
		want float64
	}{{50, .074}, {51, .15}} {
		got, err := plan.Charge(map[string]any{"input_tokens": tc.in, "output_tokens": 2, "tool_usage": map[string]any{"web_search": 2}})
		if err != nil || math.Abs(got.Total-tc.want) > 1e-12 {
			t.Fatalf("分档搜索价格错误 input=%d got=%+v want=%g err=%v", tc.in, got, tc.want, err)
		}
	}
	delete(low["details_v2"].(map[string]any), "web_search_req")
	if _, err := NewSoftPricePlan(dep, time.Now()).WithSearchRequest(true).Charge(map[string]any{"input_tokens": 20, "output_tokens": 2, "searches": 1}); err == nil {
		t.Fatal("缺失档位搜索价不应免费")
	}
	high["details_v2"].(map[string]any)["web_search_req"].(map[string]any)["unit_name"] = "second"
	if _, err := NewSoftPricePlan(dep, time.Now()).WithSearchRequest(true).Charge(map[string]any{"input_tokens": 51, "output_tokens": 2, "searches": 1}); err == nil {
		t.Fatal("未知单位不应定价")
	}
}

// TestModelinkDeepSeekSearchAndWindows 验证三个 DeepSeek 的广场快照可完整计费，分时型号按当时价格加实际搜索费。
// 使用经裁剪的公开价格夹具，覆盖高峰、非高峰、搜索和固定价；只使用内存数据，无凭据及清理副作用。
func TestModelinkDeepSeekSearchAndWindows(t *testing.T) {
	raw, err := os.ReadFile("../../catalog/testdata/modelink-ds-prices.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := catalog.BuildPriceDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id       string
		when     time.Time
		searches int
		want     float64
	}{
		{"deepseek/deepseek-v4.1-flash", time.Date(2026, 3, 2, 2, 0, 0, 0, time.UTC), 2, .00231885},
		{"deepseek/deepseek-v4.1-flash", time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC), 2, .001884065},
		{"deepseek/deepseek-v4-flash", time.Date(2026, 3, 2, 2, 0, 0, 0, time.UTC), 0, .00028986},
		{"deepseek/deepseek-v4-pro", time.Date(2026, 3, 2, 2, 0, 0, 0, time.UTC), 0, .00347826},
	} {
		t.Run(tc.id+tc.when.String(), func(t *testing.T) {
			rates, reason := catalog.MarketRates(doc.Models[tc.id])
			if reason != "" {
				t.Fatalf("市场价不能冻结: %s", reason)
			}
			dep := strictBudgetDeployment()
			dep.ModelInfo["pricing_source"] = "catalog"
			dep.LiteLLMParams = map[string]any{"model": tc.id, "rates": rates}
			plan := NewSoftPricePlan(dep, tc.when).WithSearchRequest(tc.searches > 0)
			charge, err := plan.Charge(map[string]any{"input_tokens": 1000, "output_tokens": 500, "tool_usage": map[string]any{"web_search": tc.searches}})
			if err != nil || math.Abs(charge.Total-tc.want) > 1e-11 {
				t.Fatalf("真实广场价格计算错误: charge=%+v want=%g err=%v", charge, tc.want, err)
			}
		})
	}
}
