package dialogue

import (
	"testing"
)

// TestOfficialDialogueResultBoundaries 前置非法响应和不支持的能力，验证 SDK 接入后仍拒绝丢内容的转换。
// 参数 t 为测试上下文；正常工具由协议矩阵覆盖，本例没有外部数据或清理副作用。
func TestOfficialDialogueResultBoundaries(t *testing.T) {
	for _, tc := range []struct{ protocol, raw string }{
		{"openai-chat", "{\"choices\":[{\"message\":42}]}"},
		{"openai-chat", "{\"choices\":[{\"message\":{\"tool_calls\":42}}]}"},
		{"openai-responses", "{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":42}]}"},
		{"openai-responses", "{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":42}]}]}"},
		{"anthropic-messages", "{\"content\":42}"},
		{"anthropic-messages", "{\"content\":[{\"type\":\"text\",\"text\":42}]}"},
		{"openai-chat", `{"choices":[{"message":{"content":42}}]}`},
		{"openai-chat", `{"choices":[{"message":{"refusal":42}}]}`},
		{"openai-chat", `{"choices":[{"message":{"tool_calls":[{"type":"custom","id":"x"}]}}]}`},
		{"openai-responses", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":42}]}]}`},
		{"openai-responses", `{"status":"completed","output":[{"type":"reasoning"}]}`},
		{"anthropic-messages", `{"content":[{"type":"tool_use","id":"x","name":"f","input":null}]}`},
		{"anthropic-messages", `{"content":[{"type":"thinking","thinking":42}]}`},
		{"gemini", `{"candidates":[null]}`},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"x","thought":true,"thoughtSignature":"eA=="}]}}]}`},
	} {
		if _, err := ParseDialogueResult(tc.protocol, []byte(tc.raw)); err == nil {
			t.Fatalf("%s 非法响应被接受: %s", tc.protocol, tc.raw)
		}
	}
}
