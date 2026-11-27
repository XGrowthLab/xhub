package dialogue

import (
	"encoding/json"
	"fmt"
	"math"

	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
)

// nativeMessagesStream 为标准 Messages 入口及 Messages→Responses 转换累积可校验的原生快照。
// 每请求独占；上游序号用来验证生命周期，blocks 按出现顺序紧凑保存，签名和密文原样累积，终态前不得提交会话归属。
type nativeMessagesStream struct {
	message       map[string]any
	blocks        []any
	upstreamIndex int
	blockOpen     bool
	partialJSON   string
	usage         map[string]any
	stop          string
}

// consume 校验并累积一条 Messages 原生事件，返回终态结果、是否完成和错误。
// 标准入口流转发和跨协议观测调用；参数为本次已解码事件，允许修改私有快照；不写 HTTP、不访问存储。
// 首块从零开始，后续已闭合块间可跳号；重复或倒序、错类型增量、未闭合工具参数或不完整生命周期都拒绝。
func (s *nativeMessagesStream) consume(event map[string]any) (llmdialogue.DialogueResult, bool, error) {
	fail := func(reason string) (llmdialogue.DialogueResult, bool, error) {
		return llmdialogue.DialogueResult{}, false, fmt.Errorf("invalid native Messages stream: %s", reason)
	}
	typ := streamString(event["type"])
	if typ == "ping" {
		return llmdialogue.DialogueResult{}, false, nil
	}
	if upstreamErr := dpdiagnostics.ExtractUpstreamError(event); upstreamErr != nil {
		return llmdialogue.DialogueResult{}, false, upstreamErr
	}
	if typ == "message_start" {
		m := streamMap(event["message"])
		if s.message != nil || streamString(m["id"]) == "" || m["role"] != "assistant" || m["type"] != "message" {
			return fail("invalid message_start")
		}
		if content, ok := m["content"].([]any); !ok || len(content) != 0 {
			return fail("message_start content must be empty")
		}
		s.message = m
		// 合法拒绝可不产生任何内容块；必须回构 [] 而不是 nil 编码出的 null，否则终态会被自己的解析器拒绝。
		s.blocks = []any{}
		s.upstreamIndex = -1
		s.usage = llmdialogue.MergeDialogueUsage(nil, streamMap(m["usage"]))
		return llmdialogue.DialogueResult{}, false, nil
	}
	if s.message == nil {
		return fail("event before message_start")
	}
	switch typ {
	case "content_block_start", "content_block_delta", "content_block_stop":
		number, ok := event["index"].(float64)
		if !ok || number < 0 || number > 1<<53-1 || number > float64(math.MaxInt) || number != math.Trunc(number) {
			return fail(fmt.Sprintf("%s.index must be a nonnegative safe integer", typ))
		}
		index := int(number)
		if typ == "content_block_start" {
			if s.blockOpen {
				return fail(fmt.Sprintf("content_block_start.index %d before content_block_stop.index %d", index, s.upstreamIndex))
			}
			if s.stop != "" {
				return fail(fmt.Sprintf("content_block_start.index %d after message_delta.stop_reason", index))
			}
			if s.upstreamIndex == -1 && index != 0 {
				return fail(fmt.Sprintf("first content_block_start.index must be 0, got %d", index))
			}
			if index <= s.upstreamIndex {
				return fail(fmt.Sprintf("content_block_start.index %d must be greater than previous index %d", index, s.upstreamIndex))
			}
			block := streamMap(event["content_block"])
			switch block["type"] {
			case "text":
				if _, ok := block["text"].(string); !ok {
					return fail("text must be a string")
				}
			case "thinking":
				if _, ok := block["thinking"].(string); !ok {
					return fail("thinking must be a string")
				}
			case "redacted_thinking":
				if data, ok := block["data"].(string); !ok || data == "" {
					return fail("redacted data must be nonempty")
				}
			case "tool_use":
				if streamString(block["id"]) == "" || streamString(block["name"]) == "" || streamMap(block["input"]) == nil {
					return fail("invalid tool start")
				}
			default:
				return fail("unsupported content block")
			}
			s.upstreamIndex, s.blockOpen = index, true
			s.partialJSON = ""
			s.blocks = append(s.blocks, block)
		} else {
			if !s.blockOpen {
				return fail(fmt.Sprintf("%s.index %d has no open content block", typ, index))
			}
			if s.upstreamIndex != index {
				return fail(fmt.Sprintf("%s.index %d does not match open content block index %d", typ, index, s.upstreamIndex))
			}
			// 序号只用于匹配流事件，不能作为内容数组下标，否则跳号会在快照中制造空块。
			block := streamMap(s.blocks[len(s.blocks)-1])
			if typ == "content_block_stop" {
				if block["type"] == "tool_use" && s.partialJSON != "" {
					var input map[string]any
					if json.Unmarshal([]byte(s.partialJSON), &input) != nil || input == nil {
						return fail("invalid tool arguments")
					}
					block["input"] = input
				}
				s.blockOpen = false
				s.partialJSON = ""
			} else {
				d := streamMap(event["delta"])
				field, key := "", ""
				switch d["type"] {
				case "text_delta":
					if block["type"] != "text" {
						return fail("text delta on other block")
					}
					field, key = "text", "text"
				case "thinking_delta":
					if block["type"] != "thinking" {
						return fail("thinking delta on other block")
					}
					field, key = "thinking", "thinking"
				case "signature_delta":
					if block["type"] != "thinking" {
						return fail("signature delta on other block")
					}
					field, key = "signature", "signature"
				case "input_json_delta":
					if block["type"] != "tool_use" {
						return fail("tool delta on other block")
					}
					key = "partial_json"
				default:
					return fail("unsupported content delta")
				}
				value, ok := d[key].(string)
				if !ok {
					return fail("delta must be a string")
				}
				if field == "" {
					s.partialJSON += value
				} else {
					old := block[field]
					if old != nil {
						if _, ok := old.(string); !ok {
							return fail("invalid initial content")
						}
					}
					block[field] = streamString(old) + value
				}
			}
		}
	case "message_delta":
		if s.blockOpen {
			return fail(fmt.Sprintf("message_delta before content_block_stop.index %d", s.upstreamIndex))
		}
		delta := streamMap(event["delta"])
		if value := delta["stop_reason"]; value != nil {
			reason, ok := value.(string)
			if !ok || reason == "" {
				return fail("invalid stop_reason")
			}
			if s.stop != "" && s.stop != reason {
				return fail("conflicting stop_reason")
			}
			s.stop = reason
		}
		for k, v := range delta {
			s.message[k] = v
		}
		s.usage = llmdialogue.MergeDialogueUsage(s.usage, streamMap(event["usage"]))
	case "message_stop":
		if s.blockOpen || s.stop == "" {
			return fail("message_stop without completed blocks and stop_reason")
		}
		s.message["content"], s.message["usage"] = s.blocks, s.usage
		raw, err := json.Marshal(s.message)
		if err != nil {
			return llmdialogue.DialogueResult{}, false, err
		}
		result, err := llmdialogue.ParseDialogueResult("anthropic-messages", raw)
		return result, err == nil, err
	default:
		return fail("unsupported event")
	}
	return llmdialogue.DialogueResult{}, false, nil
}
