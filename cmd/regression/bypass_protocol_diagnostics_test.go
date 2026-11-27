package regression

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestBypassProtocolFailureDiagnostics 验证 OpenAI Chat、Anthropic、Responses、Gemini 和 Vertex 透传的失败落库契约。
// 通过真实网关和隔离 PostgreSQL 对照原始供应商响应、网关错误原因、出站诊断及零费用；测试自动回收本地上游和数据库。
func TestBypassProtocolFailureDiagnostics(t *testing.T) {
	for _, protocol := range []struct {
		name, transport, endpoint, path, outbound string
	}{
		{"chat", "bypass_openai_chat", "bypass:openai-chat", "/bypass/openai/v1/chat/completions", "/v1/chat/completions"},
		{"anthropic", "bypass_anthropic_messages", "bypass:anthropic-messages", "/bypass/anthropic/v1/messages", "/v1/messages"},
		{"responses", "bypass_openai_responses", "bypass:openai-responses", "/bypass/openai/v1/responses", "/v1/responses"},
		{"gemini", "gemini_generate_content", "bypass:gemini", "/bypass/gemini/v1beta/models/audit:generateContent", "/v1beta/models/upstream:generateContent"},
		{"vertex", "vertex_generate_content", "bypass:vertex", "/bypass/vertex/v1/models/audit:generateContent", "/models/upstream:generateContent"},
	} {
		t.Run(protocol.name, func(t *testing.T) {
			t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
			t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
			google := protocol.name == "gemini" || protocol.name == "vertex"
			const fieldError = `{"error":{"code":400,"message":"Invalid request","details":[{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"generationConfig.temperature","description":"Outside the range","input":"private-echo"}]}]}}`
			const embeddedError = `{"error":{"code":"embedded_failure","message":"Generation failed"},"usage":{"input_tokens":8,"output_tokens":2}}`
			const vendorError = "{ \"type\": \"error\", \"error\": { \"type\": \"overloaded_error\", \"message\": \"Provider saturated\" }, \"request_id\": \"req-vendor-502\" }\n"
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				calls.Add(1)
				switch r.URL.Query().Get("fixture") {
				case "disconnect":
					conn, _, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				case "empty-error":
					w.WriteHeader(502)
				case "empty-success":
					w.WriteHeader(200)
				case "invalid-field":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(400)
					_, _ = io.WriteString(w, fieldError)
				case "embedded-error":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, embeddedError)
				case "vendor-error", "interrupted-error":
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Request-Id", "req-vendor-502")
					if r.URL.Query().Get("fixture") == "interrupted-error" {
						w.Header().Set("Content-Length", fmt.Sprint(len(vendorError)+100))
					}
					w.WriteHeader(502)
					_, _ = io.WriteString(w, vendorError)
				case "stream":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: ping\ndata: {}\n\n")
				case "incomplete":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp-fixture\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"content_filter\"}}}\n\n")
				}
			}))
			t.Cleanup(up.Close)
			dep := hardBudgetDeployment("audit")
			dep.LiteLLMParams["model"] = "upstream"
			dep.LiteLLMParams["api_base"] = up.URL
			dep.ModelInfo["transport"] = protocol.transport
			dep.ModelInfo["endpoint_types"] = []string{protocol.endpoint}
			h := openHarness(t, false, dep)
			admin := h.adminSession()
			owner := h.openScope(t, admin, "native-"+protocol.name)
			template := routeTemplate(t, h, admin, "native diagnostics", routeTemplateBody([]any{}, 2, 5, 100, 0))
			bindTemplate(t, h, admin, "team", owner.teamID, template)
			before := h.moneyOf(t, owner)
			type failureCase struct {
				fixture, kind, reason              string
				upstream, client, logged, attempts int
			}
			cases := []failureCase{
				{"disconnect", "transport_error", "could not receive a response from the upstream", 0, 502, 502, 2},
				{"empty-error", "", "upstream returned HTTP 502 without an error body", 502, 502, 502, 2},
				{"vendor-error", "", "Provider saturated", 502, 502, 502, 2},
				{"interrupted-error", "", "Provider saturated", 502, 502, 502, 2},
				{"empty-success", "empty_response", "empty dialogue response", 200, 502, 502, 1},
				{"embedded-error", "", "embedded_failure", 200, 200, 502, 1},
				{"stream", "transport_error", "native stream ended without successful completion", 200, 200, 502, 1},
			}
			if google {
				cases = append(cases, failureCase{"invalid-field", "", "generationConfig.temperature", 400, 400, 400, 1})
			}
			if protocol.name == "responses" {
				cases = append(cases, failureCase{"incomplete", "transport_error", "content_filter", 200, 200, 502, 1})
			}
			for _, tc := range cases {
				t.Run(tc.fixture, func(t *testing.T) {
					path, outbound := protocol.path, protocol.outbound
					stream := tc.fixture == "stream" || tc.fixture == "incomplete"
					body := map[string]any{"model": "audit", "stream": stream, "messages": []any{map[string]any{"role": "user", "content": "Audit diagnostic."}}}
					if protocol.name == "responses" {
						body = map[string]any{"model": "audit", "input": "Audit diagnostic.", "stream": stream}
					}
					if protocol.name == "anthropic" {
						body["max_tokens"] = 32
					}
					if google {
						body = map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"text": "Audit diagnostic."}}}}}
						if stream {
							path = strings.Replace(path, ":generateContent", ":streamGenerateContent", 1)
							outbound = strings.Replace(outbound, ":generateContent", ":streamGenerateContent", 1)
						}
					}
					calls.Store(0)
					reply := h.do(http.MethodPost, path+"?fixture="+tc.fixture, owner.key, body)
					if reply.status != tc.client || int(calls.Load()) != tc.attempts {
						t.Fatalf("状态或尝试次数异常: calls=%d reply=%s", calls.Load(), reply.describe())
					}
					if (tc.fixture == "invalid-field" && reply.text() != fieldError) || (tc.fixture == "empty-error" && reply.text() != "") {
						t.Fatalf("诊断改变了供应商原始 HTTP 响应: %s", reply.describe())
					}
					if tc.fixture == "embedded-error" && reply.text() != embeddedError {
						t.Fatalf("HTTP 200 中的供应商错误正文被改写: %s", reply.describe())
					}
					if tc.fixture == "vendor-error" || tc.fixture == "interrupted-error" {
						if reply.text() != vendorError || reply.header("Request-Id") != "req-vendor-502" {
							t.Fatalf("供应商原始错误正文或请求 ID 被丢弃: %s", reply.describe())
						}
					}
					detail := logDetail(t, h, admin, reply.header("x-litellm-call-id"))
					metadata, _ := detail["metadata"].(map[string]any)
					diag, _ := metadata["upstream_response"].(map[string]any)
					failure, _ := diag["failure"].(map[string]any)
					summary := fmt.Sprint(detail["error"])
					if tc.fixture == "disconnect" {
						const reason = "upstream_transport_error"
						clientError, _ := reply.json()["error"].(map[string]any)
						classification, _ := detail["diagnosis"].(map[string]any)
						if clientError["reason"] != reason || !strings.Contains(errorMessage(reply), tc.reason) ||
							strings.Contains(reply.text(), "EOF") || classification["code"] != reason ||
							failure["phase"] != "upstream_request" || !strings.Contains(fmt.Sprint(failure["transport_error"]), "EOF") {
							t.Fatalf("断连应返回安全的客户端原因并独立保存传输诊断: reply=%s diagnosis=%v failure=%v", reply.describe(), classification, failure)
						}
					}
					if diag["endpoint"] != outbound || firstFloat(diag, "status_code") != float64(tc.upstream) || firstFloat(metadata, "http_status") != float64(tc.logged) ||
						detail["status"] != "error" || detail["body_capture_status"] != "disabled" ||
						!strings.Contains(summary, tc.reason) || strings.Contains(summary, "private-echo") ||
						firstFloat(detail, "spend") != 0 || h.moneyOf(t, owner) != before {
						t.Fatalf("诊断或预算错误: %v", detail)
					}
					if tc.kind != "" && failure["kind"] != tc.kind {
						t.Fatalf("失败类型不符: %v", diag)
					}
					if tc.fixture == "interrupted-error" && !strings.Contains(fmt.Sprint(diag["read_error"]), "unexpected EOF") {
						t.Fatalf("HTTP 正文传输中断的诊断丢失: %v", diag)
					}
				})
			}
		})
	}
}
