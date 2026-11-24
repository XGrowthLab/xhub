package dialogue

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestCodexChatToolRoundTrip 验证 Codex 原始请求经 Chat 桥接后恢复 namespace/custom 身份、公开推理和工具原文。
// 参数 t 为上下文；前置内存协议对象，断言原生无损、历史续轮与回复双向一致；不执行工具，无外部数据清理。
func TestCodexChatToolRoundTrip(t *testing.T) {
	var body map[string]any
	_ = json.Unmarshal([]byte(`{"input":[{"role":"user","content":"Read the file."},{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Inspect first."}],"encrypted_content":null},{"type":"function_call","namespace":"fs","name":"read","call_id":"c1","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"用户原文"},{"type":"custom_tool_call","namespace":"fs","name":"patch","call_id":"c2","input":"line 1\nline 2"},{"type":"custom_tool_call_output","call_id":"c2","output":"done"},{"type":"message","role":"assistant","phase":"commentary","content":"Reading"}],"tools":[{"type":"namespace","name":"fs","tools":[{"type":"function","name":"read","parameters":{"type":"object"}},{"type":"custom","name":"patch","format":{"type":"grammar","definition":"patch format"}}]}],"tool_choice":{"type":"custom","namespace":"fs","name":"patch"},"reasoning":{"effort":"medium","summary":"auto"},"include":["reasoning.encrypted_content"],"client_metadata":{"session_id":"client-only"},"store":false}`), &body)
	before, _ := json.Marshal(body)
	d, err := ParseDialogue("openai-responses", body)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := EncodeDialogue(d, "openai-chat", "real")
	if err != nil {
		t.Fatal(err)
	}
	tools := dialogueList(chat["tools"])
	if len(tools) != 2 || dialogueMap(dialogueMap(tools[0])["function"])["name"] != "fs__read" || dialogueMap(dialogueMap(tools[1])["function"])["name"] != "fs__patch" {
		t.Fatalf("命名空间未投影成稳定函数: %v", tools)
	}
	if dialogueMap(dialogueMap(chat["tool_choice"])["function"])["name"] != "fs__patch" || chat["reasoning_effort"] != "medium" || chat["include"] != nil || chat["client_metadata"] != nil {
		t.Fatalf("工具选择或客户端参数未正确映射: %v", chat)
	}
	encoded, _ := json.Marshal(chat)
	if !strings.Contains(string(encoded), "用户原文") || !strings.Contains(string(encoded), "Inspect first.") || !strings.Contains(string(encoded), `"reasoning_content"`) || !strings.Contains(string(encoded), `"content\":\"line 1`) {
		t.Fatalf("工具原文、custom 输入或公开推理丢失: %s", encoded)
	}
	native, err := EncodeDialogue(d, "openai-responses", "")
	if err != nil {
		t.Fatal(err)
	}
	delete(native, "model")
	if !reflect.DeepEqual(native, body) {
		t.Fatal("原生请求被投影逻辑修改")
	}
	after, _ := json.Marshal(body)
	if string(before) != string(after) {
		t.Fatal("用户原请求被修改")
	}
	result := DialogueResult{ID: "r", ToolBindings: d.ToolBindings, Calls: []Turn{{ID: "c1", Name: "fs__read", Arguments: "{}"}, {ID: "c2", Name: "fs__patch", Arguments: `{"content":"line 1\nline 2"}`}}}
	raw, err := EncodeDialogueResult(result, "openai-responses", "public")
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	_ = json.Unmarshal(raw, &response)
	items := dialogueList(response["output"])
	if dialogueMap(items[0])["namespace"] != "fs" || dialogueMap(items[0])["name"] != "read" || dialogueMap(items[1])["type"] != "custom_tool_call" || dialogueMap(items[1])["input"] != "line 1\nline 2" {
		t.Fatalf("Responses 工具身份或 custom 原文未还原: %s", raw)
	}
}

// TestAgentToolBoundaries 验证长名称稳定且区分、名称碰撞、非法工具结构与 custom 参数失败契约。
// 参数 t 为上下文；纯内存定义，不访问供应商。错误必须在工具执行前出现，无资源需要清理。
func TestAgentToolBoundaries(t *testing.T) {
	namespace := strings.Repeat("x", 100)
	a, b := qualifiedToolName(namespace, "read"), qualifiedToolName(namespace, "write")
	if len(a) > 64 || a == b || a != qualifiedToolName(namespace, "read") || qualifiedToolName("", "read") != "read" {
		t.Fatal("长工具名称不稳定、超限或发生碰撞")
	}
	for _, raw := range []string{
		`[42]`, `[{"type":"namespace","name":"fs","tools":42}]`, `[{"type":"namespace","tools":[]}]`,
		`[{"type":"namespace","name":"fs","tools":[42]}]`, `[{"type":"custom"}]`,
	} {
		var values []any
		_ = json.Unmarshal([]byte(raw), &values)
		if _, err := projectAgentTools(values, nil); err == nil {
			t.Fatalf("非法工具定义被接受: %s", raw)
		}
	}
	bindings := map[string]ToolBinding{"patch": {Name: "patch", Custom: true}}
	for _, args := range []string{`{`, `{}`, `{"content":42}`, `{"content":"x","extra":true}`} {
		if _, err := ResponseToolItem(Turn{Name: "patch", Arguments: args}, bindings, "completed"); err == nil {
			t.Fatalf("非法 custom 工具输入被接受: %s", args)
		}
	}
	for _, item := range []map[string]any{{"type": "custom_tool_call", "input": 42}, {"type": "message", "phase": "unknown"}} {
		if err := projectAgentCall(item); err == nil {
			t.Fatalf("非法工具历史或阶段被接受: %v", item)
		}
	}
	// 无法表达的原生工具仍可走原生部署，不会为了让 Chat 成功而悄悄删除检索能力。
	d, err := ParseDialogue("openai-responses", map[string]any{"input": "Hello", "tools": []any{map[string]any{"type": "web_search"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = EncodeDialogue(d, "openai-responses", "real"); err != nil {
		t.Fatal(err)
	}
	if _, err = EncodeDialogue(d, "openai-chat", "real"); err == nil {
		t.Fatal("未支持的检索工具被静默删除")
	}
	var collision map[string]any
	_ = json.Unmarshal([]byte(`{"input":"Hello","tools":[{"type":"function","name":"fs__read","parameters":{}},{"type":"namespace","name":"fs","tools":[{"type":"function","name":"read","parameters":{}}]}]}`), &collision)
	d, err = ParseDialogue("openai-responses", collision)
	if err != nil {
		t.Fatalf("Chat 别名冲突不应破坏合法原生 namespace: %v", err)
	}
	if _, err = EncodeDialogue(d, "openai-responses", "real"); err != nil {
		t.Fatal(err)
	}
	if _, err = EncodeDialogue(d, "openai-chat", "real"); err == nil {
		t.Fatal("Chat 别名冲突没有明确拒绝")
	}
	// custom 重建不能掩盖未知能力；只拒绝桥接，原生供应商仍可校验其扩展。
	for _, raw := range []string{
		`{"type":"custom","name":"run","future_capability":true}`,
		`{"type":"custom","name":"run","format":{"type":"future_format"}}`,
		`{"type":"custom","name":"run","format":"invalid"}`,
		`{"type":"custom","name":"run","format":{"type":"grammar"}}`,
		`{"type":"custom","name":"run","format":{"type":"text","future":true}}`,
		`{"type":"namespace","name":"fs","future":true,"tools":[{"type":"function","name":"read","parameters":{}}]}`,
	} {
		var tool map[string]any
		_ = json.Unmarshal([]byte(raw), &tool)
		d, err = ParseDialogue("openai-responses", map[string]any{"input": "Hello", "tools": []any{tool}})
		if err != nil {
			t.Fatalf("扩展工具应允许原生校验: %v", err)
		}
		if _, err = EncodeDialogue(d, "openai-responses", "real"); err != nil {
			t.Fatal(err)
		}
		if _, err = EncodeDialogue(d, "openai-chat", "real"); err == nil {
			t.Fatalf("工具的未知能力被静默丢弃: %s", raw)
		}
	}
}

// TestCodexChatOptionBoundaries 验证可选返回提示与真实密文的区别、摘要枚举和客户端元数据类型/大小。
// 参数 t 为上下文；前置普通文本请求，已支持提示可桥接，未知能力/非法输入必须失败；不改身份或外部状态。
func TestCodexChatOptionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value any
		valid bool
	}{
		{"include", []any{"reasoning.encrypted_content"}, true}, {"include", []any{}, true}, {"include", []any{"unknown"}, false},
		{"reasoning", map[string]any{"effort": "high", "summary": "detailed"}, true}, {"reasoning", map[string]any{"summary": "invalid"}, false},
		{"reasoning", map[string]any{"summary": 1}, false}, {"reasoning", map[string]any{"unknown": true}, false},
		{"client_metadata", map[string]any{"session_id": "client-only"}, true}, {"client_metadata", "bad", false},
		{"client_metadata", map[string]any{"session_id": 1}, false}, {"client_metadata", map[string]any{"large": strings.Repeat("x", 65536)}, false},
	} {
		d, err := ParseDialogue("openai-responses", map[string]any{"input": "Hello", tc.key: tc.value})
		if err == nil {
			_, err = EncodeDialogue(d, "openai-chat", "real")
		}
		if (err == nil) != tc.valid {
			t.Fatalf("%s 边界不符: %v, %v", tc.key, tc.value, err)
		}
	}
	for _, value := range []any{"auto", "concise", "detailed", "none"} {
		if !validReasoningSummary(value) {
			t.Fatalf("有效摘要提示被拒绝: %v", value)
		}
	}
	d, err := ParseDialogue("openai-responses", map[string]any{"input": []any{map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "opaque"}, map[string]any{"role": "user", "content": "Hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = EncodeDialogue(d, "openai-chat", "real"); err == nil {
		t.Fatal("真实不透明密文被错误桥接")
	}
}

// TestCodexAdditionalTools 验证 Codex 内联 additional_tools 的 namespace/custom 声明和历史能桥接到 Chat。
// 参数 t 为上下文；前置纯内存动态工具请求，验证工具定义不变成用户文本、错误数组被拒绝及原生正文不丢失；无外部资源。
func TestCodexAdditionalTools(t *testing.T) {
	var body map[string]any
	_ = json.Unmarshal([]byte(`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec","description":"Execute the provided code."}]}]},{"role":"user","content":"Read the file."}]}`), &body)
	d, err := ParseDialogue("openai-responses", body)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := EncodeDialogue(d, "openai-chat", "real")
	if err != nil {
		t.Fatal(err)
	}
	tools := dialogueList(chat["tools"])
	if len(tools) != 1 || dialogueMap(dialogueMap(tools[0])["function"])["name"] != "functions__exec" || len(dialogueList(chat["messages"])) != 1 {
		t.Fatalf("内联工具声明丢失或被当作正文: %v", chat)
	}
	body["input"] = []any{map[string]any{"type": "additional_tools", "tools": "bad"}}
	if _, err = ParseDialogue("openai-responses", body); err == nil {
		t.Fatal("错误工具声明数组被接受")
	}
}

// TestResponsesUsageRequiredDetails 验证 Chat 省略或返回空 Token 明细时，Responses 必需字段仍可被 Codex 解析。
// 参数 t 为上下文；保留实测总量、已有明细和输入原对象；缺失维度只补格式默认零，不伪造费用，无需清理。
func TestResponsesUsageRequiredDetails(t *testing.T) {
	for _, details := range []map[string]any{nil, {}, {"reasoning_tokens": 4, "vendor_counter": 7}} {
		usage := map[string]any{"prompt_tokens": 8, "completion_tokens": 5, "completion_tokens_details": details}
		before, _ := json.Marshal(usage)
		out := DialogueOutputUsage(usage, "openai-responses")
		want := 0
		if details["reasoning_tokens"] != nil {
			want = 4
		}
		if dialogueMap(out["output_tokens_details"])["reasoning_tokens"] != want || dialogueMap(out["input_tokens_details"])["cached_tokens"] != 0 || out["output_tokens"] != 5 {
			t.Fatalf("Responses 必需明细缺失或实测总量改变: %v", out)
		}
		after, _ := json.Marshal(usage)
		if string(before) != string(after) {
			t.Fatal("补齐明细修改了上游原对象")
		}
	}
}

// TestReasoningContextProjection 验证 current_turn 仅移除旧轮公开推理，all_turns/auto 保留全部，错误枚举拒绝。
// 参数 t 为上下文；内存历史同时包含工具续轮，断言正文/调用 ID/当前推理不丢失且原对象不变，无外部数据清理。
func TestReasoningContextProjection(t *testing.T) {
	turns := []Turn{{Role: "user", Text: "Old question"}, {Role: "assistant", Text: "Old answer", Reasoning: "Old plan"}, {Role: "reasoning", Reasoning: "Older summary"}, {Role: "user", Text: "Current question"}, {Role: "reasoning", Reasoning: "Current plan"}, {Role: "assistant", Calls: []Turn{{ID: "c", Name: "read", Arguments: "{}"}}}, {Role: "tool", ID: "c", Text: "file-value"}}
	for _, mode := range []any{nil, "auto", "all_turns", "current_turn"} {
		out, err := projectReasoningContext(turns, mode)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), "Current plan") || !strings.Contains(string(raw), "file-value") || !strings.Contains(string(raw), "Old answer") {
			t.Fatalf("推理投影丢失正文或当前工具上下文: %s", raw)
		}
		if (mode == "current_turn") == strings.Contains(string(raw), "Old plan") {
			t.Fatalf("旧轮推理范围错误: mode=%v, %s", mode, raw)
		}
	}
	for _, mode := range []any{"invalid", true, 42} {
		if _, err := projectReasoningContext(turns, mode); err == nil {
			t.Fatalf("非法 reasoning.context 被接受: %v", mode)
		}
	}
	if turns[1].Reasoning != "Old plan" {
		t.Fatal("投影改变了原始历史")
	}
}
