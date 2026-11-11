// Package probe 验证保存的上游连接能否完整完成三种原生对话协议，不执行真实工具或记录对话正文。
package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sunqirui1987/xhub/internal/config"
	dptransport "github.com/sunqirui1987/xhub/internal/dataplane/transport"
	pc "github.com/sunqirui1987/xhub/internal/provider/contract"
	"github.com/sunqirui1987/xhub/internal/provider/registry"
)

// Result 是可持久化的安全诊断，只含固定状态、阶段、HTTP 状态与时间，不含上游正文或凭据。
type Result struct {
	Protocol   string `json:"protocol"`
	Status     string `json:"status"`
	Stage      string `json:"stage"`
	HTTPStatus int    `json:"http_status,omitempty"`
	CheckedAt  string `json:"checked_at"`
}

// Protocols 返回本轮支持验证的原生协议；调用方不能传入任意路径或扩大注册范围。
func Protocols() []string { return []string{"openai-chat", "openai-responses", "anthropic-messages"} }

// Transport 解析固定探测协议的注册传输；参数协议名，返回传输和存在标记；供探测及验证后的原生选路使用。
func Transport(protocol string) (pc.Transport, bool) {
	for _, p := range Protocols() {
		if protocol == p {
			for _, t := range registry.Transports() {
				if t.Protocol == p && t.EndpointID == "bypass:"+p {
					return t, true
				}
			}
		}
	}
	return pc.Transport{}, false
}

// Run 顺序执行普通回复、完整 SSE、工具调用与工具结果回传；ctx 控制整轮，client 使用现有网络传输，dep 必须已水合服务器凭据。
// 返回固定诊断；每步最多 30 秒、响应 1 MiB，禁止重定向泄露凭据。只生成英文测试数据，会产生供应商费用，不扣用户预算。
func Run(ctx context.Context, client *http.Client, dep config.ModelEntry, protocol string) Result {
	result := Result{Protocol: protocol, Status: "unverified", Stage: "configuration", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	tr, ok := Transport(protocol)
	if !ok || client == nil || dep.ParamString("api_base", "") == "" || dep.ParamString("api_key", "") == "" {
		return result
	}
	body := requestBody(protocol, dep.ParamString("model", ""))
	for _, stage := range []string{"reply", "stream", "tool_call", "tool_result"} {
		result.Stage = stage
		body["stream"] = stage == "stream"
		if stage == "tool_call" {
			addTool(body, protocol)
		}
		raw, contentType, status, failure := exchange(ctx, client, dep, tr, body)
		result.HTTPStatus = status
		if failure != "" {
			result.Status = failure
			return result
		}
		if stage == "stream" {
			if strings.Contains(contentType, "text/event-stream") && streamLimited(raw, protocol) {
				result.Status = "inconclusive"
				return result
			}
			if !strings.Contains(contentType, "text/event-stream") || !validStream(raw, protocol) {
				result.Status = "protocol_mismatch"
				return result
			}
			continue
		}
		var doc map[string]any
		if strings.Contains(contentType, "application/json") && json.Unmarshal(raw, &doc) == nil && limitedReply(doc, protocol) {
			result.Status = "inconclusive"
			return result
		}
		if !strings.Contains(contentType, "application/json") || json.Unmarshal(raw, &doc) != nil || !validReply(doc, protocol, stage == "tool_call") {
			result.Status = "protocol_mismatch"
			return result
		}
		if stage == "tool_call" && !toolResult(body, doc, protocol) {
			result.Status = "protocol_mismatch"
			return result
		}
	}
	result.Status = "verified"
	result.Stage = "complete"
	return result
}

// exchange 按固定注册路径发送请求，返回有界正文、类型、状态及安全失败分类；网络或读错误不返回底层地址/正文。
// 每步独立超时；关闭正文；非 2xx 和超限不可能被当作协议成功，临时错误不代表不支持。
func exchange(ctx context.Context, client *http.Client, dep config.ModelEntry, tr pc.Transport, body map[string]any) ([]byte, string, int, string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, _ := json.Marshal(body)
	address := dptransport.BypassDeploymentURL(dep.ParamString("api_base", ""), pc.Hit{Transport: tr, Action: tr.Actions[0]})
	req, err := http.NewRequestWithContext(ctx, "POST", address, bytes.NewReader(raw))
	if err != nil {
		return nil, "", 0, "configuration_error"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "XHub-Protocol-Probe/1")
	value := dep.ParamString("api_key", "")
	if tr.Auth.Prefix != "" {
		value = tr.Auth.Prefix + " " + value
	}
	req.Header.Set(tr.Auth.Header, value)
	for k, v := range tr.Headers {
		req.Header.Set(k, v)
	}
	local := *client
	local.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := local.Do(req)
	if err != nil {
		return nil, "", 0, "temporarily_failed"
	}
	defer res.Body.Close()
	status := res.StatusCode
	if status < 200 || status >= 300 {
		kind := "http_error"
		if status == 429 || status >= 500 {
			kind = "temporarily_failed"
		}
		return nil, "", status, kind
	}
	raw, err = io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return nil, "", status, "temporarily_failed"
	}
	if len(raw) > 1<<20 {
		return nil, "", status, "response_too_large"
	}
	return raw, res.Header.Get("Content-Type"), status, ""
}

// requestBody 为各协议生成有限输出的英文普通请求；参数协议与上游模型，返回独立正文，无用户内容。
func requestBody(protocol, model string) map[string]any {
	b := map[string]any{"model": model, "stream": false}
	if protocol == "openai-responses" {
		b["input"] = []any{map[string]any{"role": "user", "content": "Reply with PROBE_OK."}}
		b["max_output_tokens"] = 256
		b["store"] = false
		b["include"] = []string{"reasoning.encrypted_content"}
	} else {
		b["messages"] = []any{map[string]any{"role": "user", "content": "Reply with PROBE_OK."}}
		if protocol == "openai-chat" {
			b["max_completion_tokens"] = 256
		} else {
			b["max_tokens"] = 256
		}
	}
	return b
}

// addTool 将受控的 echo 工具和强制选择加入正文；只验证工具协议，不实际执行外部工具。
func addTool(b map[string]any, p string) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "enum": []string{"PROBE_OK"}}}, "required": []string{"value"}, "additionalProperties": false}
	fn := map[string]any{"name": "xhub_echo", "description": "Return the provided value.", "parameters": schema}
	prompt := []any{map[string]any{"role": "user", "content": "Call xhub_echo with value PROBE_OK, then repeat its result."}}
	switch p {
	case "openai-chat":
		b["messages"] = prompt
		b["tools"] = []any{map[string]any{"type": "function", "function": fn}}
		b["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "xhub_echo"}}
	case "openai-responses":
		b["input"] = prompt
		fn["type"] = "function"
		b["tools"] = []any{fn}
		b["tool_choice"] = map[string]any{"type": "function", "name": "xhub_echo"}
	case "anthropic-messages":
		b["messages"] = prompt
		b["tools"] = []any{map[string]any{"name": "xhub_echo", "description": "Return the provided value.", "input_schema": schema}}
		b["tool_choice"] = map[string]any{"type": "tool", "name": "xhub_echo"}
	}
}

// object/list/text 安全提取已解码协议字段；类型不符返回零值，由严格校验拒绝，不会 panic。
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func list(v any) []any            { a, _ := v.([]any); return a }
func text(v any) string           { s, _ := v.(string); return s }

// validReply 核验各原生协议的身份、完成状态及可见输出；工具阶段另由 toolResult 检查参数和关联 ID。
func validReply(d map[string]any, p string, tool bool) bool {
	if text(d["id"]) == "" || d["error"] != nil {
		return false
	}
	switch p {
	case "openai-chat":
		if d["object"] != "chat.completion" || len(list(d["choices"])) != 1 {
			return false
		}
		c := object(list(d["choices"])[0])
		m := object(c["message"])
		if m["role"] != "assistant" {
			return false
		}
		if tool {
			return (c["finish_reason"] == "tool_calls" || c["finish_reason"] == "stop") && len(list(m["tool_calls"])) > 0
		}
		return c["finish_reason"] == "stop" && strings.TrimSpace(text(m["content"])) != ""
	case "openai-responses":
		if d["object"] != "response" || d["status"] != "completed" {
			return false
		}
		if tool {
			return true
		}
		for _, v := range list(d["output"]) {
			m := object(v)
			if m["type"] == "message" && m["role"] == "assistant" {
				for _, v := range list(m["content"]) {
					c := object(v)
					if c["type"] == "output_text" && strings.TrimSpace(text(c["text"])) != "" {
						return true
					}
				}
			}
		}
	case "anthropic-messages":
		if d["type"] != "message" || d["role"] != "assistant" {
			return false
		}
		if tool {
			return d["stop_reason"] == "tool_use"
		}
		if d["stop_reason"] != "end_turn" {
			return false
		}
		for _, v := range list(d["content"]) {
			c := object(v)
			if c["type"] == "text" && strings.TrimSpace(text(c["text"])) != "" {
				return true
			}
		}
	}
	return false
}

// toolResult 校验 echo 工具名、非空关联 ID 及 JSON 参数，再原协议回传结果；保留 Responses 推理项和 Messages 内容顺序。
// 参数为下一轮正文、工具响应及协议；返回能否构造合法续轮；不接受任意工具或执行模型生成的命令。
func toolResult(b, d map[string]any, p string) bool {
	var calls []any
	switch p {
	case "openai-chat":
		calls = list(object(object(list(d["choices"])[0])["message"])["tool_calls"])
	case "openai-responses":
		for _, v := range list(d["output"]) {
			if object(v)["type"] == "function_call" {
				calls = append(calls, v)
			}
		}
	case "anthropic-messages":
		for _, v := range list(d["content"]) {
			if object(v)["type"] == "tool_use" {
				calls = append(calls, v)
			}
		}
	}
	if len(calls) != 1 {
		return false
	}
	call := object(calls[0])
	id := text(call["id"])
	name := text(call["name"])
	var args map[string]any
	if p == "anthropic-messages" {
		args = object(call["input"])
	} else {
		fn := call
		if p == "openai-chat" {
			if call["type"] != "function" {
				return false
			}
			fn = object(call["function"])
			name = text(fn["name"])
		} else {
			id = text(call["call_id"])
		}
		if json.Unmarshal([]byte(text(fn["arguments"])), &args) != nil {
			return false
		}
	}
	if id == "" || name != "xhub_echo" || len(args) != 1 || args["value"] != "PROBE_OK" {
		return false
	}
	delete(b, "tool_choice")
	switch p {
	case "openai-chat":
		b["messages"] = append(list(b["messages"]), object(list(d["choices"])[0])["message"], map[string]any{"role": "tool", "tool_call_id": id, "content": "PROBE_OK"})
	case "openai-responses":
		b["input"] = append(append(list(b["input"]), list(d["output"])...), map[string]any{"type": "function_call_output", "call_id": id, "output": "PROBE_OK"})
	case "anthropic-messages":
		b["messages"] = append(list(b["messages"]), map[string]any{"role": "assistant", "content": d["content"]}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "PROBE_OK"}}})
	}
	return true
}

// validStream 验证 SSE 完整帧、协议特定起止事件和非空文本；EOF、其他协议终态或错误事件不能代表成功。
// 输入已限长；只验证普通文本流，不将该结果外推为所有扩展事件或工具流均受支持。
func validStream(raw []byte, p string) bool {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data []string
	started, content, finished, terminal := false, false, false, false
	dispatch := func() bool {
		if len(data) == 0 {
			return true
		}
		s := strings.Join(data, "\n")
		data = nil
		if s == "[DONE]" {
			if p != "openai-chat" || !finished {
				return false
			}
			terminal = true
			return true
		}
		var d map[string]any
		if json.Unmarshal([]byte(s), &d) != nil || d["error"] != nil || terminal {
			return false
		}
		switch p {
		case "openai-chat":
			if d["object"] != "chat.completion.chunk" || text(d["id"]) == "" {
				return false
			}
			started = true
			for _, v := range list(d["choices"]) {
				c := object(v)
				delta := object(c["delta"])
				if strings.TrimSpace(text(delta["content"])) != "" {
					content = true
				}
				if c["finish_reason"] != nil {
					if c["finish_reason"] != "stop" {
						return false
					}
					finished = true
				}
			}
		case "openai-responses":
			switch d["type"] {
			case "response.created":
				started = object(d["response"])["object"] == "response"
			case "response.output_text.delta":
				if !started {
					return false
				}
				content = content || strings.TrimSpace(text(d["delta"])) != ""
			case "response.completed":
				terminal = validReply(object(d["response"]), p, false)
			case "error", "response.failed", "response.incomplete":
				return false
			}
		case "anthropic-messages":
			switch d["type"] {
			case "message_start":
				m := object(d["message"])
				started = m["type"] == "message" && m["role"] == "assistant" && text(m["id"]) != ""
			case "content_block_delta":
				if !started {
					return false
				}
				delta := object(d["delta"])
				content = content || (delta["type"] == "text_delta" && strings.TrimSpace(text(delta["text"])) != "")
			case "message_delta":
				finished = object(d["delta"])["stop_reason"] == "end_turn"
			case "message_stop":
				terminal = finished
			case "error":
				return false
			}
		}
		return true
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !dispatch() {
				return false
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return scanner.Err() == nil && len(data) == 0 && started && content && terminal
}

// limitedReply 识别探测输出预算耗尽，返回是否只能得出“不确定”；限制不足不代表协议不支持，也不自动增加费用重试。
// 参数为已解码响应和协议，纯读取；普通与流式探测共用，不把截断当成功。
func limitedReply(d map[string]any, p string) bool {
	switch p {
	case "openai-chat":
		for _, c := range list(d["choices"]) {
			if object(c)["finish_reason"] == "length" {
				return true
			}
		}
	case "openai-responses":
		return d["status"] == "incomplete" && object(d["incomplete_details"])["reason"] == "max_output_tokens"
	case "anthropic-messages":
		return d["stop_reason"] == "max_tokens"
	}
	return false
}

// streamLimited 从有界 SSE 中查找输出预算耗尽终态；参数原始流和协议，返回是否未能完成验证，无存储副作用。
func streamLimited(raw []byte, p string) bool {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var d map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &d) != nil {
			continue
		}
		if limitedReply(d, p) || (p == "openai-responses" && limitedReply(object(d["response"]), p)) || (p == "anthropic-messages" && limitedReply(object(d["delta"]), p)) {
			return true
		}
	}
	return false
}
