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
