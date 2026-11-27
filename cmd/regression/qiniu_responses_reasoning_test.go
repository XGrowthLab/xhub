package regression

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// TestQiniuResponsesReasoningCompatibility 验证真实网关使用 Responses 上游时三种入口的同步和 SSE 回复。
// 参数 t 为测试上下文；本地上游发送七牛推理格式，校验正文、去重、终态、费用及精确失败。harness 清理数据库和服务。
func TestQiniuResponsesReasoningCompatibility(t *testing.T) {
	var mode atomic.Int32
	h, owner, admin, id := historyHarness(t, "responses", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer local-key" {
			t.Error("请求未使用预期的 Responses 上游")
		}
		if mode.Load() == 3 {
			items, ok := body["input"].([]any)
			if !ok || len(items) != 4 || body["previous_response_id"] != nil || !strings.Contains(string(mustJSON(items)), "reasoning_text") {
				t.Error("续轮没有用已授权的实际推理历史重建输入")
			}
		}
		if mode.Load() == 2 && body["stream"] != true {
			writeJSON(w, map[string]any{
				"error":      map[string]any{"code": "qiniu_fixture", "message": "Vendor rejected the test."},
				"request_id": "qiniu-local",
			})
			return
		}
		text := any("Synthetic plan.")
		if mode.Load() == 1 || mode.Load() == 4 {
			text = 42
		}
		result := nativeAgentResponse("resp_qiniu", "Expected answer.", false, nil)
		message := result["output"].([]any)[0].(map[string]any)
		message["content"] = append(message["content"].([]any), map[string]any{"type": "reasoning_text", "text": text})
		result["output"] = []any{
			map[string]any{"id": "rs_qiniu", "type": "reasoning", "content": []any{map[string]any{"type": "reasoning_text", "text": "Synthetic plan."}}},
			message,
		}
		if body["stream"] != true {
			writeJSON(w, result)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if mode.Load() == 2 {
			fmt.Fprint(w, "data: {\"type\":\"error\",\"code\":\"qiniu_fixture\",\"message\":\"Vendor rejected the test.\"}\n\n")
			return
		}
		if mode.Load() == 4 {
			fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", mustJSON(result))
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_qiniu\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"Synthetic plan.\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Expected answer.\"}\n\n")
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", mustJSON(result))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	h.ok("POST", "/model/update", admin, map[string]any{
		"id": id, "model_info": map[string]any{"transport": "bypass_openai_responses", "endpoint_types": []string{"chat", "messages", "responses"}},
	})

	for _, tc := range []struct{ path, finished string }{
		{"/v1/responses", `"type":"response.completed"`},
		{"/v1/chat/completions", "[DONE]"},
		{"/v1/messages", `"type":"message_stop"`},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.path, stream), func(t *testing.T) {
				body := map[string]any{"model": "history-model", "stream": stream}
				if tc.path == "/v1/responses" {
					body["input"] = "Say hello."
				} else {
					body["messages"] = []any{map[string]any{"role": "user", "content": "Say hello."}}
					body["max_tokens"] = 16
				}
				got := h.do("POST", tc.path, owner.key, body)
				if got.status != 200 || !strings.Contains(got.text(), "Expected answer.") || stream && !strings.Contains(got.text(), tc.finished) {
					t.Fatalf("七牛推理格式导致正文或终态丢失: %s", got.describe())
				}
				if tc.path != "/v1/responses" && strings.Count(got.text(), "Synthetic plan.") != 1 {
					t.Fatalf("跨协议公开推理丢失或重复: %s", got.describe())
				}
				h.flushSpend()
				log := h.ok("GET", "/spend/logs/ui/"+got.header("x-litellm-call-id"), admin, nil).json()
				if log["status"] != "success" || asIntAny(log["total_tokens"]) != 10 {
					t.Fatalf("成功请求用量错误: %v", log)
				}
				if tc.path == "/v1/responses" && !stream {
					mode.Store(3)
					next := h.do("POST", tc.path, owner.key, map[string]any{
						"model": "history-model", "input": "Continue.", "previous_response_id": got.json()["id"],
					})
					mode.Store(0)
					if next.status != 200 || !strings.Contains(next.text(), "Expected answer.") {
						t.Fatalf("七牛混合推理结果不能继续对话: %s", next.describe())
					}
				}
			})
		}
	}
	body := map[string]any{"model": "history-model", "input": "Say hello.", "stream": true}
	mode.Store(1)
	got := h.do("POST", "/v1/responses", owner.key, body)
	if got.status != 200 || !strings.Contains(got.text(), "response.failed") ||
		!strings.Contains(got.text(), "output[1].content[1]: reasoning_text.text") || strings.Contains(got.text(), "response.completed") {
		t.Fatalf("非法推理字段被吞掉或返回成功终态: %s", got.describe())
	}
	h.flushSpend()
	log := h.ok("GET", "/spend/logs/ui/"+got.header("x-litellm-call-id"), admin, nil).json()
	if log["status"] != "error" || log["spend"] != float64(0) {
		t.Fatalf("推理字段失败应零扣费: %v", log)
	}

	mode.Store(2)
	got = h.do("POST", "/v1/responses", owner.key, body)
	if got.status != 502 || !strings.Contains(got.text(), "qiniu_fixture") || !strings.Contains(got.text(), "Vendor rejected the test.") {
		t.Fatalf("供应商错误详情未保留: %s", got.describe())
	}
	body["stream"] = false
	got = h.do("POST", "/v1/responses", owner.key, body)
	if got.status != 502 || !strings.Contains(got.text(), "qiniu_fixture") || !strings.Contains(got.text(), "qiniu-local") {
		t.Fatalf("HTTP 200 错误对象未保留供应商代码和请求 ID: %s", got.describe())
	}
	mode.Store(4)
	body["stream"] = true
	got = h.do("POST", "/v1/responses", owner.key, body)
	if got.status != 502 || !strings.Contains(got.text(), "output[1].content[1]: reasoning_text.text") ||
		!strings.Contains(got.text(), "upstream_invalid_response") {
		t.Fatalf("首帧协议错误被归为网络断流或丢失字段路径: %s", got.describe())
	}
}
