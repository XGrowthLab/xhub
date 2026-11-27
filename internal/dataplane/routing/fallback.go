// 本文件按错误类别遍历请求私有回退图，记录路径、阻断环并保持配置顺序。
// 调用关系与边界：请求候选选择的辅助能力；具体参数、返回及副作用见各方法说明。
package routing

import (
	"encoding/json"
	"strings"

	"github.com/sunqirui1987/xhub/internal/config"
	dpcontract "github.com/sunqirui1987/xhub/internal/dataplane/contract"
	"github.com/sunqirui1987/xhub/internal/router"
)

// FallbackKind 将上游失败分类为模板回退图使用的 general、context、content，不能识别则返回空串。
// 参数 status 为上游 HTTP 状态，body 为原始错误正文；适配与原生执行在失败后调用。
// 429 与 5xx 直接归 general；成功状态永不触发回退；其余错误只解析 JSON error 字段中的已知上下文/内容策略标记。
// 这样不会把成功回答或其他正文中的相似文字误判成回退指令。非法 JSON、缺少 error、未知错误均不自动跨模型。
// 纯分类函数，不决定请求是否可重放；流式已写出和异步创建歧义仍由执行器禁止重放。
func FallbackKind(status int, body []byte) string {
	if status >= 500 || status == 429 {
		return "general"
	}
	if status < 400 {
		return ""
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	raw, ok := doc["error"]
	if !ok {
		return ""
	}
	text, _ := json.Marshal(raw)
	s := strings.ToLower(string(text))
	for _, marker := range []string{"context_length_exceeded", "context_window_exceeded", "maximum context length", "prompt is too long", "input is too long"} {
		if strings.Contains(s, marker) {
			return "context"
		}
	}
	for _, marker := range []string{"content_policy_violation", "content_filter", "content filtering policy", "safety policy", "safety_violation"} {
		if strings.Contains(s, marker) {
			return "content"
		}
	}
	return ""
}

// fallbackQueue 保存一次请求的有序回退遍历状态，两类执行器各自持有独立实例。
// 以 Seen 阻断环并限制最多访问 32 个公开模型或组；pending 保留兄弟目标顺序，paths 保存首次到达路径。
// 配置快照只读，队列字段可变；不得跨请求共享，也不在这里执行网络请求、预算校验或记账。
type fallbackQueue struct {
	// 本请求的完整路由设置快照，保留已解析模型默认和回退图，不修改共享模板。
	settings router.RouteSettings
	// 按配置顺序等待访问的公开模型/组队列，Next 消费，Seen 去重。
	pending []string
	// 已访问公开模型/组集合，用于环保护及最多 32 个目标的访问限制。
	Seen map[string]bool
	// 当前失败时采用的回退源，显式组链优先，否则由实际成员更新。
	Current string
	// 当前展开部署的模型/组名；不同于可能指向成员的 Current。
	PoolAlias string
	// 禁止本请求新建跨模型回退，也禁止复用跨模型动态偏好；父响应归属仍须单独校验。
	Disabled bool
	// 原请求别名到实际模型池的有序回退边，每轮按当前模板验证；直接命中时为空，不能作为授权。
	Path []dpcontract.RouteStep
	// 每个排队目标首次发现的完整回退路径，恢复成功偏好及验证模板可达性使用。
	paths map[string][]dpcontract.RouteStep
}

// NewFallbackQueue 初始化单次请求的有序回退状态。
// 参数 settings 为完整路由快照，alias 为原公开模型/组，disabled 为请求的禁止回退开关；返回请求私有队列。
// 两类创建入口调用；起始 alias 已记入 Seen 并占用一个访问名额，pending 与路径表从空开始。
// 队列不应跨请求复用，不修改共享模板；禁用时仍可正常访问初始池，只不展开跨目标回退。
func NewFallbackQueue(settings router.RouteSettings, alias string, disabled bool) *fallbackQueue {
	return &fallbackQueue{settings: settings, Current: alias, PoolAlias: alias, Seen: map[string]bool{alias: true}, Disabled: disabled, paths: map[string][]dpcontract.RouteStep{}}
}

// Initial 先选原模型或已配置回退链上的健康部署，再考虑处于冷却的合法候选。
// 参数 models 为已经兼容的目录，alias 为请求模型，settings 为当前模型配置，state 为路由快照，pin 为普通会话偏好。
// 返回候选和生效配置。适配与原生执行器在尚未出站时调用；只因冷却耗尽时重走一次既有选路，不改权重、授权或共享 Redis。
// 向上游送出一次请求后仍走普通回退，避免为同一次失败额外尝试冷却目标。
func (q *fallbackQueue) Initial(models []config.ModelEntry, alias string, settings router.RouteSettings, state router.State, pin string) ([]config.ModelEntry, router.RouteSettings) {
	if pool := settings.Schedule(models, alias, state, pin); len(pool) > 0 {
		return pool, settings
	}
	pool, selected := q.Next("general", models, state)
	if len(pool) > 0 || selected.Err != nil || len(state.Cooldown) == 0 {
		return pool, selected
	}
	// 冷却是出站排序信号。所有健康路由都不可用时，仍应按原权重和回退范围让上游处理请求。
	*q = *NewFallbackQueue(q.settings, alias, q.Disabled)
	state.Cooldown = nil
	if pool := settings.Schedule(models, alias, state, pin); len(pool) > 0 {
		return pool, settings
	}
	return q.Next("general", models, state)
}

// Next 在当前失败源的对应回退列表中寻找下一个可调度模型池。
// 参数 kind 为错误类别，models 为兼容部署目录，state 为本轮运行快照；返回候选池与目标模型设置。
// 执行器耗尽当前池后调用；禁用、队列耗尽或达到最多 32 个不同公开目标时返回空池，模型配置错误通过 settings.Err 返回。
// 按配置顺序消费 pending，以 Seen 去重和阻断环；每个目标重新应用模型默认值及分配权重。
// 目标为空池时继续展开它的 general 链，避免一个空池阻塞剩余可达目标；起始模型也计入 32 个限制。
// 修改本请求的队列、当前池和路径，不访问上游；调用方仍须验证所选模型/组的权限、预算与凭据。
func (q *fallbackQueue) Next(kind string, models []config.ModelEntry, state router.State) ([]config.ModelEntry, router.RouteSettings) {
	if q.Disabled {
		return nil, q.settings
	}
	q.enqueue(q.Current, kind)
	for len(q.pending) > 0 && len(q.Seen) < 32 {
		name := q.pending[0]
		q.pending = q.pending[1:]
		if q.Seen[name] {
			continue
		}
		q.Seen[name] = true
		q.Current = name
		q.PoolAlias = name
		q.Path = q.paths[name]
		settings := q.settings.ForModel(name)
		if settings.Err != nil {
			return nil, settings
		}
		state.Allocations = settings.Policy.Shares()
		pool := settings.Schedule(models, name, state, "")
		if len(pool) > 0 {
			return pool, settings
		}
		q.enqueue(name, "general")
	}
	return nil, q.settings
}

// enqueue 将某个失败源在指定错误类别下的目标追加到待访问队列。
// 参数 from 为公开模型/组，kind 为类别；无返回值，由 Next 调用。
// 为每个目标保存首次发现的完整路径副本，保留配置顺序；重复排队由 Next 的 Seen 去重。
// 首次路径不被后续支路覆盖，使成功偏好能还原实际到达路径；仅修改请求私有状态，不修改模板。
func (q *fallbackQueue) enqueue(from, kind string) {
	for _, target := range q.settings.ModelFallbacks[from].Targets(kind) {
		if _, exists := q.paths[target]; !exists {
			path := append([]dpcontract.RouteStep(nil), q.Path...)
			q.paths[target] = append(path, dpcontract.RouteStep{From: from, To: target, Kind: kind})
		}
		q.pending = append(q.pending, target)
	}
}

// UseDeployment 确定实际部署失败时应使用哪一份回退列表。
// 参数 name 为实际部署的公开模型名；返回值无，两类执行器每次发送前调用。
// 默认使用实际成员；当前池若有显式组回退配置，则使用组名，包括显式空配置，以尊重管理员的组策略。
// 只修改 Current，保留 PoolAlias 表示本轮展开的池；切换成员时重新计算，避免沿用前一个成员的回退源。
func (q *fallbackQueue) UseDeployment(name string) {
	q.Current = name
	if _, explicit := q.settings.ModelFallbacks[q.PoolAlias]; explicit {
		q.Current = q.PoolAlias
	}
}

// HasFallbackTargets 判断回退策略的三类列表中是否至少有一个目标。
// 参数 p 为模型/组的 FallbackPolicy；返回任一列表非空时为 true。
// 原生入口据此决定是否需要先缓冲错误响应，以便在写出前尝试安全回退；不判断目标资格或请求重放安全性。
// 只读参数，空/零值策略返回 false，无状态或网络副作用。
func HasFallbackTargets(p router.FallbackPolicy) bool {
	return len(p.Fallbacks)+len(p.ContextWindow)+len(p.ContentPolicy) > 0
}
