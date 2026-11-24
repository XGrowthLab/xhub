package requestlog

import (
	"encoding/json"
	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	"strings"
	"testing"
)

// TestFailureJSONPreservesTerminalError 验证超过正文捕获上限后的失败仍独立保留，正常/无诊断/非法快照回退原文。
// 参数 t 为上下文；内存夹具无外部状态，核对原 HTTP 错误不被改写、对象不被修改，无需清理。
func TestFailureJSONPreservesTerminalError(t *testing.T) {
	fallback := strings.Repeat("partial", 400000)
	detail := map[string]any{"error": map[string]any{"message": "terminal vendor cause", "code": "vendor_busy"}, "request_id": "req-original"}
	for _, tc := range []struct {
		name     string
		diag     *dpdiagnostics.UpstreamDiagnostics
		original bool
	}{
		{"missing", nil, true},
		{"success", &dpdiagnostics.UpstreamDiagnostics{StatusCode: 200}, true},
		{"http_failure", &dpdiagnostics.UpstreamDiagnostics{StatusCode: 503, Error: detail}, true},
		{"stream_failure", &dpdiagnostics.UpstreamDiagnostics{StatusCode: 200, Error: detail}, false},
		{"invalid", &dpdiagnostics.UpstreamDiagnostics{StatusCode: 200, Error: map[string]any{"bad": func() {}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FailureJSON(tc.diag, fallback)
			if tc.original {
				if got != fallback {
					t.Fatal("错误修改回退正文")
				}
			} else if !strings.Contains(got, "terminal vendor cause") || !strings.Contains(got, "req-original") {
				t.Fatalf("尾部错误丢失: %s", got)
			}
		})
	}
}

// TestFailureJSONMissingHTTPBody 验证空的上游 HTTP 错误和截断响应仍有摘要；现有错误正文优先。
// 使用内存诊断和片段，无外部资源；校验缺少上游状态时不制造 HTTP 错误。
func TestFailureJSONMissingHTTPBody(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		status                int
		readError, body, want string
	}{
		{"empty HTTP failure", 502, "", " \n", `{"error":{"type":"upstream_empty_error_body","status_code":502,"message":"upstream returned HTTP 502 without an error body"}}`},
		{"complete HTTP failure", 502, "", "vendor details", "vendor details"},
		{"interrupted HTTP failure", 400, "unexpected EOF", "", "unexpected EOF"},
		{"interrupted HTTP success", 200, "unexpected EOF", "private model output", "unexpected EOF"},
		{"no upstream status", 0, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FailureJSON(&dpdiagnostics.UpstreamDiagnostics{StatusCode: tc.status, ReadError: tc.readError}, tc.body)
			if got != tc.want {
				t.Fatalf("失败摘要不符合观测事实: got=%q want=%q", got, tc.want)
			}
		})
	}
}

// TestCancellationFailureJSON 保留独立上游错误且没有上游证据时明确为空，不再把部分生成内容当成错误；纯内存无清理。
func TestCancellationFailureJSON(t *testing.T) {
	for _, observed := range []bool{false, true} {
		d := &dpdiagnostics.UpstreamDiagnostics{StatusCode: 200, Failure: &dpdiagnostics.Failure{Kind: "request_canceled", Phase: "response_transfer", CancellationSource: "unknown"}}
		if observed {
			d.Error = map[string]any{"error": map[string]any{"code": "vendor_overloaded"}, "request_id": "vendor-trace"}
		}
		raw := FailureJSON(d, "data: partial generated content")
		var got map[string]any
		if json.Unmarshal([]byte(raw), &got) != nil || got["diagnostics"] == nil || (got["upstream_error"] != nil) != observed {
			t.Fatalf("取消证据丢失: %s", raw)
		}
	}
}

// TestFailureJSONStreamInterruption 验证没有供应商错误帧时仍保留已观察的中断原因，部分生成内容不进入摘要。
// 使用内存夹具，不需要清理。
func TestFailureJSONStreamInterruption(t *testing.T) {
	up := &dpdiagnostics.UpstreamDiagnostics{StatusCode: 200, Failure: &dpdiagnostics.Failure{
		Phase: "response_transfer", Kind: "unexpected_eof", TransportError: "upstream stream missing terminal event",
	}}
	if got := FailureJSON(up, "data: partial generated content"); got != up.Failure.TransportError {
		t.Fatalf("流中断原因丢失: %q", got)
	}
}

// TestClientWriteFailureJSON 验证客户端写失败单独记录，不把部分模型输出冒充错误或编造上游原因。
// 内存构造有无上游错误两种状态；只验证结构和原文保护，没有外部资源需要清理。
func TestClientWriteFailureJSON(t *testing.T) {
	for _, observed := range []bool{false, true} {
		d := &dpdiagnostics.UpstreamDiagnostics{StatusCode: 200, Failure: &dpdiagnostics.Failure{Kind: "client_write_error", Phase: "client_write"}}
		if observed {
			d.Error = map[string]any{"error": map[string]any{"code": "vendor_overloaded"}}
		}
		raw := FailureJSON(d, "data: partial generated content")
		var got map[string]any
		if json.Unmarshal([]byte(raw), &got) != nil || got["diagnostics"] == nil || (got["upstream_error"] != nil) != observed || strings.Contains(raw, "partial generated content") {
			t.Fatalf("客户端写失败证据错误: %s", raw)
		}
		if errObj, ok := got["error"].(map[string]any); !ok || errObj["type"] != "client_write_error" {
			t.Fatalf("客户端写失败类型丢失: %s", raw)
		}
	}
}
