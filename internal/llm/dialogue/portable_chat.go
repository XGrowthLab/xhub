package dialogue

import "strings"

// PortableChatHistory 把已完成的 Chat 历史转换为可移植的请求副本。
// 参数为完整客户端正文；仅在新用户轮次开始、工具已配对且推理扩展已验证时清理密文。
// 返回转换后的副本和是否有改动。保留公开摘要、reasoning_content、正文及工具字段，
// 网关历史引用、厂商签名和正在执行的工具轮次沿用原来边界；选路及原生重试可调用。
func PortableChatHistory(body map[string]any) (map[string]any, bool) {
	if _, stateful := body["previous_response_id"]; stateful {
		return nil, false
	}
	messages, _ := body["messages"].([]any)
	if len(messages) < 2 || dialogueMap(messages[len(messages)-1])["role"] != "user" {
		return nil, false
	}

	out := append([]any(nil), messages...)
	pending := make(map[string]bool)
	changed := false
	for index, value := range messages {
		message := dialogueMap(value)
		role := dialogueString(message["role"])
		switch role {
		case "system", "developer":
		case "user":
			if len(pending) != 0 {
				return nil, false
			}
		case "assistant":
			if len(pending) != 0 || message["function_call"] != nil {
				return nil, false
			}
			for _, value := range dialogueList(message["tool_calls"]) {
				id := dialogueString(dialogueMap(value)["id"])
				if id == "" || pending[id] {
					return nil, false
				}
				pending[id] = true
			}
			// Anthropic 原厂签名和 XHub 封装须由各自的协议入口处理。
			for _, value := range dialogueList(message["content"]) {
				block := dialogueMap(value)
				if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
					return nil, false
				}
			}
		case "tool":
			id := dialogueString(message["tool_call_id"])
			if !pending[id] {
				return nil, false
			}
			delete(pending, id)
		default:
			return nil, false
		}

		// 先跑原有校验，避免恢复时把非法扩展字段清理成合法请求。
		if _, err := chatReasoningReplay(message["reasoning_items"], role); err != nil {
			return nil, false
		}
		items := dialogueList(message["reasoning_items"])
		public := make([]any, 0, len(items))
		messageChanged := false
		for _, value := range items {
			item := dialogueMap(value)
			cipher, exists := item["encrypted_content"]
			if !exists {
				public = append(public, value)
				continue
			}
			if strings.HasPrefix(dialogueString(cipher), "xhub_") {
				return nil, false
			}
			messageChanged = true
			if len(dialogueList(item["summary"])) == 0 {
				// 纯密文项没有需要迁移的摘要；去掉占位项也避免遮蔽公开 reasoning_content。
				continue
			}
			clean := copyHistoryObject(item)
			delete(clean, "encrypted_content")
			delete(clean, "status")
			public = append(public, clean)
		}
		if messageChanged {
			clean := copyHistoryObject(message)
			if len(public) == 0 {
				delete(clean, "reasoning_items")
			} else {
				clean["reasoning_items"] = public
			}
			out[index] = clean
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	clean := copyHistoryObject(body)
	clean["messages"] = out
	return clean, true
}

// copyHistoryObject 复制历史对象顶层键值，用于修改单个对象字段时保留未改动的嵌套值；
// 参数为只读对象，返回独立 map，调用方不得原地修改共享的嵌套历史。
func copyHistoryObject(object map[string]any) map[string]any {
	out := make(map[string]any, len(object))
	for key, value := range object {
		out[key] = value
	}
	return out
}
