package dialogue

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestQiniuResponsesReasoningProjection 验证七牛两种公开推理位置、镜像去重及密文边界。
// 参数 t 为测试上下文；输入为合成 Responses JSON，检查公开投影、正文及同协议保留，不需要外部清理。
func TestQiniuResponsesReasoningProjection(t *testing.T) {
	for _, tc := range []struct {
		name, items, public, message, messagesBlock string
	}{
		{
			"mirror",
			`{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"First."}]},{"type":"message","content":[{"type":"output_text","text":"OK"},{"type":"reasoning_text","text":"First."}]}`,
			"First.", "", "First.",
		},
		{
			"message only",
			`{"type":"message","content":[{"type":"output_text","text":"OK"},{"type":"reasoning_text","text":"Plan."}]}`,
			"Plan.", "Plan.", "Plan.",
		},
		{
			"distinct",
			`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"First."}]},{"type":"message","content":[{"type":"output_text","text":"OK"},{"type":"reasoning_text","text":"Second."}]}`,
			"First.Second.", "Second.", "First.",
		},
		{
			"encrypted",
			`{"type":"reasoning","id":"rs_1","encrypted_content":"cipher","content":[{"type":"reasoning_text","text":"Private."}]},{"type":"message","content":[{"type":"output_text","text":"OK"}]}`,
			"", "", "xhub_responses_reasoning:cipher",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Appendf(nil, `{"id":"resp_kimi","status":"completed","output":[%s]}`, tc.items)
			result, err := ParseDialogueResult("openai-responses", raw)
			if err != nil || result.Text != "OK" || result.Reasoning != tc.public || result.ResponsesMessageReasoning != tc.message {
				t.Fatalf("Responses 推理或正文投影错误: %+v %v", result, err)
			}
			chat, err := EncodeDialogueResult(result, "openai-chat", "public")
			if err != nil || !strings.Contains(string(chat), `"content":"OK"`) || strings.Contains(string(chat), "Private.") {
				t.Fatalf("Chat 正文或密文边界错误: %s %v", chat, err)
			}
			messages, err := EncodeDialogueResult(result, "anthropic-messages", "public")
			if err != nil || strings.Count(string(messages), tc.messagesBlock) != 1 || !strings.Contains(string(messages), `"text":"OK"`) || strings.Contains(string(messages), "Private.") {
				t.Fatalf("Messages 推理重复、缺失或泄露: %s %v", messages, err)
			}
			native, err := EncodeDialogueResult(result, "openai-responses", "public")
			if err != nil || !strings.Contains(string(native), `"reasoning_text"`) {
				t.Fatalf("原生 Responses 丢失上游明确提供的推理项: %s %v", native, err)
			}
		})
	}
}

// TestQiniuResponsesReasoningHistory 验证 Responses 原生回复作为下一轮输入时能够投影公开推理且不污染正文。
// 参数 t 为测试上下文；内存历史含镜像推理，检查同协议原样、Chat 去重及非法用户推理拒绝，无需清理。
func TestQiniuResponsesReasoningHistory(t *testing.T) {
	var body map[string]any
	err := json.Unmarshal([]byte(`{"input":[{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"Plan."}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"},{"type":"reasoning_text","text":"Plan."}]},{"type":"message","role":"user","content":"Continue."}]}`), &body)
	if err != nil {
		t.Fatal(err)
	}
	dialogue, err := ParseDialogue("openai-responses", body)
	if err != nil {
		t.Fatal(err)
	}
	native, err := EncodeDialogue(dialogue, "openai-responses", "public")
	raw, _ := json.Marshal(native)
	if err != nil || strings.Count(string(raw), `"type":"reasoning_text"`) != 2 {
		t.Fatalf("原生历史里的有效内容被改写: %s %v", raw, err)
	}
	chat, err := EncodeDialogue(dialogue, "openai-chat", "public")
	raw, _ = json.Marshal(chat)
	if err != nil || strings.Count(string(raw), `"reasoning_content":"Plan."`) != 1 || !strings.Contains(string(raw), `"content":"OK"`) {
		t.Fatalf("跨协议续轮推理或正文丢失或重复: %s %v", raw, err)
	}
	dialogueMap(dialogueList(body["input"])[1])["role"] = "user"
	if _, err := ParseDialogue("openai-responses", body); err == nil || !strings.Contains(err.Error(), "input[1].content[1].type") {
		t.Fatalf("用户输入的推理块未按路径拒绝: %v", err)
	}
}

// TestQiniuResponsesReasoningProjectionErrors 验证兼容仅覆盖 reasoning_text 字符串且保留未知内容错误。
// 参数 t 为测试上下文；输入为内存无效 JSON 响应，断言字段路径且不包含内容原值，无外部资源需清理。
func TestQiniuResponsesReasoningProjectionErrors(t *testing.T) {
	for _, tc := range []struct{ content, want string }{
		{`{"type":"reasoning_text","text":42}`, "output[0].content[0]: reasoning_text.text"},
		{`{"type":"reasoning_text","text":"Do not repeat.","encrypted_content":"secret"}`, "encrypted_content"},
		{`{"type":"future_content","text":"Do not repeat."}`, "unsupported response content future_content"},
	} {
		raw := fmt.Appendf(nil, `{"status":"completed","output":[{"type":"message","content":[%s]}]}`, tc.content)
		_, err := ParseDialogueResult("openai-responses", raw)
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "Do not repeat.") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("Responses 推理字段错误未按契约报告: %v", err)
		}
	}
}
