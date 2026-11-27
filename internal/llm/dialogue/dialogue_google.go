package dialogue

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// parseGoogleDialogue 把 Gemini/Vertex 正文转为共享文本和函数调用。
// 参数为网关已注入别名的正文；返回已验证对话或能力错误；统一入口调用，不丢弃未知多模态字段。
func parseGoogleDialogue(body map[string]any) (Dialogue, error) {
	if err := dialogueFields(body, "model", "stream", "contents", "systemInstruction", "generationConfig", "tools", "toolConfig", "litellm_session_id"); err != nil {
		return Dialogue{}, err
	}
	chat := map[string]any{"model": body["model"]}
	if v, ok := body["stream"]; ok {
		chat["stream"] = v
	}
	messages := []any{}
	if sys, ok := body["systemInstruction"]; ok {
		m := dialogueMap(sys)
		if m == nil {
			return Dialogue{}, fmt.Errorf("systemInstruction must be object")
		}
		if err := dialogueFields(m, "role", "parts"); err != nil {
			return Dialogue{}, err
		}
		text, err := googleText(m["parts"])
		if err != nil {
			return Dialogue{}, err
		}
		messages = append(messages, map[string]any{"role": "system", "content": text})
	}
	names := map[string][]string{}
	used := map[string]bool{}
	contents, ok := body["contents"].([]any)
	if !ok || len(contents) == 0 {
		return Dialogue{}, fmt.Errorf("nonempty contents is required")
	}
	for turnIndex, raw := range contents {
		c := dialogueMap(raw)
		if err := dialogueFields(c, "role", "parts"); err != nil {
			return Dialogue{}, err
		}
		role := dialogueString(c["role"])
		if role == "" {
			role = "user"
		}
		if role == "model" {
			role = "assistant"
		}
		if role != "user" && role != "assistant" {
			return Dialogue{}, fmt.Errorf("invalid Google content role")
		}
		parts, ok := c["parts"].([]any)
		if !ok || len(parts) == 0 {
			return Dialogue{}, fmt.Errorf("nonempty Google parts required")
		}
		text := ""
		calls := []any{}
		for i, raw := range parts {
			p := dialogueMap(raw)
			if len(p) != 1 {
				return Dialogue{}, fmt.Errorf("unsupported Google part")
			}
			switch {
			case p["text"] != nil:
				s, ok := p["text"].(string)
				if !ok {
					return Dialogue{}, fmt.Errorf("Google text must be string")
				}
				text += s
			case p["functionCall"] != nil:
				f := dialogueMap(p["functionCall"])
				if err := dialogueFields(f, "id", "name", "args"); err != nil {
					return Dialogue{}, err
				}
				if role != "assistant" {
					return Dialogue{}, fmt.Errorf("functionCall requires model role")
				}
				if _, ok := f["args"].(map[string]any); !ok {
					return Dialogue{}, fmt.Errorf("function args must be object")
				}
				id := dialogueString(f["id"])
				name := dialogueString(f["name"])
				if id == "" {
					id = fmt.Sprintf("google_%s_%d_%d", name, turnIndex, i)
				}
				if name == "" || used[id] {
					return Dialogue{}, fmt.Errorf("missing function name or duplicate function ID")
				}
				used[id] = true
				names[name] = append(names[name], id)
				args, _ := json.Marshal(f["args"])
				calls = append(calls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}})
			case p["functionResponse"] != nil:
				f := dialogueMap(p["functionResponse"])
				if err := dialogueFields(f, "id", "name", "response"); err != nil {
					return Dialogue{}, err
				}
				if role != "user" {
					return Dialogue{}, fmt.Errorf("functionResponse requires user role")
				}
				if _, ok := f["response"].(map[string]any); !ok {
					return Dialogue{}, fmt.Errorf("function response must be object")
				}
				id := dialogueString(f["id"])
				if id == "" {
					matches := names[dialogueString(f["name"])]
					if len(matches) != 1 {
						return Dialogue{}, fmt.Errorf("ambiguous Google function response requires explicit ID")
					}
					id = matches[0]
				}
				name := dialogueString(f["name"])
				matched := false
				for i, candidate := range names[name] {
					if candidate == id {
						names[name] = append(names[name][:i], names[name][i+1:]...)
						matched = true
						break
					}
				}
				if !matched {
					return Dialogue{}, fmt.Errorf("Google function response has no pending matching call")
				}
				response, _ := json.Marshal(f["response"])
				messages = append(messages, map[string]any{"role": "tool", "tool_call_id": id, "content": string(response)})
			default:
				return Dialogue{}, fmt.Errorf("unsupported Google content capability")
			}
		}
		if text != "" || len(calls) > 0 {
			m := map[string]any{"role": role, "content": text}
			if len(calls) > 0 {
				m["tool_calls"] = calls
			}
			messages = append(messages, m)
		}
	}
	chat["messages"] = messages
	if raw, exists := body["generationConfig"]; exists {
		cfg := dialogueMap(raw)
		if cfg == nil {
			return Dialogue{}, fmt.Errorf("Google configuration must be object")
		}
		if err := dialogueFields(cfg, "temperature", "topP", "maxOutputTokens", "candidateCount"); err != nil {
			return Dialogue{}, err
		}
		if v, ok := cfg["candidateCount"]; ok {
			if n, valid := dialogueNumber(v); !valid || n != 1 {
				return Dialogue{}, fmt.Errorf("only one Google candidate supported")
			}
		}
		for source, target := range map[string]string{"temperature": "temperature", "topP": "top_p", "maxOutputTokens": "max_tokens"} {
			if v, ok := cfg[source]; ok {
				chat[target] = v
			}
		}
	}
	tools := []any{}
	if raw, exists := body["tools"]; exists {
		groups, ok := raw.([]any)
		if !ok {
			return Dialogue{}, fmt.Errorf("tools must be array")
		}
		for _, raw := range groups {
			group := dialogueMap(raw)
			if group == nil {
				return Dialogue{}, fmt.Errorf("Google tool must be object")
			}
			if err := dialogueFields(group, "functionDeclarations"); err != nil {
				return Dialogue{}, err
			}
			decls, ok := group["functionDeclarations"].([]any)
			if !ok {
				return Dialogue{}, fmt.Errorf("functionDeclarations must be array")
			}
			for _, raw := range decls {
				f := dialogueMap(raw)
				if err := dialogueFields(f, "name", "description", "parameters"); err != nil {
					return Dialogue{}, err
				}
				tools = append(tools, map[string]any{"type": "function", "function": f})
			}
		}
	}
	if len(tools) > 0 {
		chat["tools"] = tools
	}
	if raw, exists := body["toolConfig"]; exists {
		cfg := dialogueMap(raw)
		if cfg == nil {
			return Dialogue{}, fmt.Errorf("Google configuration must be object")
		}
		if err := dialogueFields(cfg, "functionCallingConfig"); err != nil {
			return Dialogue{}, err
		}
		f := dialogueMap(cfg["functionCallingConfig"])
		if f == nil {
			return Dialogue{}, fmt.Errorf("functionCallingConfig must be object")
		}
		if err := dialogueFields(f, "mode", "allowedFunctionNames"); err != nil {
			return Dialogue{}, err
		}
		mode := dialogueString(f["mode"])
		choice, ok := map[string]string{"AUTO": "auto", "ANY": "required", "NONE": "none"}[mode]
		if !ok {
			return Dialogue{}, fmt.Errorf("unsupported Google function mode")
		}
		if raw, exists := f["allowedFunctionNames"]; exists {
			names := dialogueList(raw)
			if len(names) != 1 || mode != "ANY" {
				return Dialogue{}, fmt.Errorf("only one forced function supported")
			}
			chat["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": names[0]}}
		} else {
			chat["tool_choice"] = choice
		}
	}
	return ParseDialogue("openai-chat", chat)
}

// googleText 读取纯文本 parts；参数为动态数组，返回拼接文字或明确格式错误；系统提示解析调用，无副作用。
func googleText(raw any) (string, error) {
	parts, ok := raw.([]any)
	if !ok {
		return "", fmt.Errorf("Google parts must be array")
	}
	text := ""
	for _, raw := range parts {
		p := dialogueMap(raw)
		if err := dialogueFields(p, "text"); err != nil {
			return "", err
		}
		s, ok := p["text"].(string)
		if !ok {
			return "", fmt.Errorf("Google text must be string")
		}
		text += s
	}
	return text, nil
}

// encodeGoogleDialogue 按 Google 原厂形状编码共享对话；参数为对话，返回正文或不可表达能力错误。
// 上游执行调用；不写 model/stream，模型和流操作由注册路径表达，工具结果按调用 ID 找原名称。
func encodeGoogleDialogue(d Dialogue) (map[string]any, error) {
	out := map[string]any{}
	contents := []any{}
	system := ""
	names := map[string]string{}
	for _, t := range d.Turns {
		if t.Role == "system" || t.Role == "developer" {
			if system != "" {
				system += "\n"
			}
			system += t.Text
			continue
		}
		role := "user"
		if t.Role == "assistant" {
			role = "model"
		}
		parts := []any{}
		if t.Role == "tool" {
			name := names[t.ID]
			if name == "" {
				return nil, fmt.Errorf("Google tool result has no preceding call")
			}
			var response map[string]any
			if json.Unmarshal([]byte(t.Text), &response) != nil || response == nil {
				response = map[string]any{"result": t.Text}
			}
			parts = append(parts, map[string]any{"functionResponse": map[string]any{"id": t.ID, "name": name, "response": response}})
		} else {
			if t.Text != "" {
				parts = append(parts, map[string]any{"text": t.Text})
			}
			for _, c := range t.Calls {
				var args map[string]any
				if json.Unmarshal([]byte(c.Arguments), &args) != nil || args == nil {
					return nil, fmt.Errorf("Google function arguments must be object")
				}
				names[c.ID] = c.Name
				parts = append(parts, map[string]any{"functionCall": map[string]any{"id": c.ID, "name": c.Name, "args": args}})
			}
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	out["contents"] = contents
	if system != "" {
		out["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": system}}}
	}
	cfg := map[string]any{}
	for source, target := range map[string]string{"temperature": "temperature", "top_p": "topP", "max_tokens": "maxOutputTokens"} {
		if v, ok := d.Options[source]; ok {
			cfg[target] = v
		}
	}
	if len(cfg) > 0 {
		out["generationConfig"] = cfg
	}
	if len(d.Tools) > 0 {
		decls := []any{}
		for _, f := range d.Tools {
			if f["strict"] != nil {
				return nil, fmt.Errorf("Google cannot express strict tools")
			}
			cp := map[string]any{}
			for k, v := range f {
				cp[k] = v
			}
			decls = append(decls, cp)
		}
		out["tools"] = []any{map[string]any{"functionDeclarations": decls}}
	}
	if _, ok := d.Options["tool_choice"]; ok {
		chat, err := EncodeDialogue(d, "openai-chat", "")
		if err != nil {
			return nil, err
		}
		choice := chat["tool_choice"]
		f := map[string]any{}
		if s, ok := choice.(string); ok {
			f["mode"] = map[string]string{"auto": "AUTO", "required": "ANY", "none": "NONE"}[s]
		} else {
			f["mode"] = "ANY"
			f["allowedFunctionNames"] = []any{dialogueMap(dialogueMap(choice)["function"])["name"]}
		}
		out["toolConfig"] = map[string]any{"functionCallingConfig": f}
	}
	return out, nil
}

// ParseGoogleDialogueFrame 解码 Gemini/Vertex SSE 单帧；raw 是 data JSON，返回正文、公开推理、用量和已明确的结束原因。
// Google 流解码器调用。没有 finishReason 的增量保留空 Stop，独立用量帧不视为完成。不修改原始数据。
func ParseGoogleDialogueFrame(raw []byte) (DialogueResult, error) {
	return parseGoogleDialogueResult(raw, true)
}

// parseGoogleDialogueResult 解码单候选原厂响应；raw 是 JSON，frame 表示流式片段，返回共享结果或字段错误。
// 同步和 SSE 解析共用。仅规范已知结束枚举，只转换公开思考文本，未知字段和签名不能静默丢弃。
func parseGoogleDialogueResult(raw []byte, frame bool) (DialogueResult, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return DialogueResult{}, err
	}
	var result genai.GenerateContentResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return DialogueResult{}, err
	}
	r := DialogueResult{ID: result.ResponseID}
	if m["error"] != nil {
		return r, fmt.Errorf("Google upstream error")
	}
	candidates := result.Candidates
	if block := result.PromptFeedback; len(candidates) == 0 && block != nil && block.BlockReason != "" {
		return r, fmt.Errorf("Google prompt blocked: promptFeedback.blockReason=%q", block.BlockReason)
	}
	switch {
	case len(candidates) == 1 && candidates[0] != nil:
		c := candidates[0]
		switch strings.ToUpper(string(c.FinishReason)) {
		case "STOP":
			r.Stop = "stop"
		case "MAX_TOKENS":
			r.Stop = "length"
		case "":
			if !frame {
				r.Stop = "stop"
			}
		default:
			return r, fmt.Errorf("Google candidate blocked: candidates[0].finishReason=%q", c.FinishReason)
		}
		// SDK 类型解码后检查原始字段，避免把未知能力或厂商签名悄悄丢弃。
		rawParts := dialogueList(dialogueMap(dialogueMap(dialogueList(m["candidates"])[0])["content"])["parts"])
		if c.Content != nil {
			for i, part := range c.Content.Parts {
				if part == nil || i >= len(rawParts) {
					return r, fmt.Errorf("invalid Google response part at candidates[0].content.parts[%d]", i)
				}
				fields := dialogueMap(rawParts[i])
				if _, ok := fields["text"].(string); ok {
					toThought, hasThought := fields["thought"].(bool)
					if _, signed := fields["thoughtSignature"]; signed {
						return r, fmt.Errorf("unsupported candidates[0].content.parts[%d].thoughtSignature", i)
					}
					if len(fields) != 1 && (len(fields) != 2 || !hasThought) {
						return r, fmt.Errorf("unsupported Google text part fields at candidates[0].content.parts[%d]", i)
					}
					if toThought {
						r.Reasoning += part.Text
					} else {
						r.Text += part.Text
					}
				} else if f := part.FunctionCall; f != nil && len(fields) == 1 {
					if f.Args == nil || f.Name == "" {
						return r, fmt.Errorf("invalid Google function args or name")
					}
					args, _ := json.Marshal(f.Args)
					id := f.ID
					if id == "" {
						id = fmt.Sprintf("google_%s_%d", f.Name, i)
					}
					r.Calls = append(r.Calls, Turn{ID: id, Name: f.Name, Arguments: string(args)})
				} else {
					return r, fmt.Errorf("unsupported Google response capability at candidates[0].content.parts[%d]", i)
				}
			}
		}
	case frame && len(candidates) == 0 && dialogueMap(m["usageMetadata"]) != nil:
		// Google 可在终态后单独发送用量，candidates 可能省略或是空列表。
	default:
		return r, fmt.Errorf("one Google candidate required in candidates")
	}

	u := dialogueMap(m["usageMetadata"])
	usage := map[string]any{}
	for source, target := range map[string]string{"promptTokenCount": "prompt_tokens", "candidatesTokenCount": "completion_tokens"} {
		if v, ok := u[source]; ok {
			usage[target] = v
		}
	}
	// Google cachedContentTokenCount 已包含于 promptTokenCount，不能标记为需要再次加到输入的独立缓存量。
	if cached, exists := u["cachedContentTokenCount"]; exists {
		usage["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	if candidate, exists := u["candidatesTokenCount"]; exists {
		n, ok := dialogueNumber(candidate)
		if !ok || n < 0 || n != float64(int64(n)) {
			return r, fmt.Errorf("invalid Google candidate token count")
		}
		if thought, exists := u["thoughtsTokenCount"]; exists {
			v, ok := dialogueNumber(thought)
			if !ok || v < 0 || v != float64(int64(v)) {
				return r, fmt.Errorf("invalid Google thoughts token count")
			}
			n += v
		}
		usage["completion_tokens"] = n
	}
	r.Usage = NormalizeDialogueUsage(usage)
	if len(r.Calls) > 0 && r.Stop == "stop" {
		r.Stop = "tool_calls"
	}
	return r, nil
}

// encodeGoogleDialogueResult 输出 Google 单候选结果；参数为共享回复，返回 JSON 或非法工具参数错误。
// 同步和流终态调用；正文与公开推理分别使用普通文本和 thought 部分，保持实际用量，不制造厂商签名。
func encodeGoogleDialogueResult(r DialogueResult) ([]byte, error) {
	parts := []any{}
	if r.Reasoning != "" {
		parts = append(parts, map[string]any{"text": r.Reasoning, "thought": true})
	}
	if r.Text != "" {
		parts = append(parts, map[string]any{"text": r.Text})
	}
	for _, c := range r.Calls {
		var args map[string]any
		if json.Unmarshal([]byte(c.Arguments), &args) != nil || args == nil {
			return nil, fmt.Errorf("invalid Google tool args")
		}
		parts = append(parts, map[string]any{"functionCall": map[string]any{"id": c.ID, "name": c.Name, "args": args}})
	}
	stop := "STOP"
	if r.Stop == "length" {
		stop = "MAX_TOKENS"
	}
	u := map[string]any{}
	for source, target := range map[string]string{"prompt_tokens": "promptTokenCount", "completion_tokens": "candidatesTokenCount", "total_tokens": "totalTokenCount"} {
		if v, ok := r.Usage[source]; ok {
			u[target] = v
		}
	}
	if d := dialogueMap(r.Usage["prompt_tokens_details"]); d != nil {
		u["cachedContentTokenCount"] = d["cached_tokens"]
	}
	// 从 Messages 独立缓存计数输出 Google 时先还原包含缓存的输入总量，与上面的反向投影保持等价。
	separate := r.Usage["cache_read_input_tokens"]
	if separate == nil {
		separate = r.Usage["cache_read_tokens"]
	}
	if cached, ok := dialogueNumber(separate); ok {
		if input, inputOK := dialogueNumber(r.Usage["prompt_tokens"]); inputOK {
			u["promptTokenCount"] = input + cached
			if output, outputOK := dialogueNumber(r.Usage["completion_tokens"]); outputOK {
				u["totalTokenCount"] = input + cached + output
			}
		}
		u["cachedContentTokenCount"] = cached
	}
	return json.Marshal(map[string]any{"responseId": r.ID, "candidates": []any{map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": parts}, "finishReason": stop}}, "usageMetadata": u})
}
