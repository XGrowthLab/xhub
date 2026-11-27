package dialogue

import (
	"fmt"
	"strings"
)

// ResponsesReasoningSignaturePrefix 区分网关封装的 Responses 密文与 Anthropic 原厂签名。
// 参考 LiteLLM encrypted_reasoning_signature 的不透明往返；不是签名生成或解密，连接归属由路由层校验。
const ResponsesReasoningSignaturePrefix = "xhub_responses_reasoning:"

// IsResponsesReasoningSignature 判断任意客户端值是否声明网关密文封装；路由层据此要求固定连接。
// 参数为 signature/data，返回是否有保留前缀；空载荷也返回 true，随后解析器会拒绝，不能降级为普通请求。
func IsResponsesReasoningSignature(value any) bool {
	s, ok := value.(string)
	return ok && strings.HasPrefix(s, ResponsesReasoningSignaturePrefix)
}

// MessagesReasoningBlock 将一项 Responses 推理转换为 Messages thinking 或 redacted_thinking。
// 参数为上游原生项，返回独立内容块、无可见内容时的 nil 或字段错误；同步和 SSE 共用，无输入修改。
// 保留公开摘要及密文，或在没有两者时使用显式 reasoning_text；不展示密文或伪造 Anthropic 的厂商签名。
func MessagesReasoningBlock(item map[string]any) (map[string]any, error) {
	var parts []string
	if value, exists := item["summary"]; exists {
		list, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("invalid reasoning summary")
		}
		for _, value := range list {
			part := dialogueMap(value)
			text, ok := part["text"].(string)
			if !ok || part["type"] != "summary_text" {
				return nil, fmt.Errorf("invalid reasoning summary text")
			}
			parts = append(parts, text)
		}
	}
	text := strings.Join(parts, "\n\n")
	cipher := ""
	if value := item["encrypted_content"]; value != nil {
		var ok bool
		cipher, ok = value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid encrypted reasoning content")
		}
	}
	if content := item["content"]; content != nil {
		list, ok := content.([]any)
		if !ok {
			return nil, fmt.Errorf("reasoning content must be an array")
		}
		var reasoning strings.Builder
		for i, value := range list {
			part, err := responsesReasoningText(value)
			if err != nil {
				return nil, fmt.Errorf("content[%d]: %w", i, err)
			}
			reasoning.WriteString(part)
		}
		if text == "" && cipher == "" {
			text = reasoning.String()
		}
	}
	if cipher != "" {
		if text == "" {
			return map[string]any{"type": "redacted_thinking", "data": ResponsesReasoningSignaturePrefix + cipher}, nil
		}
		return map[string]any{"type": "thinking", "thinking": text, "signature": ResponsesReasoningSignaturePrefix + cipher}, nil
	}
	if text == "" {
		return nil, nil
	}
	return map[string]any{"type": "thinking", "thinking": text, "signature": ""}, nil
}

// responsesReasoningText 读取 Responses 的显式文本推理片段，供共享消息与独立推理项解析调用。
// 参数为原始内容块，返回公开文本或字段错误；不接受未知字段、密文或隐式类型转换，不修改内容块。
func responsesReasoningText(value any) (string, error) {
	part := dialogueMap(value)
	if part["type"] != "reasoning_text" {
		return "", fmt.Errorf("expected reasoning_text content")
	}
	if err := dialogueFields(part, "type", "text"); err != nil {
		return "", fmt.Errorf("invalid reasoning_text: %w", err)
	}
	text, ok := part["text"].(string)
	if !ok {
		return "", fmt.Errorf("reasoning_text.text must be a string")
	}
	return text, nil
}

// splitResponsesReasoningText 从消息内容提取显式 reasoning_text，保留其他内容供原来的严格解析。
// 参数为片段数组和字段路径，返回非推理内容、公开推理或带下标的字段错误。请求历史与响应投影调用，不改写原数据。
func splitResponsesReasoningText(parts []any, path string) ([]any, string, error) {
	content := make([]any, 0, len(parts))
	var text strings.Builder
	for i, part := range parts {
		if dialogueMap(part)["type"] != "reasoning_text" {
			content = append(content, part)
			continue
		}
		reason, err := responsesReasoningText(part)
		if err != nil {
			return nil, "", fmt.Errorf("%s[%d]: %w", path, i, err)
		}
		text.WriteString(reason)
	}
	return content, text.String(), nil
}

// messagesReasoningReplay 还原网关封装的单个 thinking/redacted 块为 Responses 原生历史项。
// 参数为客户端内容块和角色，返回受限 Turn 或错误；网关封装还原到 Responses，原厂签名仅能原样回到 Messages。
// 与 LiteLLM 一样省略重建项的 ID，避免把虚构 ID 或跨账户 ID 传给上游；密文原字节保留。
func messagesReasoningReplay(block map[string]any, role string) (Turn, error) {
	if role != "assistant" {
		return Turn{}, fmt.Errorf("thinking requires assistant text")
	}
	value := block["signature"]
	text := ""
	if block["type"] == "redacted_thinking" {
		value = block["data"]
	} else {
		var ok bool
		text, ok = block["thinking"].(string)
		if !ok {
			return Turn{}, fmt.Errorf("thinking requires assistant text")
		}
	}
	if value == nil || value == "" {
		if block["type"] == "redacted_thinking" {
			return Turn{}, fmt.Errorf("redacted thinking requires Responses envelope")
		}
		return Turn{Role: "reasoning", Reasoning: text}, nil
	}
	if !IsResponsesReasoningSignature(value) {
		if _, ok := value.(string); !ok {
			return Turn{}, fmt.Errorf("thinking signature/data must be a string")
		}
		copy, err := cloneObject(block)
		if err != nil {
			return Turn{}, err
		}
		// 原生签名和密文只保存在受协议约束的历史中；不能当作公开推理跨协议发送。
		// 网关路由另行验证身份、会话及原连接，本层只保证无损保留和跨协议拒绝。
		return Turn{Role: "reasoning", NativeProtocol: "anthropic-messages", Native: map[string]any{
			"role": "assistant", "content": []any{copy},
		}}, nil
	}
	cipher := strings.TrimPrefix(value.(string), ResponsesReasoningSignaturePrefix)
	if cipher == "" {
		return Turn{}, fmt.Errorf("encrypted reasoning envelope is empty")
	}
	summary := []any{}
	if text != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": text})
	}
	return Turn{Role: "reasoning", NativeProtocol: "openai-responses", Native: map[string]any{
		"type": "reasoning", "summary": summary, "encrypted_content": cipher,
	}}, nil
}

// messagesReasoningContent 为同步 Messages 输出收集保持分项边界的推理块。
// 参数为已校验上游结果，返回内容数组或错误；Responses 保留原项封装及独立消息中的公开推理，其他协议沿用公开文本。
func messagesReasoningContent(result DialogueResult) ([]any, error) {
	content := []any{}
	if result.Source == "openai-responses" {
		for _, value := range dialogueList(result.Native["output"]) {
			item := dialogueMap(value)
			if item["type"] != "reasoning" {
				continue
			}
			block, err := MessagesReasoningBlock(item)
			if err != nil {
				return nil, err
			}
			if block != nil {
				content = append(content, block)
			}
		}
		if result.ResponsesMessageReasoning != "" {
			content = append(content, map[string]any{"type": "thinking", "thinking": result.ResponsesMessageReasoning, "signature": ""})
		}
	} else if result.Reasoning != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": result.Reasoning, "signature": ""})
	}
	return content, nil
}
