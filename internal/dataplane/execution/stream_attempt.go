package execution

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	dpstream "github.com/sunqirui1987/xhub/internal/dataplane/stream"
	"github.com/sunqirui1987/xhub/internal/util"
)

// streamAttempt 保存统一入口单次流式尝试的观察事实，仅由当前请求创建和消费。
// 上游状态、客户端是否开始接收、计时、用量、正文和错误各自独立；nil 错误不证明所有协议都有成功终态。
// 它不改变协议转换、请求取消、预算、重试策略或日志存储，也不跨尝试共享。
type streamAttempt struct {
	upstreamStatus int
	clientStarted  bool
	usage          map[string]any
	ttft           time.Duration
	body           []byte
	err            error
}

// mustStopAfterFailure 在流处理失败时判断是否必须结束本次请求，供 Serve 的既有失败分支使用。
// preparationFailed 表示终态前历史保存失败，nativeResponses 表示不可盲目重放的原生响应；返回 true 表示不得重试。
// 入站取消另由调用方优先处理；客户端写入失败即使未报告字节也不能安全重放，成功或空错误返回 false。
func (a streamAttempt) mustStopAfterFailure(preparationFailed, nativeResponses bool) bool {
	return a.err != nil && (a.clientStarted || preparationFailed || nativeResponses || errors.Is(a.err, dpstream.ErrClientWrite))
}

// initialFailureBody 为因故必须结束的流生成首帧之前的 HTTP 错误，供 Serve 同时响应客户端和保存诊断。
// path 决定网关错误的协议形状，nativeResponses 表示原生响应不可重放，diagnostic 保存已观察的上游事实。
// 成功、已输出或写客户端失败时返回 nil。供应商明确的原生错误保留原字段，本地错误才写入网关原因。
func (a streamAttempt) initialFailureBody(path string, nativeResponses bool, diagnostic *dpdiagnostics.UpstreamDiagnostics) []byte {
	if a.err == nil || a.clientStarted || errors.Is(a.err, dpstream.ErrClientWrite) {
		return nil
	}
	const status = http.StatusBadGateway
	if !nativeResponses {
		return util.TypedReasonErrorBody(path, status, "history_unavailable", dpdiagnostics.SafeErr(a.err), "", "history_unavailable")
	}
	var vendor *dpdiagnostics.UpstreamError
	if errors.As(a.err, &vendor) {
		if raw, err := json.Marshal(dpdiagnostics.ErrorEnvelope(vendor)); err == nil {
			return raw
		}
	}
	reason, detail := dpdiagnostics.UpstreamFaultFeedback(diagnostic, dpdiagnostics.SafeErr(a.err))
	if diagnostic != nil {
		diagnostic.Outcome = &dpdiagnostics.GatewayOutcome{Code: reason}
	}
	return util.TypedReasonErrorBody(path, status, "upstream_error", detail, "", reason)
}
