package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/sunqirui1987/xhub/internal/auth"
	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/gateway/usagespool"
	"github.com/sunqirui1987/xhub/internal/iam"
	llmusage "github.com/sunqirui1987/xhub/internal/llm/usage"
)

// TestBudgetAttemptLifecycle 验证所有协议按余额放行，不改正文、不注入输出上限，取消请求不发送。
// 前置独立数据库与有限额个人；覆盖正数/零/负余额，未知模型窗口与工具输入；自动清理schema。
func TestBudgetAttemptLifecycle(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic"} {
		for _, balance := range []float64{0.000001, 0, -1} {
			t.Run(fmt.Sprintf("%s/%g", protocol, balance), func(t *testing.T) {
				cfg, st, db := testGatewayStores(t)
				s := New(cfg, st, db)
				limit := 1.0
				user, err := db.CreateUser(t.Context(), iam.Actor{}, iam.UserInput{Email: "soft@example.invalid", Password: "password123", MaxBudget: &limit})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.Engine.Exec("UPDATE users SET spend = ? WHERE id = ?", limit-balance, user.ID); err != nil {
					t.Fatal(err)
				}
				p := &auth.Principal{UserID: user.ID}
				raw := []byte(`{"input":"Hello","tools":[{"type":"web_search"},{"type":"code_interpreter"}]}`)
				r := httptest.NewRequest("POST", "/v1/responses", nil)
				w := httptest.NewRecorder()
				out, ok := s.PrepareBudgetAttempt(w, r, p, "call", config.ModelEntry{}, protocol, raw, time.Now())
				if balance <= 0 {
					if ok || w.Code != 403 {
						t.Fatalf("非正余额未拒绝: %d %s", w.Code, w.Body.String())
					}
					return
				}
				if !ok || string(out) != string(raw) || w.Header().Get("X-XHub-Budget-Guarantee") != "soft" {
					t.Fatalf("正余额未原样放行: %d %s", w.Code, w.Body.String())
				}

				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				if s.MarkBudgetAttemptSubmitted(w, r.WithContext(ctx), "call") {
					t.Fatal("取消后仍发送")
				}
				s.finishBudgetCall("call")
				if s.tokenOnlyBudget("call") {
					t.Fatal("尝试未清理")
				}
			})
		}
	}
}

// TestBudgetRetryRechecksDiskBeforePricing 模拟上次路由已冻结价格并取得磁盘预留，下一次尝试前目录失去写权限。
// 从真实网关准入入口验证返回 503、释放旧槽和旧价格，同时保留另一请求的价格。队列及临时目录随测试清理。
func TestBudgetRetryRechecksDiskBeforePricing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	spool, err := usagespool.Open(usagespool.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if err := spool.Reserve("retry"); err != nil {
		t.Fatal(err)
	}
	s := &Server{usageSpoolEnabled: true, usageSpool: spool, budgetCalls: map[string]*budgetAttempt{
		"retry": {TokenOnly: true}, "other": {TokenOnly: true},
	}}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	if _, ok := s.PrepareBudgetAttempt(w, r, &auth.Principal{}, "retry", config.ModelEntry{}, "openai-chat", nil, time.Now()); ok || w.Code != 503 {
		t.Fatalf("重试在磁盘失去权限后仍获准: HTTP %d", w.Code)
	}
	if s.tokenOnlyBudget("retry") || !s.tokenOnlyBudget("other") {
		t.Fatal("重试失败后未精确清理本请求的旧价格")
	}
	if status, err := spool.Status(); err != nil || status.Reserved != 0 {
		t.Fatalf("重试失败仍占用磁盘预留: 状态=%+v 错误=%v", status, err)
	}
}

// TestBudgetPriceSnapshot 验证空预算兼容、合法JSON合并与损坏/空快照；纯转换不写存储无需清理。
func TestBudgetPriceSnapshot(t *testing.T) {
	if got := budgetPriceSnapshot("original", nil); got != "original" {
		t.Fatal(got)
	}
	for _, raw := range []string{`{"price":1}`, `invalid`, `null`} {
		got := budgetPriceSnapshot(raw, map[string]any{"state": "uncertain"})
		var fields map[string]any
		if json.Unmarshal([]byte(got), &fields) != nil || fields["budget"] == nil {
			t.Fatalf("预算状态丢失: %s", got)
		}
	}
}

// TestSearchTokenOnlyLifecycle 验证搜索尝试无需持久预占、保留正文和明确软额度头，结束仅清理内存。
// 前置私有数据库中的有限额用户及合成请求；无上游依赖，同时检查重试不遗留旧硬额度响应头，夹具自动清理。
func TestSearchTokenOnlyLifecycle(t *testing.T) {
	cfg, st, db := testGatewayStores(t)
	s := New(cfg, st, db)
	limit := 1.0
	user, err := db.CreateUser(t.Context(), iam.Actor{}, iam.UserInput{Email: "soft-lifecycle@example.com", Password: "password123", MaxBudget: &limit})
	if err != nil {
		t.Fatal(err)
	}
	p := &auth.Principal{UserID: user.ID}
	w := httptest.NewRecorder()
	w.Header().Set("X-XHub-Budget-Reservation-Id", "old-attempt")
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	raw := []byte(`{"input":"Hello","tools":[{"type":"web_search_preview"}]}`)
	body, ok := s.PrepareBudgetAttempt(w, r, p, "search", config.ModelEntry{}, "openai-responses", raw, time.Now())
	if !ok || string(body) != string(raw) || !s.tokenOnlyBudget("search") {
		t.Fatal("搜索未进入保持正文的软预算")
	}
	if w.Header().Get("X-XHub-Budget-Reservation-Id") != "" || w.Header().Get("X-XHub-Budget-State") != "soft" || w.Header().Get("X-XHub-Billing-Scope") != "tokens_and_search" {
		t.Fatal("搜索预算头有旧状态")
	}
	if !s.MarkBudgetAttemptSubmitted(w, r, "search") {
		t.Fatal("搜索被无效预占状态阻断")
	}

	s.finishBudgetCall("search")
	if s.tokenOnlyBudget("search") {
		t.Fatal("搜索尝试未清理")
	}
	w.Header().Set("X-XHub-Budget-Reservation-Id", "stale")
	if _, ok := s.PrepareBudgetAttempt(w, r, p, "retry", config.ModelEntry{}, "openai-responses", []byte(`{"tools":[{"type":"web_search"},{"type":"code_interpreter"}]}`), time.Now()); !ok || w.Header().Get("X-XHub-Budget-Reservation-Id") != "" {
		t.Fatal("重试未统一软预算或遗留旧预占头")
	}
	s.finishBudgetCall("retry")
}

// TestPricingFailureCode 验证正常、缺价和未知内部错误仅输出有限安全原因码；纯内存，无需清理。
func TestPricingFailureCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, ""}, {fmt.Errorf("measured cache reads have no reserved price"), "cache_read_price_missing"},
		{fmt.Errorf("measured cache writes have no reserved price"), "cache_write_price_missing"},
		{fmt.Errorf("measured search usage is missing"), "search_usage_missing"},
		{llmusage.ErrSearchUsageInvalid, "search_usage_invalid"}, {fmt.Errorf("stream settlement: %w", llmusage.ErrSearchUsageConflict), "search_usage_invalid"},
		{fmt.Errorf("measured searches have no confirmed price"), "search_price_missing"},
		{fmt.Errorf("private configuration value"), "pricing_unavailable"},
	} {
		if got := pricingFailureCode(tc.err); got != tc.want {
			t.Fatalf("原因码错误 %q want %q", got, tc.want)
		}
	}
}

// TestBillingPendingCause 验证准入诊断可区分超时、冲突及数据库失败，包装错误中的私有内容不能进入分类。
// 测试只提供合成错误与 SQLSTATE，不连接外部存储，无数据需要清理。
func TestBillingPendingCause(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "deadline"}, {context.Canceled, "canceled"},
		{iam.ErrConflict, "snapshot_conflict"}, {usagespool.ErrConflict, "snapshot_conflict"}, {usagespool.ErrCorrupt, "corrupt_spool"},
		{usagespool.ErrFull, "full_spool"}, {usagespool.ErrUnavailable, "unavailable_spool"},
		{&pgconn.PgError{Code: "40001", Message: "private detail"}, "postgres_40001"}, {fmt.Errorf("private detail"), "other"},
	} {
		if got := billingPendingCause(fmt.Errorf("private wrapper: %w", tc.err)); got != tc.want {
			t.Errorf("错误诊断分类不符合预期: got=%s want=%s", got, tc.want)
		}
	}
}
