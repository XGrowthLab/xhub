package regression

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sunqirui1987/xhub/internal/catalog"
)

// TestModelContractMarketTierPriceRealGateway 验证真实管理路由、隔离 PostgreSQL 与上游计量之间的分档计费。
// 本地来源提供两档，覆盖边界、快照冻结、管理员确认和伪造拒绝；harness 在测试结束时清理 schema 与服务。
func TestModelContractMarketTierPriceRealGateway(t *testing.T) {
	before := catalog.PriceDocument{Version: 1, Models: catalog.CostMap(), Providers: catalog.Providers(), Source: catalog.PriceSource(), GeneratedAt: catalog.PriceGeneratedAt()}
	t.Cleanup(func() { _, _ = catalog.ApplyDocument(before) })
	h := newHarness(t)
	admin := h.adminSession()
	tenant := h.provision(t, admin, "tiered-market")
	var factor atomic.Int64
	factor.Store(1)
	unit := func(value float64) map[string]any {
		return map[string]any{"unit_name": "token", "unit_size": 1000000, "unit_price_usd": value}
	}
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := float64(factor.Load())
		rules := []any{
			map[string]any{"input_range": []int{0, 11}, "output_range": []int{0, 99999999}, "details_v2": map[string]any{"ncache": unit(f), "output": unit(5 * f), "cache": unit(.05 * f)}},
			map[string]any{"input_range": []int{11, 99999999}, "output_range": []int{0, 99999999}, "details_v2": map[string]any{"ncache": unit(2 * f), "output": unit(7.5 * f), "cache": unit(.1 * f)}},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "data": []any{map[string]any{"id": "fixture-tier-base", "issuer": map[string]any{"name": "OpenAI"}, "model_constraints": map[string]any{"context_length": 100, "max_completion_tokens": 20}, "pricing_rules_v2": rules}}})
	}))
	defer feed.Close()
	t.Setenv("XHUB_PRICE_FEED_URL", feed.URL)
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	created := h.ok("POST", "/model/new", admin, map[string]any{
		"model_name": "tier-fixture", "litellm_params": map[string]any{"model": "tier-upstream", "api_base": h.credentialAPIBase(), "api_key": "sk-test", "custom_llm_provider": "openai", "input_cost_per_token": 99},
		"model_info": map[string]any{"transport": "bypass_openai_chat", "endpoint_types": []string{"chat"}, "base_model": "fixture-tier-base"},
	})
	if created.json()["model_info"].(map[string]any)["pricing_source"] != "catalog" || created.json()["litellm_params"].(map[string]any)["input_cost_per_token"] != nil {
		t.Fatal("隐式目录来源未固定，或与市场档位冲突的旧平面价格仍然存在")
	}
	id := created.json()["model_info"].(map[string]any)["id"].(string)
	t.Cleanup(func() { h.do("POST", "/model/delete", admin, map[string]any{"id": id}) })
	// 查询普通用户真正拿到的价格快照；返回该模型卡片，授权失败、秘密泄漏或缺失都直接定位到接口。
	visibleCard := func() map[string]any {
		t.Helper()
		response := h.ok("GET", "/model/available", tenant.session, nil)
		if strings.Contains(string(mustJSON(response.json())), "sk-test") {
			t.Fatal("普通用户可见价格不应泄漏上游凭据")
		}
		for _, row := range listField(response.json(), "data") {
			if row["id"] == "tier-fixture" {
				return row
			}
		}
		t.Fatal("普通用户未获得已授权模型的价格卡片")
		return nil
	}
	// 断言公开价格与实际计费同用已确认快照；factor 为确认时的价格倍率，不允许市场自动覆盖。
	assertVisibleTiers := func(factor float64) {
		t.Helper()
		card := visibleCard()
		tiers, ok := card["price_tiers"].([]any)
		if !ok || len(tiers) != 2 || card["input_price"] != nil {
			t.Fatalf("分档卡片应完整显示两档，不能退回单价: %#v", card)
		}
		for index, raw := range tiers {
			tier := raw.(map[string]any)
			rates := tier["rates"].([]any)
			found := false
			for _, rawRate := range rates {
				rate := rawRate.(map[string]any)
				if rate["side"] == "input" {
					found = true
					want := float64(index+1) * factor / 1e6
					if math.Abs(rate["usd"].(float64)-want) > 1e-12 {
						t.Fatalf("展示价与已确认账单不符: got=%v want=%g", rate, want)
					}
				}
			}
			if !found {
				t.Fatalf("第 %d 档缺少输入价格", index)
			}
		}
	}
	if denied := h.do("GET", "/model/available", "sk-invalid", nil); denied.status != 401 {
		t.Fatalf("新增价格展示不能绕过鉴权: %s", denied.describe())
	}
	assertVisibleTiers(1)
	sequence := 0
	call := func(input, cached int) float64 {
		sequence++
		h.usageOverride("tier-upstream", map[string]any{"prompt_tokens": input, "completion_tokens": 5, "prompt_tokens_details": map[string]any{"cached_tokens": cached}})
		response := h.ok("POST", "/v1/chat/completions", tenant.key, chatRequest("tier-fixture", fmt.Sprintf("Verify tier prices %d.", sequence)))
		return parseFloatOrZero(response.header("x-litellm-response-cost"))
	}
	for _, tc := range []struct {
		input, cached int
		want          float64
	}{{10, 0, .000035}, {11, 0, .000036}, {12, 0, .0000615}, {12, 6, .0000501}} {
		if got := call(tc.input, tc.cached); math.Abs(got-tc.want) > 1e-10 {
			t.Fatalf("实测输入=%d 缓存=%d 的费用错误: got=%g want=%g", tc.input, tc.cached, got, tc.want)
		}
	}
	// 市场更新只改变候选；未经管理员确认，旧部署和账单仍采用已冻结报价。
	factor.Store(2)
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	assertVisibleTiers(1)
	if got := call(12, 0); math.Abs(got-.0000615) > 1e-10 {
		t.Fatalf("市场同步擅自更改已确认价格: %g", got)
	}
	rows := listField(h.ok("GET", "/price/deployments", admin, nil).json(), "deployments")
	var selected map[string]any
	for _, row := range rows {
		if row["id"] == id {
			if row["changed"] != true || len(row["proposed_tiers"].([]any)) != 2 {
				t.Fatalf("分档价格预览缺少变化或完整档位: %v", row)
			}
			selected = map[string]any{"id": id, "version": row["version"]}
		}
	}
	if selected == nil {
		t.Fatal("缺少分档部署的价格预览")
	}
	if denied := h.do("POST", "/price/deployments/apply", tenant.session, map[string]any{"items": []any{selected}}); denied.status != 403 {
		t.Fatalf("普通用户不能确认价格: %s", denied.describe())
	}
	h.ok("POST", "/price/deployments/apply", admin, map[string]any{"items": []any{selected}})
	assertVisibleTiers(2)
	if got := call(12, 0); math.Abs(got-.000123) > 1e-10 {
		t.Fatalf("确认后的第二档价格未生效: %g", got)
	}
	if r := h.do("POST", "/model/update", admin, map[string]any{"id": id, "litellm_params": map[string]any{"market_tier_semantics": "forged"}}); r.status != 400 {
		t.Fatalf("普通编辑不应伪造分档语义: %s", r.describe())
	}
	// 管理员切换手工价格必须保留明确提交的档位；普通用户不得更改，且目录确认标记不能继承。
	manualRules := []any{map[string]any{"input_range": []int{0, 99999999}, "output_range": []int{0, 99999999}, "details_v2": map[string]any{"ncache": unit(3), "output": unit(9), "cache": unit(.2)}}}
	manual := map[string]any{"id": id, "model_info": map[string]any{"pricing_source": "manual"}, "litellm_params": map[string]any{"pricing_rules_v2": manualRules, "input_cost_per_token": .000003, "output_cost_per_token": .000009}}
	if r := h.do("POST", "/model/update", tenant.session, manual); r.status != 403 {
		t.Fatalf("普通用户修改模型价格未被拒绝: %s", r.describe())
	}
	h.ok("POST", "/model/update", admin, manual)
	if card := visibleCard(); card["price_tiers"] != nil || card["input_price"] != float64(3) {
		t.Fatalf("切换手工价后必须移除旧市场档位: %#v", card)
	}
	if got := call(12, 0); math.Abs(got-.000081) > 1e-10 {
		t.Fatalf("手工分档价格未生效: %g", got)
	}
}
