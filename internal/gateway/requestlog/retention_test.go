package requestlog

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSummarizeLegacyDiagnostic 验证旧错误副本被限制，其他字段保持原值。
// 使用内存合成内容，覆盖缺失、无效和有正文的诊断；无持久资源需要清理。
func TestSummarizeLegacyDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		raw        string
		recognized bool
	}{
		{"", true}, {`{"status_code":503}`, true}, {`{"error":null}`, true},
		{`invalid`, false}, {`["unknown"]`, false},
	} {
		if value, replace, ok := summarizeLegacyDiagnostic(tc.raw); value != "" || replace || ok != tc.recognized {
			t.Fatalf("诊断兼容分支错误: raw=%q value=%q replace=%t recognized=%t", tc.raw, value, replace, ok)
		}
	}
	value, replace, ok := summarizeLegacyDiagnostic(`{"status_code":503,"usage":{"output_tokens":12},"future":{"nested":true},"error":{"error":{"code":"capacity","message":"busy","payload":{"text":"private-marker"}}}}`)
	var raw map[string]json.RawMessage
	err := json.Unmarshal([]byte(value), &raw)
	if !replace || !ok || err != nil || strings.Contains(value, "private-marker") ||
		!strings.Contains(string(raw["error"]), "capacity") || string(raw["status_code"]) != "503" ||
		string(raw["usage"]) != `{"output_tokens":12}` || string(raw["future"]) != `{"nested":true}` {
		t.Fatalf("诊断没有正确保留可观测字段: %s", value)
	}
}
