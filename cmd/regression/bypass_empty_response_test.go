package regression

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sunqirui1987/xhub/internal/iam"
)

// TestBypassEmptyResponseAccounting 使用真实网关、隔离数据库与本地上游验证空响应、账单及恢复。
// 上游首个请求返回空 200，之后返回确认用量；测试检查不自动重试，已确认费用仍结算，夹具清理数据库和服务器。
func TestBypassEmptyResponseAccounting(t *testing.T) {
	t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"msg_verified","type":"message","role":"assistant","model":"empty-test","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":2}}`)
	}))
	t.Cleanup(up.Close)
	dep := hardBudgetDeployment("empty-test")
	dep.LiteLLMParams["api_base"] = up.URL
	dep.ModelInfo["transport"] = "bypass_anthropic_messages"
	dep.ModelInfo["endpoint_types"] = []string{"bypass:anthropic-messages"}
	h := newHarness(t, dep)
	owner := h.openScope(t, h.adminSession(), "empty-owner")
	body := map[string]any{"model": "empty-test", "messages": []any{map[string]any{"role": "user", "content": "Hello"}}, "max_tokens": 32}
	before := h.moneyOf(t, owner)
	failed := h.do("POST", "/bypass/anthropic/v1/messages", owner.key, body)
	if failed.status != 502 || calls.Load() != 1 {
		t.Fatalf("空响应应返回 502 且不得重试: status=%d calls=%d body=%s", failed.status, calls.Load(), failed.describe())
	}
	h.flushSpend()
	id := failed.header("x-litellm-call-id")
	event, err := h.db.GetUsageEvent(t.Context(), iam.UsageQuery{}, id)
	if err != nil || event.Status != "error" || event.HTTPStatus != 502 || event.Cost != 0 {
		t.Fatalf("空响应应记失败并保留零网关费用: event=%+v err=%v", event, err)
	}
	row, err := h.db.GetRequestLog(t.Context(), id)
	if err != nil || !strings.Contains(row.UpstreamResponse, `"empty_response"`) || !strings.Contains(row.UpstreamResponse, `"upstream_usage_missing"`) {
		t.Fatalf("应保留上游空响应及用量缺失诊断: row=%+v err=%v", row, err)
	}
	var diagnostic map[string]any
	if err := json.Unmarshal([]byte(row.UpstreamResponse), &diagnostic); err != nil || diagnostic["status_code"] != float64(200) {
		t.Fatalf("应保留上游实际 200 状态: diagnostic=%v err=%v", diagnostic, err)
	}
	if after := h.moneyOf(t, owner); after != before {
		t.Fatalf("空响应不应扣减预算: before=%+v after=%+v", before, after)
	}
	success := h.do("POST", "/bypass/anthropic/v1/messages", owner.key, body)
	if success.status != 200 || calls.Load() != 2 || !strings.Contains(success.text(), "msg_verified") {
		t.Fatalf("用户重新请求应成功: status=%d calls=%d body=%s", success.status, calls.Load(), success.describe())
	}
	h.flushSpend()
	confirmed, err := h.db.GetUsageEvent(t.Context(), iam.UsageQuery{}, success.header("x-litellm-call-id"))
	if err != nil || confirmed.Status != "success" || confirmed.Cost <= 0 || confirmed.PromptTokens != 8 || confirmed.CompletionTokens != 2 {
		t.Fatalf("上游确认用量必须正常结算: event=%+v err=%v", confirmed, err)
	}
}

// TestBypassFailureDiagnostics 使用真实 Anthropic 透传、两次重试和 PostgreSQL 验证失败详情可查询。
// 本地上游模拟首包超时、200 正文截断和无正文 502；验证客户端原因、原始状态及独立传输诊断，禁止保存提示词或扣费。
// 私有 schema 与本地 HTTP 服务由各子测试清理。
func TestBypassFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, phase, kind, summary, transportDetail, reason string
		upstreamStatus                                      int
	}{
		{"timeout", "upstream_request", "timeout", "Timed out waiting for a response from the upstream.", "context deadline exceeded", "upstream_timeout", 0},
		{"truncated", "response_transfer", "unexpected_eof", "unexpected EOF", "unexpected EOF", "upstream_stream_interrupted", http.StatusOK},
		{"empty-502", "", "", "upstream returned HTTP 502 without an error body", "", "", http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
			t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				calls.Add(1)
				switch tc.name {
				case "timeout":
					<-r.Context().Done()
				case "truncated":
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Content-Length", "1024")
					_, _ = fmt.Fprint(w, `{"type":"message"`)
				case "empty-502":
					w.Header().Set("X-Request-ID", "upstream-empty-502")
					w.WriteHeader(http.StatusBadGateway)
				}
			}))
			t.Cleanup(up.Close)
			dep := hardBudgetDeployment("native-" + tc.name)
			dep.LiteLLMParams["api_base"] = up.URL
			dep.ModelInfo["transport"] = "bypass_anthropic_messages"
			dep.ModelInfo["endpoint_types"] = []string{"bypass:anthropic-messages"}
			h := openHarness(t, false, dep)
			admin := h.adminSession()
			owner := h.openScope(t, admin, "diagnostic-"+tc.name)
			timeout := 5.0
			if tc.name == "timeout" {
				timeout = 0.25
			}
			template := routeTemplate(t, h, admin, "diagnostic retry", routeTemplateBody([]any{}, 2, timeout, 3, 0))
			bindTemplate(t, h, admin, "team", owner.teamID, template)
			body := chatRequest(dep.ModelName, "Private diagnostic regression prompt.")
			body["max_tokens"] = 32
			before := h.moneyOf(t, owner)
			reply := h.do(http.MethodPost, "/bypass/anthropic/v1/messages", owner.key, body)
			if reply.status != http.StatusBadGateway || calls.Load() != 2 {
				t.Fatalf("两次尝试后应返回 502: calls=%d reply=%s", calls.Load(), reply.describe())
			}
			if tc.name == "empty-502" && (reply.text() != "" || reply.header("X-Request-ID") != "upstream-empty-502") {
				t.Fatalf("日志摘要不得改变透传响应: %s", reply.describe())
			}
			if tc.reason != "" {
				clientError, _ := reply.json()["error"].(map[string]any)
				if clientError["reason"] != tc.reason || strings.Contains(reply.text(), tc.transportDetail) {
					t.Fatalf("客户端应收到稳定原因且不暴露底层传输错误: %s", reply.describe())
				}
			}
			id := reply.header("x-litellm-call-id")
			detail := logDetail(t, h, admin, id)
			metadata, _ := detail["metadata"].(map[string]any)
			diagnostic, _ := metadata["upstream_response"].(map[string]any)
			failure, _ := diagnostic["failure"].(map[string]any)
			row, err := h.db.GetRequestLog(t.Context(), id)
			if err != nil || row == nil || row.RequestBody != "" || row.ResponseBody != "" || !strings.Contains(row.Error, tc.summary) {
				t.Fatalf("数据库必须独立保存安全失败摘要: row=%+v err=%v", row, err)
			}
			if diagnostic["endpoint"] != "/v1/messages" || firstFloat(diagnostic, "status_code") != float64(tc.upstreamStatus) ||
				firstFloat(metadata, "http_status") != 502 || detail["status"] != "error" ||
				!strings.Contains(fmt.Sprint(detail["error"]), tc.summary) || detail["body_capture_status"] != "disabled" ||
				firstFloat(detail, "spend") != 0 || h.moneyOf(t, owner) != before {
				t.Fatalf("诊断、状态、正文隐私或账单错误: %v", detail)
			}
			if tc.kind != "" && (failure["phase"] != tc.phase || failure["kind"] != tc.kind ||
				!strings.Contains(fmt.Sprint(failure["transport_error"]), tc.transportDetail)) {
				t.Fatalf("失败阶段、类型或原始传输诊断丢失: %v", diagnostic)
			}
		})
	}
}
