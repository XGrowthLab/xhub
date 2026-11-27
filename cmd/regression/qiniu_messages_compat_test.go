package regression

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestQiniuMessagesSparseIndices 验证真实网关的 Messages 标准流补齐序号、透传保持原文、Responses 转换完成。
// 参数 t 为上下文；使用隔离 PG、本地 Kimi 线形夹具和真实调用 Key，检查完整结果、精确扣费、失败字段和零扣费。
// harness 回收测试身份、账务和 schema，httptest 清理上游连接，无供应商用量或生产写入。
func TestQiniuMessagesSparseIndices(t *testing.T) {
	const fixture = `event: message_start
data: {"type":"message_start","message":{"id":"msg_qiniu_sparse","type":"message","role":"assistant","model":"vendor","content":[],"usage":{"input_tokens":8,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Check the format."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"synthetic-signature"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Complete"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || r.URL.Path != "/v1/messages" || !strings.Contains(string(body), `"stream":true`) {
			t.Errorf("请求未按预期送入本地 Messages SSE：path=%s err=%v", r.URL.Path, err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		wire := fixture
		if strings.Contains(string(body), "Use incorrect indices.") {
			wire = strings.Replace(wire, `"index":2,"delta"`, `"index":1,"delta"`, 1)
		}
		fmt.Fprint(w, wire)
	}))
	t.Cleanup(up.Close)
	dep := hardBudgetDeployment("qiniu-messages-sparse")
	dep.LiteLLMParams["api_base"] = up.URL
	dep.LiteLLMParams["custom_llm_provider"] = "anthropic"
	dep.ModelInfo["id"] = "qiniu-messages-sparse-deployment"
	dep.ModelInfo["transport"] = "bypass_anthropic_messages"
	dep.ModelInfo["endpoint_types"] = []string{"messages", "responses", "bypass:anthropic-messages", "chat", "gemini", "vertex"}
	dep.ModelInfo["messages_reasoning_mode"] = "adaptive"
	h := newHarness(t, dep)
	admin := h.adminSession()
	owner := h.openScope(t, admin, dep.ModelName)

	for _, tc := range []struct {
		name, path, prompt string
		invalid            bool
	}{
		{name: "standard", path: "/v1/messages", prompt: "Verify a complete flow."},
		{name: "bypass", path: "/bypass/anthropic/v1/messages", prompt: "Verify the upstream wire."},
		{name: "converted_responses", path: "/v1/responses", prompt: "Verify the converted result."},
		{name: "invalid_delta", path: "/v1/messages", prompt: "Use incorrect indices.", invalid: true},
		{name: "invalid_delta_converted", path: "/v1/responses", prompt: "Use incorrect indices.", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"model": dep.ModelName, "stream": true}
			if tc.path == "/v1/responses" {
				body["input"] = tc.prompt
				body["max_output_tokens"] = 64
			} else {
				body["messages"] = []any{map[string]any{"role": "user", "content": tc.prompt}}
				body["max_tokens"] = 64
			}
			beforeCalls, beforeMoney := calls.Load(), h.moneyOf(t, owner)
			reply := h.ok(http.MethodPost, tc.path, owner.key, body)
			if calls.Load() != beforeCalls+1 {
				t.Fatalf("一次调用不应重复请求上游：%d→%d", beforeCalls, calls.Load())
			}
			status, charge := "success", 8*testInputRate+2*testOutputRate
			if tc.invalid {
				status, charge = "error", 0
				if !strings.Contains(reply.text(), "content_block_delta.index 1 does not match open content block index 2") || strings.Contains(reply.text(), "message_stop") || strings.Contains(reply.text(), `"type":"response.completed"`) || strings.Contains(reply.text(), "Complete") {
					t.Fatalf("已经开流的坏响应未携带字段级错误或发布了成功终态：%s", reply.text())
				}
			} else {
				switch tc.name {
				case "standard":
					if strings.Contains(reply.text(), `"index":2`) || strings.Count(reply.text(), `"index":1`) != 3 || !strings.Contains(reply.text(), "synthetic-signature") || !strings.Contains(reply.text(), `"type":"message_stop"`) {
						t.Fatalf("标准 Messages 未完整补齐开始、增量、停止或签名：%s", reply.text())
					}
				case "bypass":
					if reply.text() != fixture {
						t.Fatalf("Messages Bypass 不应改写供应商序号、签名或终态：%s", reply.text())
					}
				case "converted_responses":
					if !strings.Contains(reply.text(), `"type":"response.completed"`) || strings.Contains(reply.text(), "synthetic-signature") {
						t.Fatalf("Responses 转换缺失终态或泄露上游签名：%s", reply.text())
					}
				}
				if !strings.Contains(reply.text(), "Complete") {
					t.Fatalf("可见正文丢失：%s", reply.text())
				}
			}
			if !h.moneyOf(t, owner).grewBy(beforeMoney, charge) {
				t.Fatalf("实际扣费与预期不符：状态=%s 费用=%g", status, charge)
			}
			log := logDetail(t, h, owner.session, reply.header("x-litellm-call-id"))
			if log["status"] != status || !nearlyEqual(parseFloatOrZero(fmt.Sprint(log["spend"])), charge) {
				t.Fatalf("账单没有准确记录流状态和费用：%v", log)
			}
		})
	}
	for _, tc := range []struct{ name, path string }{
		{"Chat", "/v1/chat/completions"},
		{"Gemini", "/v1beta/models/" + dep.ModelName + ":streamGenerateContent"},
		{"Vertex", "/vertex/v1/models/" + dep.ModelName + ":streamGenerateContent"},
	} {
		t.Run("unsupported_thinking_"+tc.name, func(t *testing.T) {
			body := map[string]any{
				"contents":         []any{map[string]any{"parts": []any{map[string]any{"text": "Check protocol diagnostics."}}}},
				"generationConfig": map[string]any{"maxOutputTokens": 64},
			}
			if tc.name == "Chat" {
				body = chatRequest(dep.ModelName, "Check protocol diagnostics.")
				body["stream"], body["max_tokens"] = true, 64
			}
			beforeCalls, beforeMoney := calls.Load(), h.moneyOf(t, owner)
			reply := h.do(http.MethodPost, tc.path, owner.key, body)
			clientError, _ := reply.json()["error"].(map[string]any)
			const reason = "unsupported_response_bridge"
			if reply.status != 502 || calls.Load() != beforeCalls+1 || clientError["reason"] != reason ||
				!strings.Contains(reply.text(), "Messages content_block.type thinking requires native Messages on this route") ||
				strings.Contains(reply.text(), "synthetic-signature") || strings.Contains(reply.text(), "Check the format.") {
				t.Fatalf("原厂思考的安全转换限制被误报或泄露：%s", reply.describe())
			}
			log := logDetail(t, h, owner.session, reply.header("x-litellm-call-id"))
			meta, _ := log["metadata"].(map[string]any)
			diag, _ := meta["upstream_response"].(map[string]any)
			failure, _ := diag["failure"].(map[string]any)
			if !h.moneyOf(t, owner).grewBy(beforeMoney, 0) || log["status"] != "error" || failure["kind"] != reason {
				t.Fatalf("不受支持的转换记费或分类错误：%v", log)
			}
		})
	}
}
