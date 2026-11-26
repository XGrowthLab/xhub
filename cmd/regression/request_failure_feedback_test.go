package regression

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	"github.com/sunqirui1987/xhub/internal/iam"
)

// TestRequestFailureFeedback 用隔离 PostgreSQL 和已鉴权租户验证客户端失败说明与日志归属同步。
// t 创建拥有团队的个人密钥，验证白名单拒绝、入口方法错误、匿名缺失和跨租户隔离。
// 拒绝不访问上游且费用为零；测试 harness 关闭服务器并删除私有 schema，不用线上数据或供应商凭据。
func TestRequestFailureFeedback(t *testing.T) {
	dep := chatDeployment("feedback-allowed")
	h := newHarness(t, dep)
	admin := h.adminSession()
	owner := h.openScope(t, admin, "feedback-owner")
	other := h.provision(t, admin, "feedback-other")
	h.ok(http.MethodPost, "/key/update", admin, map[string]any{"key": owner.key, "models": []string{dep.ModelName}})
	mark := len(h.upstreamCalls())
	for _, tc := range []struct {
		name, method, path, credential, reason, message string
		status                                          int
		body                                            map[string]any
	}{
		{"白名单", http.MethodPost, "/v1/chat/completions", owner.key, "model_not_allowed", "List the models", 401, chatRequest("feedback-denied", "Check model access.")},
		{"入口方法", http.MethodGet, "/responses", owner.key, "unsupported_method", "HTTP POST", 404, nil},
		{"未识别凭据", http.MethodGet, "/responses", "sk-unrecognised-fixture", "unsupported_method", "HTTP POST", 404, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := h.do(tc.method, tc.path, tc.credential, tc.body)
			errorDoc, _ := reply.json()["error"].(map[string]any)
			if reply.status != tc.status || errorDoc["reason"] != tc.reason || !strings.Contains(errorMessage(reply), tc.message) {
				t.Fatalf("客户端未收到可处理的错误: %s", reply.describe())
			}
			h.flushSpend()
			id := reply.header("x-litellm-call-id")
			var usage iam.UsageEvent
			found, err := h.db.Engine.Where("request_id = ?", id).Get(&usage)
			if err != nil || !found || usage.HTTPStatus != tc.status || usage.Cost != 0 {
				t.Fatalf("失败日志状态、费用或持久化异常: found=%t row=%+v err=%v", found, usage, err)
			}
			stored, err := h.db.GetRequestLog(t.Context(), id)
			if err != nil || stored == nil {
				t.Fatalf("失败诊断缺失: %v", err)
			}
			var diag dpdiagnostics.UpstreamDiagnostics
			if err := json.Unmarshal([]byte(stored.UpstreamResponse), &diag); err != nil || diag.Outcome == nil || diag.Outcome.RequestMethod != tc.method {
				t.Fatalf("没有保存请求方法和缺失原因: %+v %v", diag, err)
			}
			if tc.credential == owner.key {
				if usage.TeamID != owner.teamID || usage.UserID != owner.userID || usage.OrganizationID != owner.orgID {
					t.Fatal("早期失败未保留可信的用户及团队归属")
				}
			} else if usage.UserID != "" || usage.TeamID != "" || diag.Outcome.UserMissingReason != "credentials_rejected" || diag.Outcome.TeamMissingReason != "identity_unverified" {
				t.Fatalf("不能用无效密钥填入归属: %+v", diag.Outcome)
			}
			if tc.method == http.MethodGet && (usage.Model != "" || diag.Outcome.ModelMissingReason != "not_reached") {
				t.Fatalf("尚未到模型阶段的请求诊断不准确: %+v", diag.Outcome)
			}
			denied := h.do(http.MethodGet, "/spend/logs/ui/"+id, other.session, nil)
			if denied.status != http.StatusNotFound {
				t.Fatalf("其他租户可访问失败日志: %s", denied.describe())
			}
		})
	}
	if got := h.upstreamSince(mark); len(got) != 0 {
		t.Fatalf("在网关拒绝的请求不应到上游: %v", got)
	}
}
