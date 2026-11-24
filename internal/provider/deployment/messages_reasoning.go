package deployment

import (
	"fmt"

	"github.com/sunqirui1987/xhub/internal/config"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
)

// ValidateMessagesReasoning 校验管理员明确认证的 Messages 推理桥接能力，供模型保存与候选筛选调用。
// 参数为部署，返回配置错误或nil；缺省不启用，不能根据型号名字推断第三方渠道支持adaptive。
func ValidateMessagesReasoning(m config.ModelEntry) error {
	value, exists := m.ModelInfo["messages_reasoning_mode"]
	if !exists || value == nil {
		return nil
	}
	mode, ok := value.(string)
	if !ok || (mode != "disabled" && mode != "adaptive") {
		return fmt.Errorf("invalid model_info.messages_reasoning_mode")
	}
	if mode == "adaptive" {
		t, ok := Execution(m)
		if !ok || t.Protocol != "anthropic-messages" {
			return fmt.Errorf("adaptive reasoning bridge requires a native Messages transport")
		}
	}
	return nil
}

// prepareMessagesReasoning 为认证部署提取Responses effort，保留所有未实现参数的原校验。
// 返回私有请求投影、待应用effort或错误；不改调用方历史，编码完成后才写入thinking/output_config。
// 缺省/显式none走原行为；非Responses入口和未认证部署不获得新增推理能力，避免半支持Chat签名回放。
func prepareMessagesReasoning(m config.ModelEntry, d llmdialogue.Dialogue) (llmdialogue.Dialogue, string, error) {
	if err := ValidateMessagesReasoning(m); err != nil {
		return d, "", err
	}
	if m.ModelInfo["messages_reasoning_mode"] != "adaptive" || d.Source != "openai-responses" {
		return d, "", nil
	}
	reasoning, _ := d.Native["reasoning"].(map[string]any)
	value := reasoning["effort"]
	if value == nil || value == "" || value == "none" {
		return d, "", nil
	}
	effort, ok := value.(string)
	if !ok || (effort != "low" && effort != "medium" && effort != "high" && effort != "max") {
		return d, "", &llmdialogue.RequestFieldError{Field: "reasoning.effort", Message: "reasoning effort is not supported by the adaptive Messages bridge; use low, medium, high, max or none"}
	}
	native := make(map[string]any, len(d.Native))
	for k, v := range d.Native {
		native[k] = v
	}
	copy := make(map[string]any, len(reasoning))
	for k, v := range reasoning {
		copy[k] = v
	}
	delete(copy, "effort")
	native["reasoning"] = copy
	d.Native = native
	return d, effort, nil
}
