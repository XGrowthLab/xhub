// Package protocolfixture 提供隔离测试用的严格协议 HTTP 上游；不会访问商业服务。
package protocolfixture

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Reply 验证请求协议、流标记及工具关联，再输出完整原生响应；返回 nil 表示请求不满足契约。
// p 为协议，b 为实际 HTTP 正文；用于单元及真实后台 regression，不返回用户正文或真实凭据。
func Reply(p string, b map[string]any) map[string]any {
	tool := b["tool_choice"] != nil
	follow := false
	input := b["messages"]
	if p == "openai-responses" {
		input = b["input"]
	}
	turns, ok := input.([]any)
	if !ok || len(turns) == 0 || b["model"] == nil {
		return nil
	}
	if len(turns) > 1 {
		raw, _ := json.Marshal(turns)
		follow = strings.Contains(string(raw), "probe-call") && strings.Contains(string(raw), "PROBE_OK")
		if !follow {
			return nil
		}
	}
	if tool && b["tools"] == nil {
		return nil
	}
	usage := map[string]any{"input_tokens": 8, "output_tokens": 2}
	content := []any{map[string]any{"type": "text", "text": "PROBE_OK"}}
	switch p {
	case "openai-chat":
		m := map[string]any{"role": "assistant", "content": "PROBE_OK"}
		stop := "stop"
		if tool {
			m["content"] = nil
			m["tool_calls"] = []any{map[string]any{"id": "probe-call", "type": "function", "function": map[string]any{"name": "xhub_echo", "arguments": `{"value":"PROBE_OK"}`}}}
			stop = "tool_calls"
		}
		return map[string]any{"id": "chatcmpl-probe", "object": "chat.completion", "model": b["model"], "choices": []any{map[string]any{"index": 0, "message": m, "finish_reason": stop}}, "usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10}}
	case "openai-responses":
		output := []any{map[string]any{"type": "message", "id": "msg-probe", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "PROBE_OK", "annotations": []any{}}}}}
		if tool {
			output = []any{map[string]any{"type": "function_call", "id": "fc-probe", "call_id": "probe-call", "name": "xhub_echo", "arguments": `{"value":"PROBE_OK"}`, "status": "completed"}}
		}
		return map[string]any{"id": "resp-probe", "object": "response", "status": "completed", "model": b["model"], "output": output, "usage": usage}
	case "anthropic-messages":
		if b["max_tokens"] == nil {
			return nil
		}
		stop := "end_turn"
		if tool {
			content = []any{map[string]any{"type": "tool_use", "id": "probe-call", "name": "xhub_echo", "input": map[string]any{"value": "PROBE_OK"}}}
			stop = "tool_use"
		}
		return map[string]any{"id": "msg-probe", "type": "message", "role": "assistant", "model": b["model"], "content": content, "stop_reason": stop, "usage": usage}
	}
	return nil
}

// Stream 生成协议对应的完整文本 SSE，含首事件、文本、完成标志和用量；参数为协议及普通完整响应，无外部副作用。
func Stream(p string, reply map[string]any) string {
	frame := func(d any) string { b, _ := json.Marshal(d); return "data: " + string(b) + "\n\n" }
	switch p {
	case "openai-chat":
		return frame(map[string]any{"id": "chatcmpl-probe", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "PROBE_OK"}, "finish_reason": nil}}}) + frame(map[string]any{"id": "chatcmpl-probe", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": reply["usage"]}) + "data: [DONE]\n\n"
	case "openai-responses":
		return frame(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-probe", "object": "response", "status": "in_progress"}}) + frame(map[string]any{"type": "response.output_text.delta", "delta": "PROBE_OK"}) + frame(map[string]any{"type": "response.completed", "response": reply})
	default:
		return frame(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg-probe", "type": "message", "role": "assistant", "content": []any{}, "usage": map[string]any{"input_tokens": 8, "output_tokens": 0}}}) + frame(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}) + frame(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "PROBE_OK"}}) + frame(map[string]any{"type": "content_block_stop", "index": 0}) + frame(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}}) + frame(map[string]any{"type": "message_stop"})
	}
}

// Serve 校验本地测试 Key、协议路径与真实正文，然后写响应；可直接挂到 httptest.Server，调用方负责关闭服务器。
func Serve(w http.ResponseWriter, r *http.Request) {
	p := map[string]string{"/v1/chat/completions": "openai-chat", "/v1/responses": "openai-responses", "/v1/messages": "anthropic-messages"}[r.URL.Path]
	if p == "" {
		w.WriteHeader(404)
		return
	}
	if p == "anthropic-messages" {
		if r.Header.Get("x-api-key") != "fixture-protocol-key" || r.Header.Get("anthropic-version") == "" {
			w.WriteHeader(401)
			return
		}
	} else if r.Header.Get("Authorization") != "Bearer fixture-protocol-key" {
		w.WriteHeader(401)
		return
	}
	var b map[string]any
	if json.NewDecoder(r.Body).Decode(&b) != nil {
		w.WriteHeader(400)
		return
	}
	reply := Reply(p, b)
	if reply == nil {
		w.WriteHeader(400)
		return
	}
	if b["stream"] == true {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, Stream(p, reply))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reply)
}
