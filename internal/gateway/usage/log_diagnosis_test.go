package usage

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunqirui1987/xhub/internal/authz"
	"github.com/sunqirui1987/xhub/internal/iam"
)

// TestLogDiagnosisUsesObservedFacts 覆盖现有网关经验和有界新诊断，验证 502 的出站来源、503 冷却事实和缺失原因。
// 前置内存日志快照与目录资料；脱敏输出只包含固定分类，不读取正文或外部服务，无需清理。
func TestLogDiagnosisUsesObservedFacts(t *testing.T) {
	for _, tc := range []struct {
		name, code, modelMissing, userMissing, teamMissing, phase, method string
		event                                                             iam.UsageEvent
		body                                                              iam.RequestLog
		user                                                              iam.User
		found                                                             bool
		upstream                                                          int
	}{
		{
			name: "历史白名单", code: "model_not_allowed", teamMissing: "team_not_recorded", found: true,
			event: iam.UsageEvent{HTTPStatus: 401, UserID: "verified", KeyID: "key", Model: "luna"},
			body:  iam.RequestLog{Error: `{"error":{"type":"invalid_request_error","message":"model not in allowed model list"}}`},
			user:  iam.User{Name: "Verified caller", Email: "private@example.invalid"},
		},
		{
			name: "新方法错误", code: "unsupported_method", modelMissing: "model_not_applicable", method: "GET",
			event: iam.UsageEvent{HTTPStatus: 404, UserID: "verified", TeamAlias: "team"},
			body:  iam.RequestLog{Error: "Not Found", UpstreamResponse: `{"outcome":{"code":"unsupported_method","model_missing_reason":"not_reached","request_method":"GET"}}`},
			user:  iam.User{Name: "Verified caller"}, found: true,
		},
		{
			name: "历史404无方法", code: "route_not_found", modelMissing: "model_not_applicable",
			userMissing: "unverified_identity", teamMissing: "unverified_identity",
			event: iam.UsageEvent{HTTPStatus: 404},
			body:  iam.RequestLog{Error: `{"error":{"type":"not_found","message":"Not Found"}}`},
		},
		{
			name: "鉴权库故障", code: "service_failure", modelMissing: "body_unreadable",
			userMissing: "identity_lookup_failed", teamMissing: "unverified_identity",
			event: iam.UsageEvent{HTTPStatus: 500},
			body:  iam.RequestLog{UpstreamResponse: `{"outcome":{"user_missing_reason":"identity_lookup_failed","model_missing_reason":"body_unreadable","team_missing_reason":"identity_unverified"}}`},
		},
		{
			name: "真实上游状态优先于供应商自定义原因", code: "upstream_http_error", upstream: 502,
			modelMissing: "model_not_recorded", userMissing: "user_not_recorded", teamMissing: "unverified_identity",
			event: iam.UsageEvent{HTTPStatus: 502, OutboundEndpoint: "/third-party"},
			body:  iam.RequestLog{Error: `{"error":{"reason":"model_not_allowed","message":"private upstream echo"}}`, UpstreamResponse: `{"status_code":502,"headers":{"Authorization":"private upstream echo"}}`},
		},
		{
			name: "上游200后截断", code: "upstream_stream_interrupted", upstream: 200, phase: "response_transfer",
			modelMissing: "model_not_recorded", userMissing: "user_unavailable", teamMissing: "team_name_not_recorded",
			event: iam.UsageEvent{HTTPStatus: 502, UserID: "deleted", TeamID: "historical"},
			body:  iam.RequestLog{UpstreamResponse: `{"status_code":200,"failure":{"phase":"response_transfer","kind":"unexpected_eof"}}`},
		},
		{
			name: "上游成功后网关保存历史失败", code: "history_unavailable", upstream: 200,
			event: iam.UsageEvent{HTTPStatus: 502, Model: "model", UserID: "caller", TeamAlias: "team"},
			body:  iam.RequestLog{Error: `{"error":{"type":"history_unavailable","message":"conversation history could not be saved"}}`, UpstreamResponse: `{"status_code":200}`},
			user:  iam.User{Name: "Display name"}, found: true,
		},
		{
			name: "停用模型", code: "model_disabled",
			event: iam.UsageEvent{HTTPStatus: 400, Model: "model", UserID: "caller", TeamAlias: "team"},
			body:  iam.RequestLog{Error: `{"error":{"type":"model_disabled","message":"model is disabled: model"}}`},
			user:  iam.User{Name: "Display name"}, found: true,
		},
		{
			name: "取消优先于状态提示", code: "request_canceled", upstream: 200, phase: "response_transfer",
			userMissing: "user_name_not_configured", teamMissing: "no_team",
			event: iam.UsageEvent{HTTPStatus: 499, UserID: "caller", Model: "model"},
			body:  iam.RequestLog{UpstreamResponse: `{"status_code":200,"failure":{"phase":"response_transfer","kind":"request_canceled"},"outcome":{"team_missing_reason":"no_team"}}`},
			user:  iam.User{Name: " "}, found: true,
		},
		{
			name: "拒绝越界自定义原因/阶段/方法", code: "service_failure",
			modelMissing: "model_not_recorded", userMissing: "user_not_recorded", teamMissing: "unverified_identity",
			event: iam.UsageEvent{HTTPStatus: 502},
			body:  iam.RequestLog{Error: "private upstream echo", UpstreamResponse: `{"status_code":1000,"outcome":{"code":"private upstream echo","request_method":"private upstream echo"},"failure":{"phase":"private upstream echo"}}`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := diagnoseLog(tc.event, tc.body, tc.user, tc.found)
			want := logDiagnosis{
				Code: tc.code, ModelMissingReason: tc.modelMissing, UserMissingReason: tc.userMissing, TeamMissingReason: tc.teamMissing,
				UpstreamStatus: tc.upstream, Phase: tc.phase, RequestMethod: tc.method,
			}
			if got != want {
				t.Fatalf("日志诊断错误: got=%+v want=%+v", got, want)
			}
			encoded, err := json.Marshal(got)
			if err != nil || strings.Contains(string(encoded), "private") {
				t.Fatalf("列表诊断泄露原始诊断或用户邮箱: %s %v", encoded, err)
			}
		})
	}
	ev := iam.UsageEvent{HTTPStatus: 503, Model: "model"}
	body := iam.RequestLog{UpstreamResponse: `{"routing":{"compatible":2,"cooling":2,"available":0}}`}
	got := diagnoseLog(ev, body, iam.User{}, false)
	if got.Code != "all_deployments_cooling_down" || got.CompatibleDeployments == nil || *got.CompatibleDeployments != 2 ||
		got.CoolingDeployments == nil || *got.CoolingDeployments != 2 {
		t.Fatalf("兼容部署全部冷却的事实丢失: %+v", got)
	}
}

// TestLogSeverityQueryValidation 验证日志级别接收白名单枚举并兼容旧状态条件，非法值返回错误。
// 前置内存 HTTP 请求，无外部服务或清理。
func TestLogSeverityQueryValidation(t *testing.T) {
	for _, level := range []string{"warning", "error", ""} {
		q, err := logQuery(httptest.NewRequest("GET", "/?status_filter=error&severity="+level, nil), &authz.Scope{})
		if err != nil || q.Severity != level || q.Status != "error" {
			t.Fatalf("级别/旧筛选没有共同生效: %+v %v", q, err)
		}
	}
	if _, err := logQuery(httptest.NewRequest("GET", "/?severity=unknown", nil), &authz.Scope{}); err == nil {
		t.Fatal("非法级别不能丢弃后返回不限结果")
	}
}
