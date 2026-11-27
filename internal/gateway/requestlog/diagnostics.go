package requestlog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
)

// DiagnosticJSON 将传输事实和计费统计合并为持久化 JSON。
// 参数 upstream 为原始诊断，可为 nil；usage 为实际计费输入；返回 JSON 或空字符串。
// 调用：recordSpend；复制响应头后打码凭据，不修改原始对象，不保存生成内容。
func DiagnosticJSON(upstream *dpdiagnostics.UpstreamDiagnostics, usage map[string]any) string {
	if upstream == nil {
		return ""
	}
	saved := *upstream
	if upstream.Error != nil {
		if raw, err := json.Marshal(upstream.Error); err == nil {
			var summary map[string]any
			message := FailureSummary(string(raw))
			if json.Unmarshal([]byte(message), &summary) == nil {
				saved.Error = summary
			} else {
				saved.Error = map[string]any{"error": map[string]any{"message": message}}
			}
		}
	}
	saved.Headers = diagnosticHeaders(upstream.Headers)
	saved.Trailers = diagnosticHeaders(upstream.Trailers)
	saved.BillingUsage = usage
	raw, err := json.Marshal(saved)
	if err != nil {
		return ""
	}
	return string(raw)
}

// diagnosticHeaders 克隆并脱敏完整多值响应头；参数 headers 为上游头，返回独立副本。
// 调用：诊断持久化；保留全部名称、值数量与非敏感值，密钥和 Cookie 替换为星号。
func diagnosticHeaders(headers http.Header) http.Header {
	result := headers.Clone()
	for key, values := range result {
		for i, value := range values {
			if sensitiveHeader(key) {
				values[i] = "***"
			} else {
				values[i] = redactSecretText(value)
			}
		}
	}
	return result
}

// FailureJSON 选取流内明确错误、原失败正文或空正文时已观测到的诊断，避免失败日志只剩状态。
// 参数 upstream 为观测事实、fallback 为原失败正文；recordSpend 调用，返回日志错误详情，不修改客户端响应或正文捕获。
// 已识别的供应商错误或网关生成的明确正文优先；没有这些正文时使用观测到的读取错误或空正文说明。
func FailureJSON(upstream *dpdiagnostics.UpstreamDiagnostics, fallback string) string {
	// 取消不能把整个部分 SSE 当作错误原因；保留独立供应商错误，未观测到时显式为 null。
	if upstream != nil && upstream.Failure != nil && upstream.Failure.Kind == "request_canceled" {
		raw, err := json.Marshal(map[string]any{"error": map[string]any{"type": "request_canceled", "message": "request connection canceled; initiator unknown"}, "diagnostics": upstream.Failure, "upstream_error": upstream.Error})
		if err == nil {
			return string(raw)
		}
	}
	if upstream != nil && upstream.Failure != nil && upstream.Failure.Kind == "client_write_error" {
		// 写客户端失败与上游错误是独立事实，不把已观察到的部分生成正文冒充错误原因。
		raw, err := json.Marshal(map[string]any{"error": map[string]any{"type": "client_write_error", "message": "client response write failed; initiator unknown"}, "diagnostics": upstream.Failure, "upstream_error": upstream.Error})
		if err == nil {
			return string(raw)
		}
	}
	if upstream == nil {
		return fallback
	}
	if upstream.StatusCode < 400 && upstream.Error != nil {
		if raw, err := json.Marshal(upstream.Error); err == nil {
			return string(raw)
		}
	}
	if upstream.Outcome != nil && upstream.Outcome.Code != "" && strings.TrimSpace(fallback) != "" {
		// 执行器已返回可检索的网关错误；底层传输原因独立保存在诊断中。
		return fallback
	}
	if failure := upstream.Failure; failure != nil && failure.Phase == "response_transfer" && upstream.StatusCode < 400 && failure.TransportError != "" {
		// 已开始输出的流可能没有供应商错误帧；用观测到的中断原因解释失败，生成内容仍走正文策略。
		return failure.TransportError
	}
	if upstream.ReadError != "" && (upstream.StatusCode < 400 || strings.TrimSpace(fallback) == "") {
		// 截断的成功正文可能含模型输出，使用独立读取错误作为失败摘要。
		return upstream.ReadError
	}
	if upstream.StatusCode >= 400 && strings.TrimSpace(fallback) == "" {
		return fmt.Sprintf(`{"error":{"type":"upstream_empty_error_body","status_code":%d,"message":"upstream returned HTTP %d without an error body"}}`, upstream.StatusCode, upstream.StatusCode)
	}
	return fallback
}
