package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/store"
)

// TestDeploymentPricePreview 验证纯价格对比、完整维度、手工覆盖和不可应用原因。
// 使用独立部署和市场数据，正常、边界及失败输入均无网络和持久化副作用。
func TestDeploymentPricePreview(t *testing.T) {
	var row map[string]any
	_ = json.Unmarshal([]byte(`{"pricing_rules_v2":[{"details_v2":{"input":{"unit_name":"token","unit_size":1000000,"unit_price_usd":2},"output":{"unit_name":"token","unit_size":1000000,"unit_price_usd":6}}}]}`), &row)
	dep := config.ModelEntry{ModelName: "name", LiteLLMParams: map[string]any{"input_cost_per_token": .000001, "output_cost_per_token": .000003}, ModelInfo: map[string]any{"id": "id", "db_model": true, "base_model": "base", "pricing_source": "manual"}}
	doc := catalog.PriceDocument{Models: map[string]map[string]any{"base": row}}
	p := pricePreview(dep, doc)
	if !p.Changed || !p.Manual || p.Reason != "" || len(p.Current) != 2 {
		t.Fatalf("手工价格差异错误: %+v", p)
	}
	dep.LiteLLMParams["rates"] = p.Proposed
	if p = pricePreview(dep, doc); p.Changed {
		t.Fatal("相同费率误报变化")
	}
	reordered := []catalog.Rate{p.Proposed[1], p.Proposed[0]}
	reordered[0].Label = "changed label"
	if !samePrices(p.Proposed, reordered) {
		t.Fatal("仅标签和顺序变化误报调价")
	}
	row["feed_unavailable"] = true
	if p = pricePreview(dep, doc); p.Reason != "delisted" {
		t.Fatal("来源下架仍允许更新")
	}
	delete(row, "feed_unavailable")
	dep.ModelInfo["base_model"] = "missing"
	if p = pricePreview(dep, doc); p.Reason != "unmapped" {
		t.Fatal("未知模型错误绑定")
	}
	dep.ModelInfo["db_model"] = false
	if p = pricePreview(dep, doc); p.Reason != "config_model" {
		t.Fatal("配置部署允许数据库调价")
	}
	dep.ModelInfo["pricing_source"] = "manual"
	dep.LiteLLMParams = nil
	if len(effectiveRates(dep)) != 0 {
		t.Fatal("缺价手工部署自动回落目录")
	}
}

// TestMarketDailyPlanAndRestart 使用真实 PostgreSQL 验证默认每日、跨实例去重、关闭不重启、失败状态和恢复。
// 来源为本机服务，价格与部署快照在重载后保持一致；服务器和 schema 由测试清理，目录通过 cleanup 恢复。
func TestMarketDailyPlanAndRestart(t *testing.T) {
	before := catalog.PriceDocument{Version: 1, Models: catalog.CostMap(), Providers: catalog.Providers(), Source: catalog.PriceSource(), GeneratedAt: catalog.PriceGeneratedAt()}
	t.Cleanup(func() { _, _ = catalog.ApplyDocument(before) })
	h := &storedModelHost{st: openPriceStore(t)}
	var calls atomic.Int32
	var fail atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"status":true,"data":[{"id":"daily-model","issuer":{"name":"OpenAI"},"pricing_rules_v2":[{"details_v2":{"input":{"unit_name":"token","unit_size":1000000,"unit_price_usd":2},"output":{"unit_name":"token","unit_size":1000000,"unit_price_usd":6}}}]}]}`))
	}))
	defer up.Close()
	t.Setenv("XHUB_PRICE_FEED_URL", up.URL)
	ctx := context.Background()
	if err := ensureDailyReload(ctx, h); err != nil {
		t.Fatal(err)
	}
	if plan := loadCostReload(h); !plan.Scheduled || plan.IntervalHours == nil || *plan.IntervalHours != 24 {
		t.Fatalf("默认不是每天: %+v", plan)
	}
	if _, err := refreshCatalog(ctx, h, true); err != nil {
		t.Fatal(err)
	}
	// 快照读取不得混入内置目录，也不得改写全局供应商切片。
	var original string
	if err := h.st.DB.QueryRow(`SELECT value_json FROM proxy_config WHERE namespace=$1 AND key='catalog'`, marketSnapshotNS).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.DB.Exec(`UPDATE proxy_config SET value_json=$1 WHERE namespace=$2 AND key='catalog'`, `{"models":{"only-new":{}},"providers":[]}`, marketSnapshotNS); err != nil {
		t.Fatal(err)
	}
	stored, err := readMarket(ctx, h)
	if err != nil || len(stored.Models) != 1 || stored.Models["only-new"] == nil {
		t.Fatalf("市场快照混入本地模型: %d, %v", len(stored.Models), err)
	}
	if _, err := h.st.DB.Exec(`UPDATE proxy_config SET value_json=$1 WHERE namespace=$2 AND key='catalog'`, original, marketSnapshotNS); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshCatalog(ctx, h, true); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("第二个实例重复执行同一次到期任务")
	}
	plan := loadCostReload(h)
	if plan.LastRun == nil || plan.LastAttempt == nil || plan.LastError != "" {
		t.Fatalf("成功状态不完整: %+v", plan)
	}
	// 新市场资料与计费目录必须隔离，即使重新执行启动装载也不能偷偷新增计费来源。
	if _, ok := catalog.ModelRow("daily-model"); ok {
		t.Fatal("刷新修改了全局计费目录")
	}
	params := map[string]any{"model": "upstream"}
	info := map[string]any{"base_model": "daily-model", "pricing_source": "catalog", "id": "deployment"}
	if err := bindInitialMarketPrice(ctx, h, params, info); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpsertProxyModel(store.ProxyModel{ID: "deployment", ModelName: "public", Params: params, Info: info}); err != nil {
		t.Fatal(err)
	}
	LoadPriceOverrides(h)
	LoadStored(h)
	if len(h.models) != 1 || len(effectiveRates(h.models[0])) != 2 {
		t.Fatal("重载未保留部署已确认价格")
	}
	fail.Store(true)
	if _, err := refreshLocalCatalog(ctx, h); err == nil {
		t.Fatal("失败刷新未报错")
	}
	plan = loadCostReload(h)
	if plan.LastError == "" || plan.LastRun == nil {
		t.Fatal("失败未保留最后成功时间或缺少状态")
	}
	doc, err := readMarket(ctx, h)
	if err != nil || doc.Models["daily-model"] == nil {
		t.Fatal("失败清空了市场")
	}
	plan.Scheduled = false
	plan.NextRun = nil
	if err := saveCostReload(h, plan); err != nil {
		t.Fatal(err)
	}
	if err := ensureDailyReload(ctx, h); err != nil {
		t.Fatal(err)
	}
	if loadCostReload(h).Scheduled {
		t.Fatal("重启重新开启了已关闭计划")
	}
	if _, err := refreshCatalog(ctx, h, true); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("已关闭计划仍抓取来源")
	}
	if _, err := time.Parse(time.RFC3339, *plan.LastRun); err != nil {
		t.Fatal("最后成功时间无效")
	}
}

// TestMarketScheduleSerialization 验证取消计划与在途同步串行，最新成功状态和管理员开关都保留。
// 使用真实 PostgreSQL 和受控来源，通道只同步事件不增加超时；服务、schema 与目录由 cleanup 回收。
func TestMarketScheduleSerialization(t *testing.T) {
	h := &storedModelHost{st: openPriceStore(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDailyReload(ctx, h); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"status":true,"data":[{"id":"serialized-market","issuer":{"name":"OpenAI"}}]}`))
	}))
	defer up.Close()
	t.Setenv("XHUB_PRICE_FEED_URL", up.URL)
	fetched, changed := make(chan error, 1), make(chan error, 1)
	go func() { _, err := refreshCatalog(ctx, h, true); fetched <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("同步未进入来源请求")
	}
	go func() {
		changed <- updateCostReload(ctx, h, func(plan *costReloadPlan) { plan.Scheduled = false; plan.NextRun = nil; plan.IntervalHours = nil })
	}()
	close(release)
	if err := <-fetched; err != nil {
		t.Fatal(err)
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	plan := loadCostReload(h)
	if plan.Scheduled || plan.NextRun != nil || plan.LastRun == nil {
		t.Fatalf("取消覆盖了同步结果或开关失效: %+v", plan)
	}
}

// TestMarketTransactionRollback 验证数据库更新中途失败整笔回滚，锁被释放且下次操作能正常提交。
// 使用专属 schema，错误为受控业务错误，不依赖外部服务；openPriceStore 自动清理数据库。
func TestMarketTransactionRollback(t *testing.T) {
	h := &storedModelHost{st: openPriceStore(t)}
	ctx := context.Background()
	failure := errors.New("rejected")
	err := h.st.MarketTransaction(ctx, func(tx *sql.Tx) error {
		if err := writeCatalogJSON(ctx, tx, "market-test", "rollback", map[string]any{"value": 1}); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("事务未传播业务失败: %v", err)
	}
	var count int
	if err = h.st.DB.QueryRow(`SELECT count(*) FROM proxy_config WHERE namespace='market-test'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("回滚留下部分数据: count=%d err=%v", count, err)
	}
	if err = h.st.MarketTransaction(ctx, func(tx *sql.Tx) error { return writeCatalogJSON(ctx, tx, "market-test", "commit", true) }); err != nil {
		t.Fatal(err)
	}
	if err = h.st.DB.QueryRow(`SELECT count(*) FROM proxy_config WHERE namespace='market-test'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("后续事务未提交: count=%d err=%v", count, err)
	}
}

// TestMarketStatusFailure 验证数据库读取失败不会伪装成关闭同步；使用独立 schema，关闭后 cleanup 仍回收资源。
func TestMarketStatusFailure(t *testing.T) {
	h := &storedModelHost{st: openPriceStore(t)}
	if err := h.st.DB.Close(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	CostMapReloadStatus(h, w, httptest.NewRequest("GET", "/reload/model_cost_map/status", nil))
	if w.Code != 500 {
		t.Fatalf("状态读取失败被伪装为成功: %d %s", w.Code, w.Body.String())
	}
}
