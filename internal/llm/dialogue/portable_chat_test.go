package dialogue

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPortableChatHistory 验证旧密文可切换成公开 Chat 历史。前置完整工具结果和新用户消息，
// 清理后保留工具 ID、公开摘要和 DeepSeek 推理文本。所有输入是内存 JSON，无需清理。
func TestPortableChatHistory(t *testing.T) {
	const request = `{"messages":[
		{"role":"user","content":"Read the service status."},
		{"role":"assistant","reasoning_content":"Read before answering.","reasoning_items":[{"type":"reasoning","id":"rs_old","summary":[{"type":"summary_text","text":"Public plan"}],"encrypted_content":"old-cipher","status":"completed"}],"tool_calls":[{"id":"read_1"}]},
		{"role":"tool","tool_call_id":"read_1","content":"Offline"},
		{"role":"assistant","content":"The service is offline."},
		{"role":"user","content":"Continue."}
	]}`
	var body map[string]any
	if err := json.Unmarshal([]byte(request), &body); err != nil {
		t.Fatal(err)
	}
	recovered, ok := PortableChatHistory(body)
	if !ok {
		t.Fatal("完整公开 Chat 历史没有生成")
	}
	raw, _ := json.Marshal(recovered)
	for _, want := range []string{"Read before answering.", "Public plan", "read_1", "The service is offline.", "Continue."} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("恢复丢失正文或协议字段 %q: %s", want, raw)
		}
	}
	original, _ := json.Marshal(body)
	if strings.Contains(string(raw), "old-cipher") || !strings.Contains(string(original), "old-cipher") {
		t.Fatal("旧密文未从出站副本移除，或原历史被改写")
	}
	messages := dialogueList(body["messages"])
	body["messages"] = messages[:3]
	if _, ok := PortableChatHistory(body); ok {
		t.Fatal("工具返回中的请求不应转换")
	}
	body["messages"] = messages
	item := dialogueMap(dialogueList(dialogueMap(messages[1])["reasoning_items"])[0])
	item["encrypted_content"] = "xhub_messages_v1:private"
	if _, ok := PortableChatHistory(body); ok {
		t.Fatal("网关引用不能被清理为公开历史")
	}
	item["encrypted_content"] = "old-cipher"
	item["unknown_signature"] = "unsupported"
	if _, ok := PortableChatHistory(body); ok {
		t.Fatal("非法推理扩展不能因恢复而被接受")
	}
}
