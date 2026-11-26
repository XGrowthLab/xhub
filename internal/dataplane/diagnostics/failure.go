package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Failure 保存请求中断时可观测的事实，不根据 499 推断用户操作或供应商责任。
// Phase 指明读取入站正文、等待上游或传输响应；取消来源未知时固定标为 unknown。
// 错误文本经过已有脱敏器并限制长度；不保存额外正文、鉴权头或任意 context cause。
type Failure struct {
	Phase               string `json:"phase"`
	Kind                string `json:"kind"`
	TransportError      string `json:"transport_error,omitempty"`
	RequestContextError string `json:"request_context_error,omitempty"`
	CancellationSource  string `json:"cancellation_source,omitempty"`
	ElapsedMS           int64  `json:"elapsed_ms"`
	BytesRead           *int64 `json:"bytes_read,omitempty"`
	BodyLimit           int64  `json:"body_limit,omitempty"`
	// RequestBodyOmitted 表示读取失败的入站正文未保存；BytesRead仍记录实际读取量，避免误解为空请求。
	RequestBodyOmitted bool `json:"request_body_omitted,omitempty"`
}

// ObserveFailure 根据入站上下文和实际错误建立独立诊断；参数 elapsed 为请求已经历时间。
// 执行器及正文读取入口调用，返回纯值；只检查入站 ctx，不读取已主动释放的 attempt context。
// 没有读取到供应商错误时不构造供应商原因；nil 错误用于出站前发现取消的边界。
func ObserveFailure(ctx context.Context, phase string, err error, elapsed time.Duration) *Failure {
	f := &Failure{Phase: phase, Kind: "transport_error", ElapsedMS: elapsed.Milliseconds()}
	if err != nil {
		f.TransportError = SafeErr(err)
		if len(f.TransportError) > 1024 {
			f.TransportError = f.TransportError[:1024]
		}
	}
	var limit *http.MaxBytesError
	var timeout net.Error
	switch {
	case errors.As(err, &limit):
		f.Kind = "body_too_large"
		f.BodyLimit = limit.Limit
	case errors.Is(err, context.Canceled):
		f.Kind = "request_canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		f.Kind = "timeout"
	case errors.Is(err, io.ErrUnexpectedEOF):
		f.Kind = "unexpected_eof"
	}
	if ctx != nil && ctx.Err() != nil {
		f.RequestContextError = ctx.Err().Error()
	}
	if f.RequestContextError != "" || f.Kind == "request_canceled" {
		f.CancellationSource = "unknown"
	}
	return f
}

// BodyFailureStatus 将读取失败映射为 HTTP 状态和稳定错误类型；仅超限为413。
// 参数 f 来自 ObserveFailure，返回状态、类型和固定说明，Handler 调用；不修改规则或泄漏底层地址。
func BodyFailureStatus(f *Failure) (int, string, string) {
	if f.Kind == "body_too_large" {
		return 413, "request_body_too_large", "request body exceeds 64 MiB limit"
	}
	if f.Kind == "timeout" {
		return 408, "request_body_timeout", "request body read timed out"
	}
	if f.Kind == "unexpected_eof" {
		return 400, "request_body_read_error", "request body ended before Content-Length was received"
	}
	if f.Kind == "request_canceled" || f.RequestContextError == context.Canceled.Error() {
		return 499, "request_canceled", "request connection canceled while reading body; initiator unknown"
	}
	return 400, "request_body_read_error", "request body could not be read completely"
}

// UpstreamFaultFeedback 为网关生成的失败响应提供稳定原因和可操作说明。
// d 是已观察的上游诊断，fallback 是执行器已经过脱敏的协议校验说明；返回原因与文案。
// 仅根据传输阶段和已观察状态描述故障，不公开网络地址、不预测是否完成了上游请求，也不替换上游原始错误。
func UpstreamFaultFeedback(d *UpstreamDiagnostics, fallback string) (string, string) {
	if d != nil && d.Failure != nil {
		failure := d.Failure
		if failure.Kind == "timeout" {
			if failure.Phase == "response_transfer" {
				return "upstream_timeout", "The upstream response timed out before it was complete."
			}
			return "upstream_timeout", "Timed out waiting for a response from the upstream."
		}
		if failure.Phase == "response_transfer" {
			if d.StatusCode >= 400 {
				return "upstream_stream_interrupted", fmt.Sprintf("The upstream returned HTTP %d, then its error response was interrupted.", d.StatusCode)
			}
			return "upstream_stream_interrupted", "The upstream response ended before it was complete."
		}
	}
	if d != nil && d.StatusCode >= 200 && d.StatusCode < 300 && fallback != "" {
		return "upstream_invalid_response", fallback
	}
	return "upstream_transport_error", "The gateway could not receive a response from the upstream. Check the request ID with support if the problem persists."
}

// RecordEarlyFailure 将准入诊断保存到请求级响应记录器，供最外层错误落库使用。
// 参数 w 可含 Gin/缓冲包装，failure 为不含凭据的诊断；返回是否找到接收器。
// 只调用内部方法，不写响应头/正文。执行器的 ReleaseCall 会清理附注，所以提前拒绝不能使用全局 callNotes 暂存。
func RecordEarlyFailure(w http.ResponseWriter, failure *UpstreamDiagnostics) bool {
	for w != nil {
		if sink, ok := w.(interface{ SetFailureDiagnostics(*UpstreamDiagnostics) }); ok {
			sink.SetFailureDiagnostics(failure)
			return true
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = wrapper.Unwrap()
	}
	return false
}
