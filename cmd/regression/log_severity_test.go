package regression

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/iam"
)

// TestLogSeverityContract 用隔离 PostgreSQL、真实 HTTP 网关和本地上游验证失败分级、身份展示和分页。
// 测试用租户与密钥仅接受示例模型，另一个租户不能查看日志；同一测试补充旧版 HTTP 200 失败终态。
// harness 在退出时回收网关和数据库 schema，不使用供应商凭据。
func TestLogSeverityContract(t *testing.T) {
	const model = "severity-contract-model"
	h := newHarness(t, chatDeployment(model))
	admin := h.adminSession()
	owner := h.provision(t, admin, "severity-owner")
	other := h.provision(t, admin, "severity-other")
	key := h.modelKey(t, owner.session, owner.teamID, "severity-restricted", model)

	// 两类警告都会真实经过网关，白名单拒绝和方法错误不应访问供应商。
	rejected := h.do(http.MethodPost, "/v1/chat/completions", key, chatRequest("missing-model", "Verify model authorization."))
	method := h.do(http.MethodGet, "/responses", key, nil)
	anonymous := h.do(http.MethodPost, "/v1/chat/completions", "", chatRequest(model, "Verify missing credential."))
	if rejected.status != 401 || !strings.Contains(rejected.text(), "model_not_allowed") ||
		method.status != 404 || !strings.Contains(method.text(), "unsupported_method") ||
		anonymous.status != 401 || len(h.upstreamCalls()) != 0 {
		t.Fatalf("网关拒绝契约错误: %s / %s / %s", rejected.describe(), method.describe(), anonymous.describe())
	}

	h.scriptStatus(model, 502)
	upstream := h.do(http.MethodPost, "/v1/chat/completions", key, chatRequest(model, "Verify upstream error reporting."))
	if upstream.status != 502 || len(h.upstreamCalls()) == 0 {
		t.Fatalf("未取得真实上游失败: %s", upstream.describe())
	}
	h.flushSpend()
	legacyTime := time.Now().UTC().Add(-time.Hour)
	if err := h.db.RecordUsage(t.Context(), []iam.UsageRecord{{
		RequestID: "severity-legacy", UserID: owner.userID, OwnerType: iam.OwnerPersonal,
		Model: model, Status: "failed", HTTPStatus: 200, TS: legacyTime, Error: "internal diagnostic text",
	}}); err != nil {
		t.Fatal(err)
	}

	type logExpectation struct {
		id, severity, code string
	}
	expected := []logExpectation{
		{rejected.header("x-litellm-call-id"), "warning", "model_not_allowed"},
		{method.header("x-litellm-call-id"), "warning", "unsupported_method"},
		{upstream.header("x-litellm-call-id"), "error", "upstream_http_error"},
		{"severity-legacy", "error", "service_failure"},
	}
	for _, want := range expected {
		result := logDetail(t, h, owner.session, want.id)
		diag, valid := result["diagnosis"].(map[string]any)
		if !valid || result["severity"] != want.severity || diag["code"] != want.code ||
			result["user"] != owner.userID || result["user_name"] != "severity-owner-user" {
			t.Fatalf("请求 %s 诊断或用户不匹配: %v", want.id, result)
		}
		if want.code == "unsupported_method" &&
			(diag["request_method"] != "GET" || diag["model_missing_reason"] != "model_not_applicable" || result["team_id"] != owner.teamID) {
			t.Fatalf("方法拒绝未保留真实归属/缺失原因: %v", result)
		}
		if want.code == "upstream_http_error" && numberOrZero(diag["upstream_status"]) != 502 {
			t.Fatalf("实测上游状态丢失: %v", result)
		}
		if want.id == "severity-legacy" && diag["team_missing_reason"] != "team_not_recorded" {
			t.Fatalf("历史日志被误认为当前团队: %v", result)
		}
		if got := h.do(http.MethodGet, "/spend/logs/ui/"+want.id, other.session, nil); got.status != 404 {
			t.Fatalf("跨用户详情未按原权限隐藏: %s", got.describe())
		}
	}

	for _, level := range []string{"warning", "error"} {
		seen := map[string]bool{}
		for p := 1; p <= 2; p++ {
			path := fmt.Sprintf("/spend/logs/ui?status_filter=error&severity=%s&page_size=1&page=%d", level, p)
			result := h.ok(http.MethodGet, path, owner.session, nil)
			rows := rowsOf(result, "data")
			if numberOrZero(result.json()["total"]) != 2 || len(rows) != 1 {
				t.Fatalf("%s 必须先筛选再分页: %s", level, result.describe())
			}
			entry := rows[0]
			id := stringField(entry, "request_id")
			if seen[id] || entry["severity"] != level || entry["user_name"] != "severity-owner-user" ||
				strings.Contains(result.text(), "internal diagnostic text") {
				t.Fatalf("列表重复、错误分级、缺失姓名或泄露原始正文: %s", result.describe())
			}
			seen[id] = true
		}
	}
	if result := h.ok(http.MethodGet, "/spend/logs/ui?status_filter=error", owner.session, nil); numberOrZero(result.json()["total"]) != 4 {
		t.Fatalf("旧失败条件仍需包含警告及错误: %s", result.describe())
	}
	if result := h.do(http.MethodGet, "/spend/logs/ui?severity=unknown", owner.session, nil); result.status != 400 {
		t.Fatalf("错误级别必须明确拒绝: %s", result.describe())
	}
	anonymousID := anonymous.header("x-litellm-call-id")
	anonymousLog := logDetail(t, h, admin, anonymousID)
	diag, ok := anonymousLog["diagnosis"].(map[string]any)
	if !ok || anonymousLog["user"] != "" || anonymousLog["user_name"] != nil || diag["user_missing_reason"] != "credentials_missing" {
		t.Fatalf("匿名日志不得猜测身份: %v", anonymousLog)
	}
	if got := h.do(http.MethodGet, "/spend/logs/ui/"+anonymousID, owner.session, nil); got.status != 404 {
		t.Fatalf("匿名日志不得扩大到普通用户: %s", got.describe())
	}
}
