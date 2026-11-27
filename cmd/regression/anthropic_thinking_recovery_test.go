package regression

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// TestAnthropicBypassThinkingRecovery 覆盖真实网关的历史签名 400 和同部署补发。
// 参数 t 为回归上下文。隔离 PostgreSQL 和本地供应商验证实际载荷、原生同步/SSE、未完成
// 工具续轮及拒绝补发的错误和账单。harness 关闭服务并回收模型、用户与账单。
func TestAnthropicBypassThinkingRecovery(t *testing.T) {
	const signatureError = "messages.1.content.0: Invalid `signature` in `thinking` block"
	var calls atomic.Int32
	var mode atomic.Int32
	const (
		recoverHistory int32 = iota
		protectToolTurn
		keepActiveTool
		failRecovery
	)
	h, owner, admin, _ := historyHarness(t, "messages", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "local-key" ||
			string(body["thinking"]) != `{"type":"adaptive"}` || string(body["opaque"]) != "9007199254740993" {
			t.Errorf("上游路径、凭据或协议字段改变: %s, %s", r.URL.Path, mustJSON(body))
		}
		history := string(body["messages"])
		if strings.Contains(history, "OLD_SIGNATURE") {
			if mode.Load() != protectToolTurn && !strings.Contains(history, "OLD_REDACTED") {
				t.Error("首次出站已改写原厂历史")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"type": "error", "error": map[string]any{"type": "<nil>", "message": signatureError}})
			return
		}
		if strings.Contains(history, "OLD_REDACTED") || !strings.Contains(history, "tool_result") ||
			!strings.Contains(history, "tool_use") || !strings.Contains(history, "Keep this answer.") {
			t.Errorf("第 %d 次调用丢失正文或工具记录，或未清理旧 thinking: %s", n, history)
		}
		if mode.Load() == keepActiveTool && !strings.Contains(history, "ACTIVE_SIGNATURE") {
			t.Error("补发丢失了当前工具轮次的签名")
		}
		if mode.Load() == failRecovery {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": signatureError + " (second attempt)"}})
			return
		}
		historyMessagesReply(w, []any{
			map[string]any{"type": "thinking", "thinking": "A new plan.", "signature": "NEW_SIGNATURE"},
			map[string]any{"type": "text", "text": "Recovered."},
		}, string(body["stream"]) == "true")
	}))
	const endpoint = "/bypass/anthropic/v1/messages"
	const history = `{"role":"user","content":"Check the service."},
		{"role":"assistant","content":[{"type":"thinking","thinking":"Old plan","signature":"OLD_SIGNATURE"},{"type":"redacted_thinking","data":"OLD_REDACTED"},{"type":"tool_use","id":"check_1","name":"check","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"check_1","content":"Timed out"}]},
		{"role":"assistant","content":[{"type":"text","text":"Keep this answer."}]},
		{"role":"user","content":"Continue."}`
	const pendingTool = `{"role":"user","content":"Check the service."},
		{"role":"assistant","content":[{"type":"thinking","thinking":"Old plan","signature":"OLD_SIGNATURE"},{"type":"tool_use","id":"check_1","name":"check","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"check_1","content":"Timed out"}]}`
	const continuedTool = history + `,
		{"role":"assistant","content":[{"type":"thinking","thinking":"Continue","signature":"ACTIVE_SIGNATURE"},{"type":"tool_use","id":"check_2","name":"check","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"check_2","content":"Healthy"}]}`
	for _, tc := range []struct {
		name, messages string
		stream         bool
		mode           int32
		wantStatus     int
		wantCalls      int32
	}{
		{"sync", history, false, recoverHistory, 200, 2},
		{"sse", history, true, recoverHistory, 200, 2},
		{"active-tool", pendingTool, false, protectToolTurn, 400, 1},
		{"finish-new-tool", continuedTool, false, keepActiveTool, 200, 2},
		{"rejected-retry", history, false, failRecovery, 400, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode.Store(tc.mode)
			beforeCalls, beforeSpend := calls.Load(), h.moneyOf(t, owner)
			payload := fmt.Sprintf(`{"model":"history-model","max_tokens":256,"thinking":{"type":"adaptive"},"opaque":9007199254740993,"stream":%t,"messages":[%s]}`, tc.stream, tc.messages)
			got := h.doRaw("POST", endpoint, owner.key, "application/json", payload)
			if got.status != tc.wantStatus || calls.Load() != beforeCalls+tc.wantCalls {
				t.Fatalf("出站次数或状态不符，调用 %d 次: %s", calls.Load()-beforeCalls, got.describe())
			}
			wantSpend, wantLog := 0.0, "error"
			if tc.wantStatus == 200 {
				blocks := historyReadMessages(t, got, tc.stream)
				if want := []any{
					map[string]any{"type": "thinking", "thinking": "A new plan.", "signature": "NEW_SIGNATURE"},
					map[string]any{"type": "text", "text": "Recovered."},
				}; !reflect.DeepEqual(blocks, want) {
					t.Fatalf("新模型的签名或回复丢失: %v", blocks)
				}
				wantSpend, wantLog = 8*testInputRate+2*testOutputRate, "success"
			} else if !strings.Contains(got.text(), signatureError) ||
				strings.Contains(got.text(), "second attempt") != (tc.mode == failRecovery) {
				t.Fatalf("上游错误详情未保留: %s", got.describe())
			}
			if !h.moneyOf(t, owner).grewBy(beforeSpend, wantSpend) {
				t.Fatalf("余额应只扣最终成功用量 %g", wantSpend)
			}
			h.flushSpend()
			log := h.ok("GET", "/spend/logs/ui/"+got.header("x-litellm-call-id"), admin, nil).json()
			actualSpend, _ := floatField(log, "spend")
			if log["status"] != wantLog || !nearlyEqual(actualSpend, wantSpend) {
				t.Fatalf("最终日志与扣费不符: %v", log)
			}
		})
	}
}

// TestAnthropicCipherRecoveryAcrossEntrypoints 验证真实两个 Messages 入口处理兼容上游的密文验签错误。
// 输入 t 为回归上下文。前置隔离数据库和本地供应商，验证当前工具轮保留、补发次数及最终账单。
// harness 退出时关闭供应商、网关和数据库会话，并回收其 schema。
func TestAnthropicCipherRecoveryAcrossEntrypoints(t *testing.T) {
	const old = "eGh1Yi1zdGFsZS1zaWduYXR1cmUtb2xk"
	const current = "QUJDQUNUSVZFX1NJR05BVFVSRQ=="
	const history = `[{"role":"user","content":"Check the service."},
		{"role":"assistant","content":[{"type":"thinking","thinking":"Old plan","signature":"` + old + `"},{"type":"text","text":"Keep this answer."}]},
		{"role":"user","content":"Try again."},
		{"role":"assistant","content":[{"type":"thinking","thinking":"Check again","signature":"` + current + `"},{"type":"tool_use","id":"check_1","name":"check","input":{"name":"db"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"check_1","content":"Healthy."}]}]`
	for _, endpoint := range []string{"/bypass/anthropic/v1/messages", "/v1/messages"} {
		t.Run(endpoint, func(t *testing.T) {
			type scenario struct {
				name, cipher  string
				stream, fail  bool
				status, calls int
			}
			var selected atomic.Pointer[scenario]
			outbound := make(chan string, 8)
			h, owner, admin, _ := historyHarness(t, "messages", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages json.RawMessage
					Stream   bool
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("无法解析出站正文: %v", err)
					w.WriteHeader(422)
					return
				}
				if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "local-key" {
					t.Errorf("补发必须沿用原有上游路径和凭据: %s", r.URL.Path)
				}
				messages := string(body.Messages)
				outbound <- messages
				currentCase := selected.Load()
				if strings.Contains(messages, old) || currentCase.fail {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					message := fmt.Sprintf("The encrypted content %s could not be verified. Reason: Encrypted content could not be decrypted or parsed.", currentCase.cipher)
					writeJSON(w, map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": message, "code": nil}})
					return
				}
				historyMessagesReply(w, []any{map[string]any{"type": "text", "text": "Recovered."}}, body.Stream)
			}))
			for _, tc := range []scenario{
				{"completed cipher", "eGh1...b2xk", false, false, 200, 2},
				{"completed cipher SSE", old, true, false, 200, 2},
				{"active tool cipher", current, false, false, 400, 1},
				{"unknown cipher", "Q2lwaGVy", false, false, 400, 1},
				{"second rejection", old, false, true, 400, 2},
			} {
				t.Run(tc.name, func(t *testing.T) {
					selected.Store(&tc)
					balance := h.moneyOf(t, owner)
					// 标准入口会缓存成功正文，使用各用例自己的用户消息以观察真实上游尝试。
					messages := strings.Replace(history, "Try again.", "Try again. Scenario: "+tc.name, 1)
					payload := fmt.Sprintf(`{"model":"history-model","max_tokens":128,"thinking":{"type":"adaptive"},"stream":%t,"messages":%s}`, tc.stream, messages)
					got := h.doRaw("POST", endpoint, owner.key, "application/json", payload)
					if got.status != tc.status || len(outbound) != tc.calls {
						t.Fatalf("出站次数或状态不符，调用 %d 次: %s", len(outbound), got.describe())
					}
					var original, expected, retry []any
					if json.Unmarshal([]byte(<-outbound), &original) != nil || json.Unmarshal([]byte(messages), &expected) != nil ||
						!reflect.DeepEqual(original, expected) {
						t.Fatalf("首发改写了请求历史: %v", original)
					}
					if tc.calls == 2 {
						if json.Unmarshal([]byte(<-outbound), &retry) != nil || len(retry) != len(original) ||
							!reflect.DeepEqual(original[2:], retry[2:]) || strings.Contains(string(mustJSON(retry)), old) ||
							!strings.Contains(string(mustJSON(retry)), "Keep this answer.") {
							t.Fatalf("补发改写了当前工具轮次或正文: %v", retry)
						}
					}
					wantSpend, wantLog := 0.0, "error"
					if tc.status == 200 {
						if !strings.Contains(got.text(), "Recovered.") {
							t.Fatalf("客户端没有收到补发的真实回复: %s", got.describe())
						}
						wantSpend, wantLog = 8*testInputRate+2*testOutputRate, "success"
					} else if !strings.Contains(got.text(), "Encrypted content could not be decrypted or parsed.") {
						t.Fatalf("原始错误没有返回客户端: %s", got.describe())
					}
					if !h.moneyOf(t, owner).grewBy(balance, wantSpend) {
						t.Fatalf("不应对失败尝试扣费，预期总费 %g", wantSpend)
					}
					h.flushSpend()
					log := h.ok("GET", "/spend/logs/ui/"+got.header("x-litellm-call-id"), admin, nil).json()
					actualSpend, _ := floatField(log, "spend")
					if log["status"] != wantLog || !nearlyEqual(actualSpend, wantSpend) {
						t.Fatalf("最终调用日志和计费不符: %v", log)
					}
				})
			}
		})
	}
}
