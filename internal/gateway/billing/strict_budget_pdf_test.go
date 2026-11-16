package billing

import (
	"encoding/base64"
	"testing"

	"github.com/sunqirui1987/xhub/internal/catalog"
)

// TestStrictBudgetPDF 验证各协议内联 PDF 按完整窗口预占并按实测 Token 计费，非法文件不能绕过预算。
// 使用独立内存部署和合成文件，覆盖正常文件、文件引用及非用户角色；无需外部服务或数据清理。
func TestStrictBudgetPDF(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\nsynthetic\n%%EOF"))
	for _, tc := range []struct {
		protocol string
		part     map[string]any
	}{
		{"openai-chat", map[string]any{"type": "file", "file": map[string]any{"filename": "fixture.pdf", "file_data": "data:application/pdf;base64," + data}}},
		{"openai-responses", map[string]any{"type": "input_file", "filename": "fixture.pdf", "file_data": "data:application/pdf;base64," + data}},
		{"anthropic-messages", map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": data}}},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			body := strictBudgetBody(tc.protocol)
			field := "messages"
			if tc.protocol == "openai-responses" {
				field = "input"
			}
			body[field] = []any{map[string]any{"role": "user", "content": []any{tc.part}}}
			plan, _ := strictBudgetMustPlan(t, strictBudgetDeployment(), body, tc.protocol)
			if !plan.frozen.InputImagesIncluded || plan.InputBound != 100 {
				t.Fatal("PDF 未按多模态完整窗口预占")
			}
			if _, err := plan.Charge(catalog.Usage{PromptTokens: 50, CompletionTokens: 1}); err != nil {
				t.Fatalf("PDF 实测用量计费失败: %v", err)
			}
			body[field] = []any{map[string]any{"role": "assistant", "content": []any{tc.part}}}
			_, _, err := StrictBudgetPlan(strictBudgetDeployment(), strictBudgetEncode(t, body), tc.protocol, plan.frozen.StartedAt)
			strictBudgetExpectError(t, err, "unsupported_budget")
		})
	}
}
