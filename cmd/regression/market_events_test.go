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
	if _, err := h.db.Engine.Exec(`UPDATE users SET role='admin' WHERE id=?`, tenant.userID); err != nil {
		t.Fatal(err)
	}
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
		var events int
		if err := h.store.DB.QueryRow(`SELECT count(*) FROM proxy_config WHERE namespace='market_change_events'`).Scan(&events); err != nil {
			t.Fatal(err)
		}
		return events
	}
	if _, err := h.db.Engine.Exec(`UPDATE users SET role='user' WHERE id=?`, tenant.userID); err != nil {
		t.Fatal(err)
	}
	if r := h.do("POST", "/reload/model_cost_map", tenant.session, nil); r.status != 401 && r.status != 403 {
		t.Fatalf("普通用户刷新应拒绝: %s", r.describe())
	}
	if count() != 0 {
		t.Fatal("无权刷新产生了事件")
	}
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	price = 2
	if _, err := h.db.Engine.Exec(`UPDATE users SET role='admin' WHERE id=?`, tenant.userID); err != nil {
		t.Fatal(err)
	}
	h.ok("POST", "/reload/model_cost_map", admin, nil)
	if count() != 1 {
		t.Fatal("成功变价未产生一个持久事件")
	}
	if err := h.db.ScanMarketNotifications(t.Context()); err != nil {
		t.Fatal(err)
	}
	list := h.ok("GET", "/notifications", tenant.session, nil).json()
	items := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["type"] != "market_update" {
		t.Fatalf("管理员未收到市场变化: %#v", list)
	}
	id := items[0].(map[string]any)["id"].(string)
	if _, err := h.db.Engine.Exec(`UPDATE users SET role='user' WHERE id=?`, tenant.userID); err != nil {
		t.Fatal(err)
	}
	if got := h.ok("GET", "/notifications", tenant.session, nil).json()["total"]; got != float64(0) {
		t.Fatalf("撤销管理员后仍可读取市场消息: %v", got)
	}
	if got := h.ok("GET", "/notifications/unread", tenant.session, nil).json()["count"]; got != float64(0) {
		t.Fatalf("撤权后未读数仍包含市场消息: %v", got)
	}
	if r := h.do("POST", "/notifications/read", tenant.session, map[string]any{"id": id}); r.status != 404 {
		t.Fatalf("撤权后仍可操作市场消息: %s", r.describe())
	}
	if count() != 0 {
		t.Fatal("成功投递事件未清理")
	}
	fail = true
	if r := h.do("POST", "/reload/model_cost_map", admin, nil); r.status != 502 {
		t.Fatalf("失败来源未返回错误: %s", r.describe())
	}
	if count() != 0 {
		t.Fatal("失败刷新产生了额外事件")
	}
}
