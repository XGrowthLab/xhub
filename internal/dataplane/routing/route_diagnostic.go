// 本文件只读解释目录、能力、权重和冷却各阶段候选数量，供接口错误与关联日志使用。
// 调用关系与边界：请求候选选择的辅助能力；具体参数、返回及副作用见各方法说明。
package routing

import (
	"errors"
	"fmt"
	"math"

	"github.com/sunqirui1987/xhub/internal/config"
	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
	providerdeployment "github.com/sunqirui1987/xhub/internal/provider/deployment"
	"github.com/sunqirui1987/xhub/internal/router"
	"github.com/sunqirui1987/xhub/internal/util"
)

// routeDiagnosis 保存目录匹配、停用、兼容、权重分配和冷却各阶段的只读统计。
// 由 DiagnoseRoute 创建，Failure 映射错误类别，Log 输出关联 callID 的诊断。
// 统计不推进随机或轮询调度、不证明权限，也不能替代 Schedule 返回的真实执行候选。
type routeDiagnosis struct {
	// 完整目录条目数、模型匹配数、停用数、兼容数、可调度数、冷却数和零权重数；各阶段口径不同，不可直接相加。
	loaded, matched, disabled, compatible, available, cooling, zeroWeight int
	// 请求能力入口，用于诊断协议兼容，而非出站 URL。
	endpoint string
	// 兼容性筛选的逐部署决定及排除理由，保留给日志解释。
	decisions []providerdeployment.CandidateDecision
	// 模板分配中已不对应当前部署的配置，用于提示陈旧 ID。
	stale []router.Allocation
	// 当前冷却与分配等只读快照，诊断不推进真实调度状态。
	state router.State
}

// DiagnoseRoute 只读分析候选消失在哪个路由阶段，供执行失败日志与错误响应使用。
// 参数 directory 为完整目录，eligible 为能力候选；alias、endpoint 指定请求模型与入口；settings、state 是当前模板与运行快照；dialogue 为可选对话需求；adapted 区分适配与原生检查；nativeBody 是可选原生正文。
// 返回 routeDiagnosis，含模型/组匹配、停用、能力、权重、冷却等统计与最终失败原因。
// 执行器没有候选时调用；复制必要目录元数据以匹配停用模型，不能改变真实目录或让诊断本身推进随机/轮询调度。
// 只观察配置与状态，不发送探测、不扣费、不修复旧权重；管理员应根据诊断修正模板或部署。
func DiagnoseRoute(directory, eligible []config.ModelEntry, alias, endpoint string, settings router.RouteSettings, state router.State, dialogue *llmdialogue.Dialogue, adapted bool, nativeBody ...map[string]any) routeDiagnosis {
	// 单部署的历史冷却键不参与本轮选路，诊断展示同样的有效状态。
	if len(state.Cooldown) > 0 && !settings.HasCooldownAlternatives(eligible, alias) {
		state.Cooldown = nil
	}
	d := routeDiagnosis{loaded: len(directory), endpoint: endpoint, state: state}
	active, _ := DropDisabled(directory)
	matched := settings.Candidates(active, alias)
	d.matched = len(matched)
	if !adapted {
		dialogue = nil
	}
	// 决定按启用候选在前、停用候选在后存放；响应映射只依据启用候选。
	_, d.decisions = providerdeployment.Candidates(matched, endpoint, dialogue, nativeBody...)
	// 单独匹配暂停目录，避免全局暂停计数把其他公开模型误判为已禁用；复制元数据不修改共享配置。
	var paused []config.ModelEntry
	for _, dep := range directory {
		if !dep.Disabled() {
			continue
		}
		info := make(map[string]any, len(dep.ModelInfo))
		for k, v := range dep.ModelInfo {
			info[k] = v
		}
		info["disabled"] = false
		dep.ModelInfo = info
		paused = append(paused, dep)
	}
	for _, dep := range settings.Candidates(paused, alias) {
		d.disabled++
		d.decisions = append(d.decisions, providerdeployment.CandidateDecision{Deployment: dep, Reason: "deployment disabled"})
	}
	compatible := settings.Candidates(eligible, alias)
	d.compatible = len(compatible)
	d.available = len(router.Available(compatible, settings.Strategy(), state))
	for _, dep := range compatible {
		if state.Cooldown[router.CooldownID(dep)] {
			d.cooling++
		}
		if router.IsSplitStrategy(settings.Strategy()) {
			w, exists := state.Allocations[router.DeploymentID(dep)]
			if exists && (w <= 0 || math.IsNaN(w) || math.IsInf(w, 0)) {
				d.zeroWeight++
			}
		}
	}
	ids := map[string]bool{}
	for _, decision := range d.decisions {
		ids[router.DeploymentID(decision.Deployment)] = true
	}
	for _, allocation := range settings.Policy.Allocations {
		if !ids[allocation.DeploymentID] {
			d.stale = append(d.stale, allocation)
		}
	}
	return d
}

// Log 将路由诊断摘要写入进程日志，保留调用 ID 以关联同一次请求。
// 参数 path、callID、alias 为入站路径、调用标识和公开模型；settings 为生效模板；返回值无。
// 执行器在无候选分支调用；仅输出计数、模板来源和原因，不输出供应商密钥或请求正文。
// 副作用为日志写入，不重新调度或修改诊断对象。
func (d routeDiagnosis) Log(path, callID, alias string, settings router.RouteSettings) {
	util.Debug("process path=%s step=route_diagnosis call_id=%q model=%q endpoint=%q loaded=%d matched=%d disabled=%d compatible=%d available=%d cooling=%d zero_weight=%d strategy=%q template_id=%q template_name=%q source=%q rule_source=%q", path, callID, alias, d.endpoint, d.loaded, d.matched, d.disabled, d.compatible, d.available, d.cooling, d.zeroWeight, settings.Strategy(), settings.TemplateID, settings.TemplateName, settings.Source, settings.RuleSource)
	for _, decision := range d.decisions {
		dep, reason := decision.Deployment, decision.Reason
		weight, exists := d.state.Allocations[router.DeploymentID(dep)]
		if !exists {
			weight = 1
		}
		if reason == "" && d.state.Cooldown[router.CooldownID(dep)] {
			reason = "deployment is cooling down"
		}
		if reason == "" && router.IsSplitStrategy(settings.Strategy()) && (weight <= 0 || math.IsNaN(weight) || math.IsInf(weight, 0)) {
			reason = "deployment has no allocated traffic"
		}
		if reason == "" {
			reason = "eligible"
		}
		protocol := ""
		transport, _ := dep.ModelInfo["transport"].(string)
		if execution, ok := providerdeployment.Execution(dep); ok {
			protocol = execution.Protocol
		}
		util.Debug("process path=%s step=route_candidate call_id=%q model=%q deployment_id=%q public_model=%q upstream_model=%q connection=%q supplier=%q upstream_host=%q transport=%q protocol=%q weight=%g weight_applied=%t reason=%q", path, callID, alias, router.DeploymentID(dep), dep.ModelName, dep.ParamString("model", ""), dep.ParamString("litellm_credential_name", ""), dep.ParamString("custom_llm_provider", ""), dpdiagnostics.BaseHost(dep.ParamString("api_base", "")), transport, protocol, weight, router.IsSplitStrategy(settings.Strategy()), dpdiagnostics.SafeErr(errors.New(reason)))
	}
	for _, allocation := range d.stale {
		util.Debug("process path=%s step=route_allocation call_id=%q model=%q deployment_id=%q weight=%g reason=%q", path, callID, alias, allocation.DeploymentID, allocation.Weight, "allocation references no matching deployment")
	}
}

// Failure 把路由诊断转换为客户端可处理的 HTTP 状态、错误类型和简短说明。
// 参数 alias 为请求公开模型；返回 status、code、message 和可选的稳定原因，供执行器写协议错误包络。
// 不存在、停用或能力不兼容通常返回 400；分配不可用或没有合规部署返回 503。冷却仅用于候选优先级，不能单独产生错误。
// 纯映射，不写 HTTP 或日志，不把诊断当作绕过资格检查的降级策略。
func (d routeDiagnosis) Failure(alias string) (int, string, string, string) {
	if d.matched == 0 {
		if d.disabled > 0 {
			return 400, "model_disabled", "model is disabled: " + alias, "model_disabled"
		}
		return 400, "invalid_request", "model not found: " + alias, "model_not_found"
	}
	if d.compatible == 0 {
		// 保留原有两个公开错误分类。其他兼容拒绝沿用 model_unavailable，
		// 由调用方附上 ProtocolProblems，避免依赖原错误类型的客户端行为变化。
		for _, known := range []error{llmdialogue.ErrWebSearchUnsupported, llmdialogue.ErrMessagesImageDetailUnsupported} {
			all := true
			for _, decision := range d.decisions[:d.matched] {
				if !errors.Is(decision.CapabilityError, known) {
					all = false
					break
				}
			}
			if all {
				return 400, "unsupported_capability", known.Error(), "protocol_unsupported"
			}
		}
		return 400, "model_unavailable", fmt.Sprintf("model %s exists but has no deployment compatible with endpoint %s and this request; check model transport, endpoint configuration and request capabilities", alias, d.endpoint), "protocol_unsupported"
	}
	if d.zeroWeight == d.compatible {
		return 503, "model_unavailable", "model " + alias + " has no allocated traffic; check deployment weights in model defaults or the selected route template", "routing_no_capacity"
	}
	return 503, "model_unavailable", "model " + alias + " has no available deployment; check route settings and allocations", "routing_no_capacity"
}

// ProtocolProblems 返回启用候选中的公开能力拒绝。pinned 非空时只查看必须续接的部署；
// 普通请求只在兼容候选为零时返回。多连接的同一错误按协议及字段去重，不公开部署或凭据信息。
func (d routeDiagnosis) ProtocolProblems(pinned string) []dpdiagnostics.ProtocolProblem {
	if pinned == "" && d.compatible > 0 {
		return nil
	}
	var problems []dpdiagnostics.ProtocolProblem
	seen := map[dpdiagnostics.ProtocolProblem]bool{}
	for _, decision := range d.decisions[:d.matched] {
		err := decision.CapabilityError
		if err == nil || (pinned != "" && router.DeploymentID(decision.Deployment) != pinned) {
			continue
		}
		var field, detail string
		var requestField *llmdialogue.RequestFieldError
		switch {
		case errors.As(err, &requestField):
			field, detail = requestField.Field, requestField.Message
		case errors.Is(err, llmdialogue.ErrMessagesImageDetailUnsupported):
			detail = llmdialogue.ErrMessagesImageDetailUnsupported.Error()
		case errors.Is(err, llmdialogue.ErrWebSearchUnsupported):
			detail = llmdialogue.ErrWebSearchUnsupported.Error()
		default:
			continue
		}
		profile, _ := providerdeployment.Execution(decision.Deployment)
		problem := dpdiagnostics.NewProtocolProblem(field, profile.Protocol, detail)
		if !seen[problem] {
			problems = append(problems, problem)
			seen[problem] = true
		}
	}
	return problems
}

// Snapshot 投影路由失败的阶段计数、配置来源与逐部署排除原因，供授权请求日志使用。
// 参数 settings 为当前有效模板，pinned 为续接必须使用的稳定部署ID；返回独立JSON对象。
// 执行器仅在拒绝时保存，最多100个候选并标注截断；不保存密钥/地址/请求正文，不推断冷却最初诱因。
func (d routeDiagnosis) Snapshot(settings router.RouteSettings, pinned string) map[string]any {
	candidates := make([]map[string]any, 0, min(100, len(d.decisions)))
	pinReason := "not_in_matched_candidates"
	for i, decision := range d.decisions {
		dep := decision.Deployment
		reason := decision.Reason
		cooling := d.state.Cooldown[router.CooldownID(dep)]
		weight, exists := d.state.Allocations[router.DeploymentID(dep)]
		if !exists {
			weight = 1
		}
		if reason == "" && cooling {
			reason = "deployment is cooling down"
		}
		if reason == "" && router.IsSplitStrategy(settings.Strategy()) && (weight <= 0 || math.IsNaN(weight) || math.IsInf(weight, 0)) {
			reason = "deployment has no allocated traffic"
		}
		if reason == "" {
			reason = "eligible"
		}
		if router.DeploymentID(dep) == pinned {
			pinReason = reason
		}
		if i < 100 {
			safeReason := dpdiagnostics.NewProtocolProblem("", "", reason).Details
			candidates = append(candidates, map[string]any{"deployment_id": router.DeploymentID(dep), "transport": providerdeployment.SelectedTransport(dep), "reason": safeReason, "cooling": cooling})
		}
	}
	result := map[string]any{"endpoint": d.endpoint, "matched": d.matched, "disabled": d.disabled, "compatible": d.compatible, "available": d.available, "cooling": d.cooling, "zero_weight": d.zeroWeight, "strategy": settings.Strategy(), "template_id": settings.TemplateID, "template_source": settings.Source, "candidates": candidates, "candidates_truncated": len(d.decisions) > 100}
	if pinned != "" {
		result["required_deployment_id"] = pinned
		if pinReason == "eligible" {
			pinReason = "current route or affinity validation excluded the original target"
		}
		result["required_deployment_reason"] = pinReason
	}
	return result
}
