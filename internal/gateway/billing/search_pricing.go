package billing

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/sunqirui1987/xhub/internal/catalog"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
)

// WebSearchRequested 检查实际出站 JSON 是否声明已知的联网搜索能力；protocol 为出站协议，body 为原始正文。
// 软预算冻结时调用；不修改请求、不执行搜索，也不把普通客户端函数或历史调用计为新的服务器搜索。
func WebSearchRequested(protocol string, body []byte) bool {
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return false
	}
	return llmdialogue.NativeWebSearchRequested(protocol, obj)
}

// SearchUsage 读取供应商明确报告的搜索总次数；raw 为原始实测 usage，返回次数、是否报告和冲突错误。
// 计费及日志调用；已知平级与供应商嵌套别名必须一致，不从工具调用数、提示词或搜索结果推断缺失数量。
func SearchUsage(raw map[string]any) (int, bool, error) {
	count, seen := 0, false
	accept := func(value any) error {
		if value == nil {
			return fmt.Errorf("search usage is missing")
		}
		n, ok := budgetUsageInteger(value)
		if !ok || seen && count != n {
			return fmt.Errorf("search usage is invalid or inconsistent")
		}
		count, seen = n, true
		return nil
	}
	for _, key := range []string{"searches", "search_count", "web_search_requests"} {
		if value, exists := raw[key]; exists {
			if err := accept(value); err != nil {
				return 0, false, err
			}
		}
	}
	for parent, field := range map[string]string{"tool_usage": "web_search", "server_tool_use": "web_search_requests"} {
		value, exists := raw[parent]
		if !exists || value == nil {
			continue
		}
		m, ok := value.(map[string]any)
		if !ok {
			return 0, false, fmt.Errorf("search usage container is invalid")
		}
		if value, exists := m[field]; exists {
			if err := accept(value); err != nil {
				return 0, false, err
			}
		}
	}
	return count, seen, nil
}

// budgetSearchRate 从冻结费率选择当前时段的明确搜索单价；rates 为已验证费率，window 为当前计费时段。
// 返回已归一化的搜索单价或缺失；同一窗口重复、无效单位及非有限价格均报错，绝不回落为猜测价或免费。
func budgetSearchRate(rates []catalog.Rate, window string) (*catalog.Rate, error) {
	for _, selectedWindow := range []string{window, "all"} {
		var selected *catalog.Rate
		for _, rate := range rates {
			if rate.Measure != "query" || rate.Side != "output" || rate.Window != selectedWindow {
				continue
			}
			if _, isSearch := rate.WebSearchQuery(); !isSearch {
				continue
			}
			if selected != nil || rate.Fallback || rate.UnitSize <= 0 || math.IsNaN(rate.USD) || math.IsInf(rate.USD, 0) || rate.USD < 0 {
				return nil, fmt.Errorf("search price is invalid or ambiguous")
			}
			copy := rate
			selected = &copy
		}
		if selected != nil {
			return selected, nil
		}
		if window == "all" {
			break
		}
	}
	return nil, nil
}
