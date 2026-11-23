package deployment

import (
	"errors"

	"github.com/sunqirui1987/xhub/internal/config"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
)

// CandidateDecision 保存同一入口的兼容性判断；Deployment 仅内部使用，禁止直接序列化凭据。
type CandidateDecision struct {
	Deployment config.ModelEntry
	Reason     string
	// CapabilityError 仅包含可公开的已知能力错误，其他排除原因只供诊断使用。
	CapabilityError error
}

// Candidates 对已匹配公开模型的部署执行目录、声明和请求能力筛选。
// 参数 list、endpoint、dialogue、nativeBody：部署、目录入口、可选已解析对话及可选原生正文；返回兼容部署与全部判断。
// 调用：实际统一选路、原生选路及只读预览；不附加凭据、不调用上游、不推进策略状态。
func Candidates(list []config.ModelEntry, endpoint string, dialogue *llmdialogue.Dialogue, nativeBody ...map[string]any) ([]config.ModelEntry, []CandidateDecision) {
	pool := make([]config.ModelEntry, 0, len(list))
	decisions := make([]CandidateDecision, 0, len(list))
	for _, dep := range list {
		err := AllowsEndpoint(dep, endpoint)
		if err == nil && dialogue != nil {
			_, _, err = EncodeDialogue(dep, *dialogue)
		}
		if err == nil && dialogue == nil && len(nativeBody) > 0 && dep.ModelInfo["web_search_support"] == "unsupported" {
			// 透传不能擅自删改工具；显式禁用时筛掉该部署，不能绕过普通接口的能力限制。
			protocol := ""
			switch endpoint {
			case "bypass:openai-responses":
				protocol = "openai-responses"
			case "bypass:openai-chat":
				protocol = "openai-chat"
			case "bypass:anthropic-messages":
				protocol = "anthropic-messages"
			}
			if llmdialogue.NativeWebSearchRequested(protocol, nativeBody[0]) {
				err = llmdialogue.ErrWebSearchUnsupported
			}
		}
		decision := CandidateDecision{Deployment: dep}
		if err != nil {
			decision.Reason = err.Error()
			var field *llmdialogue.RequestFieldError
			switch {
			case errors.As(err, &field):
				decision.CapabilityError = field
			case errors.Is(err, llmdialogue.ErrWebSearchUnsupported):
				decision.CapabilityError = llmdialogue.ErrWebSearchUnsupported
			case errors.Is(err, llmdialogue.ErrMessagesImageDetailUnsupported):
				decision.CapabilityError = llmdialogue.ErrMessagesImageDetailUnsupported
			}
		} else {
			pool = append(pool, dep)
		}
		decisions = append(decisions, decision)
	}
	return pool, decisions
}
