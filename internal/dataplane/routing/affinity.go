// 本文件在当前能力、策略、冷却和回退图下重新验证会话偏好；缓存目标不能直接当作执行许可。
// 调用关系与边界：请求候选选择的辅助能力；具体参数、返回及副作用见各方法说明。
package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/sunqirui1987/xhub/internal/config"
	dpcontract "github.com/sunqirui1987/xhub/internal/dataplane/contract"
	providerdeployment "github.com/sunqirui1987/xhub/internal/provider/deployment"
	"github.com/sunqirui1987/xhub/internal/router"
)

// BindConnection 在凭据补齐后校验并记录实际连接，防止同一部署 ID 更新配置后重放旧历史。
// 参数 plan 为请求私有计划，dep 为已解析凭据的候选；返回能否继续，失败不修改计划。
// 两类创建执行器在外发前调用；固定续接要求已有且相同的指纹，普通请求记录新指纹。
// 指纹覆盖地址、账户凭据、上游型号和传输，仅在内存编码后保存 SHA256；不记录密钥明文。
// 定价、权重与请求超时不影响连接身份；旧缓存缺少指纹时失败关闭，调用方需开启新会话。
func BindConnection(plan *dpcontract.RoutePlan, dep config.ModelEntry) bool {
	if plan == nil {
		return false
	}
	identity := map[string]any{"version": 1, "transport": providerdeployment.SelectedTransport(dep), "model": dep.ParamString("model", dep.ModelName)}
	for _, key := range []string{"api_base", "api_key", "custom_llm_provider", "litellm_credential_name", "organization", "project", "headers", "extra_headers", "api_version"} {
		identity[key] = dep.LiteLLMParams[key]
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(raw)
	id := hex.EncodeToString(digest[:])
	if plan.Required && (plan.Target.ConnectionID == "" || plan.Target.ConnectionID != id) {
		return false
	}
	plan.Target.ConnectionID = id
	return true
}

// RuntimePin 把会话保存的稳定 deployment_id 转换为当前部署的运行身份。
// 参数 models 为本轮兼容候选，id 为稳定部署 ID；返回当前 CooldownID，空 ID、删除或不兼容时返回空串。
// 两类执行器和偏好校验调用；稳定身份用于跨配置保留偏好，运行身份用于当前冷却、并发和调度。
// 只读候选；地址变化时返回新的运行身份，不沿用已失效的地址，也不替缺少稳定 ID 的部署建立粘性。
func RuntimePin(models []config.ModelEntry, id string) string {
	if id != "" {
		for _, dep := range models {
			if router.DeploymentID(dep) == id {
				return router.CooldownID(dep)
			}
		}
	}
	return ""
}

// Target 记录当前尝试部署对应的稳定会话目标，供首次 Claim 和成功 Save 使用。
// 参数 dep 为实际候选；返回部署 ID、公开模型、当前池及从原别名到当前池的回退路径。
// 执行器在尝试前调用；Path 复制为独立切片，后续队列变化不会改写已交给缓存的路径。
// 不修改队列；部署缺少稳定 ID 时返回目标仍为空 ID，由上层决定不保存偏好。
func (q *fallbackQueue) Target(dep config.ModelEntry) dpcontract.RouteTarget {
	return dpcontract.RouteTarget{DeploymentID: router.DeploymentID(dep), Model: dep.ModelName, Pool: q.PoolAlias, Path: append([]dpcontract.RouteStep(nil), q.Path...)}
}

// preferredPool 按当前模板重新验证历史会话目标，而不是直接信任缓存里的部署。
// 参数 target 为历史目标；alias 为原请求模型/组；models、state 为当前能力候选和运行快照；allowFallback 控制能否复用跨模型路径。
// 返回合法且首项为目标部署的池及该池设置；失效返回空池，ForModel 配置错误保留在返回设置的 Err 中。
// SelectAffinity 调用：逐条验证回退类别、源、目的边和最终池，再重新调度检查部署 ID 与模型名。
// 组没有显式回退配置时才允许实际成员作为回退源；关闭回退不允许绕过开关复用旧跨模型偏好。
// 只读模板并使用本地状态副本；本方法不验证凭据或租户权限，执行器发送前仍必须检查。
func (q *fallbackQueue) preferredPool(target dpcontract.RouteTarget, alias string, models []config.ModelEntry, state router.State, allowFallback bool) ([]config.ModelEntry, router.RouteSettings) {
	poolName := alias
	if target.DeploymentID == "" {
		return nil, q.settings
	}
	if len(target.Path) > 0 && !allowFallback {
		return nil, q.settings
	}
	for _, step := range target.Path {
		if step.Kind != "general" && step.Kind != "context" && step.Kind != "content" {
			return nil, q.settings
		}
		sourceOK := step.From == poolName
		// 无组别名链时，现有回退规则允许真实成员的回退链。
		if !sourceOK {
			if _, explicit := q.settings.ModelFallbacks[poolName]; explicit {
				return nil, q.settings
			}
			for _, group := range q.settings.RoutingGroups {
				if group.Name == poolName {
					for _, member := range group.Models {
						sourceOK = sourceOK || member == step.From
					}
				}
			}
		}
		edgeOK := false
		for _, name := range q.settings.ModelFallbacks[step.From].Targets(step.Kind) {
			edgeOK = edgeOK || name == step.To
		}
		if !sourceOK || !edgeOK {
			return nil, q.settings
		}
		poolName = step.To
	}
	if target.Pool != poolName {
		return nil, q.settings
	}
	settings := q.settings.ForModel(poolName)
	if settings.Err != nil {
		return nil, settings
	}
	pin := RuntimePin(models, target.DeploymentID)
	if pin == "" {
		return nil, settings
	}
	state.Allocations = settings.Policy.Shares()
	pool := settings.Schedule(models, poolName, state, pin)
	if len(pool) == 0 || router.DeploymentID(pool[0]) != target.DeploymentID || pool[0].ModelName != target.Model {
		return nil, settings
	}
	return pool, settings
}

// SelectAffinity 将可验证会话偏好应用到本轮候选，并协调同会话首次并发请求的选择。
// 参数 plan 为请求私有计划；pool 为正常调度结果；alias、models、state 为原模型与当前候选/快照；settings 为本轮设置。
// 返回最终候选池及其生效设置；两类创建执行器在发送前调用，权限和凭据检查仍留在执行链。
// Required 父响应只允许经过校验的唯一原部署，冷却不阻止尝试；其他资格失效仍返回空池。普通偏好失效则保留正常调度结果。
// Claim 只协调缓存赢家，不持有上游执行锁；赢家也必须重新验证，不能让过期缓存覆盖当前能力或回退开关。
// 合法偏好会恢复队列 PoolAlias、Current、Path 并补充 Seen，防止随后回退再次访问旧路径；普通动态路径复制后持有。
func (q *fallbackQueue) SelectAffinity(plan *dpcontract.RoutePlan, pool []config.ModelEntry, alias string, models []config.ModelEntry, state router.State, settings router.RouteSettings) ([]config.ModelEntry, router.RouteSettings) {
	if plan.Required {
		// 父响应上下文属于特定部署。即使同一池还有健康候选，也不能把续接
		// 请求交给其他部署。允许在同一合法连接上尝试，防止仅因冷却丢弃续接。
		state.Cooldown = nil
		owned, cfg := q.preferredPool(plan.Target, alias, models, state, true)
		if len(owned) > 0 {
			q.PoolAlias, q.Current, q.Path = plan.Target.Pool, plan.Target.Pool, plan.Target.Path
			return owned[:1], cfg
		}
		return nil, settings
	}
	target := plan.Target
	preferred, chosen := q.preferredPool(target, alias, models, state, !q.Disabled)
	// 普通会话偏好只是优先级提示；配置变更或冷却使其失效时，仍使用
	// 本轮正常调度的 pool/settings，不让陈旧缓存阻断整个请求。
	if len(preferred) > 0 {
		pool, settings = preferred, chosen
	}
	if len(pool) == 0 {
		return pool, settings
	}
	if plan.Claim != nil && router.DeploymentID(pool[0]) != "" {
		candidate := q.Target(pool[0])
		if len(preferred) > 0 {
			candidate = target
		}
		winner := plan.Claim(candidate)
		// 并发首请求可能选出另一赢家；Claim 只协调归属，不能授予执行权限。
		// 必须按当前目录与回退规则再验一次，失效赢家不替换本轮合法候选。
		if won, cfg := q.preferredPool(winner, alias, models, state, !q.Disabled); len(won) > 0 {
			pool, settings, target = won, cfg, winner
			preferred = won
		}
	}
	if len(preferred) > 0 {
		q.PoolAlias, q.Current, q.Path = target.Pool, target.Pool, append([]dpcontract.RouteStep(nil), target.Path...)
		for _, step := range q.Path {
			// 恢复历史路径也要恢复访问集合，否则下一轮回退会再次进入
			// 已走过的模型，破坏队列的去重和防环约束。
			q.Seen[step.From] = true
			q.Seen[step.To] = true
		}
	}
	return pool, settings
}
