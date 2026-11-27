package execution

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	dpstream "github.com/sunqirui1987/xhub/internal/dataplane/stream"
)

// TestStreamAttemptFailureBoundary 验证流失败后的既有重试边界，涵盖输出、历史准备和原生响应。
// 使用独立内存事实检查成功及各类失败，不访问网络或数据库，测试结束无需清理。
func TestStreamAttemptFailureBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                                             string
		started, preparationFailed, native, failed, want bool
	}{
		{"uncommitted_failure", false, false, false, true, false},
		{"committed_failure", true, false, false, true, true},
		{"history_failure", false, true, false, true, true},
		{"native_failure", false, false, true, true, true},
		{"successful_output", true, false, false, false, false},
		{"no_error_history", false, true, false, false, false},
		{"no_error_native", false, false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := streamAttempt{upstreamStatus: 200, clientStarted: tc.started}
			if tc.failed {
				a.err = errors.New("synthetic upstream failure")
			}
			if got := a.mustStopAfterFailure(tc.preparationFailed, tc.native); got != tc.want {
				t.Fatalf("失败流是否停止不符: got=%v want=%v", got, tc.want)
			}
		})
	}
}

// TestStreamAttemptClientWriteFailureNeverRetries 验证未写出字节的客户端写失败也不能重放。
// 内存尝试结果覆盖包装错误和无错误；不访问数据库或网络，无需清理。
func TestStreamAttemptClientWriteFailureNeverRetries(t *testing.T) {
	a := streamAttempt{err: errors.Join(dpstream.ErrClientWrite, errors.New("closed"))}
	if !a.mustStopAfterFailure(false, false) {
		t.Fatal("客户端写失败不应重试")
	}
	a.err = nil
	if a.mustStopAfterFailure(false, false) {
		t.Fatal("成功请求不能被误判为失败")
	}
}

// TestInitialStreamFailureBody 校验原生首帧错误与网关故障的 HTTP JSON 和匹配的诊断原因。
// 内存中构造供应商帧、协议读取中断、历史错误及不可再写的流；无网络或数据库资源，无需清理。
func TestInitialStreamFailureBody(t *testing.T) {
	const path = "/v1/responses"
	vendor := &dpdiagnostics.UpstreamError{Envelope: map[string]any{
		"error":       map[string]any{"type": "api_error", "code": "vendor_busy", "message": "Capacity exhausted"},
		"response_id": "resp_vendor",
	}}
	for _, tc := range []struct {
		name, protocol                        string
		attempt                               streamAttempt
		native                                bool
		kind, code, reason, ref, envelopeType string
	}{
		{name: "原生供应商错误", protocol: "openai-responses", attempt: streamAttempt{err: vendor}, native: true, kind: "api_error", code: "vendor_busy", ref: "resp_vendor"},
		{name: "消息历史兼容供应商错误", protocol: "anthropic-messages", attempt: streamAttempt{err: vendor}, native: true, kind: "api_error", code: "vendor_busy", ref: "resp_vendor", envelopeType: "error"},
		{name: "原生传输中断", attempt: streamAttempt{err: io.ErrUnexpectedEOF}, native: true, kind: "upstream_error", code: "502", reason: "upstream_stream_interrupted"},
		{name: "历史准备错误", attempt: streamAttempt{err: errors.New("history commit failed")}, kind: "history_unavailable", code: "502", reason: "history_unavailable"},
		{name: "正常流"},
		{name: "已开始发送", attempt: streamAttempt{err: vendor, clientStarted: true}, native: true},
		{name: "客户端写失败", attempt: streamAttempt{err: dpstream.ErrClientWrite}, native: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diagnostic := &dpdiagnostics.UpstreamDiagnostics{StatusCode: http.StatusOK, Failure: &dpdiagnostics.Failure{
				Phase: "response_transfer", Kind: "unexpected_eof", TransportError: "unexpected EOF",
			}}
			raw := tc.attempt.initialFailureBody(path, tc.protocol, tc.native, diagnostic)
			if tc.kind == "" {
				if raw != nil || diagnostic.Outcome != nil {
					t.Fatalf("不可写 HTTP 错误的流不应生成响应: %s", raw)
				}
				return
			}
			var doc struct {
				Error      struct{ Type, Code, Reason string }
				Type       string
				ResponseID string `json:"response_id"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil || doc.Error.Type != tc.kind ||
				doc.Error.Code != tc.code || doc.Error.Reason != tc.reason || doc.ResponseID != tc.ref || doc.Type != tc.envelopeType {
				t.Fatalf("首帧错误正文与观测事实不符: %s (%v)", raw, err)
			}
			if tc.native && tc.reason != "" {
				if diagnostic.Outcome == nil || diagnostic.Outcome.Code != tc.reason {
					t.Fatalf("网关原因未与客户端错误关联: %+v", diagnostic.Outcome)
				}
			} else if diagnostic.Outcome != nil {
				t.Fatalf("不应附加网关原因: %+v", diagnostic.Outcome)
			}
		})
	}
}
