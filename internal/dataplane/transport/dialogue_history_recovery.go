package transport

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
)

// OpenAI 错误可用参数路径或消息里的 item ID 定位密文。匹配已知描述，不把错误正文里的密文记入日志。
var (
	responsesCipherParam = regexp.MustCompile(`^input(?:\.([0-9]+)|\[([0-9]+)\])\.encrypted_content$`)
	responsesCipherID    = regexp.MustCompile(`(?i)^the encrypted content for item ([a-z0-9_-]+) could not be verified\b`)
	chatCipherParam      = regexp.MustCompile(`^messages(?:\.[0-9]+|\[[0-9]+\])\.reasoning_items(?:\.[0-9]+|\[[0-9]+\])\.encrypted_content$`)
)

// RecoverDialogueHistory 按真实上游协议构造一次兼容请求。参数为已治理的出站 JSON、
// 上游状态和完整错误正文；返回独立正文与可否重试。只处理明确的历史验签 400。
// 统一执行器及 Bypass 调用；不触碰 DeepSeek 公开推理、真实工具数据或压缩历史。
// 固定续接由调用方拦截。本函数不发 HTTP，不写日志或会话存储。
func RecoverDialogueHistory(protocol string, payload []byte, status int, response []byte) ([]byte, bool) {
	if status != http.StatusBadRequest {
		return nil, false
	}
	if protocol == "anthropic-messages" {
		return RecoverAnthropicThinking(payload, status, response)
	}
	if protocol != "openai-chat" && protocol != "openai-responses" {
		return nil, false
	}
	var rejection struct {
		Error struct {
			Code, Param, Message string
		}
	}
	if json.Unmarshal(response, &rejection) != nil || rejection.Error.Code != "invalid_encrypted_content" {
		return nil, false
	}
	// 原生扩展和工具参数里可能有大整数，不用 float64 重建出站副本。
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var body map[string]any
	if decoder.Decode(&body) != nil {
		return nil, false
	}
	if _, exists := body["previous_response_id"]; exists {
		return nil, false
	}
	if _, exists := body["conversation"]; exists {
		return nil, false
	}

	var recovered map[string]any
	var ok bool
	if protocol == "openai-chat" {
		if rejection.Error.Param != "" && !chatCipherParam.MatchString(rejection.Error.Param) {
			return nil, false
		}
		recovered, ok = llmdialogue.PortableChatHistory(body)
	} else {
		recovered, ok = recoverResponsesReasoning(body, rejection.Error.Param, rejection.Error.Message)
	}
	if !ok {
		return nil, false
	}
	out, err := json.Marshal(recovered)
	return out, err == nil
}

// recoverResponsesReasoning 仅省略已结束轮次中的外部加密 reasoning 项。
// 参数为出站副本及错误位置，返回修改后正文或拒绝恢复。保留独立公开摘要、消息、
// 工具输入及结果；密文摘要和原始推理 ID 一同省略，避免把不可复用的 ID 再交给上游。
// 工具续轮需要准确的旧项位置；压缩内容和服务端引用可能是唯一历史，必须完整保留。
func recoverResponsesReasoning(body map[string]any, param, message string) (map[string]any, bool) {
	input, _ := body["input"].([]any)
	if len(input) == 0 {
		return nil, false
	}
	lastUser, identified := -1, -1
	var id string
	if match := responsesCipherID.FindStringSubmatch(strings.TrimSpace(message)); match != nil {
		id = match[1]
	}
	if param != "" {
		match := responsesCipherParam.FindStringSubmatch(param)
		if match == nil {
			return nil, false
		}
		var err error
		identified, err = strconv.Atoi(match[1] + match[2])
		if err != nil || identified >= len(input) {
			return nil, false
		}
	}
	pending := make(map[string]bool)
	isNewTurn, isToolOutput := false, false
	for index, value := range input {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		kind, _ := item["type"].(string)
		role, _ := item["role"].(string)
		cipher, _ := item["encrypted_content"].(string)
		if kind == "compaction" || kind == "compaction_summary" || kind == "item_reference" ||
			strings.HasPrefix(cipher, "xhub_") || (kind != "reasoning" && item["encrypted_content"] != nil) ||
			item["encrypted_function_args"] != nil {
			return nil, false
		}
		if id != "" && item["id"] == id {
			if identified != -1 && identified != index {
				return nil, false
			}
			identified = index
		}
		isNewTurn = (kind == "" || kind == "message") && role == "user"
		if isNewTurn {
			if len(pending) != 0 {
				return nil, false
			}
			lastUser = index
		}
		var valid bool
		isToolOutput, valid = responsesRecoveryTool(item, pending)
		if !valid {
			return nil, false
		}
	}
	if lastUser <= 0 || len(pending) != 0 || (!isNewTurn && !isToolOutput) ||
		(identified == -1 && (id != "" || !isNewTurn)) || identified >= lastUser {
		return nil, false
	}
	if identified >= 0 {
		item := input[identified].(map[string]any)
		if item["type"] != "reasoning" || item["encrypted_content"] == nil || (id != "" && item["id"] != id) {
			return nil, false
		}
	}

	out := make([]any, 0, len(input))
	for i, value := range input {
		item := value.(map[string]any)
		cipher, _ := item["encrypted_content"].(string)
		if i < lastUser && (identified < 0 || i == identified) && item["type"] == "reasoning" && cipher != "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == len(input) {
		return nil, false
	}
	body["input"] = out
	return body, true
}

// responsesRecoveryTool 跟踪客户端要配对的 Responses 工具项。参数为当前项及本次扫描的
// 待返回调用 ID 集，返回是否为工具结果及关联是否合法。服务端工具结果仍作为不透明原文保留。
func responsesRecoveryTool(item map[string]any, pending map[string]bool) (output, valid bool) {
	call, _ := item["call_id"].(string)
	switch item["type"] {
	case "function_call", "custom_tool_call", "computer_call", "local_shell_call", "shell_call", "apply_patch_call", "tool_search_call", "program":
		if item["execution"] == "server" {
			return false, true
		}
		if call == "" || pending[call] {
			return false, false
		}
		pending[call] = true
	case "function_call_output", "custom_tool_call_output", "computer_call_output", "local_shell_call_output", "shell_call_output", "apply_patch_call_output", "tool_search_output", "program_output":
		if !pending[call] {
			return true, false
		}
		delete(pending, call)
		return true, true
	}
	return false, true
}
