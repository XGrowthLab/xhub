package regression

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	modelcontract "github.com/sunqirui1987/xhub/contracts/models"
	"github.com/sunqirui1987/xhub/internal/catalog"
)

// TestModelContractMarketPriceConfirmation 验证真实管理路由、数据库和推理计费完整链路。
// 本地来源改变价格/撤回模型后不自动调价；单个与批量确认可计费，过期版本整批回滚、越权拒绝。
// harness 清理专用 schema 与服务，测试恢复全局目录；不使用真实供应商凭据。
func TestModelContractMarketPriceConfirmation(t *testing.T) {
	before := catalog.PriceDocument{Version: 1, Models: catalog.CostMap(), Providers: catalog.Providers(), Source: catalog.PriceSource(), GeneratedAt: catalog.PriceGeneratedAt()}
	t.Cleanup(func() { _, _ = catalog.ApplyDocument(before) })
	h := newHarness(t)
	admin := h.adminSession()
	v, err := modelcontract.NewValidator(modelcontract.Document)
	if err != nil {
		t.Fatal(err)
	}
	c := &modelContractClient{t: t, h: h, v: v, admin: admin}
	tenant := h.provision(t, admin, "market-confirm")
	const base = "regression-market-model"
	catalog.SetModel(base, map[string]any{"input_cost_per_token": .000002, "output_cost_per_token": .000006})
	var mu sync.Mutex
	input := float64(4)
	removed := false
	fail := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			w.WriteHeader(503)
			return
		}
		id := base
		if removed {
			id = "replacement-model"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "data": []any{map[string]any{"id": id, "issuer": map[string]any{"name": "OpenAI"}, "pricing_rules_v2": []any{map[string]any{"details_v2": map[string]any{"input": map[string]any{"unit_name": "token", "unit_size": 1000000, "unit_price_usd": input}, "output": map[string]any{"unit_name": "token", "unit_size": 1000000, "unit_price_usd": 6}}}}}}})
	}))
	defer upstream.Close()
	t.Setenv("XHUB_PRICE_FEED_URL", upstream.URL)
	ids := []string{}
	for _, name := range []string{"market-first", "market-second"} {
		result := h.ok("POST", "/model/new", admin, map[string]any{"model_name": name, "litellm_params": map[string]any{"model": "openai/" + name, "api_base": h.credentialAPIBase(), "api_key": "sk-test"}, "model_info": map[string]any{"transport": "bypass_openai_chat", "endpoint_types": []string{"chat"}, "pricing_source": "catalog", "base_model": base}})
		ids = append(ids, result.json()["model_info"].(map[string]any)["id"].(string))
	}
	// 先保留一笔旧账单，确认前后的同一用量费用必须可区分且历史不重算。
	calls := 0
	call := func(name string) reply {
		calls++
		return h.ok("POST", "/v1/chat/completions", tenant.key, chatRequest(name, fmt.Sprintf("Hello %d", calls)))
	}
	original := call("market-first")
	oldCost := parseFloatOrZero(original.header("x-litellm-response-cost"))
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	// 来源快照创建后，人工新增条目的上下架仍独立可用；再次同步不能把人工条目误标来源下架。
	h.ok("POST", "/price/model", admin, map[string]any{"id": "manual-after-sync", "litellm_provider": "openai", "input_cost_per_token": .000001})
	h.ok("POST", "/price/model/listing", admin, map[string]any{"id": "manual-after-sync", "delisted": true})
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	if row := priceSelectionRow(t, h.ok("GET", "/price/catalog", admin, nil), "manual-after-sync"); row["feed_unavailable"] == true || row["delisted"] != true {
		t.Fatalf("人工条目的来源与隐藏状态错误: %v", row)
	}
	h.ok("POST", "/price/model/listing", admin, map[string]any{"id": "manual-after-sync", "delisted": false})
	// 来源快照形成以后新增的本地计价模型，仍能用于创建部署。
	manualDeployment := h.ok("POST", "/model/new", admin, map[string]any{"model_name": "manual-catalog-test", "litellm_params": map[string]any{"model": "openai/manual-catalog-test", "api_base": h.credentialAPIBase(), "api_key": "sk-test"}, "model_info": map[string]any{"transport": "bypass_openai_chat", "endpoint_types": []string{"chat"}, "pricing_source": "catalog", "base_model": "manual-after-sync"}})
	manualID := manualDeployment.json()["model_info"].(map[string]any)["id"].(string)
	h.ok("POST", "/model/delete", admin, map[string]any{"id": manualID})
	// 人工条目没有来源基线，不能用“恢复”删除；专用 schema 和目录快照在 cleanup 中回收。
	if r := h.do("POST", "/price/model/reset", admin, map[string]any{"id": "manual-after-sync"}); r.status != 409 {
		t.Fatal("人工条目被错误恢复为来源模型")
	}
	if got := parseFloatOrZero(call("market-first").header("x-litellm-response-cost")); got != oldCost {
		t.Fatalf("同步擅自调价: %v -> %v", oldCost, got)
	}
	preview := func() []map[string]any {
		return listField(c.call("GET", "/price/deployments", admin, nil, 200, true).json(), "deployments")
	}
	selected := func(rows []map[string]any, id string) map[string]any {
		for _, row := range rows {
			if row["id"] == id {
				return map[string]any{"id": id, "version": row["version"]}
			}
		}
		t.Fatalf("预览缺少部署%s", id)
		return nil
	}
	// 真实路由拒绝空批次、重复 ID、缺版本和超量选择；不能出现部分写入。
	for _, items := range [][]any{nil, {map[string]any{"id": ids[0]}}, {map[string]any{"id": ids[0], "version": "v"}, map[string]any{"id": ids[0], "version": "v"}}, make([]any, 501)} {
		if res := c.call("POST", "/price/deployments/apply", admin, map[string]any{"items": items}, 400, false); res.status != 400 || errorMessage(res) == "" {
			t.Fatalf("非法调价选择未拒绝: %s", res.describe())
		}
	}
	rows := preview()
	first := selected(rows, ids[0])
	second := selected(rows, ids[1])
	for _, token := range []string{"", tenant.session} {
		r := h.do("POST", "/price/deployments/apply", token, map[string]any{"items": []any{first}})
		if r.status != 401 && r.status != 403 {
			t.Fatalf("越权调价未阻断: %s", r.describe())
		}
	}
	c.call("POST", "/price/deployments/apply", admin, map[string]any{"items": []any{first}}, 200, true)
	newCost := parseFloatOrZero(call("market-first").header("x-litellm-response-cost"))
	// 普通目录模式编辑会发 rates:null，不能因此丢失已确认的价格快照。
	h.ok("POST", "/model/update", admin, map[string]any{"id": ids[0], "litellm_params": map[string]any{"rates": nil}, "model_info": map[string]any{"pricing_source": "catalog", "base_model": base}})
	if got := parseFloatOrZero(call("market-first").header("x-litellm-response-cost")); got != newCost {
		t.Fatalf("编辑清除了已确认价格: %v -> %v", newCost, got)
	}
	if newCost <= oldCost {
		t.Fatalf("确认调价未改变实际计费: %v -> %v", oldCost, newCost)
	}
	if got := parseFloatOrZero(call("market-second").header("x-litellm-response-cost")); got != oldCost {
		t.Fatal("单个调价修改了未选部署")
	}
	r := c.call("POST", "/price/deployments/apply", admin, map[string]any{"items": []any{second, first}}, 409, true)
	if r.status != 409 {
		t.Fatalf("旧版本未整批拒绝: %s", r.describe())
	}
	if got := parseFloatOrZero(call("market-second").header("x-litellm-response-cost")); got != oldCost {
		t.Fatal("冲突整批操作部分提交")
	}
	mu.Lock()
	input = 8
	mu.Unlock()
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	if r = h.do("POST", "/price/deployments/apply", admin, map[string]any{"items": []any{second}}); r.status != 409 {
		t.Fatal("来源变化后旧确认未拒绝")
	}
	rows = preview()
	h.ok("POST", "/price/deployments/apply", admin, map[string]any{"items": []any{selected(rows, ids[0]), selected(rows, ids[1])}})
	for _, name := range []string{"market-first", "market-second"} {
		if got := parseFloatOrZero(call(name).header("x-litellm-response-cost")); got <= newCost {
			t.Fatalf("批量调价未生效 %s:%v", name, got)
		}
	}
	oldBill := breakdownOf(t, h, admin, original.header("x-litellm-call-id"))
	if !nearlyEqual(numberOrZero(oldBill["total_cost"]), oldCost) {
		t.Fatalf("历史账单被重算: %v", oldBill)
	}
	mu.Lock()
	removed = true
	mu.Unlock()
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	if row := priceSelectionRow(t, h.ok("GET", "/price/catalog", admin, nil), base); row["feed_unavailable"] != true {
		t.Fatal("来源下架未保留记录和标记")
	}
	if got := parseFloatOrZero(call("market-first").header("x-litellm-response-cost")); got <= newCost {
		t.Fatal("来源下架影响已部署计费")
	}
	for _, p := range preview() {
		if p["base_model"] == base && p["reason"] != "delisted" {
			t.Fatalf("下架仍可调价: %v", p)
		}
	}
	mu.Lock()
	removed = false
	mu.Unlock()
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	if row := priceSelectionRow(t, h.ok("GET", "/price/catalog", admin, nil), base); row["feed_unavailable"] == true {
		t.Fatal("来源恢复后仍标记下架")
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	if r = h.do("POST", "/reload/model_cost_map", admin, nil); r.status != 502 {
		t.Fatal("失败源被算成功")
	}
	for _, id := range ids {
		h.ok("POST", "/model/delete", admin, map[string]any{"id": id})
	}
}
