package dialogue

// 参照 LiteLLM 35970c3 的 namespace/custom 工具双向映射；MIT 来源见 third_party/litellm/NOTICE.md。
import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// ToolBinding 记录 Chat 函数名对应的 Responses 工具身份；仅用于本请求的跨协议回复，不授予工具执行权限。
type ToolBinding struct {
	Name, Namespace string
	Custom          bool
	// ClientSearch 表示由客户端执行的工具发现；只还原调用身份，网关不搜索或执行工具。
	ClientSearch bool
}

// qualifiedToolName 为 namespace 函数构造稳定的 Chat 名称；参数为原命名空间和函数名，命名空间别名最长 64 字节，顶层名保持原值。
// 请求和历史投影共用；长名称追加摘要防止截断碰撞，原始身份通过 ToolBinding 恢复，无外部副作用。
func qualifiedToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	joined := namespace + "__" + name
	if len(joined) <= 64 {
		return joined
	}
	return fmt.Sprintf("%s_%x", joined[:39], sha256.Sum256([]byte(joined)))[:64]
}

// agentToolProjection 保留扁平化工具及原请求位置、还原表和协议转换限制。
type agentToolProjection struct {
	tools       []any
	fields      []string
	bindings    map[string]ToolBinding
	bridgeError error
}

// projectAgentTools 将 Responses 函数、单层 namespace、custom 及客户端 tool_search 投影为共享函数，返回投影或输入错误。
// paths 仅记录来自历史项的工具原始位置，其他工具位置取自顶层数组。
// ParseDialogue 调用；未知原生工具保留在 Native，阻止桥接但允许原生路径；重复/碰撞名称明确拒绝，不静默覆盖工具身份。
// custom 的语法定义仅作为模型提示，不能等价提供原厂 grammar 强约束；本方法不执行工具或修改输入。
func projectAgentTools(values []any, paths map[int]string) (agentToolProjection, error) {
	var projection agentToolProjection
	bindings := map[string]ToolBinding{}
	seen := map[string]string{}
	var bridgeError error
	for i, value := range values {
		path := paths[i]
		if path == "" {
			path = fmt.Sprintf("tools[%d]", i)
		}
		tool := dialogueMap(value)
		if tool == nil {
			return agentToolProjection{}, &RequestFieldError{Field: path, Message: "tool must be an object"}
		}
		if tool["type"] == "tool_search" && tool["execution"] != nil && tool["execution"] != "client" && tool["execution"] != "server" {
			return agentToolProjection{}, &RequestFieldError{Field: path + ".execution", Message: "tool_search execution must be server or client"}
		}
		clientSearch := tool["type"] == "tool_search" && tool["execution"] == "client"
		if clientSearch {
			// LiteLLM 区分客户端与托管搜索；这里将客户端搜索保留为普通函数，而非丢掉工具发现能力。
			if err := dialogueFields(tool, "type", "execution", "description", "parameters"); err != nil {
				bridgeError = atRequestField(path, err)
			}
			tool = map[string]any{"type": "function", "name": "tool_search", "description": tool["description"], "parameters": tool["parameters"]}
		}
		namespace := ""
		members := []any{tool}
		if tool["type"] == "namespace" {
			if err := dialogueFields(tool, "type", "name", "description", "tools"); err != nil {
				bridgeError = atRequestField(path, err)
			}
			namespace = dialogueString(tool["name"])
			var ok bool
			members, ok = tool["tools"].([]any)
			if namespace == "" || !ok {
				return agentToolProjection{}, &RequestFieldError{Field: path, Message: "namespace requires name and tools array"}
			}
		}
		for j, value := range members {
			memberPath := path
			if namespace != "" {
				memberPath = fmt.Sprintf("%s.tools[%d]", path, j)
			}
			member := dialogueMap(value)
			if member == nil {
				return agentToolProjection{}, &RequestFieldError{Field: memberPath, Message: "namespace tool must be an object"}
			}
			kind := dialogueString(member["type"])
			if kind != "function" && kind != "custom" {
				bridgeError = &RequestFieldError{Field: memberPath + ".type", Message: fmt.Sprintf("tool type %q has no cross-protocol mapping; use a function tool or a native Responses deployment that supports this tool", kind)}
				continue
			}
			name := dialogueString(member["name"])
			if name == "" {
				return agentToolProjection{}, &RequestFieldError{Field: memberPath + ".name", Message: "tool name is required"}
			}
			alias := qualifiedToolName(namespace, name)
			cp, _ := cloneObject(member)
			delete(cp, "cache_control")
			// defer_loading 是发现性能提示；Chat 使用客户端已提供的定义，原生请求仍保留原标记。
			if flag, exists := cp["defer_loading"]; exists {
				if _, ok := flag.(bool); !ok {
					return agentToolProjection{}, &RequestFieldError{Field: memberPath + ".defer_loading", Message: "defer_loading must be boolean"}
				}
				delete(cp, "defer_loading")
			}
			cp["name"] = alias
			if kind == "custom" {
				// custom 会被重建成函数，必须先检查能力边界，避免未知格式或扩展在重建时静默消失；原生正文不受影响。
				if err := dialogueFields(member, "type", "name", "description", "format", "cache_control", "defer_loading"); err != nil {
					bridgeError = atRequestField(memberPath, err)
				}
				description := dialogueString(member["description"])
				if value := member["format"]; value != nil {
					format := dialogueMap(value)
					switch format["type"] {
					case "grammar":
						if err := dialogueFields(format, "type", "syntax", "definition"); err != nil {
							bridgeError = atRequestField(memberPath+".format", err)
						} else if dialogueString(format["definition"]) == "" {
							bridgeError = &RequestFieldError{Field: memberPath + ".format.definition", Message: "custom grammar requires a nonempty definition"}
						}
						description += "\n\nInput format:\n" + dialogueString(format["definition"])
					case "text":
						if err := dialogueFields(format, "type"); err != nil {
							bridgeError = atRequestField(memberPath+".format", err)
						}
					default:
						bridgeError = &RequestFieldError{Field: memberPath + ".format.type", Message: fmt.Sprintf("unsupported custom format %q; mapped formats are text and grammar", dialogueString(format["type"]))}
					}
				}
				// 与 LiteLLM 一样在 schema 中明确原文包装字段，避免模型将代码二次转义或误用 Responses 的 input 字段。
				cp = map[string]any{"type": "function", "name": alias, "description": description, "parameters": map[string]any{"type": "object", "properties": map[string]any{"content": map[string]any{"type": "string", "description": "The raw " + name + " input following the specified format, without an extra JSON string wrapper."}}, "required": []any{"content"}, "additionalProperties": false}}
			}
			fingerprint, _ := json.Marshal([]any{namespace, cp, clientSearch})
			if previous, exists := seen[alias]; exists {
				// 工具发现续轮可能重复已经加载的定义；完全相同的身份可合并，不同身份或 schema 仍拒绝桥接。
				if previous == string(fingerprint) {
					continue
				}
				bridgeError = &RequestFieldError{Field: memberPath + ".name", Message: "conflicting tool name after protocol mapping"}
			}
			seen[alias] = string(fingerprint)
			if namespace != "" || kind == "custom" || clientSearch {
				bindings[alias] = ToolBinding{Name: name, Namespace: namespace, Custom: kind == "custom", ClientSearch: clientSearch}
			}
			projection.tools = append(projection.tools, cp)
			projection.fields = append(projection.fields, memberPath)
		}
	}
	projection.bindings, projection.bridgeError = bindings, bridgeError
	return projection, nil
}

// projectAgentCall 将带 namespace/custom 的 Responses 历史调用转为共享函数调用；参数为独立副本，返回输入格式错误。
// ParseDialogue 在原生正文以外的投影调用；公开工具字符串包装为 JSON，保留 call_id，既不执行也不重写用户输入字符串。
func projectAgentCall(item map[string]any) error {
	kind := dialogueString(item["type"])
	if kind == "custom_tool_call" {
		input, ok := item["input"].(string)
		if !ok {
			return fmt.Errorf("custom tool input must be a string")
		}
		raw, _ := json.Marshal(map[string]any{"content": input})
		item["type"], item["arguments"] = "function_call", string(raw)
		delete(item, "input")
	} else if kind == "custom_tool_call_output" {
		item["type"] = "function_call_output"
	}
	if item["type"] == "function_call" {
		item["name"] = qualifiedToolName(dialogueString(item["namespace"]), dialogueString(item["name"]))
		delete(item, "namespace")
	}
	// phase 是客户端显示阶段提示；正文与调用身份已经保留，Chat 没有独立的阶段字段。
	if phase := item["phase"]; phase != nil {
		if phase != "commentary" && phase != "final_answer" {
			return fmt.Errorf("unsupported message phase")
		}
		delete(item, "phase")
	}
	return nil
}

// ResponseToolItem 根据本请求的映射还原 Responses 工具项；参数为完整函数调用、映射和生命周期状态，返回项或 custom/搜索参数错误。
// JSON 及 SSE 编码器共用；函数 item.id 使用 fc_ 合成身份，与关联 call_id 分离；原生 custom/客户端搜索保持既有身份契约。
// custom 兼容 content/input 单字符串包装，不修补代码；客户端负责执行和语法校验。
func ResponseToolItem(call Turn, bindings map[string]ToolBinding, status string) (map[string]any, error) {
	item := map[string]any{"id": ResponseItemID("fc", call.ID), "type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments, "status": status}
	binding, ok := bindings[call.Name]
	if !ok {
		return item, nil
	}
	item["name"] = binding.Name
	if binding.Namespace != "" {
		item["namespace"] = binding.Namespace
	}
	if binding.ClientSearch {
		item["id"] = call.ID
		var args map[string]any
		if json.Unmarshal([]byte(call.Arguments), &args) != nil || args == nil {
			return nil, fmt.Errorf("client tool_search arguments must be an object")
		}
		item["type"], item["execution"], item["arguments"] = "tool_search_call", "client", args
		delete(item, "name")
	} else if binding.Custom {
		item["id"] = call.ID
		var args map[string]any
		if json.Unmarshal([]byte(call.Arguments), &args) != nil {
			return nil, fmt.Errorf("invalid custom tool arguments")
		}
		content, valid := args["content"].(string)
		if !valid {
			// 七牛 DeepSeek 实测会用 Responses 的 input 单字段包装；只接受明确字符串别名，不猜测其他字段或改写代码。
			content, valid = args["input"].(string)
		}
		if !valid || len(args) != 1 {
			return nil, fmt.Errorf("custom tool requires a content string")
		}
		item["type"], item["input"] = "custom_tool_call", content
		delete(item, "arguments")
	}
	return item, nil
}

// projectClientSearchHistory 将客户端工具发现记录投影为 Chat 函数历史，返回加载的工具定义或输入错误。
// 参数 item 是 ParseDialogue 创建的副本；只接受 client 执行及对象参数，保留 call_id 和结果原文，原生历史不变。
// 调用方将返回定义加入当前工具目录，网关自身不会搜索、执行工具或改变用户权限。
func projectClientSearchHistory(item map[string]any) ([]any, error) {
	if item["execution"] != "client" || dialogueString(item["call_id"]) == "" {
		return nil, fmt.Errorf("tool search history requires client execution and call_id")
	}
	delete(item, "execution")
	if item["type"] == "tool_search_call" {
		if err := dialogueFields(item, "type", "id", "call_id", "status", "arguments"); err != nil {
			return nil, err
		}
		args := dialogueMap(item["arguments"])
		if args == nil {
			return nil, fmt.Errorf("client tool_search arguments must be an object")
		}
		raw, _ := json.Marshal(args)
		item["type"], item["name"], item["arguments"] = "function_call", "tool_search", string(raw)
		return nil, nil
	}
	if err := dialogueFields(item, "type", "id", "call_id", "status", "tools"); err != nil {
		return nil, err
	}
	tools, ok := item["tools"].([]any)
	if !ok {
		return nil, fmt.Errorf("client tool_search output requires tools array")
	}
	raw, _ := json.Marshal(map[string]any{"tools": tools})
	item["type"], item["output"] = "function_call_output", string(raw)
	delete(item, "tools")
	return tools, nil
}

// agentClientMetadata 验证 Codex 的客户端跟踪元数据；参数为任意字段，返回类型/大小错误。
// 跨协议选项投影调用；只丢弃不影响推理语义的跟踪提示，不能用它覆盖网关身份、权限、会话或预算。
func agentClientMetadata(value any) error {
	m, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("client_metadata must be an object")
	}
	for _, value := range m {
		if _, ok := value.(string); !ok {
			return fmt.Errorf("client_metadata values must be strings")
		}
	}
	raw, _ := json.Marshal(m)
	if len(raw) > 64<<10 {
		return fmt.Errorf("client_metadata is too large")
	}
	return nil
}

// validReasoningSummary 判断 Responses 摘要提示是否为已知枚举；参数为任意 JSON 值，返回合法性，无副作用。
// bridgeOptions 使用；这些提示不改变推理 effort，Chat 已保留公开 reasoning_content，但不承诺原生摘要粒度。
func validReasoningSummary(value any) bool {
	s, ok := value.(string)
	return ok && strings.Contains("|auto|concise|detailed|none|", "|"+s+"|")
}

// projectReasoningContext 将 Responses 的公开推理保留范围投影到 Chat 历史；参数为已有轮次和 context 提示，返回独立切片或枚举错误。
// EncodeDialogue 调用；current_turn 仅移除最后一条用户消息之前的公开推理，保留正文、工具和关联 ID；auto/all_turns 沿用完整可用历史。
// 不解释厂商密文，也不修改原轮次；不透明推理在外层能力检查中已经拒绝跨协议。
func projectReasoningContext(turns []Turn, context any) ([]Turn, error) {
	if context == nil || context == "auto" || context == "all_turns" {
		return append([]Turn(nil), turns...), nil
	}
	if context != "current_turn" {
		return nil, fmt.Errorf("invalid reasoning context")
	}
	lastUser := -1
	for index, turn := range turns {
		if turn.Role == "user" {
			lastUser = index
		}
	}
	var projected []Turn
	for index, turn := range turns {
		if index < lastUser {
			if turn.Role == "reasoning" {
				continue
			}
			turn.Reasoning = ""
		}
		projected = append(projected, turn)
	}
	return projected, nil
}
