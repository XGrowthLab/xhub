package dialogue

import (
	"fmt"
	"math"
	"strings"
)

// projectChatToolIndices 校验并移除共享投影中的工具索引；参数为原生正文的独立消息副本，返回字段错误。
// ParseDialogue 调用；同步上游和 SDK 累积后的 index 是位置元数据，工具关联仍使用 id；同协议原文不修改。
func projectChatToolIndices(message map[string]any) error {
	for _, value := range dialogueList(message["tool_calls"]) {
		call := dialogueMap(value)
		if index, exists := call["index"]; exists {
			n, ok := dialogueNumber(index)
			if !ok || n < 0 || math.Trunc(n) != n {
				return fmt.Errorf("tool call index must be a nonnegative integer")
			}
			delete(call, "index")
		}
	}
	return nil
}

// projectMessagesCaller 校验传统直接工具来源并从共享投影移除；参数为块副本，返回非法或不支持来源错误。
// 请求/响应解析共用；原生正文仍保留 caller，不把程序化调用来源静默伪装成普通函数。
func projectMessagesCaller(block map[string]any) error {
	value, exists := block["caller"]
	if !exists {
		return nil
	}
	caller := dialogueMap(value)
	if block["type"] != "tool_use" || caller == nil {
		return fmt.Errorf("tool caller requires tool_use object")
	}
	if err := dialogueFields(caller, "type"); err != nil {
		return err
	}
	if caller["type"] != "direct" {
		return fmt.Errorf("unsupported tool caller; only direct tool calls are supported")
	}
	delete(block, "caller")
	return nil
}

// projectMessagesResult 提取原生 Messages 的公开正文和工具投影，保留签名/密文的同协议输出限制。
// 参数为可修改的响应副本，返回公开推理、跨协议拒绝原因和字段错误；ParseDialogueResult 调用，无外部副作用。
func projectMessagesResult(response map[string]any) (string, string, error) {
	parts, ok := response["content"].([]any)
	if !ok {
		return "", "", fmt.Errorf("invalid upstream content blocks")
	}
	projected := []any{}
	reasoning, bridgeError := "", ""
	for _, part := range parts {
		block := dialogueMap(part)
		if block == nil {
			return "", "", fmt.Errorf("invalid upstream content block")
		}
		switch block["type"] {
		case "thinking", "redacted_thinking":
			fields := []string{"type", "thinking", "signature"}
			if block["type"] == "redacted_thinking" {
				fields = []string{"type", "data"}
			}
			if err := dialogueFields(block, fields...); err != nil {
				return "", "", err
			}
			turn, err := messagesReasoningReplay(block, "assistant")
			if err != nil {
				return "", "", err
			}
			if turn.Native != nil {
				bridgeError = "opaque Messages thinking requires native Messages"
			} else {
				reasoning += turn.Reasoning
			}
		default:
			if err := projectMessagesCaller(block); err != nil {
				return "", "", err
			}
			projected = append(projected, block)
		}
	}
	response["content"] = projected
	return reasoning, bridgeError, nil
}

// projectResponsesResult 提取 Responses 的共享正文与公开推理，同时保留原始响应供同协议使用。
// 参数为独立响应副本。返回公开推理、消息独有的推理、跨协议拒绝原因及字段错误，供 ParseDialogueResult 调用。
// 供应商会在独立 reasoning 项与消息中重复给出 reasoning_text；去掉镜像文本，保留不同内容，不把推理拼入正文。
func projectResponsesResult(response map[string]any) (reasoning, messageReasoning, bridgeError string, err error) {
	list, ok := response["output"].([]any)
	if !ok {
		return "", "", "", fmt.Errorf("invalid upstream output")
	}
	items := []any{}
	var standalone strings.Builder
	var messageTexts []string
	standaloneTexts := map[string]bool{}
	for i, value := range list {
		item := dialogueMap(value)
		if item == nil {
			return "", "", "", fmt.Errorf("invalid upstream output item")
		}
		switch item["type"] {
		case "message", "function_call":
			if item["namespace"] != nil && item["namespace"] != "" {
				bridgeError = "namespaced function call"
			}
			if item["type"] == "message" {
				if parts, ok := item["content"].([]any); ok {
					parts, text, err := splitResponsesReasoningText(parts, fmt.Sprintf("output[%d].content", i))
					if err != nil {
						return "", "", "", err
					}
					content := []any{}
					for _, part := range parts {
						block := dialogueMap(part)
						if block["type"] == "refusal" {
							if _, ok := block["refusal"].(string); !ok {
								return "", "", "", fmt.Errorf("invalid refusal")
							}
							bridgeError = "refusal output"
						} else {
							content = append(content, part)
						}
					}
					item["content"] = content
					if text != "" {
						messageTexts = append(messageTexts, text)
					}
				}
			}
			items = append(items, item)
		case "reasoning":
			if dialogueString(item["id"]) == "" {
				return "", "", "", fmt.Errorf("reasoning item ID required")
			}
			// 校验独立项，密文仍由原协议或 Messages 封装承载。
			block, err := MessagesReasoningBlock(item)
			if err != nil {
				return "", "", "", fmt.Errorf("output[%d]: %w", i, err)
			}
			text := dialogueString(block["thinking"])
			standalone.WriteString(text)
			standaloneTexts[text] = true
		default:
			bridgeError = "output item " + dialogueString(item["type"])
		}
	}
	response["output"] = items
	var unique strings.Builder
	for _, text := range messageTexts {
		if text != standalone.String() && !standaloneTexts[text] {
			unique.WriteString(text)
		}
	}
	return standalone.String() + unique.String(), unique.String(), bridgeError, nil
}
