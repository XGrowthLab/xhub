package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	// 预览版本不把 API Key 纳入摘要；可计费条件变更仍必须改变版本。
	version := p.Version
	dep.LiteLLMParams["api_key"] = "synthetic-secret"
	if updated := pricePreview(dep, doc); updated.Version != version {
		t.Fatal("凭据影响价格版本摘要")
	}
	dep.ModelName = "other-name"
	if updated := pricePreview(dep, doc); updated.Version == version {
		t.Fatal("公开模型名称变化未使预览过期")
	}
	dep.ModelName = "name"
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

// TestMarketScheduleSerialization 验证慢来源不阻塞取消计划，取消后旧响应不能恢复计划或更新快照。
// 使用真实 PostgreSQL 和受控来源，通道同步事件；服务、schema 与目录由 cleanup 回收。
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
	select {
	case err := <-changed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		close(release)
		t.Fatal("慢来源阻塞取消计划")
	}
	close(release)
	if err := <-fetched; err != nil {
		t.Fatal(err)
	}
	plan := loadCostReload(h)
	if plan.Scheduled || plan.NextRun != nil || plan.LastRun != nil {
		t.Fatalf("取消后旧响应被错误应用或开关失效: %+v", plan)
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

// lockedMarketHost 使用真实互斥锁记录模型锁的获取，隔离数据库由调用方测试清理。
type lockedMarketHost struct {
	storedModelHost
	mu        sync.Mutex
	attempted chan struct{}
	once      sync.Once
}

// LockModels 为管理请求获取模型锁，同时通知测试调用已经开始等待；无额外存储副作用。
func (h *lockedMarketHost) LockModels() { h.once.Do(func() { close(h.attempted) }); h.mu.Lock() }

// UnlockModels 释放测试模型锁，由持锁的管理请求调用。
func (h *lockedMarketHost) UnlockModels() { h.mu.Unlock() }

// TestPriceApplyDoesNotBlockModelsWhileSyncing 使用真实 PostgreSQL 锁与 HTTP 处理器验证慢同步不会占用推理模型锁。
// 独立事务模拟同步阻塞，未释放同步锁前其他请求必须能持有模型锁；测试释放事务并清理 schema。
func TestPriceApplyDoesNotBlockModelsWhileSyncing(t *testing.T) {
	h := &lockedMarketHost{storedModelHost: storedModelHost{st: openPriceStore(t)}, attempted: make(chan struct{})}
	locked, release, transactionDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		transactionDone <- h.st.MarketTransaction(context.Background(), func(*sql.Tx) error { close(locked); <-release; return nil })
	}()
	<-locked
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest("POST", "/price/deployments/apply", strings.NewReader(`{"items":[{"id":"missing","version":"v"}]}`))
		ApplyDeploymentPrices(h, httptest.NewRecorder(), req)
	}()
	select {
	case <-h.attempted:
		close(release)
		<-transactionDone
		<-done
		t.Fatal("等待市场同步时提前获取模型锁，会阻塞推理")
	case <-time.After(250 * time.Millisecond):
	}
	if !h.mu.TryLock() {
		close(release)
		<-transactionDone
		<-done
		t.Fatal("同步期间模型锁不可用")
	}
	h.mu.Unlock()
	close(release)
	if err := <-transactionDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("调价处理未结束")
	}
}

// TestMarketOnlyModelRequiresPriceSnapshot 验证只在市场出现但没有完整价格规则的模型不能保存为未定价部署。
// 使用真实隔离 PostgreSQL，覆盖缺价及不存在的模型；结束时清理测试 schema。
func TestMarketOnlyModelRequiresPriceSnapshot(t *testing.T) {
	h := &storedModelHost{st: openPriceStore(t)}
	ctx := context.Background()
	if err := h.st.MarketTransaction(ctx, func(tx *sql.Tx) error {
		return writeCatalogJSON(ctx, tx, marketSnapshotNS, "catalog", catalog.PriceDocument{Models: map[string]map[string]any{"market-only-without-rules": {"input_cost_per_token": 0.000001}}})
	}); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"market-only-without-rules", "missing-market-model"} {
		params := map[string]any{"model": "upstream"}
		info := map[string]any{"base_model": base, "pricing_source": "catalog"}
		if err := bindInitialMarketPrice(ctx, h, params, info); err == nil {
			t.Fatalf("%s 缺费率仍允许创建", base)
		}
	}
}

// TestMarketSlowResponseCannotReplaceNewer 验证两个真实来源请求交错时旧响应不能覆盖新快照。
// 首个请求受控挂起，第二个先提交，之后放行第一个；测试使用隔离 PostgreSQL 和本机上游并清理资源。
func TestMarketSlowResponseCannotReplaceNewer(t *testing.T) {
	h := &storedModelHost{st: openPriceStore(t)}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := "newer"
		if calls.Add(1) == 1 {
			id = "older"
			close(entered)
			<-release
		}
		_, _ = fmt.Fprintf(w, `{"status":true,"data":[{"id":%q,"issuer":{"name":"OpenAI"}}]}`, id)
	}))
	defer up.Close()
	t.Setenv("XHUB_PRICE_FEED_URL", up.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := refreshLocalCatalog(ctx, h); first <- err }()
	<-entered
	if _, err := refreshLocalCatalog(ctx, h); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	doc, err := readMarket(ctx, h)
	if err != nil || doc.Models["newer"] == nil || doc.Models["older"] != nil {
		t.Fatalf("旧来源响应覆盖了新快照: %v", err)
	}
}

// TestMarketReadFailureIsServerError 验证市场读取失败不会误报成用户参数错误。
// 使用真实隔离数据库和已取消的请求上下文触发读取失败，响应不得暴露数据库细节；测试结束清理 schema。
func TestMarketReadFailureIsServerError(t *testing.T) {
	h := &storedModelHost{st: openPriceStore(t)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := `{"model_name":"market-error","litellm_params":{"model":"openai/test","api_base":"http://127.0.0.1:1","api_key":"test"},"model_info":{"pricing_source":"catalog","base_model":"market-model","transport":"bypass_openai_chat","endpoint_types":["chat"]}}`
	req := httptest.NewRequest("POST", "/model/new", strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	New(h, w, req)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "market catalog unavailable") {
		t.Fatalf("存储失败应返回 500: %d %s", w.Code, w.Body.String())
	}
}
