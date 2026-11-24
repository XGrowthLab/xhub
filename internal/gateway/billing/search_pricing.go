package billing

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

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

// budgetSearchRate 从冻结费率选择当前时段的明确搜索单价；rates 为已验证费率，window 为当前计费时段。
// 返回单价或缺失；同一窗口重复、无效单位及非有限价格均报错，绝不回落为猜测价或免费。
func budgetSearchRate(rates []catalog.Rate, window string) (*catalog.Rate, error) {
	for _, selectedWindow := range []string{window, "all"} {
		var selected *catalog.Rate
		for _, rate := range rates {
			if rate.Measure != "query" || rate.Side != "output" || rate.Window != selectedWindow {
				continue
			}
			if rate.Variant != "search" && rate.SourceKey != "search_context_cost_per_query" && !strings.HasPrefix(rate.SourceKey, "web_search_req") {
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
