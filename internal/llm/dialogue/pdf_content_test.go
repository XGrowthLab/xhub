package dialogue

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// pdfBody 构造带有前后文本的协议内联 PDF；protocol 为入口协议，返回独立请求与原始文件数据。
// 供纯内存跨协议测试使用，不访问外部服务或写入用户文件，测试结束由 GC 回收。
func pdfBody(protocol string) (map[string]any, string) {
	data := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\nsynthetic\n%%EOF"))
	part, _ := encodePDFPart(ContentPart{Type: "file", Filename: "fixture.pdf", URL: data}, protocol)
	textType, field := "text", "messages"
	if protocol == "openai-responses" {
		textType, field = "input_text", "input"
	}
	return map[string]any{field: []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": textType, "text": "Before 原文"}, part, map[string]any{"type": textType, "text": "After"}}}}}, data
}

// TestPDFProtocolMatrix 验证三种协议之间全部九种组合保留文件字节、名称、图文顺序与用户文本。
// 使用独立内存合成 PDF 和原始正文快照，不访问供应商、不写磁盘，测试完成自动回收。
func TestPDFProtocolMatrix(t *testing.T) {
	for _, source := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
		body, data := pdfBody(source)
		before, _ := json.Marshal(body)
		parsed, err := ParseDialogue(source, body)
		if err != nil || len(parsed.Turns) != 1 || len(parsed.Turns[0].Content) != 3 {
			t.Fatalf("%s 文件解析失败: %+v %v", source, parsed.Turns, err)
		}
		if parsed.Turns[0].Content[1].URL != data {
			t.Fatal("文件内容被改变")
		}
		for _, target := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
			encoded, err := EncodeDialogue(parsed, target, "upstream")
			if err != nil {
				t.Fatalf("%s -> %s: %v", source, target, err)
			}
			again, err := ParseDialogue(target, encoded)
			if err != nil || !reflect.DeepEqual(parsed.Turns, again.Turns) {
				t.Fatalf("%s -> %s 文件或顺序改变: %+v %v", source, target, again.Turns, err)
			}
		}
		after, _ := json.Marshal(body)
		if string(before) != string(after) {
			t.Fatal("原始请求被修改")
		}
	}
}

// TestPDFInputBoundaries 验证无效 Base64、错误 MIME、路径文件名、非用户角色及不透明引用均明确拒绝。
// 仅构造内存请求，不联网，不写文件，不保留测试数据。
func TestPDFInputBoundaries(t *testing.T) {
	for _, block := range []map[string]any{
		{"type": "input_file", "file_id": "file-private"},
		{"type": "input_file", "file_data": "https://example.com/a.pdf"},
		{"type": "input_file", "file_data": "data:text/plain;base64,JVBERi0="},
		{"type": "input_file", "file_data": "data:application/pdf;base64,???"},
		{"type": "input_file", "file_data": "data:application/pdf;base64,YWJj"},
		{"type": "input_file", "filename": "../x.pdf", "file_data": "data:application/pdf;base64,JVBERi0="},
		{"type": "input_file", "file_data": "data:application/pdf;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(maxInlinePDFBytes)+4)},
	} {
		if _, err := parsePDFPart(block, "openai-responses", "user"); err == nil {
			t.Fatal("非法 PDF 被接受")
		}
	}
	body, _ := pdfBody("openai-chat")
	block := body["messages"].([]any)[0].(map[string]any)["content"].([]any)[1].(map[string]any)
	if _, err := parsePDFPart(block, "openai-chat", "assistant"); err == nil {
		t.Fatal("非用户文件被接受")
	}
	if _, err := encodePDFPart(ContentPart{Type: "file", Filename: "a.pdf", URL: "https://example.com"}, "openai-chat"); err == nil {
		t.Fatal("远程文件被静默转换")
	}
}
