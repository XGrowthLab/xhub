package dialogue

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestCloneObjectKeepsDecodedNumberType 验证深拷贝同时保留精确数字和已有普通数字类型。
// 参数 t 为测试上下文；覆盖嵌套容器隔离及非法 JSON 数值。纯内存数据无需清理。
func TestCloneObjectKeepsDecodedNumberType(t *testing.T) {
	exact := json.Number("9007199254740993")
	item := map[string]any{"exact": exact}
	source := map[string]any{"array": []any{item}, "regular": float64(17)}
	cloned, err := cloneObject(source)
	if err != nil {
		t.Fatal(err)
	}
	copiedItem := cloned["array"].([]any)[0].(map[string]any)
	if copiedItem["exact"] != exact || cloned["regular"] != float64(17) {
		t.Fatalf("JSON 深拷贝改变了数字类型或精度: %#v", cloned)
	}
	copiedItem["exact"] = json.Number("31")
	if item["exact"] != exact {
		t.Fatal("修改副本影响了原始请求")
	}
	if _, err := cloneObject(map[string]any{"invalid": json.Number("not a number")}); err == nil {
		t.Fatal("深拷贝接受了不可序列化的数字")
	}
}

// TestAgentNativeRequests 验证 GPT/Grok Responses 与 DeepSeek Chat 的原生字段保留及跨协议拒绝。
// 参数 t 为单测上下文；前置标准 Agent/工具多轮正文，断言模型替换、输入不变、无静默丢参；纯内存无需清理。
func TestAgentNativeRequests(t *testing.T) {
	for _, raw := range []string{
		`{"model":"alias","input":[{"role":"user","content":"Read the file."},{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"cipher"},{"type":"function_call","call_id":"c","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"value"}],"reasoning":{"effort":"high","summary":"auto"},"include":["reasoning.encrypted_content"],"text":{"format":{"type":"text"}},"prompt_cache_key":"cache-key","vendor_extension":{"kept":true},"store":false}`,
		`{"model":"alias","messages":[{"role":"user","content":"Read the file."},{"role":"assistant","content":null,"reasoning_content":"I should read it","tool_calls":[{"id":"c","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c","content":"value"}],"thinking":{"type":"enabled"},"reasoning_effort":"high","stream":true,"stream_options":{"include_usage":true}}`,
	} {
		var body map[string]any
		_ = json.Unmarshal([]byte(raw), &body)
		protocol := "openai-responses"
		target := "openai-chat"
		if body["messages"] != nil {
			protocol, target = target, protocol
		}
		d, err := ParseDialogue(protocol, body)
		if err != nil {
			t.Fatal(err)
		}
		out, err := EncodeDialogue(d, protocol, "vendor-real")
		if err != nil {
			t.Fatal(err)
		}
		out["model"] = "alias"
		if !reflect.DeepEqual(out, body) {
			t.Fatalf("%s 原生字段丢失: %#v", protocol, out)
		}
		if _, err = EncodeDialogue(d, target, "real"); err == nil {
			t.Fatal("无法表达的能力被跨协议静默删除")
		}
	}
}

// TestAgentBridgeOptions 验证参考 LiteLLM 的 reasoning 与结构化输出双向映射、边界与未知字段失败。
// 参数 t 为上下文，前置有效文本输入和扩展字段，输出需保留语义；无外部副作用或清理。
func TestAgentBridgeOptions(t *testing.T) {
	for _, effort := range []string{"none", "low", "medium", "high", "xhigh"} {
		body := map[string]any{"input": "Hello", "reasoning": map[string]any{"effort": effort}, "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "answer", "strict": true, "schema": map[string]any{"type": "object"}}}, "parallel_tool_calls": false}
		d, err := ParseDialogue("openai-responses", body)
		if err != nil {
			t.Fatal(err)
		}
		chat, err := EncodeDialogue(d, "openai-chat", "real")
		if err != nil {
			t.Fatal(err)
		}
		if chat["reasoning_effort"] != effort || chat["parallel_tool_calls"] != false {
			t.Fatalf("桥接参数改变: %v", chat)
		}
		back, err := ParseDialogue("openai-chat", chat)
		if err != nil {
			t.Fatal(err)
		}
		response, err := EncodeDialogue(back, "openai-responses", "real")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(response["text"], body["text"]) || !reflect.DeepEqual(response["reasoning"], body["reasoning"]) {
			t.Fatalf("结构化输出未往返: %v", response)
		}
	}
	for key, value := range map[string]any{"reasoning": "high", "include": []any{1}, "text": false, "parallel_tool_calls": "true", "tools": 1, "store": "false"} {
		_, err := ParseDialogue("openai-responses", map[string]any{"input": "Hello", key: value})
		if err == nil {
			t.Fatalf("错误 %s 类型被接受", key)
		}
	}
}

// TestDeepSeekReasoningRoundTrip 验证公开推理在 Chat→Responses→工具续轮 Chat 中不丢失。
// 参数 t 为上下文；前置含 reasoning_content 的工具调用，结果关联 ID/推理完全一致；内存测试无需清理。
func TestDeepSeekReasoningRoundTrip(t *testing.T) {
	raw := []byte(`{"id":"resp_deepseek","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"Read the file first.","tool_calls":[{"id":"c","type":"function","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`)
	r, err := ParseDialogueResult("openai-chat", raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeDialogueResult(r, "openai-responses", "public")
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	_ = json.Unmarshal(encoded, &response)
	input := append([]any{map[string]any{"role": "user", "content": "Read the file."}}, dialogueList(response["output"])...)
	input = append(input, map[string]any{"type": "function_call_output", "call_id": "c", "output": "file text"})
	d, err := ParseDialogue("openai-responses", map[string]any{"input": input})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := EncodeDialogue(d, "openai-chat", "deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	messages := dialogueList(chat["messages"])
	if dialogueMap(messages[1])["reasoning_content"] != "Read the file first." || !strings.Contains(string(encoded), "rs_") && !strings.Contains(string(encoded), "_reasoning") {
		t.Fatalf("DeepSeek 推理历史丢失: %s %v", encoded, messages)
	}
}

// TestNativeResponseReasoning 验证 Responses 原生输出及 Chat 扩展保留密文、摘要及供应商用量，其他不兼容输出拒绝。
// 参数 t 为上下文，前置有效原生响应；模型只改公开别名，错误终态和损坏 JSON 失败，无需清理。
func TestNativeResponseReasoning(t *testing.T) {
	raw := []byte(`{"id":"resp_native","object":"response","status":"completed","model":"real","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Plan"}],"encrypted_content":"cipher"},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Done","annotations":[]}]}],"usage":{"input_tokens":8,"output_tokens":2,"output_tokens_details":{"reasoning_tokens":1},"vendor_counter":42}}`)
	r, err := ParseDialogueResult("openai-responses", raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := EncodeDialogueResult(r, "openai-responses", "public")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "cipher") || !strings.Contains(string(out), "vendor_counter") || !strings.Contains(string(out), `"model":"public"`) {
		t.Fatalf("原生响应丢失: %s", out)
	}
	if out, err = EncodeDialogueResult(r, "openai-chat", "public"); err != nil || !strings.Contains(string(out), `"reasoning_items"`) {
		t.Fatalf("Chat 未保留加密推理扩展: %v %s", err, out)
	}
	if _, err = EncodeDialogueResult(r, "gemini", "public"); err == nil {
		t.Fatal("密文不能投影到没有承载机制的协议")
	}
	for _, bad := range []string{`{`, `{"status":"failed","output":[]}`, `{"status":"completed","output":42}`} {
		if _, err := ParseDialogueResult("openai-responses", []byte(bad)); err == nil {
			t.Fatalf("损坏响应被接受: %s", bad)
		}
	}
}

// TestMessagesAgentBridgeHints 验证 Claude Code 的缓存提示、adaptive effort、保留历史及不支持配置边界。
// 参数 t 为上下文；前置真实 CLI 形状的内存正文，原请求不变，已支持参数映射，未知格式/清理要求/预算推理明确失败；无需清理。
func TestMessagesAgentBridgeHints(t *testing.T) {
	for _, invalid := range []string{"", "thinking_budget", "context_clear", "format", "metadata", "bad_context", "bad_text"} {
		body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "用户原始输入", "cache_control": map[string]any{"type": "ephemeral"}}}}}, "system": []any{map[string]any{"type": "text", "text": "Instructions", "cache_control": map[string]any{"type": "ephemeral"}}}, "metadata": map[string]any{"user_id": "client-hint"}, "thinking": map[string]any{"type": "adaptive", "display": "omitted"}, "output_config": map[string]any{"effort": "low"}, "context_management": map[string]any{"edits": []any{map[string]any{"type": "clear_thinking_20251015", "keep": "all"}}}}
		switch invalid {
		case "thinking_budget":
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 1024}
		case "context_clear":
			body["context_management"] = map[string]any{"edits": []any{map[string]any{"type": "clear_thinking_20251015", "keep": "none"}}}
		case "format":
			body["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema"}}
		case "metadata":
			body["metadata"] = map[string]any{"business_field": "cannot drop"}
		case "bad_context":
			body["context_management"] = map[string]any{"edits": "invalid"}
		case "bad_text":
			body["text"] = map[string]any{"format": "invalid"}
		}
		before, _ := json.Marshal(body)
		d, err := ParseDialogue("anthropic-messages", body)
		if err != nil {
			t.Fatal(err)
		}
		out, err := EncodeDialogue(d, "openai-responses", "real")
		if (err != nil) != (invalid != "") {
			t.Fatalf("Messages 边界 %s: out=%v err=%v", invalid, out, err)
		}
		if invalid == "" && (dialogueMap(out["reasoning"])["effort"] != "low" || !strings.Contains(string(mustEncode(out)), "用户原始输入")) {
			t.Fatalf("Messages 桥接丢失用户文本或 effort: %v", out)
		}
		after, _ := json.Marshal(body)
		if string(after) != string(before) {
			t.Fatal("桥接修改了用户原始请求")
		}
	}
}

// mustEncode 为上面的纯 JSON 参数断言编码正文；参数为测试对象，返回 JSON 字节，无外部副作用。
// 输入均由测试构造；异常编码返回 nil，由业务断言报告缺失内容，不访问磁盘或网络。
func mustEncode(value any) []byte { raw, _ := json.Marshal(value); return raw }

// TestNativeRefusalAndLifecycleBoundaries 验证原生拒绝响应仍是可观察的有效回复，异步/多候选在外发前拒绝。
// 参数 t 为上下文；输入为内存原生结果和请求，原生 refusal 保留、跨协议明确拒绝；不创建任务或外部数据，无需清理。
func TestNativeRefusalAndLifecycleBoundaries(t *testing.T) {
	for protocol, raw := range map[string]string{"openai-chat": `{"id":"r","choices":[{"message":{"role":"assistant","content":null,"refusal":"blocked"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`, "openai-responses": `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"blocked"}]}],"usage":{"input_tokens":8,"output_tokens":2}}`} {
		result, err := ParseDialogueResult(protocol, []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := EncodeDialogueResult(result, protocol, "alias")
		if err != nil || !strings.Contains(string(encoded), "blocked") {
			t.Fatalf("原生拒绝丢失: %s %v", encoded, err)
		}
		if _, err := EncodeDialogueResult(result, "anthropic-messages", "alias"); err == nil {
			t.Fatal("拒绝响应不应跨协议静默删除")
		}
	}
	for _, extra := range []map[string]any{{"background": true}, {"background": "false"}, {"n": 2}} {
		body := map[string]any{"input": "Hello"}
		for k, v := range extra {
			body[k] = v
		}
		if _, err := ParseDialogue("openai-responses", body); err == nil {
			t.Fatalf("不支持的生命周期或多候选被放行: %v", body)
		}
	}
}
