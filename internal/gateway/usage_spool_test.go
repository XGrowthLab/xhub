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
	if s.admitUsageSpool(w, r, "call") || w.Code != 503 || !strings.Contains(w.Body.String(), "billing_storage_unavailable") {
		t.Fatal("初始化失败静默绕过持久化准入")
	}
	if err := s.CloseUsageSpool(); err != nil {
		t.Fatal(err)
	}
	s.usageSpoolEnabled = false
	if !s.admitUsageSpool(httptest.NewRecorder(), r, "call") {
		t.Fatal("未配置队列不应改变兼容路径")
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
