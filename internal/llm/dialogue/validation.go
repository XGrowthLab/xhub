package dialogue

import (
	"fmt"
	"strings"
)

// dialogueString 读取协议字符串；参数为动态字段，返回原字符串；解析器调用，无副作用。
func dialogueString(v any) string { s, _ := v.(string); return s }

// dialogueMap 安全读取协议对象；参数为动态值，返回对象或空值；解析器调用。
func dialogueMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

// dialogueList 安全读取协议数组；参数为动态值，返回数组或空值；解析器调用。
func dialogueList(v any) []any { a, _ := v.([]any); return a }

// textContent 校验纯文本块；参数为内容字段，返回文本或明确能力错误，不丢弃图片等未知块。
// 调用：统一请求和工具结果解析；空内容允许仅工具调用的助手消息。
func textContent(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	parts, ok := v.([]any)
	if !ok {
		return "", fmt.Errorf("content must be text or text blocks")
	}
	var out strings.Builder
	for _, p := range parts {
		m := dialogueMap(p)
		switch dialogueString(m["type"]) {
		case "text", "input_text", "output_text":
			out.WriteString(dialogueString(m["text"]))
		default:
			return "", fmt.Errorf("unsupported content capability %v", m["type"])
		}
	}
	return out.String(), nil
}

// dialogueFields 校验动态对象字段白名单；参数为对象及可表达字段，返回明确错误。
// 调用：统一解析器和工具选择；缺失对象与未知能力在外部调用前拒绝，不修改输入。
func dialogueFields(m map[string]any, fields ...string) error {
	if m == nil {
		return fmt.Errorf("expected protocol object")
	}
	allowed := map[string]bool{}
	for _, key := range fields {
		allowed[key] = true
	}
	for key := range m {
		if !allowed[key] {
			return &RequestFieldError{Field: key, Message: "unsupported unified capability"}
		}
	}
	return nil
}
