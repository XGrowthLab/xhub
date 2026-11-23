package dialogue

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestRequestFieldError 验证入站及出站限制返回原始字段路径；前置纯内存协议请求，无需资源清理。
func TestRequestFieldError(t *testing.T) {
	for _, tc := range []struct{ name, source, target, raw, field, guidance string }{
		{"unknown option", "openai-chat", "anthropic-messages", `{"messages":[{"role":"user","content":"Hello"}],"vendor_extra":true}`, "vendor_extra", "requires native openai-chat"},
		{"nested message", "anthropic-messages", "", `{"messages":[{"role":"user","content":[{"type":"text","text":"Hello","extension":true}]}]}`, "messages[0].content[0].extension", "unsupported unified capability"},
		{"tool type at ingress", "openai-chat", "", `{"messages":[{"role":"user","content":"Hello"}],"tools":[{"type":"mcp"}]}`, "tools[0].type", "only function tools"},
		{"hosted tool conversion", "openai-responses", "openai-chat", `{"input":"Hello","tools":[{"type":"mcp"}]}`, "tools[0].type", "has no cross-protocol mapping"},
		{"custom tool format", "openai-responses", "openai-chat", `{"input":"Hello","tools":[{"type":"custom","name":"run","format":{"type":"vendor"}}]}`, "tools[0].format.type", "text and grammar"},
		{"tool declared in history", "openai-responses", "openai-chat", `{"input":[{"role":"user","content":"Hello"},{"type":"additional_tools","tools":[{"type":"mcp"}]}]}`, "input[1].tools[0].type", "has no cross-protocol mapping"},
		{"display conversion", "anthropic-messages", "openai-chat", `{"messages":[{"role":"user","content":"Hello"}],"thinking":{"type":"adaptive","display":"summary"}}`, "thinking.display", "requires native Messages"},
		{"strict tool conversion", "openai-chat", "anthropic-messages", `{"messages":[{"role":"user","content":"Hello"}],"tools":[{"type":"function","function":{"name":"read","strict":true,"parameters":{"type":"object"}}}]}`, "tools[0].function.strict", "strict tools cannot be expressed"},
		{"image detail", "openai-responses", "anthropic-messages", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AQID","detail":"original"}]}]}`, "input[0].content[0].detail", "image detail cannot be expressed"},
		{"image after projected items", "openai-responses", "anthropic-messages", `{"input":[{"type":"additional_tools","tools":[]},{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AQID","detail":"original"}]}]}`, "input[1].content[0].detail", "image detail cannot be expressed"},
		{"nested tool declaration", "openai-responses", "", `{"input":"Hello","tools":[{"type":"namespace","name":"files","tools":[{"type":"function","name":"read","parameters":{"type":"object"},"vendor_extra":true}]}]}`, "tools[0].tools[0].vendor_extra", "unsupported unified capability"},
		{"unknown include", "openai-responses", "openai-chat", `{"input":"Hello","include":["vendor"]}`, "include[0]", "requires native Responses"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &body); err != nil {
				t.Fatal(err)
			}
			request, err := ParseDialogue(tc.source, body)
			if tc.target != "" {
				if err != nil {
					t.Fatalf("正常原生候选在选路前被拦截: %v", err)
				}
				if _, err := EncodeDialogue(request, tc.source, "native"); err != nil {
					t.Fatalf("跨协议限制阻塞了同协议: %v", err)
				}
				_, err = EncodeDialogue(request, tc.target, "bridge")
			}
			var field *RequestFieldError
			if !errors.As(err, &field) || field.Field != tc.field || !strings.Contains(field.Message, tc.guidance) {
				t.Fatalf("字段诊断错误，期望 %s / %s，实际 %v", tc.field, tc.guidance, err)
			}
		})
	}
}
