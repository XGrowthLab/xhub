package dialogue

import "strings"

// NativeWebSearchRequested 检查原生对话正文是否声明托管搜索；protocol 为出站协议，body 是已解析的请求副本。
// 路由能力检查与计费共同调用；只识别协议明确的搜索工具、工具选择及 Responses 的 additional_tools，
// 不把历史搜索结果或普通同名函数当作新请求，也不修改正文或访问外部服务。
func NativeWebSearchRequested(protocol string, body map[string]any) bool {
	if protocol != "openai-responses" && protocol != "openai-chat" && protocol != "anthropic-messages" {
		return false
	}
	if protocol == "openai-chat" && body["web_search_options"] != nil {
		return true
	}
	isSearch := func(kind string) bool {
		// 原生协议可能增加带日期的搜索类型；显式禁用时不能让未知版本绕过策略或漏记搜索费用。
		return kind == "web_search" || strings.HasPrefix(kind, "web_search_")
	}
	containsTools := func(container map[string]any) bool {
		tools, _ := container["tools"].([]any)
		for _, item := range tools {
			tool, _ := item.(map[string]any)
			kind, _ := tool["type"].(string)
			if isSearch(kind) {
				return true
			}
		}
		return false
	}
	if containsTools(body) {
		return true
	}
	if choice, ok := body["tool_choice"].(map[string]any); ok {
		kind, _ := choice["type"].(string)
		if isSearch(kind) {
			return true
		}
	}
	if protocol == "openai-responses" {
		items, _ := body["input"].([]any)
		for _, item := range items {
			part, _ := item.(map[string]any)
			if part["type"] == "additional_tools" && containsTools(part) {
				return true
			}
		}
	}
	return false
}
