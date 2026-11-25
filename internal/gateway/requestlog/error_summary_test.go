package requestlog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestFailureSummaryPreservesCauseWithoutFullBody 验证上游错误摘要保留错误码和原因，不保存请求或生成正文。
// 输入为合成 JSON、流式正文和纯文本；纯函数不访问外部服务，也无需清理。
func TestFailureSummaryPreservesCauseWithoutFullBody(t *testing.T) {
	raw := `{"error":{"code":"vendor_busy","message":"Vendor is busy","request":{"messages":[{"content":"private-content"}]},"data":"private-data","payload":{"text":"private-payload"},"details":{"terminal_cause":"retry later","text":"private-detail"}},"choices":[{"text":"private-output"}]}`
	got := FailureSummary(raw)
	if !strings.Contains(got, "vendor_busy") || !strings.Contains(got, "Vendor is busy") || !strings.Contains(got, "retry later") || strings.Contains(got, "private-") {
		t.Fatalf("错误摘要不符合预期: %s", got)
	}
	got = FailureSummary("data: {\"choices\":[{\"delta\":{\"content\":\"private-output\"}}]}\n\n")
	if strings.Contains(got, "private-output") {
		t.Fatalf("流输出误写入错误摘要: %s", got)
	}
	got = FailureSummary("plain upstream failure")
	if !strings.Contains(got, "plain upstream failure") {
		t.Fatalf("纯文本原因丢失: %s", got)
	}
	if got := FailureSummary("invalid data: format"); got != "invalid data: format" {
		t.Fatalf("普通错误文本被误判为流式响应: %q", got)
	}
}

// TestFailureSummaryBoundsVendorExtensions 验证供应商大型扩展不会无限增长，仍保留错误码与原因。
// 输入仅为合成 JSON；内存测试无外部副作用。
func TestFailureSummaryBoundsVendorExtensions(t *testing.T) {
	errorFields := map[string]any{"code": "busy", "message": "Vendor busy"}
	for i := 0; i < 80; i++ {
		errorFields[fmt.Sprintf("extra_%03d", i)] = strings.Repeat("x", 5000)
	}
	raw, err := json.Marshal(map[string]any{"error": errorFields})
	if err != nil {
		t.Fatal(err)
	}
	got := FailureSummary(string(raw))
	if len(got) > 16<<10 || !json.Valid([]byte(got)) || !strings.Contains(got, "busy") || !strings.Contains(got, "Vendor busy") {
		t.Fatalf("大错误摘要无界或丢失核心原因: bytes=%d", len(got))
	}
	veryLarge := `{"error":{"message":"` + strings.Repeat("z", 1<<20) + `"}}`
	if got := FailureSummary(veryLarge); len(got) > 100 || !strings.Contains(got, "limit") {
		t.Fatalf("超大错误没有安全降级: length=%d", len(got))
	}
	longText := "error starts " + strings.Repeat("x", 8000) + " terminal cause"
	if got := FailureSummary(longText); !strings.Contains(got, "error starts") || !strings.Contains(got, "terminal cause") || !strings.Contains(got, "[truncated]") || len(got) >= len(longText) {
		t.Fatalf("长错误摘要未保留首尾或未限制长度: %s", got)
	}
}

// TestFailureSummaryGoogleFieldViolations 验证已知 Google 字段错误保留具体原因与字段，未声明的扩展和回显仍被丢弃。
// 参数 t 为测试上下文；使用内存错误 JSON，不访问外部供应商或写库，无需清理。
func TestFailureSummaryGoogleFieldViolations(t *testing.T) {
	raw := `{"error":{"code":400,"details":[{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"generationConfig.temperature","description":"Outside the range","reason":"INVALID_VALUE","input":"private-value"},{"field":{"data":"private-object"}},42]},{"@type":"custom","fieldViolations":[{"field":"private-other"}]}]}}`
	got := FailureSummary(raw)
	for _, want := range []string{"google.rpc.BadRequest", "generationConfig.temperature", "Outside the range", "INVALID_VALUE"} {
		if !strings.Contains(got, want) {
			t.Fatalf("字段诊断遗漏 %q: %s", want, got)
		}
	}
	if strings.Contains(got, "private-") {
		t.Fatalf("不应记录未知诊断或请求回显: %s", got)
	}
}
