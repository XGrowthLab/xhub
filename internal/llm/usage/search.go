package usage

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
)

var (
	// ErrSearchUsageInvalid 表示供应商已返回计量字段，但类型或数值无法用于计费。
	ErrSearchUsageInvalid = errors.New("search usage is invalid")
	// ErrSearchUsageConflict 表示同一用量对象中等价的搜索计数报告了不同值。
	ErrSearchUsageConflict = errors.New("search usage aliases disagree")
)

// SearchCountPaths 返回已知的实测联网搜索累计计数路径，供结算、目录和流式归并共用。
// 每次返回独立切片；同一帧的多个路径代表同一计量，工具总数和客户端工具发现不属于此计量。
func SearchCountPaths() []string {
	return []string{
		"searches",
		"search_count",
		"web_search_requests",
		"tool_usage.web_search",
		"server_tool_use.web_search_requests",
		"server_tool_use.tool_search",                     // DeepSeek 原生工具计量字段，区别于请求中的客户端工具发现。
		"server_side_tool_usage_details.web_search_calls", // xAI 独立计量，不使用聚合工具总数。
	}
}

// ParseSearchCount 读取原始用量中的搜索次数，返回次数、是否明确报告及非法或冲突错误。
// 计费用量、日志和流式归并均调用；显式零有效，缺失不会估计为零，同帧重复报告不累加。不修改输入。
func ParseSearchCount(raw map[string]any) (int, bool, error) {
	count, reported := 0, false
	for _, path := range SearchCountPaths() {
		parent, child, nested := strings.Cut(path, ".")
		value, exists := raw[parent]
		if nested {
			if value == nil {
				continue
			}
			details, ok := value.(map[string]any)
			if !ok {
				return 0, false, ErrSearchUsageInvalid
			}
			value, exists = details[child]
		}
		if !exists {
			continue
		}
		n, ok := searchCountInteger(value)
		if !ok {
			return 0, false, ErrSearchUsageInvalid
		}
		if reported && count != n {
			return 0, false, ErrSearchUsageConflict
		}
		count, reported = n, true
	}
	return count, reported, nil
}

// searchCountInteger 将供应商原始数值转换为数据库可接收的非负整数，供 ParseSearchCount 调用。
// 拒绝显式 null、字符串、分数和溢出；有效性独立于是否为零，无外部副作用。
func searchCountInteger(v any) (int, bool) {
	var n float64
	switch value := v.(type) {
	case int:
		n = float64(value)
	case int64:
		n = float64(value)
	case float32:
		n = float64(value)
	case float64:
		n = value
	case json.Number:
		var err error
		n, err = value.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(n) || n < 0 || n > math.MaxInt32 || n != math.Trunc(n) {
		return 0, false
	}
	return int(n), true
}
