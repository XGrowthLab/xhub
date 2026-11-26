package regression

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// TestDialogueHistoryRecoveryAcrossEntrypoints 验证统一 Messages/Chat 与 OpenAI 原生透传
// 各自的历史兼容。参数 t 为回归上下文。前置真实 HTTP、PostgreSQL 与本地严格上游；
// 验证同步和流式、真实出站次数、原始错误、单次扣费及服务端归属。harness 回收隔离资源。
func TestDialogueHistoryRecoveryAcrossEntrypoints(t *testing.T) {
	const chat = `"messages":[{"role":"user","content":"Read the service."},{"role":"assistant","content":"Keep the answer.","reasoning_content":"Public thought","reasoning_items":[{"type":"reasoning","id":"rs_old","summary":[{"type":"summary_text","text":"Public thought"}],"encrypted_content":"OLD_CIPHER"}]},{"role":"user","content":"Continue."}]`
	const responses = `"input":[{"role":"user","content":"Read the service."},{"type":"reasoning","id":"rs_old","summary":[],"encrypted_content":"OLD_CIPHER"},{"role":"assistant","content":"Keep the answer."},{"role":"user","content":"Continue."}]`
	const messages = `"messages":[{"role":"user","content":"Read the service."},{"role":"assistant","content":[{"type":"thinking","thinking":"Check","signature":"OLD_CIPHER"},{"type":"text","text":"Keep the answer."}]},{"role":"user","content":"Continue."}]`
	for _, tc := range []struct {
		name, path, upstream, transport, endpoint, input string
		wantCalls                                        int32
	}{
		{"messages-standard", "/v1/messages", "/v1/messages", "bypass_anthropic_messages", "messages", messages, 2},
		{"chat-standard", "/v1/chat/completions", "/v1/chat/completions", "bypass_openai_chat", "chat", chat, 1},
		{"chat-bypass", "/bypass/openai/v1/chat/completions", "/v1/chat/completions", "bypass_openai_chat", "chat", chat, 2},
		{"responses-bypass", "/bypass/openai/v1/responses", "/v1/responses", "bypass_openai_responses", "responses", responses, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			h, owner, admin, id := historyHarness(t, "messages", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var input map[string]any
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				payload := string(mustJSON(input))
				if r.URL.Path != tc.upstream || !strings.Contains(payload, "Keep the answer.") || !strings.Contains(payload, "Continue.") {
					t.Errorf("执行协议或公开历史改变: %s %s", r.URL.Path, payload)
				}
				if strings.Contains(payload, "OLD_CIPHER") {
					code, message := "invalid_encrypted_content", "The encrypted content for item rs_old could not be verified."
					if tc.endpoint == "messages" {
						code, message = "invalid_request_error", "messages.1.content.0: Invalid `signature` in `thinking` block"
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(400)
					writeJSON(w, map[string]any{"error": map[string]any{"code": code, "message": message}})
					return
				}
				stream := input["stream"] == true
				switch tc.endpoint {
				case "messages":
					historyMessagesReply(w, []any{map[string]any{"type": "text", "text": "Recovered."}}, stream)
				case "chat":
					if !strings.Contains(payload, "Public thought") {
						t.Error("Chat 公开摘要在迁移时丢失")
					}
					writeChatAgentResponse(w, "chat_recovered", "Recovered.", "", "", stream)
				case "responses":
					writeAgentResponse(w, nativeAgentResponse("resp_recovered", "Recovered.", false, nil), stream)
				}
			}))
			h.ok("POST", "/model/update", admin, map[string]any{"id": id, "model_info": map[string]any{"transport": tc.transport, "endpoint_types": []string{tc.endpoint}}})
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
					before, balance := calls.Load(), h.moneyOf(t, owner)
					got := h.doRaw("POST", tc.path, owner.key, "application/json",
						fmt.Sprintf(`{"model":"history-model","stream":%t,"max_tokens":128,%s}`, stream, tc.input))
					if got.status != 200 || !strings.Contains(got.text(), "Recovered.") || calls.Load() != before+tc.wantCalls {
						t.Fatalf("历史恢复状态或尝试次数错误，出站 %d 次: %s", calls.Load()-before, got.describe())
					}
					expectedSpend := 8*testInputRate + 2*testOutputRate
					if !h.moneyOf(t, owner).grewBy(balance, expectedSpend) {
						t.Fatalf("只应扣取成功尝试的费用 %g", expectedSpend)
					}
					h.flushSpend()
					log := h.ok("GET", "/spend/logs/ui/"+got.header("x-litellm-call-id"), admin, nil).json()
					if amount, _ := floatField(log, "spend"); log["status"] != "success" || !nearlyEqual(amount, expectedSpend) {
						t.Fatalf("最终成功日志或费用错误: %v", log)
					}
				})
			}
			if tc.endpoint == "responses" {
				before, balance := calls.Load(), h.moneyOf(t, owner)
				compact := strings.Replace(tc.input, `"type":"reasoning"`, `"type":"compaction"`, 1)
				got := h.doRaw("POST", tc.path, owner.key, "application/json", `{"model":"history-model",`+compact+`}`)
				if got.status != 400 || !strings.Contains(got.text(), "invalid_encrypted_content") ||
					calls.Load() != before+1 || !h.moneyOf(t, owner).grewBy(balance, 0) {
					t.Fatalf("压缩历史不得被丢弃或重试: %s", got.describe())
				}
			}
		})
	}
}
