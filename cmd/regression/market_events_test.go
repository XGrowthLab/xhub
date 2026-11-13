package regression

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMarketEventHTTPRegression 验证管理员真实刷新路由与数据库事件原子提交，以及失败和无权调用不写事件。
// 使用本地价格源、专用数据库 schema；测试框架和服务器在结束时清理，不依赖外部供应商。
func TestMarketEventHTTPRegression(t *testing.T) {
	h := newHarness(t)
	admin := h.adminSession()
	tenant := h.provision(t, admin, "market-events")
	price := 1
	fail := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "data": []any{map[string]any{"id": "market-event-model", "issuer": map[string]any{"name": "OpenAI"}, "pricing_rules_v2": []any{map[string]any{"details_v2": map[string]any{"text_input": map[string]any{"unit_name": "token", "unit_size": 1000, "unit_price_usd": price}}}}}}})
	}))
	t.Cleanup(up.Close)
	t.Setenv("XHUB_PRICE_FEED_URL", up.URL)
	count := func() int {
		t.Helper()
		events, err := h.store.ListConfig("market_change_events")
		if err != nil {
			t.Fatal(err)
		}
		return len(events)
	}
	if r := h.do("POST", "/reload/model_cost_map", tenant.session, nil); r.status != 401 && r.status != 403 {
		t.Fatalf("普通用户刷新应拒绝: %s", r.describe())
	}
	if count() != 0 {
		t.Fatal("无权刷新产生了事件")
	}
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	price = 2
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	if count() != 1 {
		t.Fatal("成功变价未产生一个持久事件")
	}
	fail = true
	if r := h.do("POST", "/reload/model_cost_map", admin, nil); r.status != 502 {
		t.Fatalf("失败来源未返回错误: %s", r.describe())
	}
	if count() != 1 {
		t.Fatal("失败刷新产生了额外事件")
	}
}
