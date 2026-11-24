package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/sunqirui1987/xhub/cmd/regression/testsupport"
	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/gateway/usagespool"
	"github.com/sunqirui1987/xhub/internal/iam"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestUsageSpoolConfiguration 验证省略配置保持兼容，显式错误容量及超出 1–64 的合并上限均被拒绝；环境由t.Setenv恢复，无外部依赖。
func TestUsageSpoolConfiguration(t *testing.T) {
	for _, name := range []string{"XHUB_USAGE_SPOOL_DIR", "XHUB_USAGE_SPOOL_MAX_BYTES", "XHUB_USAGE_SPOOL_RECORD_BYTES", "XHUB_USAGE_SPOOL_MAX_ENTRIES", "XHUB_USAGE_SPOOL_GROUP_SIZE"} {
		t.Setenv(name, "")
	}
	if _, enabled, err := usageSpoolConfig(); enabled || err != nil {
		t.Fatal("默认不应启用磁盘队列")
	}
	t.Setenv("XHUB_USAGE_SPOOL_DIR", t.TempDir())
	for _, name := range []string{"XHUB_USAGE_SPOOL_MAX_BYTES", "XHUB_USAGE_SPOOL_RECORD_BYTES", "XHUB_USAGE_SPOOL_MAX_ENTRIES"} {
		t.Run(name, func(t *testing.T) {
			for _, bad := range []string{"0", "-1", "abc", "999999999999999999999999"} {
				t.Setenv(name, bad)
				if _, enabled, err := usageSpoolConfig(); !enabled || err == nil {
					t.Fatalf("非法容量被接受: %s", bad)
				}
			}
			t.Setenv(name, "1024")
			if _, enabled, err := usageSpoolConfig(); !enabled || err != nil {
				t.Fatal("合法容量被拒绝")
			}
		})
	}
	for _, raw := range []string{"-1", "0", "65", "invalid", "1", "16", "64"} {
		t.Setenv("XHUB_USAGE_SPOOL_GROUP_SIZE", raw)
		cfg, enabled, err := usageSpoolConfig()
		want, parseErr := strconv.Atoi(raw)
		valid := parseErr == nil && want >= 1 && want <= usagespool.MaxGroupSize
		if !enabled || (err == nil) != valid || valid && cfg.GroupSize != want {
			t.Fatalf("合并写盘配置解析错误: 输入=%s 结果=%d err=%v", raw, cfg.GroupSize, err)
		}
	}

}

// TestUsageSpoolAdmissionFailure 验证初始化失败明确阻止新付费请求，关闭安全幂等；前置临时目录，结束自动删除。
func TestUsageSpoolAdmissionFailure(t *testing.T) {
	t.Setenv("XHUB_USAGE_SPOOL_DIR", t.TempDir())
	t.Setenv("XHUB_USAGE_SPOOL_MAX_ENTRIES", "bad")
	s := &Server{}
	s.initializeUsageSpool()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	if _, admitted := s.admitUsageSpool(w, r, "call"); admitted || w.Code != 503 || !strings.Contains(w.Body.String(), "billing_storage_unavailable") {
		t.Fatal("初始化失败静默绕过持久化准入")
	}
	if err := s.CloseUsageSpool(); err != nil {
		t.Fatal(err)
	}
	s.usageSpoolEnabled = false
	if pending, admitted := s.admitUsageSpool(httptest.NewRecorder(), r, "call"); !admitted || pending != 0 {
		t.Fatal("未配置队列不应改变兼容路径")
	}
}

// TestUsageSpoolProbeFallback 验证关闭队列、初始化故障、缺少标识及取消的请求不会提前操作磁盘。
// 每个子测试使用独立请求，实际队列由 t.Cleanup 关闭，临时目录自动删除；所有回退都保留原请求上下文。
func TestUsageSpoolProbeFallback(t *testing.T) {
	spool, err := usagespool.Open(usagespool.Config{Dir: filepath.Join(t.TempDir(), "spool")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spool.Close() })
	for _, mode := range []string{"disabled", "missing-spool", "init-error", "missing-id", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s := &Server{usageSpoolEnabled: true, usageSpool: spool}
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			id := "stream"
			switch mode {
			case "disabled":
				s.usageSpoolEnabled = false
			case "missing-spool":
				s.usageSpool = nil
			case "init-error":
				s.usageSpoolErr = usagespool.ErrUnavailable
			case "missing-id":
				id = ""
			case "canceled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			if got := s.WithStreamBudgetProbe(r, id); got != r {
				t.Fatal("回退时改变了请求或启动了探针")
			}
		})
	}
}

// TestUsageSpoolAdmissionBacklog 通过网关准入入口预留真实临时磁盘，验证带回旧账数量、满队列的 503 和结清后的再次放行。
// 所有账单使用合成请求 ID，不访问供应商；测试结束后关闭队列并删除整个私有目录。
func TestUsageSpoolAdmissionBacklog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	spool, err := usagespool.Open(usagespool.Config{Dir: dir, MaxEntries: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spool.Close() })
	server := &Server{usageSpool: spool, usageSpoolEnabled: true}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	// 先发起流式探针，再发布旧账；只有正式准入时才读取水位。
	request = server.WithStreamBudgetProbe(request, "first")
	if request.Context().Value(streamBudgetProbeKey{}) == nil {
		t.Fatal("流式网关没有为可用的磁盘队列准备探针")
	}
	owed := iam.UsageRecord{RequestID: "older-charge", TS: time.Now(), Cost: 1}
	if err := spool.Put(owed); err != nil {
		t.Fatalf("创建待结账单失败: %v", err)
	}
	if n, ok := server.admitUsageSpool(httptest.NewRecorder(), request, "first"); !ok || n != 1 {
		t.Fatalf("新请求必须看到此前已发布的账单: admitted=%v pending=%d", ok, n)
	}
	w := httptest.NewRecorder()
	if _, ok := server.admitUsageSpool(w, request, "full"); ok || w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "billing_storage_unavailable") {
		t.Fatalf("容量已满时未返回存储拒绝: admitted=%v status=%d", ok, w.Code)
	}
	if err := spool.Acknowledge(owed); err != nil {
		t.Fatalf("确认旧账失败: %v", err)
	}
	if n, ok := server.admitUsageSpool(httptest.NewRecorder(), request, "resumed"); !ok || n != 0 {
		t.Fatalf("已结清的旧账不应再次阻塞准入: admitted=%v pending=%d", ok, n)
	}
	// 已占用的同步交付槽仍然存在；如果其待结目录读失败，保持客户端原本能据以重试的 pending 错误。
	spool.Release("first")
	marker, err := iam.UnconfirmedDelivery(iam.UsageRecord{RequestID: "resumed", TS: time.Now(),
		Status: "success", HTTPStatus: 200, DeliveryPending: true, DeliverySpoolOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	marker.DeliveryUnconfirmed = true
	if err := spool.BeginDelivery(marker); err != nil {
		t.Fatalf("创建在途交付标记失败: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "quarantine")); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	if _, ok := server.admitUsageSpool(w, request, "resumed"); ok || w.Code != http.StatusServiceUnavailable ||
		!strings.Contains(w.Body.String(), "billing_storage_pending") || w.Header().Get("X-XHub-Usage-Persistence") != "" {
		t.Fatalf("已持有交付槽的读取故障改变了客户端错误契约: admitted=%v status=%d", ok, w.Code)
	}
}

// TestBrowserUsageSpoolControlBoundaries 验证故障夹具仅接受专属路由/POST/私有schema/专属型号，无数据库或网络副作用。
func TestBrowserUsageSpoolControlBoundaries(t *testing.T) {
	for _, tc := range []struct {
		path, method, body, dsn string
		handled                 bool
		status                  int
	}{
		{"/other", "POST", `{}`, "", false, 200},
		{"/usage-spool/defer", "POST", `{"model":"spool-browser-test"}`, "postgres://unused/?search_path=e2e_test", true, 409},
		{"/usage-spool/defer", "POST", `{"model":"spool-browser-test"}`, "postgres://unused/?search_path=public", true, 400},
		{"/usage-spool/fail", "GET", `{}`, "postgres://unused/?search_path=e2e_test", true, 400},
		{"/usage-spool/fail", "POST", `{"model":"real-model"}`, "postgres://unused/?search_path=e2e_test", true, 400},
		{"/usage-spool/fail", "POST", `{"model":"spool-browser-test"}`, "postgres://unused/?search_path=public", true, 400},
	} {
		t.Run(tc.path+tc.method+tc.dsn, func(t *testing.T) {
			s := &Server{Cfg: &config.Config{GeneralSettings: config.GeneralSettings{DatabaseURL: tc.dsn}}, Client: &http.Client{}}
			handler, cleanup := newBrowserUsageSpoolControl(s, t.TempDir())
			defer cleanup()
			w := httptest.NewRecorder()
			handled := handler(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if handled != tc.handled || w.Code != tc.status {
				t.Fatalf("夹具边界错误: handled=%v status=%d", handled, w.Code)
			}
		})
	}
}

// TestUsageSpoolMaintenanceFailsClosed 验证队列初始化失败、丢失和损坏时维护流程不能报告可安全恢复。
// 使用独占临时目录和隔离错误注入；核验待处理检查、确认操作与损坏证据保留，测试自动清理。
func TestUsageSpoolMaintenanceFailsClosed(t *testing.T) {
	ctx := context.Background()
	rec := iam.UsageRecord{RequestID: "maintenance-test"}
	s := &Server{}
	if pending, err := s.HasPendingLocalUsage(ctx); pending || err != nil {
		t.Fatalf("未启用时不应有待处理数据: %v %v", pending, err)
	}
	if err := s.AcknowledgeDeliverySpool(ctx, rec); err != nil {
		t.Fatalf("未启用时确认应无副作用: %v", err)
	}
	s.usageSpoolEnabled = true
	if _, err := s.HasPendingLocalUsage(ctx); !errors.Is(err, usagespool.ErrUnavailable) {
		t.Fatalf("队列缺失必须失败: %v", err)
	}
	if err := s.AcknowledgeDeliverySpool(ctx, rec); !errors.Is(err, usagespool.ErrUnavailable) {
		t.Fatalf("队列缺失不能确认: %v", err)
	}
	initializationFailure := errors.New("isolated spool init failure")
	s.usageSpoolErr = initializationFailure
	if _, err := s.HasPendingLocalUsage(ctx); !errors.Is(err, initializationFailure) {
		t.Fatalf("初始化故障丢失: %v", err)
	}
	if err := s.AcknowledgeDeliverySpool(ctx, rec); !errors.Is(err, initializationFailure) {
		t.Fatalf("初始化故障不能确认: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "spool")
	sp, err := usagespool.Open(usagespool.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sp.Close() })
	s.usageSpoolErr = nil
	s.usageSpool = sp
	if err := os.WriteFile(filepath.Join(dir, "quarantine", "evidence"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HasPendingLocalUsage(ctx); !errors.Is(err, usagespool.ErrCorrupt) {
		t.Fatalf("隔离证据不能被视为队列安全: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "quarantine", "evidence")); err != nil || string(data) != "damaged" {
		t.Fatalf("故障证据不能被清除: %q %v", data, err)
	}
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HasPendingLocalUsage(ctx); !errors.Is(err, usagespool.ErrUnavailable) {
		t.Fatalf("关闭的队列不能被视为空: %v", err)
	}
}

// TestBrowserSpoolConstraintUsesIAMContract 用真实隔离 PostgreSQL 模拟账单约束失败，检查控制端接受 IAM 的规范化错误。
// 前置专属 e2e_ schema 及临时磁盘，验证待结准入报错及清理后的零费入账；测试退出释放连接、schema 与快照。
func TestBrowserSpoolConstraintUsesIAMContract(t *testing.T) {
	dsn := testsupport.Postgres(t, "e2e_spool_control")
	db, err := iam.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dir := t.TempDir()
	sp, err := usagespool.Open(usagespool.Config{Dir: filepath.Join(dir, "usage-spool")})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Cfg: &config.Config{GeneralSettings: config.GeneralSettings{DatabaseURL: dsn}}, IAM: db, Client: &http.Client{}, usageSpoolEnabled: true, usageSpool: sp}
	handle, cleanup := newBrowserUsageSpoolControl(s, dir)
	t.Cleanup(cleanup)
	w := httptest.NewRecorder()
	if !handle(w, httptest.NewRequest("POST", "/usage-spool/defer", strings.NewReader(`{"model":"spool-browser-contract"}`))) || w.Code != http.StatusOK {
		t.Fatalf("已触发的私有约束未被控制夹具识别: %d %s", w.Code, w.Body)
	}
	if err := s.flushLocalUsage(t.Context(), true); !errors.Is(err, iam.ErrInvalid) {
		t.Fatalf("数据库约束退避中准入放行: %v", err)
	}
	cleanup()
	var event iam.UsageEvent
	ok, err := db.Engine.Where("request_id=?", "e2e-spool-admission-spool-browser-contract").Get(&event)
	if err != nil || !ok || event.Cost != 0 {
		t.Fatalf("恢复后专属零费账单未严格结清: 存在=%v 费用=%g err=%v", ok, event.Cost, err)
	}
}
