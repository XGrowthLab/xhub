// Package httpx writes JSON bodies and error envelopes, and it stamps a call ID on the response. Inference paths use the provider's own error shape.
package util

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

var logTraceOnceHttpx sync.Once

// CallID returns a new 32-character hexadecimal call identifier.
// 参数：无。
// 返回 string（string）：新的 32 位十六进制调用 id。
// 调用：dataplane/official.go、dataplane/serve.go、gateway/catalog.go、gateway/engine.go
// 测试：无直接单测
func CallID() string {
	logTraceOnceHttpx.Do(func() { Trace("enter util.CallID") })

	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// SetCallID writes x-litellm-call-id. If a value is already set, the caller decides whether to overwrite it.
// 参数 w（http.ResponseWriter）：调用方的 HTTP 响应，状态码和正文写在这里；id（string）：这一次调用的 id。空串表示还没有分配。
// 返回：无。调用 id 写在响应头 x-litellm-call-id 上，不另写正文。
// 调用：dataplane/official.go、dataplane/serve.go、gateway/catalog.go、gateway/engine.go
// 测试：无直接单测
func SetCallID(w http.ResponseWriter, id string) {
	w.Header().Set("x-litellm-call-id", id)
}

// WriteError writes the gateway JSON error. Inference paths should call WriteTypedError so the provider envelope is used instead.
// 参数 w（http.ResponseWriter）：调用方的 HTTP 响应，状态和正文写在这里；status（int）：HTTP 状态码；typ（string）：错误类型或分类名，写进响应或用来选择分支；msg（string）：给调用方或日志的说明。
// 返回：无。错误状态码和 JSON 正文写进响应。
// 调用：gateway/access.go、gateway/catalog.go、gateway/config_overrides.go、gateway/engine.go
// 测试：无直接单测
func WriteError(w http.ResponseWriter, status int, typ, msg string) {
	WriteTypedError(w, "", status, typ, msg)
}

// WriteTypedError picks the error envelope from the request path. Status 429 also sets Retry-After to 1. A missing call ID is filled in.
// 参数 w（http.ResponseWriter）：调用方的 HTTP 响应，状态和正文写在这里；path（string）：URL 路径，用来匹配路由；status（int）：HTTP 状态码；typ（string）：错误类型或分类名，写进响应或用来选择分支；msg（string）：给调用方或日志的说明。
// 返回：无。按路径选择错误包络，把状态码和 JSON 正文写进响应。
// 调用：dataplane/official.go、dataplane/serve.go、gateway/limits.go、gateway/models/available.go
// 测试：无直接单测
func WriteTypedError(w http.ResponseWriter, path string, status int, typ, msg string) {
	WriteTypedFieldError(w, path, status, typ, msg, "")
}

// WriteTypedFieldError 写入现有协议错误包络。field 是已脱敏的请求字段路径，Chat/Responses 同时填入 error.param。
// 兼容拒绝时调用，其他协议由 msg 说明字段；空字段保持原来的响应形状。它只写响应，不保存请求。
func WriteTypedFieldError(w http.ResponseWriter, path string, status int, typ, msg, field string) {
	WriteTypedReasonError(w, path, status, typ, msg, field, "")
}

// WriteTypedReasonError 写入可由客户端处理的稳定原因。w、path、status、typ、msg、field 沿用协议错误接口，reason 为空时保留原形状。
// 数据面准入、选路和出站故障调用；无返回值，保留 HTTP 状态、原 error.type/code 和调用 ID，仅新增 error.reason。
func WriteTypedReasonError(w http.ResponseWriter, path string, status int, typ, msg, field, reason string) {
	if w.Header().Get("x-litellm-call-id") == "" {
		SetCallID(w, CallID())
	}
	if w.Header().Get("x-litellm-version") == "" {
		w.Header().Set("x-litellm-version", "xhub-dev")
	}
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_, _ = w.Write(TypedReasonErrorBody(path, status, typ, msg, field, reason))
}

// TypedErrorBody 根据入站路径、状态、错误类型、消息及可选字段生成网关错误正文。
// 返回包含行尾换行的 JSON；供网关响应和失败日志共用，确保两处使用同一错误包络。只编码固定结构和标量，不写响应。
func TypedErrorBody(path string, status int, typ, msg, field string) []byte {
	return TypedReasonErrorBody(path, status, typ, msg, field, "")
}

// TypedReasonErrorBody 返回带可选稳定原因的协议错误 JSON。参数与 WriteTypedReasonError 一致。
// 数据面响应与落库共用；空原因保留旧字段。不读请求、不写响应，也不改变供应商原始错误。
func TypedReasonErrorBody(path string, status int, typ, msg, field, reason string) []byte {
	p := strings.ToLower(path)
	var doc map[string]any
	switch {
	case isAnthropicMessagesPath(p):
		doc = map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    typ,
				"message": msg,
			},
		}
	case isGeminiNativePath(p):
		doc = map[string]any{
			"error": map[string]any{
				"code":    status,
				"message": msg,
				"status":  googleRPCStatus(status),
			},
		}
	default:
		var param any
		if field != "" {
			param = field
		}
		doc = map[string]any{
			"error": map[string]any{
				"message": msg,
				"type":    typ,
				"param":   param,
				"code":    strconv.Itoa(status),
			},
		}
	}
	if reason != "" {
		doc["error"].(map[string]any)["reason"] = reason
	}
	body, _ := json.Marshal(doc)
	return append(body, '\n')
}

// isAnthropicMessagesPath reports whether the path uses the Anthropic error envelope instead of the OpenAI error object.
// 参数 p（string）：入站 URL 路径。含 /messages，且不是 chat 或 /threads 时走 Anthropic 错误包络。
// 返回 bool（bool）：这条路径要使用 Anthropic 错误包络，而不是 OpenAI 错误对象时返回真。
// 调用：仅在 util.go 内使用
// 测试：无直接单测
func isAnthropicMessagesPath(p string) bool {
	if strings.Contains(p, "chat") || strings.Contains(p, "/threads") {
		return false
	}
	return strings.Contains(p, "/messages")
}

// isGeminiNativePath reports whether the path uses the native Gemini error shape.
// 参数 p（string）：入站 URL 路径。含 generatecontent、streamgeneratecontent，或含 counttokens 且不含 /messages 时走 Gemini 原生错误。
// 返回 bool（bool）：这条路径要使用 Gemini 原生错误形状时返回真。
// 调用：仅在 util.go 内使用
// 测试：无直接单测
func isGeminiNativePath(p string) bool {
	return strings.Contains(p, "generatecontent") ||
		strings.Contains(p, "streamgeneratecontent") ||
		(strings.Contains(p, "counttokens") && !strings.Contains(p, "/messages"))
}

// googleRPCStatus maps an HTTP status code to a Google RPC status name.
// 参数 code（int）：错误码或业务码。
// 返回 string（string）：HTTP 状态码对应的 Google RPC 状态名。
// 调用：仅在 util.go 内使用
// 测试：无直接单测
func googleRPCStatus(code int) string {
	switch code {
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "PERMISSION_DENIED"
	case 404:
		return "NOT_FOUND"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 400:
		return "INVALID_ARGUMENT"
	case 502, 503:
		return "UNAVAILABLE"
	default:
		if code >= 500 {
			return "INTERNAL"
		}
		return "INVALID_ARGUMENT"
	}
}

// WriteJSON sets the status code and writes JSON. A later encoding failure does not change the status that was already sent.
// 参数 w（http.ResponseWriter）：调用方的 HTTP 响应，状态和正文写在这里；status（int）：HTTP 状态码；v（any）：JSON 里读出的动态值。数字、字符串和对象都要接住，类型不符时按零值而不是 panic。
// 返回：无。状态码和 JSON 正文写进响应。
// 调用：gateway/access.go、gateway/bypass.go、gateway/catalog.go、gateway/config_overrides.go
// 测试：无直接单测
func WriteJSON(w http.ResponseWriter, status int, v any) {
	if w.Header().Get("x-litellm-call-id") == "" {
		SetCallID(w, CallID())
	}
	if w.Header().Get("x-litellm-version") == "" {
		w.Header().Set("x-litellm-version", "xhub-dev")
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
