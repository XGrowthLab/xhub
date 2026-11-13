package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sunqirui1987/xhub/internal/catalog"
)

// TestDiffMarketChanges 验证上下架、恢复和价格变化；纯内存快照不修改输入，无外部资源需要清理。
func TestDiffMarketChanges(t *testing.T) {
	rule := func(price float64) []any {
		return []any{map[string]any{"details_v2": map[string]any{"input": map[string]any{"unit_price_usd": price}}}}
	}
	row := func(price float64) map[string]any {
		return map[string]any{"market_catalog": true, "pricing_rules_v2": rule(price)}
	}
	prior := catalog.PriceDocument{Models: map[string]map[string]any{"changed": row(1), "removed": row(1), "archived": {"market_catalog": true, "feed_unavailable": true, "pricing_rules_v2": rule(1)}, "unknown": {"market_catalog": true}, "manual": {"market_catalog": false}}}
	current := catalog.PriceDocument{Models: map[string]map[string]any{"changed": row(2), "added": row(3), "archived": row(1), "unknown": row(4)}}
	got, err := diffMarketChanges(prior, current)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"added:added", "restored:archived", "price_changed:changed", "delisted:removed", "price_information_changed:unknown"}
	if len(got) != len(want) {
		t.Fatalf("变化数量: got %#v want %#v", got, want)
	}
	for i, item := range got {
		if item.Kind+":"+item.Model != want[i] {
			t.Fatalf("第 %d 项: %#v", i, item)
		}
	}
	if got[2].Previous == nil || got[2].Current == nil {
		t.Fatal("价格变化缺少新旧来源报价")
	}
}

// TestMarketPriceJSON 验证规则顺序不影响比较、未知价格不当作零、非法规则返回错误；不需要清理资源。
func TestMarketPriceJSON(t *testing.T) {
	a := map[string]any{"pricing_rules_v2": []any{map[string]any{"side": "in", "price": 1}, map[string]any{"side": "out", "price": 2}}}
	b := map[string]any{"pricing_rules_v2": []any{map[string]any{"price": 2, "side": "out"}, map[string]any{"price": 1, "side": "in"}}}
	x, ok, err := marketPriceJSON(a)
	if err != nil || !ok {
		t.Fatalf("编码失败: %v", err)
	}
	y, ok, err := marketPriceJSON(b)
	if err != nil || !ok || x != y {
		t.Fatalf("等价规则顺序变化被认为不同: %s %s %v", x, y, err)
	}
	if _, ok, err = marketPriceJSON(map[string]any{}); err != nil || ok {
		t.Fatal("缺失价格不应当作零")
	}
	if _, _, err = marketPriceJSON(map[string]any{"pricing_rules_v2": "invalid"}); err == nil {
		t.Fatal("非法价格规则未报错")
	}
}

// TestMarketEventsPersistOnSuccessfulRefresh 使用隔离 PostgreSQL 和本地来源验证事件与快照一起提交。
// 验证首次基线、重复同步、下架、恢复、价格变化和失败不产事件；服务和测试 schema 自动清理。
func TestMarketEventsPersistOnSuccessfulRefresh(t *testing.T) {
	h := &priceHost{store: openPriceStore(t), allow: true}
	model := func(id string, price int) map[string]any {
		return map[string]any{"id": id, "issuer": map[string]any{"name": "OpenAI"}, "pricing_rules_v2": []any{map[string]any{"details_v2": map[string]any{"text_input": map[string]any{"unit_name": "token", "unit_size": 1000, "unit_price_usd": price}}}}}
	}
	payload := map[string]any{"status": true, "data": []any{model("a", 1), model("b", 1)}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(payload) }))
	t.Cleanup(server.Close)
	t.Setenv("XHUB_PRICE_FEED_URL", server.URL)
	refresh := func() {
		t.Helper()
		if _, err := refreshLocalCatalog(context.Background(), h); err != nil {
			t.Fatal(err)
		}
	}
	events := func() map[string]any {
		t.Helper()
		got, err := h.store.ListConfig(MarketEventNamespace)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	refresh()
	if len(events()) != 0 {
		t.Fatal("首次同步不应发送历史变化")
	}
	refresh()
	if len(events()) != 0 {
		t.Fatal("重复同步产生事件")
	}
	payload["data"] = []any{model("a", 2), model("c", 1)}
	refresh()
	got := events()
	if len(got) != 1 {
		t.Fatalf("应持久化一个变化事件: %#v", got)
	}
	for _, raw := range got {
		event := raw.(map[string]any)
		if len(event["changes"].([]any)) != 3 {
			t.Fatalf("未记录新增、下架和价格变化: %#v", event)
		}
	}
	payload["status"] = false
	if _, err := refreshLocalCatalog(context.Background(), h); err == nil {
		t.Fatal("失败来源未返回错误")
	}
	if len(events()) != 1 {
		t.Fatal("失败同步产生事件")
	}
	payload["status"] = true
	payload["data"] = []any{model("a", 2), model("b", 1), model("c", 1)}
	refresh()
	if len(events()) != 2 {
		t.Fatal("恢复模型未产生事件")
	}
	payload["data"] = []any{model("a", 2), model("a", 3)}
	if _, err := refreshLocalCatalog(context.Background(), h); err == nil || len(events()) != 2 {
		t.Fatal("重复 ID 的无效来源不应产生事件")
	}
	// 注入仅阻断事件写入的数据库故障，验证事件与来源快照不会部分提交。
	if _, err := h.store.DB.Exec(`CREATE FUNCTION reject_market_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.namespace = 'market_change_events' THEN RAISE EXCEPTION 'event write failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_market_event BEFORE INSERT OR UPDATE ON proxy_config FOR EACH ROW EXECUTE FUNCTION reject_market_event()`); err != nil {
		t.Fatal(err)
	}
	before, err := h.store.ListConfig(marketSnapshotNS)
	if err != nil {
		t.Fatal(err)
	}
	payload["data"] = []any{model("a", 5), model("b", 1), model("c", 1)}
	if _, err := refreshLocalCatalog(context.Background(), h); err == nil {
		t.Fatal("事件写入故障被忽略")
	}
	after, err := h.store.ListConfig(marketSnapshotNS)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(before["catalog"])
	a, _ := json.Marshal(after["catalog"])
	if string(a) != string(b) || len(events()) != 2 {
		t.Fatal("事件写入失败后目录或事件部分提交")
	}
}
