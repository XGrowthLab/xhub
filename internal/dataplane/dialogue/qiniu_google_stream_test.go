package dialogue

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
)

// TestQiniuGoogleSSEReasoning 验证两种 Google 入口从 Chat 流保留公开推理且终态不重发正文或推理。
// 参数 t 为测试上下文，前置正确 Chat stop 和 DONE。内存 HTTP 响应由管道关闭，无外部资源。
func TestQiniuGoogleSSEReasoning(t *testing.T) {
	frames := "data: {\"id\":\"upstream\",\"choices\":[{\"delta\":{\"reasoning_content\":\"Public summary\"}}]}\n\n" +
		"data: {\"id\":\"upstream\",\"choices\":[{\"delta\":{\"content\":\"Visible answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
	for _, target := range []string{"gemini", "vertex"} {
		t.Run(target, func(t *testing.T) {
			w := httptest.NewRecorder()
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(frames))}
			started, usage, _, raw, err := PipeDialogue(w, resp, time.Now(), "test", "openai-chat", target)
			output := string(raw)
			if err != nil || !started || usage["completion_tokens"] != float64(2) ||
				!strings.Contains(output, `"text":"Public summary","thought":true`) ||
				strings.Count(output, "Public summary") != 1 || strings.Count(output, "Visible answer") != 1 ||
				strings.Count(output, `"finishReason":"STOP"`) != 1 || strings.Contains(output, "thoughtSignature") {
				t.Fatalf("Google SSE 推理、正文、用量或结束帧不正确: %s, error=%v usage=%+v", output, err, usage)
			}
		})
	}
}

// TestQiniuGoogleSSETerminal 验证上游仅给正文和用量时返回字段缺失，明确终态才输出成功。
// 前置含 Google 内容、独立用量或显式错误的内存流，两个端点分别检查，响应关闭后无需外部清理。
func TestQiniuGoogleSSETerminal(t *testing.T) {
	text := `{"candidates":[{"content":{"parts":[{"text":"Visible answer"}]}}]}`
	usage := `{"candidates":[],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2}}`
	for _, source := range []string{"gemini", "vertex"} {
		for _, tc := range []struct{ name, frames, marker string }{
			{"complete", text + "\n\n" + `data: {"candidates":[{"finishReason":"stop"}]}` + "\n\ndata: " + usage, ""},
			{"missing_tail", text + "\n\ndata: " + usage, "candidates[0].finishReason"},
			{"usage_only", usage, "candidates[0].finishReason"},
			{"blocked", text + "\n\ndata: " + `{"candidates":[{"finishReason":"safety"}]}`, "safety"},
			{"invalid_json", text + "\n\ndata: " + `{"candidates":}`, "invalid Google SSE JSON"},
			{"vendor_failure", text + "\n\ndata: " + `{"error":{"code":503,"status":"UNAVAILABLE","message":"upstream maintenance","details":[{"reason":"CAPACITY"}]},"request_id":"vendor-test"}`, "upstream maintenance"},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: " + tc.frames + "\n\n"))}
				wrote, measured, _, data, err := PipeDialogue(httptest.NewRecorder(), resp, time.Now(), "test", source, source)
				output := string(data)
				if tc.marker == "" {
					if err != nil || !wrote || strings.Count(output, `"finishReason":"STOP"`) != 1 || measured["completion_tokens"] != float64(2) {
						t.Fatalf("有效终态漏转或尾帧用量丢失: %s %v %+v", output, err, measured)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), tc.marker) || strings.Contains(output, `"finishReason":"STOP"`) {
					t.Fatalf("Google SSE 失败原因丢失或伪造终态: %s %v", output, err)
				}
				if tc.name == "vendor_failure" {
					if !strings.Contains(output, `"code":503`) || !strings.Contains(output, "CAPACITY") ||
						!strings.Contains(output, "vendor-test") || strings.Contains(output, "error_origin") {
						t.Fatalf("供应商错误未原样保留: %s", output)
					}
				} else if !errors.Is(err, dpdiagnostics.ErrInvalidUpstreamResponse) ||
					(wrote && (!strings.Contains(output, `"code":502`) || !strings.Contains(output, "upstream_invalid_response") ||
						!strings.Contains(output, tc.marker) || strings.Contains(output, "event: error"))) {
					t.Fatalf("Google SSE SDK 错误帧形状无效: %s %v", output, err)
				}
			})
		}
	}
}
