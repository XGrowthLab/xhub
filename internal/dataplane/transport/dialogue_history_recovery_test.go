package transport

import (
	"net/http"
	"strings"
	"testing"
)

// TestRecoverOpenAIResponsesHistory 验证已完成历史的原生密文可定点恢复。前置工具及大整数，
// 校验工具续轮、活跃项、游标和压缩项的边界；无外部调用或资源。
func TestRecoverOpenAIResponsesHistory(t *testing.T) {
	const base = `{"input":[{"role":"user","content":"Start."},
		{"type":"reasoning","id":"rs_old","encrypted_content":"OLD_CIPHER"},
		{"role":"assistant","content":"Earlier answer."},
		{"role":"user","content":"Continue."}`
	const active = `,{"type":"reasoning","id":"rs_active","encrypted_content":"ACTIVE_CIPHER"},
		{"type":"function_call","id":"fc_1","name":"lookup","arguments":"{}","call_id":"call_1"},
		{"type":"function_call_output","call_id":"call_1","output":"Keep result"}`
	const errorCode = `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified."}}`
	const oldItem = `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content for item rs_old could not be verified."}}`
	for _, tc := range []struct {
		name, request, rejection string
		want                     bool
	}{
		{"new turn", base + `],"vendor_value":9007199254740993}`, errorCode, true},
		{"active with an older error", base + active + `]}`, oldItem, true},
		{"active with a parameter", base + active + `]}`, `{"error":{"code":"invalid_encrypted_content","param":"input[1].encrypted_content"}}`, true},
		{"active unlocated", base + active + `]}`, errorCode, false},
		{"current tool signature", base + active + `]}`, `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content for item rs_active could not be verified."}}`, false},
		{"missing tool result", base + `,{"type":"function_call","call_id":"missing"}]}`, oldItem, false},
		{"compaction", strings.Replace(base, `"type":"reasoning"`, `"type":"compaction"`, 1) + `]}`, errorCode, false},
		{"private reference", strings.Replace(base, "OLD_CIPHER", "xhub_messages_v1:private", 1) + `]}`, errorCode, false},
		{"parent", base + `],"previous_response_id":"resp_old"}`, errorCode, false},
		{"different field", base + `]}`, `{"error":{"code":"invalid_encrypted_content","param":"input[1].encrypted_function_args"}}`, false},
		{"unrelated error", base + `]}`, `{"error":{"code":"invalid_request_error","message":"invalid_encrypted_content"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RecoverDialogueHistory("openai-responses", []byte(tc.request), http.StatusBadRequest, []byte(tc.rejection))
			if ok != tc.want {
				t.Fatalf("历史恢复决定不同: %s", got)
			}
			if !ok {
				return
			}
			if strings.Contains(string(got), "OLD_CIPHER") || !strings.Contains(string(got), "Earlier answer.") {
				t.Fatalf("错误历史被保留或正文丢失: %s", got)
			}
			for _, preserve := range []string{"ACTIVE_CIPHER", "call_1", "Keep result", "9007199254740993"} {
				if strings.Contains(tc.request, preserve) && !strings.Contains(string(got), preserve) {
					t.Fatalf("恢复时改动了 %q: %s", preserve, got)
				}
			}
		})
	}
}

// TestRecoverOpenAIChatHistory 验证仅明确密文错误才能转换完成轮次，DeepSeek 要求的
// reasoning_content 和原工具消息不变。协议样本在内存中，不连接真实供应商。
func TestRecoverOpenAIChatHistory(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"Read."},{"role":"assistant","content":"Done.","reasoning_content":"Keep public","reasoning_items":[{"type":"reasoning","id":"rs_old","summary":[],"encrypted_content":"OLD_CIPHER"}]},{"role":"user","content":"Continue."}]}`)
	rejection := []byte(`{"error":{"code":"invalid_encrypted_content","param":"messages[1].reasoning_items[0].encrypted_content"}}`)
	got, ok := RecoverDialogueHistory("openai-chat", body, http.StatusBadRequest, rejection)
	if !ok || strings.Contains(string(got), "OLD_CIPHER") || !strings.Contains(string(got), `"reasoning_content":"Keep public"`) {
		t.Fatalf("Chat 密文恢复丢失公开推理或失败: %s", got)
	}
	for _, code := range []int{401, 422, 500} {
		if _, ok := RecoverDialogueHistory("openai-chat", body, code, rejection); ok {
			t.Fatalf("HTTP %d 不应触发历史恢复", code)
		}
	}
	if _, ok := RecoverDialogueHistory("openai-chat", body, 400, []byte(`{"error":{"code":"missing_reasoning_content","message":"reasoning_content is required"}}`)); ok {
		t.Fatal("DeepSeek 工具续轮错误不能用密文规则重试")
	}
}
