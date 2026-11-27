package transport

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const thinkingRejected = "{\"type\":\"error\",\"error\":{\"type\":\"<nil>\",\"message\":\"***.***.content.0: Invalid `signature` in `thinking` block (request id: fixture)\"}}"

// TestRecoverAnthropicThinking 验证签名错误只清理历史助手块。前置已结束工具轮次及新用户消息；
// 检查新 thinking 配置、旧工具输入、用户正文和大整数仍相同，内存测试无需清理。
func TestRecoverAnthropicThinking(t *testing.T) {
	payload := []byte(`{"model":"upstream","thinking":{"type":"adaptive"},"opaque":9007199254740993,"messages":[
		{"role":"user","content":"Check the service."},
		{"role":"assistant","content":[{"type":"thinking","thinking":"Observe","signature":"old"},{"type":"redacted_thinking","data":"old-opaque"},{"type":"tool_use","id":"tool_a","name":"check","input":{"name":"db"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_a","content":"Timed out"}]},
		{"role":"assistant","content":[{"type":"thinking","thinking":"Explain","signature":"old-final"},{"type":"text","text":"The database timed out."}]},
		{"role":"user","content":[{"type":"text","text":"What should I try next?"}]}
	]}`)
	recovered, ok := RecoverAnthropicThinking(payload, http.StatusBadRequest, []byte(thinkingRejected))
	if !ok {
		t.Fatal("明确的历史验签错误未生成兼容请求")
	}
	expected := `{"model":"upstream","thinking":{"type":"adaptive"},"opaque":9007199254740993,"messages":[
		{"role":"user","content":"Check the service."},
		{"role":"assistant","content":[{"type":"tool_use","id":"tool_a","name":"check","input":{"name":"db"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_a","content":"Timed out"}]},
		{"role":"assistant","content":[{"type":"text","text":"The database timed out."}]},
		{"role":"user","content":[{"type":"text","text":"What should I try next?"}]}
	]}`
	var got, want map[string]json.RawMessage
	if json.Unmarshal(recovered, &got) != nil || json.Unmarshal([]byte(expected), &want) != nil {
		t.Fatal("无法解析兼容载荷或预期结构")
	}
	// 统一 JSON 排版后按 RawMessage 比较，防止 float64 舍入供应商扩展字段。
	for key, expectedRaw := range want {
		var keys json.RawMessage = expectedRaw
		want[key], _ = json.Marshal(keys)
	}
	var gotMessages, wantMessages []map[string]json.RawMessage
	_ = json.Unmarshal(got["messages"], &gotMessages)
	_ = json.Unmarshal(want["messages"], &wantMessages)
	delete(got, "messages")
	delete(want, "messages")
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotMessages, wantMessages) {
		t.Fatalf("兼容请求改动了应保留的字段: %s", recovered)
	}
	if second, ok := RecoverAnthropicThinking(recovered, http.StatusBadRequest, []byte(thinkingRejected)); ok || second != nil {
		t.Fatal("已清理的正文不应再次发送")
	}
}

// TestRecoverAnthropicHistoricalErrorDuringTools 验证仅在上游明确指向已完成轮次时兼容。
// 前置两个活跃工具步骤、普通旧轮次及脱敏/错误索引样本；验证当前签名原样保留，无外部资源。
func TestRecoverAnthropicHistoricalErrorDuringTools(t *testing.T) {
	payload := []byte(`{"messages":[
		{"role":"user","content":"Check the service."},
		{"role":"assistant","content":[{"type":"thinking","signature":"OLD_SIGNATURE"},{"type":"text","text":"Timed out."}]},
		{"role":"user","content":"Try again."},
		{"role":"assistant","content":[{"type":"thinking","signature":"ACTIVE_FIRST"},{"type":"tool_use","id":"first","name":"check","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"first","content":"OK"}]},
		{"role":"assistant","content":[{"type":"thinking","signature":"ACTIVE_SECOND"},{"type":"tool_use","id":"second","name":"verify","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"second","content":"Healthy"}]}
	]}`)
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"messages.1.content.0", true},
		{"messages.1.content.1", false},
		{"messages.3.content.0", false},
		{"messages.5.content.0", false},
		{"messages.1.content.42", false},
		{"***.***.content.0", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			message := tc.path + ": Invalid `signature` in `thinking` block"
			rejection, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "param": 0}})
			recovered, ok := RecoverAnthropicThinking(payload, http.StatusBadRequest, rejection)
			if ok != tc.want {
				t.Fatalf("错误位置的恢复决定不符: %s", recovered)
			}
			if !ok {
				return
			}
			var before, after struct{ Messages []map[string]json.RawMessage }
			_ = json.Unmarshal(payload, &before)
			_ = json.Unmarshal(recovered, &after)
			if string(after.Messages[1]["content"]) != `[{"type":"text","text":"Timed out."}]` ||
				!reflect.DeepEqual(before.Messages[2:], after.Messages[2:]) {
				t.Fatalf("清理旧轮次时改变了正在执行的工具链: %s", recovered)
			}
		})
	}
}

// TestRecoverAnthropicThinkingCipher 验证兼容供应商回显的密文只定位到唯一已结束块。
// 前置旧签名、脱敏密文、当前工具轮及引用相同文本的工具结果；验证成功载荷和拒绝边界，无需清理。
func TestRecoverAnthropicThinkingCipher(t *testing.T) {
	const old = "eGh1Yi1zdGFsZS1zaWduYXR1cmUtb2xk"
	const active = "QUJDQUNUSVZFX1NJR05BVFVSRQ=="
	const redacted = "UkVEQUNURUQ="
	const payload = `{"messages":[
		{"role":"user","content":"Check the service."},
		{"role":"assistant","content":[{"type":"thinking","signature":"` + old + `"},{"type":"redacted_thinking","data":"` + redacted + `"},{"type":"text","text":"Timed out."}]},
		{"role":"user","content":"Try again."},
		{"role":"assistant","content":[{"type":"thinking","signature":"` + active + `"},{"type":"tool_use","id":"check_1","name":"check","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"check_1","content":"` + old + `"}]}
	]}`
	const errorPrefix = "The encrypted content "
	const errorSuffix = " could not be verified. Reason: Encrypted content could not be decrypted or parsed."
	for _, tc := range []struct {
		name, body, message, kind, param string
		want                             bool
	}{
		{"full old signature", payload, errorPrefix + old + errorSuffix, "invalid_request_error", "", true},
		{"abbreviated old signature", payload, errorPrefix + "eGh1...b2xk" + errorSuffix + " (request id: upstream-1)", "invalid_request_error", "", true},
		{"redacted thinking", payload, errorPrefix + redacted + errorSuffix, "invalid_request_error", "", true},
		{"active tool", payload, errorPrefix + active + errorSuffix, "invalid_request_error", "", false},
		{"collision with active tool", strings.Replace(payload, active, "eGh1OTHERb2xk", 1), errorPrefix + "eGh1...b2xk" + errorSuffix, "invalid_request_error", "", false},
		{"unknown cipher", payload, errorPrefix + "QUJD" + errorSuffix, "invalid_request_error", "", false},
		{"short abbreviation", payload, errorPrefix + "eGh...b2xk" + errorSuffix, "invalid_request_error", "", false},
		{"different case", payload, errorPrefix + "EGh1...b2xk" + errorSuffix, "invalid_request_error", "", false},
		{"quoted error", payload, "Invalid tool result: " + errorPrefix + old + errorSuffix, "invalid_request_error", "", false},
		{"different reason", payload, "The encrypted content " + old + " could not be verified. Reason: Invalid tool payload.", "invalid_request_error", "", false},
		{"different error type", payload, errorPrefix + old + errorSuffix, "authentication_error", "", false},
		{"unrelated param", payload, errorPrefix + old + errorSuffix, "invalid_request_error", "tools.0.input", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejection, _ := json.Marshal(map[string]any{"error": map[string]any{
				"type": tc.kind, "message": tc.message, "param": tc.param, "code": nil,
			}})
			got, ok := RecoverAnthropicThinking([]byte(tc.body), http.StatusBadRequest, rejection)
			if ok != tc.want {
				t.Fatalf("密文验签恢复决定不符: got %v want %v", ok, tc.want)
			}
			if !ok {
				return
			}
			var before, after struct{ Messages []map[string]json.RawMessage }
			_ = json.Unmarshal([]byte(tc.body), &before)
			_ = json.Unmarshal(got, &after)
			if string(after.Messages[1]["content"]) != `[{"type":"text","text":"Timed out."}]` ||
				!reflect.DeepEqual(before.Messages[2:], after.Messages[2:]) {
				t.Fatalf("密文恢复改变了当前工具轮次: %s", got)
			}
			if _, ok := RecoverAnthropicThinking(got, http.StatusBadRequest, rejection); ok {
				t.Fatal("清理后不得再根据工具结果里的字符串触发恢复")
			}
		})
	}
}

// TestRecoverAnthropicThinkingRefused 验证未结束工具轮次、错误状态和无安全清理目标时保持原请求。
// 参数 t 为测试上下文；前置内存协议样本；失败时输出用例名，无上游调用或测试数据需清理。
func TestRecoverAnthropicThinkingRefused(t *testing.T) {
	signed := `{"role":"assistant","content":[{"type":"thinking","signature":"old"},{"type":"text","text":"Ready."}]}`
	tool := `{"role":"assistant","content":[{"type":"thinking","signature":"old"},{"type":"tool_use","id":"tool_a","name":"check","input":{}}]}`
	user := `{"role":"user","content":"Continue."}`
	toolResult := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_a","content":"Done"}]}`
	for _, tc := range []struct {
		name     string
		messages string
		status   int
		error    string
	}{
		{"wrong status", signed + "," + user, 422, thinkingRejected},
		{"ordinary error", signed + "," + user, 400, `{"error":{"message":"Invalid tool result"}}`},
		{"quoted error", signed + "," + user, 400, "{\"error\":{\"message\":\"Invalid user input: Invalid `signature` in `thinking` block\"}}"},
		{"unstructured error", signed + "," + user, 400, `Invalid ` + "`signature` in `thinking` block"},
		{"pending tool result", tool + "," + toolResult, 400, thinkingRejected},
		{"merged user tool result", tool + "," + toolResult + "," + user, 400, thinkingRejected},
		{"unanswered tool", tool + "," + user, 400, thinkingRejected},
		{"split pending assistant", tool + "," + signed + "," + user, 400, thinkingRejected},
		{"assistant prefill", user + "," + signed, 400, thinkingRejected},
		{"only thinking", `{"role":"assistant","content":[{"type":"thinking","signature":"old"}]},` + user, 400, thinkingRejected},
		{"no thinking", `{"role":"assistant","content":"Ready."},` + user, 400, thinkingRejected},
		{"malformed block", signed + `,{"role":"user","content":[null]}`, 400, thinkingRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"messages":[` + tc.messages + `]}`)
			if got, ok := RecoverAnthropicThinking(body, tc.status, []byte(tc.error)); ok || got != nil {
				t.Fatalf("不可安全清理的请求生成了兼容载荷: %s", got)
			}
		})
	}
}
