package transport

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// 匹配原厂块路径或供应商脱敏路径。要求错误从此处开始，避免被引用的用户正文触发改写。
var anthropicThinkingSignatureError = regexp.MustCompile("(?i)^((messages\\.(?P<message>[0-9]+)|\\*\\*\\*\\.\\*\\*\\*)\\.content\\.(?P<block>[0-9]+):\\s*)?invalid `signature` in `thinking` block\\b")

// RecoverAnthropicThinking 为已被上游明确拒绝的 Messages 历史构造一次兼容请求。
// 参数为实际出站正文、上游状态及错误正文。仅 HTTP 400 的 thinking 签名错误可触发；
// 返回清理后的正文及成功标志，保持用户内容、已完成的工具记录和当前 thinking 设置。
// 原生执行器使用结果在原部署重试一次。工具续轮只有在错误明确定位到旧轮次时才允许恢复，
// 并保留当前工具轮次的完整历史。无可清理块或结构无法确认时返回 nil、false。
// 仅修改本次出站副本，不更改原请求或任何会话存储。
func RecoverAnthropicThinking(payload []byte, status int, response []byte) ([]byte, bool) {
	if status != http.StatusBadRequest {
		return nil, false
	}
	var rejection struct {
		Error struct {
			Message string
		}
	}
	if json.Unmarshal(response, &rejection) != nil {
		return nil, false
	}
	location := anthropicThinkingSignatureError.FindStringSubmatch(strings.TrimSpace(rejection.Error.Message))
	if location == nil {
		return nil, false
	}

	var request map[string]json.RawMessage
	var messages []map[string]json.RawMessage
	if json.Unmarshal(payload, &request) != nil || json.Unmarshal(request["messages"], &messages) != nil {
		return nil, false
	}
	scope, ok := anthropicCompletedTurns(messages)
	if !ok {
		return nil, false
	}
	if scope.toolContinuation && !anthropicHistoricalError(messages[:scope.before], location) {
		return nil, false
	}
	changed := false
	for index, message := range messages[:scope.before] {
		raw := bytes.TrimSpace(message["content"])
		if string(message["role"]) != `"assistant"` || len(raw) == 0 || raw[0] != '[' {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(raw, &blocks) != nil {
			return nil, false
		}
		var kept []json.RawMessage
		for _, block := range blocks {
			kind := anthropicBlockType(block)
			// 最后一组助手消息发出了工具请求，但末尾用户消息未返回结果时，
			// 不能把这组消息当作已完成的历史，也不能触发一次新调用。
			if kind == "" || (!scope.toolContinuation && index >= scope.latestAssistant && kind == "tool_use") {
				return nil, false
			}
			if kind != "thinking" && kind != "redacted_thinking" {
				kept = append(kept, block)
			}
		}
		if len(kept) == len(blocks) {
			continue
		}
		if len(kept) == 0 {
			// 避免制造无内容的 assistant 消息或删除整个历史轮次。
			return nil, false
		}
		message["content"], _ = json.Marshal(kept)
		changed = true
	}
	if !changed {
		return nil, false
	}
	request["messages"], _ = json.Marshal(messages)
	recovered, err := json.Marshal(request)
	return recovered, err == nil
}

// anthropicCompletedScope 以 before 划分可以清理的旧轮次。普通问答同时记录紧邻的助手组，
// 供调用方拒绝未提交工具结果的历史；工具续轮以本轮最早的用户消息为界。
type anthropicCompletedScope struct {
	before, latestAssistant int
	toolContinuation        bool
}

// anthropicCompletedTurns 从历史尾部向前遍历用户/助手组，确定已结束轮次的范围。
// 参数为只读原生消息；返回旧轮次上界或能否恢复。跨连续工具调用回到发起本轮的
// 用户消息，保留之后的签名。无法确认边界时拒绝恢复，无副作用。
func anthropicCompletedTurns(messages []map[string]json.RawMessage) (anthropicCompletedScope, bool) {
	end := len(messages)
	for end > 0 {
		user, toolResult, ok := anthropicTrailingUsers(messages[:end])
		if !ok {
			break
		}
		assistant := user
		for assistant > 0 && string(messages[assistant-1]["role"]) == `"assistant"` {
			assistant--
		}
		if !toolResult {
			return anthropicCompletedScope{before: user, latestAssistant: assistant, toolContinuation: end != len(messages)}, true
		}
		if assistant == user {
			break
		}
		end = assistant
	}
	return anthropicCompletedScope{}, false
}

// anthropicTrailingUsers 读取末尾连续 user 消息，参数为尚未检查的历史，
// 返回组起点、是否含工具结果、是否可识别。多个 user 消息会被上游合成一轮；
// 助手预填、无用户组或内容无法解析时拒绝识别，纯读取。
func anthropicTrailingUsers(messages []map[string]json.RawMessage) (int, bool, bool) {
	index := len(messages)
	toolResult := false
	for index > 0 && string(messages[index-1]["role"]) == `"user"` {
		content := bytes.TrimSpace(messages[index-1]["content"])
		if len(content) == 0 {
			return 0, false, false
		}
		if content[0] != '"' {
			var blocks []json.RawMessage
			if json.Unmarshal(content, &blocks) != nil || len(blocks) == 0 {
				return 0, false, false
			}
			for _, block := range blocks {
				kind := anthropicBlockType(block)
				if kind == "" {
					return 0, false, false
				}
				toolResult = toolResult || kind == "tool_result"
			}
		}
		index--
	}
	return index, toolResult, index != len(messages)
}

// anthropicHistoricalError 核对上游报告的消息/块索引是否指向已结束轮次的 thinking。
// 参数为允许清理的历史前缀和错误匹配结果，返回是否确认。工具续轮中只有明确的旧块
// 路径才允许补发，路径省略、脱敏或超界时保留原错误，无副作用。
func anthropicHistoricalError(completed []map[string]json.RawMessage, location []string) bool {
	message, messageErr := strconv.Atoi(location[anthropicThinkingSignatureError.SubexpIndex("message")])
	block, blockErr := strconv.Atoi(location[anthropicThinkingSignatureError.SubexpIndex("block")])
	if messageErr != nil || blockErr != nil || message >= len(completed) || string(completed[message]["role"]) != `"assistant"` {
		return false
	}
	var blocks []json.RawMessage
	return json.Unmarshal(completed[message]["content"], &blocks) == nil &&
		block < len(blocks) && anthropicBlockType(blocks[block]) == "thinking"
}

// anthropicBlockType 提取工具轮次检查使用的块类型；参数为单个原生 JSON 内容块，
// 返回类型或空串（缺失、非字符串或非法结构）。只检查块顶层，不读取正文及工具参数。
func anthropicBlockType(block json.RawMessage) string {
	var header struct{ Type string }
	if json.Unmarshal(block, &header) != nil {
		return ""
	}
	return header.Type
}
