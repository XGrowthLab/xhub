package diagnostics

import (
	"errors"
	"strings"
)

// ProtocolProblem 是可公开的字段定位。Target 仅包含登记的协议标识，Details 不含请求正文。
type ProtocolProblem struct {
	Field   string `json:"field,omitempty"`
	Target  string `json:"target_protocol,omitempty"`
	Details string `json:"details"`
}

// ProtocolRejection 保存请求实际发往上游前的兼容拒绝，便于响应和已授权调用日志使用同一证据。
type ProtocolRejection struct {
	Stage    string            `json:"stage"`
	Source   string            `json:"source_protocol"`
	Problems []ProtocolProblem `json:"problems"`
}

// NewProtocolProblem 将字段和可公开的能力说明脱敏、限长并归一为单行。调用方传入登记的目标协议。
// 请求值只允许协议枚举等已公开信息；构造器不读取或输出原始请求或连接配置。
func NewProtocolProblem(field, target, details string) ProtocolProblem {
	field = protocolDiagnosticText(field, 256)
	details = protocolDiagnosticText(details, 768)
	return ProtocolProblem{Field: field, Target: target, Details: details}
}

// protocolDiagnosticText 对来自错误的文本应用已有脱敏和长度限制；返回独立字符串。
func protocolDiagnosticText(text string, limit int) string {
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(SafeErr(errors.New(text))), " ")
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}
