package router

import (
	"github.com/sunqirui1987/xhub/internal/config"
)

// Schedule 为适配与官方数据面生成本次请求的部署尝试顺序。
// 参数 list 是完整部署目录，alias 是公开模型或路由组名，strategy 是已校验策略，
// st 是运行状态，pinned 是会话固定的运行身份；返回已过滤、排序的新切片。
// 只有存在替代部署时才使用冷却；粘性仍遵守该范围和流量权重。本函数不修改部署目录、共享配置或 Redis。
func Schedule(list []config.ModelEntry, alias, strategy string, st State, pinned string) []config.ModelEntry {
	pool := candidatesForState(list, alias, st)
	if len(st.Cooldown) > 0 && !hasCooldownAlternatives(pool, strategy, st) {
		// 模型下架、停用、权重变动或旧版本可能留有冷却键。只对当前请求忽略，避免清掉其他路由使用的状态。
		st.Cooldown = nil
	}
	pool = Available(pool, strategy, st)
	if pinned != "" && !st.Cooldown[pinned] {
		for i, e := range pool {
			if CooldownID(e) == pinned {
				out := make([]config.ModelEntry, 0, len(pool))
				out = append(out, e)
				rest := append(append([]config.ModelEntry(nil), pool[:i]...), pool[i+1:]...)
				// 只有一个剩余目标时已无需排序，避免为已确定的粘性首选消耗随机抽样。
				if len(rest) > 1 {
					rest = Order(rest, alias, strategy, st)
				}
				out = append(out, rest...)
				return out
			}
		}
	}
	return Order(pool, alias, strategy, st)
}

// hasCooldownAlternatives 检查已匹配的部署池是否有两个不同的运行身份可接流量。
// 参数 pool 已按模型或组匹配，strategy 和 st 提供权重；返回是否启用冷却，供调度和模板资格检查复用。
// 判断基于冷却前的候选，避免只剩一个健康部署时停止维护多部署健康状态；不改调用方快照。
func hasCooldownAlternatives(pool []config.ModelEntry, strategy string, st State) bool {
	if IsSplitStrategy(strategy) && len(pool) > 1 {
		st.Cooldown = nil
		pool = Available(pool, strategy, st)
	}
	if len(pool) < 2 {
		return false
	}
	first := CooldownID(pool[0])
	for _, candidate := range pool[1:] {
		if CooldownID(candidate) != first {
			return true
		}
	}
	return false
}

// Available 按策略过滤当前可选部署，不排序也不抽样。
// 参数 pool 是已匹配候选，strategy 是已校验策略，st 是运行状态；返回新切片。
// traffic-split 还排除非正或非法权重，其他策略只排除冷却部署；没有健康候选时返回空。
func Available(pool []config.ModelEntry, strategy string, st State) []config.ModelEntry {
	if IsSplitStrategy(strategy) {
		return splitCandidates(pool, st)
	}
	return openCandidates(pool, st)
}
