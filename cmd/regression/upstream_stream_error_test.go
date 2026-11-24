package regression

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestUpstreamStreamErrorContract 通过真实路由、供应商 HTTP 和 PG 验证首帧/中途错误不会丢失。
// 参数 t 为测试上下文；覆盖三种协议原生及 Chat→Responses 转换、错误日志/状态/鉴权与不重放。
// 不启用提示词保存也必须记录错误；httptest 和私有 schema 在 Cleanup 回收，无生产依赖。
func TestUpstreamStreamErrorContract(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
		targets := []string{protocol}
		if protocol == "openai-chat" {
			targets = append(targets, "openai-responses")
		}
		for _, target := range targets {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/partial=%v", protocol, target, partial), func(t *testing.T) {
					var calls atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						w.Header().Set("Content-Type", "text/event-stream")
						w.Header().Set("X-Request-Id", "req-header")
						if partial {
							prelude := map[string]string{
								"openai-chat":        `{"id":"r","choices":[{"index":0,"delta":{"content":"Partial answer"}}]}`,
								"openai-responses":   `{"type":"response.output_text.delta","delta":"Partial answer"}`,
								"anthropic-messages": `{"type":"message_start","message":{"id":"r","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1}}}`,
							}[protocol]
							fmt.Fprintf(w, "data: %s\n\n", prelude)
						}
						detail := `{"code":"vendor_capacity","type":"overloaded_error","message":"Vendor pool busy: 保留原文","details":{"retryable":true}}`
						event := `{"type":"error","error":` + detail + `,"request_id":"req-vendor"}`
						if protocol == "openai-responses" {
							event = `{"type":"response.failed","response":{"id":"r","error":` + detail + `},"request_id":"req-vendor"}`
						}
						fmt.Fprintf(w, "data: %s\n\n", event)
					}))
					t.Cleanup(upstream.Close)
					dep := chatDeployment("stream-error-contract")
					dep.LiteLLMParams["api_base"] = upstream.URL + "/v1"
					dep.ModelInfo["transport"] = map[string]string{"openai-chat": "bypass_openai_chat", "openai-responses": "bypass_openai_responses", "anthropic-messages": "bypass_anthropic_messages"}[protocol]
					if protocol == "anthropic-messages" {
						dep.LiteLLMParams["custom_llm_provider"] = "anthropic"
					}
					dep.ModelInfo["endpoint_types"] = []string{"chat", "responses", "messages"}
					h := openHarness(t, false, dep)
					admin := h.adminSession()
					path := "/chat/completions"
					body := chatRequest(dep.ModelName, "Explain the failure.")
					if target == "openai-responses" {
						path = "/responses"
						body = map[string]any{"model": dep.ModelName, "input": "Explain the failure."}
					}
					if target == "anthropic-messages" {
						path = "/v1/messages"
						body["max_tokens"] = 32
					}
					body["stream"] = true
					got := h.do("POST", path, admin, body)
					if got.status != 200 && got.status != 502 {
						t.Fatalf("错误返回状态异常: %s", got.describe())
					}
					for _, marker := range []string{"vendor_capacity", "overloaded_error", "保留原文", "req-vendor", "retryable"} {
						if !strings.Contains(got.text(), marker) {
							t.Fatalf("客户端诊断丢失 %s: %s", marker, got.describe())
						}
					}
					if calls.Load() != 1 {
						t.Fatalf("失败请求被重放 %d 次", calls.Load())
					}
					detail := logDetail(t, h, admin, got.header("x-litellm-call-id"))
					if rows := h.spendLogs(t, admin); len(rows) != 1 {
						t.Fatalf("单次失败流应只产生一条调用记录: %v", rows)
					}
					diag := detail["metadata"].(map[string]any)["upstream_response"].(map[string]any)
					if diag["status_code"] != float64(200) || detail["status"] != "error" {
						t.Fatalf("HTTP握手与失败状态混淆: %v", detail)
					}
					for _, marker := range []string{"vendor_capacity", "保留原文", "req-vendor"} {
						if !strings.Contains(firstString(detail, "error"), marker) || !strings.Contains(string(mustJSON(diag["error"])), marker) {
							t.Fatalf("落库诊断丢失 %s: %v", marker, detail)
						}
					}
					if denied := h.do("GET", "/spend/logs/ui/"+got.header("x-litellm-call-id"), "", nil); denied.status != 401 {
						t.Fatalf("匿名可读日志: %s", denied.describe())
					}
				})
			}
		}
	}
}

// TestGatewayStreamInterruptionDiagnostics 验证有输出、无结束帧的兼容 Responses 流仍保留具体中断原因。
// 本地供应商发送部分输出后关闭；通过真实 HTTP 和 PostgreSQL 查同一调用，验证状态、零费用和关闭正文后的隐私。
// 测试负责回收本地服务器与私有 schema，无生产依赖。
func TestGatewayStreamInterruptionDiagnostics(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Unfinished private output\"}\n\n")
	}))
	t.Cleanup(upstream.Close)
	dep := chatDeployment("stream-interruption")
	dep.LiteLLMParams["api_base"] = upstream.URL + "/v1"
	dep.ModelInfo["transport"] = "bypass_openai_responses"
	dep.ModelInfo["endpoint_types"] = []string{"responses"}
	h := openHarness(t, false, dep)
	admin := h.adminSession()
	reply := h.do(http.MethodPost, "/responses", admin, map[string]any{
		"model": dep.ModelName, "input": "Test incomplete stream.", "stream": true,
	})
	if reply.status != http.StatusOK || !strings.Contains(reply.text(), "Unfinished private output") {
		t.Fatalf("本例需先收到已开始的 SSE: %s", reply.describe())
	}
	detail := logDetail(t, h, admin, reply.header("x-litellm-call-id"))
	metadata, _ := detail["metadata"].(map[string]any)
	up, _ := metadata["upstream_response"].(map[string]any)
	errorText := firstString(detail, "error")
	if firstFloat(metadata, "http_status") != 502 || firstFloat(up, "status_code") != 200 ||
		detail["status"] != "error" || detail["body_capture_status"] != "disabled" || firstFloat(detail, "spend") != 0 ||
		!strings.Contains(errorText, "upstream stream ended without completion") || strings.Contains(errorText, "Unfinished private output") {
		t.Fatalf("流中断未保留实际原因，或部分生成内容泄露: %v", detail)
	}
}
