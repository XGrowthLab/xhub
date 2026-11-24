package models

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/config"
)

// TestAvailableConfirmedTierPrices 验证已确认分档、缓存期限与快照隔离；前置内存市场夹具，无网络或持久化，结束由 GC 回收。
func TestAvailableConfirmedTierPrices(t *testing.T) {
	tiers, reason := catalog.MarketTiers(marketTierPreviewFixture(t))
	if reason != "" {
		t.Fatal(reason)
	}
	dep := config.ModelEntry{ModelName: "renamed-tier", ModelInfo: map[string]any{"pricing_source": "catalog"}, LiteLLMParams: map[string]any{"market_tier_semantics": "modelink_open_closed_v1", "market_price_tiers": tiers, "api_key": "secret-never-visible", "input_cost_per_token": 99}}
	card := availableCard(dep)
	shown, ok := card["price_tiers"].([]availablePriceTier)
	if !ok || len(shown) != 2 || shown[0].InputRange != tiers[0].InputRange || shown[1].Rates[0].USD != tiers[1].Rates[0].USD {
		t.Fatalf("已确认的两档价格与区间必须完整显示: %#v", card)
	}
	if card["input_price"] != nil || card["output_price"] != nil {
		t.Fatal("分档不能伪装为单一费率")
	}
	raw, _ := json.Marshal(card)
	if strings.Contains(string(raw), "secret-never-visible") || strings.Contains(string(raw), "api_key") {
		t.Fatal("公开价格不能泄漏部署秘密")
	}
	shown[0].Rates[0].USD = 0
	if tiers[0].Rates[0].USD == 0 {
		t.Fatal("展示对象不可修改已确认价格")
	}
}

// TestAvailablePricesRejectInvalidSnapshots 覆盖空快照、坏语义、非法金额及不连续区间；任何坏档都不回退旧平面价格，无外部数据。
func TestAvailablePricesRejectInvalidSnapshots(t *testing.T) {
	for _, name := range []string{"missing", "bad-json", "wrong-source", "wrong-semantics", "negative", "infinite", "gap"} {
		t.Run(name, func(t *testing.T) {
			tiers, _ := catalog.MarketTiers(marketTierPreviewFixture(t))
			dep := config.ModelEntry{ModelInfo: map[string]any{"pricing_source": "catalog"}, LiteLLMParams: map[string]any{"market_tier_semantics": "modelink_open_closed_v1", "market_price_tiers": tiers, "input_cost_per_token": 1}}
			switch name {
			case "missing":
				delete(dep.LiteLLMParams, "market_price_tiers")
			case "bad-json":
				dep.LiteLLMParams["market_price_tiers"] = "invalid"
			case "wrong-source":
				dep.ModelInfo["pricing_source"] = "manual"
			case "wrong-semantics":
				dep.LiteLLMParams["market_tier_semantics"] = "unknown"
			case "negative":
				tiers[0].Rates[0].USD = -1
			case "infinite":
				tiers[0].Rates[0].USD = math.Inf(1)
			case "gap":
				tiers[1].InputRange[0]++
			}
			card := availableCard(dep)
			if card["price_tiers"] != nil || card["input_price"] != nil {
				t.Fatalf("坏分档不能回退成有效报价: %#v", card)
			}
		})
	}
}

// TestPublicRatesSnapshot 验证普通已确认费率、免费、历史搜索、真实秒计价和非法输入。
// 模型卡片只返回公开维度并保留原始计费快照，全部为内存数据，无清理副作用。
func TestPublicRatesSnapshot(t *testing.T) {
	valid := []catalog.Rate{
		{Measure: "token", Side: "input", Window: "all", USD: 0},
		{Measure: "second", Side: "output", Variant: "search", Window: "all", USD: .01},
		{Measure: "second", Side: "output", Variant: "video", Window: "all", USD: .25},
	}
	dep := config.ModelEntry{LiteLLMParams: map[string]any{"rates": valid}}
	card := availableCard(dep)
	if rates, ok := card["rates"].([]availableRate); !ok || len(rates) != 3 || rates[0].USD != 0 || rates[1].Measure != "query" || rates[1].Variant != "search" || rates[1].USD != .01 || rates[2].Measure != "second" || valid[1].Measure != "second" {
		t.Fatalf("免费和搜索费率必须保留: %#v", card)
	}
	for _, rates := range [][]catalog.Rate{nil, {{Measure: "", Side: "input", Window: "all"}}, {{Measure: "token", Side: "input", Window: "all", USD: math.NaN()}}, {{Measure: "token", Side: "input", Window: "all", USD: -1}}} {
		if _, ok := publicRates(rates); ok {
			t.Fatalf("非法费率被接受: %#v", rates)
		}
	}
}
