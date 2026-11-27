package dialogue

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestQiniuResponsesReasoningSSE 验证跨协议 SSE 根据完整终态发布公开 reasoning_text，摘要不重放，不公开受密文约束的增量。
// 参数 t 为测试上下文；用内存响应验证正常、终态补全及字段错误，无网络、数据库及清理依赖。
func TestQiniuResponsesReasoningSSE(t *testing.T) {
	for _, tc := range []struct {
		name, frame, reasoningItem, messageReasoning, wantError string
		wantPlan                                                int
	}{
		{
			"terminal only",
			"",
			"",
			`,{"type":"reasoning_text","text":"Plan."}`,
			"",
			1,
		},
		{
			"mirrored summary",
			`data: {"type":"response.reasoning_summary_text.delta","delta":"Plan."}` + "\n\n",
			`{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Plan."}]},`,
			`,{"type":"reasoning_text","text":"Plan."}`,
			"",
			1,
		},
		{
			"opaque raw delta",
			`data: {"type":"response.reasoning_text.delta","delta":"Plan."}` + "\n\n",
			`{"id":"rs_1","type":"reasoning","encrypted_content":"cipher","content":[{"type":"reasoning_text","text":"Plan."}]},`,
			"",
			"",
			0,
		},
		{
			"malformed delta",
			`data: {"type":"response.reasoning_text.delta","delta":42}` + "\n\n",
			"",
			"",
			"response.reasoning_text.delta.delta",
			0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal := fmt.Sprintf(`data: {"type":"response.completed","response":{"id":"resp","status":"completed","output":[%s{"type":"message","content":[{"type":"output_text","text":"OK"}%s]}]}}`+"\n\n", tc.reasoningItem, tc.messageReasoning)
			raw := tc.frame + terminal
			writer := httptest.NewRecorder()
			_, _, _, output, err := PipeDialogue(writer, &http.Response{Body: io.NopCloser(strings.NewReader(raw))}, time.Now(), "public", "openai-responses", "openai-chat")
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || strings.Contains(string(output), "[DONE]") {
					t.Fatalf("格式错误未按约定失败: %v %s", err, output)
				}
				return
			}
			if err != nil || !strings.Contains(string(output), `"content":"OK"`) || strings.Count(string(output), `"reasoning_content":"Plan."`) != tc.wantPlan || !strings.Contains(string(output), "[DONE]") {
				t.Fatalf("公开推理或终态输出错误: %v %s", err, output)
			}
		})
	}
}
