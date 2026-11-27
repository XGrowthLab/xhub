// 本文件串联统一入口的鉴权、预算、护栏、缓存、调度、重试、协议转换及最终用量记录。
// 调用关系与边界：数据面执行编排；具体参数、返回及副作用见各方法说明。
package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	dpcontract "github.com/sunqirui1987/xhub/internal/dataplane/contract"
	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	dpdialogue "github.com/sunqirui1987/xhub/internal/dataplane/dialogue"
	dprouting "github.com/sunqirui1987/xhub/internal/dataplane/routing"
	dpstream "github.com/sunqirui1987/xhub/internal/dataplane/stream"
	dptransport "github.com/sunqirui1987/xhub/internal/dataplane/transport"
	dpusage "github.com/sunqirui1987/xhub/internal/dataplane/usage"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
	providercapability "github.com/sunqirui1987/xhub/internal/provider/capability"
	providerdeployment "github.com/sunqirui1987/xhub/internal/provider/deployment"
	"github.com/sunqirui1987/xhub/internal/router"
	"github.com/sunqirui1987/xhub/internal/util"
)

var (
	// 已收到 429/5xx 的内部哨兵；最终对外状态统一为 502，原始状态和正文另行记录。
	errUpstreamStatus = errors.New("upstream_error")
	// 流读取正常结束但未输出内容时不能提交成功归属，仍可在未写出前尝试下一候选。
	errEmptyUpstream = errors.New("empty upstream stream")
)

// commitResponsesHistory 在公开 Responses 成功前确认历史已写入。
// 支持检查结果的宿主返回实际错误；旧测试宿主保持原接口，调用方负责错误响应与计费记录。
func commitResponsesHistory(h dpcontract.Adapted, plan dpcontract.RoutePlan, deploymentID, responseID string) error {
	if responseID != "" && plan.SessionID != "" && len(plan.History) == 0 {
		return errors.New("conversation history unavailable")
	}
	if checked, ok := h.(interface {
		CommitRouteChecked(dpcontract.RoutePlan, string, string) error
	}); ok {
		return checked.CommitRouteChecked(plan, deploymentID, responseID)
	}
	h.CommitRoute(plan, deploymentID, responseID)
	return nil
}

// Serve 执行一次统一推理请求，将调用方协议适配成部署登记的上游协议，并把结果转换回来。
// 参数 h 提供身份、路由、凭据、缓存、护栏与记账能力；w 接收 HTTP 响应；r 提供上下文、头和只读取一次的 JSON 正文；op 是目录操作名，如 chat、responses、embedding。
// 返回值：无；所有成功、拒绝和失败结果直接写入 w。网关统一推理入口调用本方法。
//
// 执行顺序有业务约束：先鉴权并建立会话计划；Responses 恢复授权历史后检查预算与护栏；
// 随后执行并发钩子和扩展，解析请求级模板与协议，检查缓存，再选择兼容部署和动态偏好。
// 每轮发送前补齐服务端凭据并检查实际目标权限；失败时按模板尝试同池部署或跨模型回退。
// 成功路径记录真实出站诊断、会话归属、响应正文和用量；释放并发名额、正文和尝试上下文。
//
// 会话偏好只影响顺序，不能绕过当前能力、冷却、权限和预算。Required 父响应归属禁止换目标。
// 流式响应一旦写出便不能重试，否则客户端会混合两次回复且可能重复计费；断流只记失败。
// Responses 在发布完成事件前保存已验证历史，使客户端收到终态后立即续接也能找到上下文。
// 响应缓存隔离租户、会话、查询及完整路由配置，命中后复核实际模型权限；Responses 不读此缓存。
// 副作用包括上游 HTTP 请求、流式写出、进程/Redis 路由状态、会话缓存和用量记账。
func Serve(h dpcontract.Adapted, w http.ResponseWriter, r *http.Request, op string) {
	dpdiagnostics.TraceHop(r.URL.Path)
	util.Trace("process path=%s step=start op=%s", r.URL.Path, op)
	start := time.Now()
	callID := util.CallID()
	defer h.ReleaseCall(callID)
	util.SetCallID(w, callID)
	// 第一阶段：先取得可信身份，再读取客户端模型和会话信号；这些信号不能自行授予权限。
	p := h.RequireLLMPrincipal(w, r)
	if p == nil {
		util.Debug("process path=%s step=auth ok=false", r.URL.Path)
		return
	}
	beginLogCapture(r.Context(), h, callID, p)
	util.Debug("process path=%s step=auth ok=true kind=%s", r.URL.Path, p.Kind)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		util.Error("process path=%s step=body reason=invalid body", r.URL.Path)
		util.WriteTypedError(w, r.URL.Path, 400, "invalid_request", "invalid body")
		return
	}
	callerProtocol := dpdialogue.CallerProtocol(op)
	body, err := decodeServeBody(raw, callerProtocol != "")
	if err != nil {
		util.Error("process path=%s step=body reason=invalid json", r.URL.Path)
		util.WriteTypedError(w, r.URL.Path, 400, "invalid_request", "invalid json")
		return
	}
	alias, _ := body["model"].(string)
	if alias == "" {
		util.Error("process path=%s step=model reason=required", r.URL.Path)
		util.WriteTypedError(w, r.URL.Path, 400, "invalid_request", "model required")
		return
	}
	cachePolicy := clientCachePolicy{read: true, write: true}
	if callerProtocol != "" {
		cachePolicy, err = consumeClientCachePolicy(body)
		if err != nil {
			util.WriteTypedError(w, r.URL.Path, 400, "invalid_request", err.Error())
			return
		}
	}
	// 原生解析资格仍在历史恢复前观察；选路目录在准入和护栏后另取，不能提前冻结安全检查。
	nativeEligible := op == "responses" && providerdeployment.NativeResponsesConfigured(h.Models()) && h.NativeResponsesAllowed(p.UserID)
	// 会话计划先于调度生成；这里只读偏好或父响应归属，最终候选仍要按当前配置重新验证。
	if op == "responses" {
		// 统一入口的完整客户端历史允许跨模型迁移；不把未知来源的账户密文当作访问旧会话的凭据。
		// 仅改本次副本；previous_response_id 仍走严格归属并在下方恢复服务器保存的原始推理。
		body = llmdialogue.PortableResponsesObject(body, "input")
		w.Header().Set("X-XHub-History-Mode", "portable")
	}
	if op == "chat" {
		// 新用户轮次使用公开历史，保持与标准 Responses 的迁移语义一致。
		// 活跃工具轮次、服务器引用仍走原归属校验，并正常鉴权、预算校验和护栏。
		if portable, ok := llmdialogue.PortableChatHistory(body); ok {
			body = portable
			w.Header().Set("X-XHub-History-Mode", "portable")
		}
	}
	plan := h.PlanRoute(r, alias, body, p)
	w.Header().Set("X-Session-Id", plan.SessionID)
	h.AnnotateCall(callID, dpcontract.CallNote{SessionID: plan.SessionID, SessionSource: plan.SessionSource})
	if plan.Err != nil {
		util.WriteTypedError(w, r.URL.Path, 400, "invalid_request", plan.Err.Error())
		return
	}
	// 恢复经过授权的历史后才检查输入额度和护栏，避免只检查本轮短输入而漏掉真实出站上下文。
	if op == "responses" {
		// Messages 签名引用仍交给既有专用桥接，不进入原生灰度。
		nativeEligible = nativeEligible && len(plan.MessagesHistory) == 0
		body, err = dpdialogue.ResponsesRequestBody(body, plan.History, nativeEligible)
		if err != nil {
			writeProtocolValidation(w, r.URL.Path, callerProtocol, err)
			return
		}
	}
	est := dpusage.EstimateTokens(body)
	if !h.AdmitRequest(w, r.URL.Path, p, alias, est) {
		util.Debug("process path=%s step=limits refused model=%s", r.URL.Path, alias)
		return
	}
	if op == "chat" || op == "responses" || op == "messages" || op == "gemini" || op == "vertex" || op == "" {
		if blocked, msg := h.GuardrailBlocks(callID, body, nativeEligible); blocked {
			util.Error("process path=%s step=guardrail blocked model=%s", r.URL.Path, alias)
			// 护栏拒绝也属于一次真实请求：保存交互并记零费用失败行，控制台才能定位拦截原因。
			h.RememberExchange(callID, r, raw, nil)
			h.RecordSpend(w, p, callID, alias, op, nil, start, false, http.StatusBadRequest, "")
			util.WriteTypedError(w, r.URL.Path, 400, "guardrail_failed", msg)
			return
		}
		// 脱敏可能原地修改 body，必须重编码，让缓存键、交互日志和出站编码器看到同一份输入。
		raw, err = json.Marshal(body)
		if err != nil {
			util.WriteTypedError(w, r.URL.Path, 400, "invalid_request", "invalid guarded request")
			return
		}
	}

	// 第二阶段：读取身份链选中的请求级路由模板，再应用模型默认设置。
	// 策略、重试、超时与冷却都从这个快照读取，不能混入进程共享配置。
	// 保留未套用模型的 baseRouteCfg，跨模型回退时重新解析目标的默认设置。
	directory := h.Models()
	routeCfg := h.RouteSettingsFor(p, directory)
	if routeCfg.Err != nil {
		util.WriteTypedError(w, r.URL.Path, 503, "unavailable", "route template unavailable")
		return
	}
	baseRouteCfg := routeCfg
	routeCfg = routeCfg.ForModel(alias)
	if routeCfg.Err != nil {
		util.WriteTypedError(w, r.URL.Path, 400, "invalid_request", routeCfg.Err.Error())
		return
	}
	tenant := p.Hash
	if tenant == "" {
		tenant = "user:" + p.UserID
	}
	var dialogue llmdialogue.Dialogue
	if callerProtocol != "" {
		if nativeEligible {
			dialogue, err = llmdialogue.ParseNativeResponses(dpdialogue.DialogueRequestBody(body))
			dialogue.DeferredResponses = true
		} else if op == "responses" && len(plan.MessagesHistory) > 0 {
			dialogue, err = llmdialogue.ParseResponsesWithMessagesHistory(dpdialogue.DialogueRequestBody(body), plan.MessagesHistory)
		} else {
			dialogue, err = llmdialogue.ParseDialogue(callerProtocol, dpdialogue.DialogueRequestBody(body))
		}
		if err != nil {
			writeProtocolValidation(w, r.URL.Path, callerProtocol, err)
			return
		}
	}
	scope := map[string]any{
		"models": directory, "router": routeCfg.Settings,
		"model_defaults": routeCfg.ModelDefaults,
		"session":        plan.SessionID, "query": r.URL.RawQuery,
	}
	// 路由文档完整参与缓存隔离；空配置和非空配置使用同一键结构。
	scope["routing_groups"] = routeCfg.RoutingGroups
	scope["fallbacks"] = routeCfg.ModelFallbacks
	cacheScope, err := json.Marshal(scope)
	if err != nil {
		util.WriteTypedError(w, r.URL.Path, 503, "unavailable", "invalid route configuration")
		return
	}
	ck := util.CacheKey(tenant, op, alias, string(raw), string(cacheScope))
	// 内部诊断必须探测当前上游；空缓存键也会令响应写入器跳过存储，普通推理请求仍沿用原键和策略。
	if responseCacheDisabled(r.Context()) {
		ck = ""
	}
	stream, _ := body["stream"].(bool)
	// 路由与回退配置已经进入缓存键，不再用配置是否存在决定缓存读取。
	// Responses 的响应 ID 必须对应本次执行和上下文，不能复用已过期的响应缓存 ID。
	if cachePolicy.read && ck != "" && op != "responses" && !stream {
		if hit, ok := h.ResponseCache().Get(ck); ok {
			// 缓存记录实际执行模型和组；复用成功正文前仍检查当前权限和预算，
			// 与首次调用检查同一范围，不能靠禁用整个回退功能来代替授权。
			checked := map[string]bool{alias: true}
			for _, model := range hit.Models {
				if model != "" && !checked[model] {
					if !h.EnforceIdentityLimits(w, r.URL.Path, p, model, est) {
						return
					}
					checked[model] = true
				}
			}
			elapsed := time.Since(start)
			pt, ct := dpusage.BodyUsage(hit.Body)
			util.Debug("process path=%s step=cache hit=true model=%s", r.URL.Path, alias)
			dpdiagnostics.LogMetrics(r.URL.Path, alias, true, pt, ct, elapsed, elapsed)
			h.RememberExchange(callID, r, raw, hit.Body)
			h.AnnotateCall(callID, dpcontract.CallNote{SessionID: plan.SessionID, CacheKey: ck, CacheHit: true})
			h.WriteCacheHit(w, p, callID, alias, ck, op, hit.Body, start)
			return
		}
		util.Trace("process path=%s step=cache hit=false model=%s", r.URL.Path, alias)
	}
	// no-store仍可读取已有缓存，但本次执行不能写入；空键沿用内部诊断已有的禁止存储路径。
	if !cachePolicy.write {
		ck = ""
	}

	if err := router.ValidateStrategy(routeCfg.Strategy()); err != nil {
		util.Error("process path=%s step=strategy model=%s", r.URL.Path, alias)
		util.WriteTypedError(w, r.URL.Path, 400, "invalid_request", err.Error())
		return
	}
	// 第三阶段：先过滤停用和协议不兼容项，再调度；偏好不能把已剔除的部署塞回候选。
	// 权重仅取自解析后的模板，旧部署权重不会参与任何策略。
	models := directory
	models, _ = dprouting.DropDisabled(models)
	// 实际请求与预览使用同一兼容性判断，策略只处理已经兼容的候选。
	if dpdialogue.CallerProtocol(op) != "" {
		models, _ = providerdeployment.Candidates(models, providercapability.EndpointForOp(op), &dialogue)
	} else {
		models, _ = providerdeployment.Candidates(models, providercapability.EndpointForOp(op), nil)
	}
	state := h.RouteState()
	state.Allocations = routeCfg.Policy.Shares()
	// 诊断保留匹配、兼容与可用三个阶段；只读计算不推进调度状态，不改变既有回退顺序。
	diagnosis := dprouting.DiagnoseRoute(directory, models, alias, providercapability.EndpointForOp(op), routeCfg, state, &dialogue, dpdialogue.CallerProtocol(op) != "")
	diagnosis.Log(r.URL.Path, callID, alias, routeCfg)
	runtimePin := dprouting.RuntimePin(models, plan.Pinned)
	disableFallbacks, _ := body["disable_fallbacks"].(bool)
	fallbacks := dprouting.NewFallbackQueue(baseRouteCfg, alias, plan.Required || disableFallbacks)
	pool, routeCfg := fallbacks.Initial(models, alias, routeCfg, state, runtimePin)
	for target := range fallbacks.Seen {
		if target == alias {
			continue
		}
		settings := baseRouteCfg.ForModel(target)
		if settings.Err == nil {
			targetState := state
			targetState.Allocations = settings.Policy.Shares()
			dprouting.DiagnoseRoute(directory, models, target, providercapability.EndpointForOp(op), settings, targetState, &dialogue, dpdialogue.CallerProtocol(op) != "").Log(r.URL.Path, callID, target, settings)
		}
	}
	pool, routeCfg = fallbacks.SelectAffinity(&plan, pool, alias, models, state, routeCfg)
	if plan.Required && len(pool) == 0 {
		snapshot := diagnosis.Snapshot(routeCfg, plan.Target.DeploymentID)
		if problems := diagnosis.ProtocolProblems(plan.Target.DeploymentID); len(problems) > 0 {
			writeProtocolRejection(w, r.URL.Path, 409, "continuation_unavailable", "request_conversion", callerProtocol, "The original deployment cannot continue this conversation.", problems, snapshot)
			return
		}
		dpdiagnostics.RecordEarlyFailure(w, &dpdiagnostics.UpstreamDiagnostics{Routing: snapshot})
		util.WriteTypedError(w, r.URL.Path, 409, "continuation_unavailable", "original deployment is unavailable")
		return
	}
	if routeCfg.Err != nil {
		util.WriteTypedError(w, r.URL.Path, 503, "unavailable", "fallback route unavailable")
		return
	}
	util.Debug("process path=%s step=route model=%s deployments=%d stream=%t call_id=%q endpoint=%q resolved_model=%q fallback_visited=%d", r.URL.Path, alias, len(pool), stream, callID, providercapability.EndpointForOp(op), fallbacks.PoolAlias, len(fallbacks.Seen)-1)
	if len(pool) == 0 {
		snapshot := diagnosis.Snapshot(routeCfg, "")
		status, kind, message, reason := diagnosis.Failure(alias)
		util.Error("process path=%s step=route call_id=%q model=%q reason=%q message=%q fallback_visited=%d", r.URL.Path, callID, alias, kind, message, len(fallbacks.Seen)-1)
		if problems := diagnosis.ProtocolProblems(""); len(problems) > 0 {
			reason := ""
			if kind != "unsupported_capability" {
				// 有候选因配置等原因被拒时，保留总体路由说明。字段只能解释其中一部分。
				reason = message
			}
			writeProtocolRejection(w, r.URL.Path, status, kind, "request_conversion", callerProtocol, reason, problems, snapshot)
			return
		}
		dpdiagnostics.RecordEarlyFailure(w, &dpdiagnostics.UpstreamDiagnostics{
			Routing: snapshot, Outcome: &dpdiagnostics.GatewayOutcome{Code: reason},
		})
		util.WriteTypedReasonError(w, r.URL.Path, status, kind, message, "", reason)
		return
	}
	// 第四阶段：外层遍历模型回退图，中层遍历部署池，内层按该目标模板重试。
	// Retries 至少返回 1，省略重试字段也会执行首次尝试；这不是额外重试次数。

	var lastErr error
	var lastHTTPError dptransport.ForwardResponse
	var lastProvider string
	var lastDiagnostic *dpdiagnostics.UpstreamDiagnostics
	cancelPhase := "before_upstream"
	triedHTTP := false
	missingCredential := false
	// writeResponseFailure 记录并返回同一份网关 502 错误。部署和用量沿用当前尝试，供应商响应事实仍在独立诊断。
	// 供 Responses 同步传输、结果验证和历史提交失败调用，避免成功正文覆盖最终错误。调用方收到写入后的错误响应。
	writeResponseFailure := func(deployment, kind, detail string, usage map[string]any) {
		const status = http.StatusBadGateway
		reason := kind
		if kind == "upstream_error" {
			reason, detail = dpdiagnostics.UpstreamFaultFeedback(lastDiagnostic, detail)
		}
		if lastDiagnostic != nil {
			lastDiagnostic.Outcome = &dpdiagnostics.GatewayOutcome{Code: reason}
		}
		h.RememberExchange(callID, r, raw, util.TypedReasonErrorBody(r.URL.Path, status, kind, detail, "", reason))
		h.RecordSpend(w, p, callID, alias, op, usage, start, false, status, deployment)
		util.WriteTypedReasonError(w, r.URL.Path, status, kind, detail, "", reason)
	}
	// stopCanceled 在调用方上下文结束时记录一次零费用取消并终止执行。
	// 参数保留已读取正文、用量、部署和首字时间；返回是否已处理取消。
	// 每次出站及传输错误分支调用，不能检查已主动释放的 attempt context，否则正常完成也会被误判。
	// 499 只表示调用方中止，不增加共享部署失败数；已验证的成功终态仍走原有成功结算路径。
	stopCanceled := func(response []byte, usage map[string]any, deployment string, ttft time.Duration) bool {
		if r.Context().Err() == nil {
			return false
		}
		if len(response) == 0 {
			response = []byte("request connection canceled; initiator unknown")
		}
		if lastDiagnostic == nil {
			lastDiagnostic = &dpdiagnostics.UpstreamDiagnostics{}
		}
		lastDiagnostic.Failure = dpdiagnostics.ObserveFailure(r.Context(), cancelPhase, lastErr, time.Since(start))
		lastDiagnostic.Failure.Kind = "request_canceled"
		h.AnnotateCall(callID, dpcontract.CallNote{Upstream: lastDiagnostic})
		h.RememberExchange(callID, r, raw, response)
		h.AnnotateCall(callID, dpcontract.CallNote{Streamed: stream, TTFTMs: dpusage.TtftMillis(ttft), Provider: lastProvider, CacheKey: ck, SessionID: plan.SessionID})
		h.RecordSpend(w, p, callID, alias, op, usage, start, false, 499, deployment)
		util.Info("process path=%s step=cancel status=499 call_id=%q", r.URL.Path, callID)
		return true
	}
	// 分开保存“尚未发 HTTP”“凭据缺失”“构造失败”和“上游失败”的事实，
	// 使最终错误能指出配置或协议问题，避免一律归因为缺少密钥。

	for len(pool) > 0 {
		kind := "general"
		// 参数 id 是本次失败部署。仅在当前池有替代部署时累积冷却，固定会话续接始终使用原部署。
		noteFailure := func(id string) {
			if !plan.Required && routeCfg.HasCooldownAlternatives(models, fallbacks.PoolAlias) {
				h.NoteFailure(id, routeCfg)
			}
		}
		for _, rawDep := range pool {
			cancelPhase = "before_upstream"
			if stopCanceled(nil, nil, "", 0) {
				return
			}
			// 组耗尽后采用最后尝试成员的回退链，保留原组名的初始权限检查。
			fallbacks.UseDeployment(rawDep.ModelName)
			if !plan.Required {
				plan.Target = fallbacks.Target(rawDep)
			}
			// 回退目标可能是另一个组；组别名与真实成员都必须通过权限和预算检查。
			if fallbacks.PoolAlias != alias && fallbacks.PoolAlias != rawDep.ModelName && !h.EnforceIdentityLimits(w, r.URL.Path, p, fallbacks.PoolAlias, est) {
				return
			}
			if triedHTTP {
				// 重新解析的是调用方身份，确认密钥未在两次出站之间撤销；不是重新附加供应商凭据。
				// 身份与预算必须在继续执行前复核，不能沿用首次鉴权作为整个重试链的许可。
				p2, err := h.ResolveRequest(r)
				if err != nil || p2 == nil || !p2.CanInfer() {
					util.WriteTypedError(w, r.URL.Path, 401, "invalid_api_key", "Authentication Error, No api key passed in.")
					return
				}
				p = p2
				if !h.EnforceIdentityLimits(w, r.URL.Path, p, alias, est) {
					return
				}
			}
			if rawDep.ModelName != alias && !h.EnforceIdentityLimits(w, r.URL.Path, p, rawDep.ModelName, est) {
				return
			}
			dep, credentialErr := h.AttachCredential(rawDep)
			if credentialErr != nil {
				missingCredential = true
				util.Debug("skip deployment path=%s model=%s reason=credential_unavailable", r.URL.Path, alias)
				continue
			}
			// 固定历史必须比对首次外发时的实际连接，不能只靠可编辑的稳定部署 ID。
			if !dprouting.BindConnection(&plan, dep) {
				dpdiagnostics.RecordEarlyFailure(w, &dpdiagnostics.UpstreamDiagnostics{Routing: map[string]any{"required_deployment_id": plan.Target.DeploymentID, "required_deployment_reason": "connection_identity_changed_or_missing"}})
				util.WriteTypedError(w, r.URL.Path, 409, "unavailable", "response deployment unavailable")
				return
			}
			upstreamModel := dep.ParamString("model", alias)
			supplier := dep.ParamString("custom_llm_provider", "")
			realModel := upstreamModel
			lastProvider = supplier
			var built dptransport.Upstream
			// 每个候选独立选择执行器，不能把前一候选的转换结果复用于回退部署。
			dialogue, err := providerdeployment.PrepareDialogue(dep, dialogue)
			if err != nil {
				lastErr = err
				continue
			}
			if op == "responses" {
				path := "legacy"
				if dialogue.NativeResponses {
					path = "native"
				}
				w.Header().Set("X-XHub-Responses-Path", path)
				util.Debug("responses execution call_id=%s path=%s", callID, path)
			}
			apiBase := dep.ParamString("api_base", "")
			did := router.CooldownID(rawDep)
			// 在执行时保存已解析凭证的连接身份，最终失败可能没有 depID，且配置可能随后改变。
			connection, upstreamModel := dep.MonitorIdentity()
			h.AnnotateCall(callID, dpcontract.CallNote{Supplier: connection, APIBase: dep.LogAPIBase(), UpstreamModel: upstreamModel, UpstreamSelected: true})
			if dpdialogue.CallerProtocol(op) != "" {
				built, err = dpdialogue.BuildDialogue(dep, dialogue)
				if errors.Is(err, dpdialogue.ErrDialogueCredentials) {
					missingCredential = true
					util.Debug("skip deployment path=%s provider=%s model=%s reason=missing api key or api base", r.URL.Path, supplier, realModel)
					continue
				}
				if err != nil {
					lastErr = err
					continue
				}
			} else {
				built, err = dpdialogue.BuildRegisteredOperation(dep, body, op)
				if errors.Is(err, dpdialogue.ErrDialogueCredentials) {
					missingCredential = true
					continue
				}
				if err != nil {
					lastErr = err
					continue
				}
			}

			execution, _ := providerdeployment.Execution(dep)
			repairingHistory := false
			limit := routeCfg.Retries()
			for try := 0; try < limit; try++ {
				if stopCanceled(nil, nil, did, 0) {
					return
				}
				if try > 0 {
					fresh, err := h.ResolveRequest(r)
					if err != nil || fresh == nil || !fresh.CanInfer() {
						util.WriteTypedError(w, r.URL.Path, 401, "invalid_api_key", "invalid api key")
						return
					}
					p = fresh
					if rawDep.ModelName != alias && !h.EnforceIdentityLimits(w, r.URL.Path, p, rawDep.ModelName, est) {
						return
					}
					if !h.EnforceIdentityLimits(w, r.URL.Path, p, alias, est) {
						return
					}
				}
				// 每次尝试重新设置，回退到原生搜索部署时不能遗留旧降级提示。
				w.Header().Del("X-XHub-Dropped-Capabilities")
				if len(built.DroppedCapabilities) > 0 {
					w.Header().Set("X-XHub-Dropped-Capabilities", strings.Join(built.DroppedCapabilities, ","))
					util.Info("process step=capability_dropped call_id=%q deployment_id=%q capability=web_search reason=deployment_unsupported", callID, router.DeploymentID(rawDep))
				}
				if budget, ok := h.(dpcontract.BudgetHost); ok {
					var allowed bool
					built.Body, allowed = budget.PrepareBudgetAttempt(w, r, p, callID, dep, execution.Protocol, built.Body, start)
					if !allowed {
						return
					}
				}
				triedHTTP = true
				util.Trace("process path=%s step=attempt n=%d provider=%s model=%s base_host=%s", r.URL.Path, try+1, supplier, realModel, dpdiagnostics.BaseHost(apiBase))
				h.IncBusy(did)
				// 每次尝试拥有独立 deadline；复制客户端并清零 Timeout，让当前模板控制完整读取时限。
				// cancelAttempt、正文 Close 和 DecBusy 必须在每条退出路径配对，不能只清理成功路径。
				attemptCtx, cancelAttempt := context.WithTimeout(r.Context(), time.Duration(routeCfg.TimeoutSeconds()*float64(time.Second)))
				req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, built.URL, bytes.NewReader(built.Body))
				if err != nil {
					cancelAttempt()
					h.DecBusy(did)
					lastErr = err
					util.Error("upstream request path=%s provider=%s model=%s err=%s", r.URL.Path, supplier, realModel, dpdiagnostics.SafeErr(err))
					continue
				}
				for key, values := range built.Header {
					req.Header[key] = values
				}
				dptransport.SetUpstreamCallID(req.Header, callID)
				client := *h.HTTPClient()
				client.Timeout = 0
				// 明确地址不跟随重定向，防止凭据被转发到未配置的连接。
				client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
				req, firstByte := dpdiagnostics.TraceFirstByte(req)
				if budget, ok := h.(dpcontract.BudgetHost); ok && !budget.MarkBudgetAttemptSubmitted(w, r, callID) {
					cancelAttempt()
					h.DecBusy(did)
					return
				}
				cancelPhase = "upstream_request"
				resp, err := client.Do(req)
				if err != nil {
					// 即使未收到响应，也保存本次尝试的实际出站路径，便于失败日志定位。
					lastDiagnostic = &dpdiagnostics.UpstreamDiagnostics{Endpoint: req.URL.EscapedPath(), DroppedCapabilities: built.DroppedCapabilities, Failure: dpdiagnostics.ObserveFailure(r.Context(), "upstream_request", err, time.Since(start))}
					h.AnnotateCall(callID, dpcontract.CallNote{Upstream: lastDiagnostic})
					cancelAttempt()
					h.DecBusy(did)
					lastErr = err
					if stopCanceled(nil, nil, did, 0) {
						return
					}
					noteFailure(did)
					util.Error("upstream dial path=%s provider=%s model=%s base_host=%s err=%s", r.URL.Path, supplier, realModel, dpdiagnostics.BaseHost(apiBase), dpdiagnostics.SafeErr(err))
					if dialogue.NativeResponses || repairingHistory {
						// 提交后网络错误不能证明供应商未执行，原生灰度不自动重放生成。
						writeResponseFailure(did, "upstream_error", dpdiagnostics.SafeErr(err), nil)
						return
					}
					continue
				}
				// 先观察原始响应，再执行错误重试和协议转换。
				resp.Request = req
				diagnostic := dpdiagnostics.ObserveUpstream(resp)
				lastDiagnostic = diagnostic
				cancelPhase = "response_transfer"
				diagnostic.DroppedCapabilities = built.DroppedCapabilities
				if !firstByte.IsZero() {
					diagnostic.FirstByteAt = *firstByte
				}
				h.AnnotateCall(callID, dpcontract.CallNote{Upstream: diagnostic})
				if !repairingHistory && (resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests) {
					// 重试前保存完整错误正文；若所有候选都失败，最终日志必须能还原最后一次上游响应。
					errorBody, readErr := io.ReadAll(resp.Body)
					resp.Body.Close()
					cancelAttempt()
					h.DecBusy(did)
					completeError := readErr == nil || diagnostic.ObserveCompleteErrorBody(errorBody)
					if cause := r.Context().Err(); cause != nil {
						// 上游响应头和入站取消是不同事实；即使只允许一次尝试，也必须在返回前记录 499。
						receivedError := errorBody
						if !completeError {
							receivedError = nil
						}
						lastErr = readErr
						if lastErr == nil {
							lastErr = cause
						}
						if stopCanceled(receivedError, nil, did, 0) {
							return
						}
					}
					noteFailure(did)
					lastHTTPError = dptransport.ForwardResponse{StatusCode: resp.StatusCode, Header: resp.Header, Body: errorBody}
					lastErr = errUpstreamStatus
					util.Error("upstream status path=%s provider=%s model=%s base_host=%s status=%d", r.URL.Path, supplier, realModel, dpdiagnostics.BaseHost(apiBase), resp.StatusCode)
					continue
				}

				// 专用 4xx 在分类后切换模型；普通 4xx 保留原状态与正文。
				if resp.StatusCode >= 400 {
					// 回退判断基于原始预读事实：传输截断时不能仅凭错误片段切换模型。
					errorAttempt := readBufferedAttempt(resp)
					completeError := errorAttempt.readErr == nil || diagnostic.ObserveCompleteErrorBody(errorAttempt.body)
					// 正文预读时请求上下文可能结束；不能把不完整响应当作完整上游 4xx，也不能重试。
					if errorAttempt.readErr != nil && r.Context().Err() != nil {
						receivedError := errorAttempt.body
						if !completeError {
							receivedError = nil
						}
						cancelAttempt()
						h.DecBusy(did)
						lastErr = errorAttempt.readErr
						if stopCanceled(receivedError, nil, did, 0) {
							return
						}
					}
					if !repairingHistory && !plan.Required && errorAttempt.readErr == nil && r.Context().Err() == nil {
						if recovered, ok := dptransport.RecoverDialogueHistory(execution.Protocol, built.Body, errorAttempt.status, errorAttempt.body); ok {
							// 已收到完整拒绝且还未发客户端响应。同一部署只补发一次，
							// 复用调用 ID 并重新进行鉴权、预算检查和出站计量。
							cancelAttempt()
							h.DecBusy(did)
							util.Debug("retry after invalid reasoning history call_id=%s", callID)
							built.Body, repairingHistory, limit = recovered, true, try+2
							continue
						}
					}
					upstreamError := dptransport.ForwardResponse{StatusCode: errorAttempt.status, Header: resp.Header, Body: errorAttempt.body}
					errorKind := dprouting.FallbackKind(errorAttempt.status, errorAttempt.body)
					if !repairingHistory && errorAttempt.readErr == nil && errorKind != "" && !fallbacks.Disabled && len(baseRouteCfg.ModelFallbacks[fallbacks.Current].Targets(errorKind)) > 0 {
						cancelAttempt()
						h.DecBusy(did)
						lastHTTPError = upstreamError
						lastErr = errUpstreamStatus
						kind = errorKind
						goto nextFallbackModel
					}
					// HTTP 错误正文已经读取并完成回退判断，保留字节和供应商请求 ID，不走成功结构或 SSE 编码。
					cancelAttempt()
					h.DecBusy(did)
					h.NoteLatency(did, float64(time.Since(start).Milliseconds()))
					h.SetChatHeaders(w, p, alias, apiBase)
					h.RememberExchange(callID, r, raw, errorAttempt.body)
					h.AnnotateCall(callID, dpcontract.CallNote{Provider: supplier, CacheKey: ck, SessionID: plan.SessionID})
					h.RecordSpend(w, p, callID, alias, op, nil, start, false, errorAttempt.status, did)
					dptransport.WriteThrough(w, upstreamError)
					return
				}
				h.NoteLatency(did, float64(time.Since(start).Milliseconds()))
				h.SetChatHeaders(w, p, alias, apiBase)
				if stream {
					attempt := streamAttempt{upstreamStatus: resp.StatusCode}
					preparationFailed := false
					if dpdialogue.CallerProtocol(op) != "" && resp.StatusCode < 400 {
						execution, _ := providerdeployment.Execution(dep)
						// 终态发送前提交归属与历史，使收到完成事件的客户端能立即续接。
						// commit 参数 result 为验证后的回复，无返回值；Responses 历史及 Chat/Messages 推理归属均须先于终态可见。
						commit := func(result llmdialogue.DialogueResult) {
							if op == "responses" {
								plan.History = dpdialogue.ResponseHistory(dialogue, body, result)
							}
							if op == "responses" || op == "messages" || op == "chat" {
								h.CommitRoute(plan, router.DeploymentID(rawDep), result.ID)
							}
						}
						checkedCommit := func(result llmdialogue.DialogueResult) error {
							plan.History = dpdialogue.ResponseHistory(dialogue, body, result)
							err := commitResponsesHistory(h, plan, router.DeploymentID(rawDep), result.ID)
							preparationFailed = err != nil
							return err
						}
						if op == "responses" && execution.Protocol == "openai-responses" {
							attempt.clientStarted, attempt.usage, attempt.ttft, attempt.body, attempt.err = dpdialogue.PipePortableResponsesChecked(w, resp, start, alias, dialogue.NativeResponses, checkedCommit)
						} else if op == "responses" && execution.Protocol == "anthropic-messages" && plan.PrepareMessages != nil {
							// 引用必须在 output_item.done 之前持久化；工具增量可以先发送，完成项不能先于归属。
							prepare := func(result llmdialogue.DialogueResult) (llmdialogue.DialogueResult, error) {
								prepared, err := plan.PrepareMessages(plan.Target, result)
								if err == nil && plan.CommitMessagesHistory != nil {
									plan.History = dpdialogue.ResponseHistory(dialogue, body, prepared)
									err = plan.CommitMessagesHistory(plan.Target, prepared.ID, plan.History)
								}
								preparationFailed = err != nil
								return prepared, err
							}
							attempt.clientStarted, attempt.usage, attempt.ttft, attempt.body, attempt.err = dpdialogue.PipeDialogueWithPreparation(w, resp, start, alias, execution.Protocol, dpdialogue.CallerProtocol(op), dialogue.ToolBindings, prepare)
						} else if op == "responses" {
							attempt.clientStarted, attempt.usage, attempt.ttft, attempt.body, attempt.err = dpdialogue.PipeDialogueWithCheckedCommit(w, resp, start, alias, execution.Protocol, dpdialogue.CallerProtocol(op), dialogue.ToolBindings, checkedCommit)
						} else {
							attempt.clientStarted, attempt.usage, attempt.ttft, attempt.body, attempt.err = dpdialogue.PipeDialogueWithTools(w, resp, start, alias, execution.Protocol, dpdialogue.CallerProtocol(op), dialogue.ToolBindings, commit)
						}
					} else {
						attempt.clientStarted, attempt.usage, attempt.ttft, attempt.body, attempt.err = dpstream.PipeStream(w, resp, start)
					}
					h.DecBusy(did)
					cancelAttempt()
					if attempt.err != nil {
						lastErr = attempt.err
						diagnostic.Failure = dpdiagnostics.ObserveFailure(r.Context(), "response_transfer", attempt.err, time.Since(start))
						// 写失败会同时触发请求上下文取消；已观察到写入错误时保留更具体的诊断事实。
						if !errors.Is(attempt.err, dpstream.ErrClientWrite) && stopCanceled(attempt.body, attempt.usage, did, attempt.ttft) {
							return
						}
						if errors.Is(attempt.err, dpstream.ErrClientWrite) {
							// 写客户端失败不证明供应商异常，也不证明用户主动取消；保留可观测阶段并避免污染供应商冷却。
							diagnostic.Failure.Phase = "client_write"
							diagnostic.Failure.Kind = "client_write_error"
							// net.OpError 可能包含客户端和网关地址；日志仅保留安全的失败类别。
							diagnostic.Failure.TransportError = "client response write failed"
							util.Error("client response write path=%s model=%s", r.URL.Path, realModel)
						} else {
							// 本地历史写入失败属于存储故障，不能触发供应商冷却。
							if !preparationFailed {
								noteFailure(did)
							}
							util.Error("upstream stream path=%s provider=%s model=%s base_host=%s err=%s", r.URL.Path, supplier, realModel, dpdiagnostics.BaseHost(apiBase), dpdiagnostics.SafeErr(attempt.err))
						}
						if attempt.mustStopAfterFailure(preparationFailed, dialogue.NativeResponses || repairingHistory) {
							// 已输出、历史提交失败或已做过一次兼容重发时不再换目标；原生响应同样不可盲目重放。
							clientError := attempt.initialFailureBody(r.URL.Path, dpdialogue.CallerProtocol(op), dialogue.NativeResponses || repairingHistory, diagnostic)
							captured := attempt.body
							if len(clientError) != 0 {
								captured = clientError
							}
							h.RememberExchange(callID, r, raw, captured)
							h.AnnotateCall(callID, dpcontract.CallNote{Streamed: true, TTFTMs: dpusage.TtftMillis(attempt.ttft), Provider: supplier, CacheKey: ck, SessionID: plan.SessionID})
							h.RecordSpend(w, p, callID, alias, op, attempt.usage, start, false, http.StatusBadGateway, did)
							if len(clientError) != 0 {
								// SSE 处理器在读首帧前已设置流类型，此处尚未写出正文，可以返回 JSON。
								w.Header().Set("Content-Type", "application/json")
								util.WriteJSON(w, http.StatusBadGateway, json.RawMessage(clientError))
							}
							return
						}
						continue
					}
					if attempt.clientStarted {
						if dpdialogue.CallerProtocol(op) != "" {
							attempt.usage = llmdialogue.NormalizeDialogueUsage(attempt.usage)
						} else {
							attempt.usage = dpusage.CompleteUsage(attempt.usage, body, attempt.body)
						}
						pt, ct := dpusage.UsageCounts(attempt.usage)
						dpdiagnostics.LogMetrics(r.URL.Path, alias, false, pt, ct, attempt.ttft, time.Since(start))
						depID := did
						h.RememberExchange(callID, r, raw, attempt.body)
						h.AnnotateCall(callID, dpcontract.CallNote{
							Streamed: true, TTFTMs: dpusage.TtftMillis(attempt.ttft), Provider: supplier, CacheKey: ck,
							SessionID: plan.SessionID,
						})
						if attempt.upstreamStatus < 400 && op != "responses" {
							h.CommitRoute(plan, router.DeploymentID(rawDep), dpstream.ResponseID(attempt.body))
						}
						h.RecordSpend(w, p, callID, alias, op, attempt.usage, start, false, attempt.upstreamStatus, depID)
						return
					}
					lastErr = errEmptyUpstream
					util.Error("upstream stream path=%s provider=%s model=%s base_host=%s err=%s", r.URL.Path, supplier, realModel, dpdiagnostics.BaseHost(apiBase), dpdiagnostics.SafeErr(errEmptyUpstream))
					continue
				}
				messagesHistoryCommitted := false
				attempt := readBufferedAttempt(resp)
				cancelAttempt()
				h.DecBusy(did)
				if attempt.readErr != nil {
					lastErr = attempt.readErr
					diagnostic.Failure = dpdiagnostics.ObserveFailure(r.Context(), "response_transfer", attempt.readErr, time.Since(start))
					if stopCanceled(attempt.body, nil, did, 0) {
						return
					}
					noteFailure(did)
					if dialogue.NativeResponses || repairingHistory {
						writeResponseFailure(did, "upstream_error", dpdiagnostics.SafeErr(attempt.readErr), nil)
						return
					}
					continue
				}
				if dpdialogue.CallerProtocol(op) != "" && attempt.status < 400 {
					var result llmdialogue.DialogueResult
					var conversionErr error
					if dialogue.NativeResponses {
						result, conversionErr = llmdialogue.ParseNativeResponsesResult(attempt.body)
					} else {
						result, conversionErr = llmdialogue.ParseDialogueResult(execution.Protocol, attempt.body)
					}
					if conversionErr != nil {
						if dialogue.NativeResponses || repairingHistory {
							// 上游已返回成功HTTP，原生终态损坏不能再次生成；保留失败和已有用量供对账。
							noteFailure(did)
							var observed map[string]any
							_ = json.Unmarshal(attempt.body, &observed)
							usage, _ := observed["usage"].(map[string]any)
							writeResponseFailure(did, "upstream_error", dpdiagnostics.SafeErr(conversionErr), llmdialogue.NormalizeDialogueUsage(usage))
							return
						}
						lastErr = conversionErr
						// 无法解析的上游 HTTP 200 正文与网络错误、5xx 使用相同的失败记录条件。
						noteFailure(did)
						continue
					}
					result.ToolBindings = dialogue.ToolBindings
					if op == "responses" && execution.Protocol == "anthropic-messages" && plan.PrepareMessages != nil {
						prepared, prepareErr := plan.PrepareMessages(plan.Target, result)
						if prepareErr == nil && plan.CommitMessagesHistory != nil {
							plan.History = dpdialogue.ResponseHistory(dialogue, body, prepared)
							prepareErr = plan.CommitMessagesHistory(plan.Target, prepared.ID, plan.History)
						}
						if prepareErr != nil {
							// 上游已完整完成，本地历史失败不能再次付费生成；保留真实 usage 和失败原因，预占待核对。
							h.AnnotateCall(callID, dpcontract.CallNote{Provider: supplier, SessionID: plan.SessionID})
							writeResponseFailure(did, "history_unavailable", dpdiagnostics.SafeErr(prepareErr), llmdialogue.NormalizeDialogueUsage(result.Usage))
							return
						}
						result = prepared
						messagesHistoryCommitted = plan.CommitMessagesHistory != nil
					}
					attempt.body, conversionErr = llmdialogue.EncodeDialogueResult(result, dpdialogue.CallerProtocol(op), alias)
					if conversionErr == nil && op == "responses" {
						// 编码后统一清理，覆盖原生和 Chat 桥接生成的 reasoning ID；原始结果仍供服务器续接。
						var public map[string]any
						conversionErr = json.Unmarshal(attempt.body, &public)
						if conversionErr == nil {
							attempt.body, conversionErr = json.Marshal(llmdialogue.PortableResponsesObject(public, "output"))
						}
					}
					if conversionErr != nil {
						lastErr = conversionErr
						// 编码失败同样不能提交路由归属；记录部署失败后再尝试候选项。
						noteFailure(did)
						continue
					}
					if op == "responses" {
						plan.History = dpdialogue.ResponseHistory(dialogue, body, result)
					}
				}
				elapsed := time.Since(start)
				pt, ct := dpusage.BodyUsage(attempt.body)
				util.Info("process path=%s step=upstream status=%d provider=%s model=%s", r.URL.Path, attempt.status, supplier, realModel)
				dpdiagnostics.LogMetrics(r.URL.Path, alias, false, pt, ct, elapsed, elapsed)
				depID := did
				h.RememberExchange(callID, r, raw, attempt.body)
				h.AnnotateCall(callID, dpcontract.CallNote{
					TTFTMs: dpusage.TtftMillis(elapsed), Provider: supplier, CacheKey: ck,
					SessionID: plan.SessionID,
				})
				if attempt.status < 400 && !messagesHistoryCommitted {
					if op == "responses" {
						if err := commitResponsesHistory(h, plan, router.DeploymentID(rawDep), dpstream.ResponseID(attempt.body)); err != nil {
							var response struct {
								Usage map[string]any `json:"usage"`
							}
							_ = json.Unmarshal(attempt.body, &response)
							writeResponseFailure(did, "history_unavailable", "conversation history could not be saved", llmdialogue.NormalizeDialogueUsage(response.Usage))
							return
						}
					} else {
						h.CommitRoute(plan, router.DeploymentID(rawDep), dpstream.ResponseID(attempt.body))
					}
				}
				h.WriteChatJSON(w, p, callID, alias, ck, op, "", attempt.body, attempt.status, start, depID, fallbacks.PoolAlias, rawDep.ModelName)
				return
			}
			if repairingHistory {
				// 兼容出站已结束，即使没有成功终态也不能使用改写的历史换其他部署。
				goto completedAttempts
			}
		}

	nextFallbackModel:
		pool, routeCfg = fallbacks.Next(kind, models, state)
		if routeCfg.Err != nil {
			util.WriteTypedError(w, r.URL.Path, 503, "unavailable", "fallback route unavailable")
			return
		}
	}

completedAttempts:
	// 最终失败写交互与零费用用量行，控制台可通过请求 ID 对照客户端收到的错误。
	// 未发出 HTTP 时区分凭据和编码错误；已经尝试 HTTP 时保留最后上游状态/正文或安全错误。
	if !triedHTTP {
		switch {
		case missingCredential:
			util.Error("dataplane path=%s status=401 code=authentication_error provider=%s", r.URL.Path, lastProvider)
			h.RememberExchange(callID, r, raw, []byte("This model has no upstream API key configured."))
			h.RecordSpend(w, p, callID, alias, op, nil, start, false, http.StatusUnauthorized, "")
			util.WriteTypedError(w, r.URL.Path, 401, "authentication_error", "This model has no upstream API key configured.")
		case lastErr != nil:
			// 部署存在但不能构造所需协议请求，报告安全编码错误才能指出应修复的配置或能力。
			util.Error("dataplane path=%s status=400 code=provider_not_implemented provider=%s err=%s",
				r.URL.Path, lastProvider, dpdiagnostics.SafeErr(lastErr))
			detail := dpdiagnostics.SafeErr(lastErr)
			h.RememberExchange(callID, r, raw, []byte("The request could not be encoded for "+lastProvider+": "+detail))
			// RecordSpend 必须在写响应前执行，确保失败行携带最终 HTTP 状态和错误正文。
			h.RecordSpend(w, p, callID, alias, op, nil, start, false, http.StatusBadRequest, "")
			util.WriteTypedError(w, r.URL.Path, 400, "execution_error",
				"The request could not be encoded for "+lastProvider+": "+detail)
		default:
			util.Error("dataplane path=%s status=400 code=provider_not_implemented", r.URL.Path)
			h.RememberExchange(callID, r, raw, []byte("execution_error"))
			h.RecordSpend(w, p, callID, alias, op, nil, start, false, http.StatusBadRequest, "")
			util.WriteTypedError(w, r.URL.Path, 400, "execution_error", "execution_error")
		}
		return
	}
	// 供应商流内错误已有结构，不能再次压成 upstream_error 字符串；HTTP 200 仅代表流握手成功。
	var upstreamFailure *dpdiagnostics.UpstreamError
	if errors.As(lastErr, &upstreamFailure) {
		envelope := dpdiagnostics.ErrorEnvelope(upstreamFailure)
		if dpdialogue.CallerProtocol(op) == "anthropic-messages" {
			envelope["type"] = "error"
		}
		rawError, _ := json.Marshal(envelope)
		h.RememberExchange(callID, r, raw, rawError)
		h.RecordSpend(w, p, callID, alias, op, nil, start, false, http.StatusBadGateway, "")
		util.WriteJSON(w, http.StatusBadGateway, envelope)
		return
	}

	if lastHTTPError.StatusCode >= 400 && errors.Is(lastErr, errUpstreamStatus) {
		// 只返回最后一次真实 HTTP 错误。诊断独立记录空正文和传输异常，原始响应保持状态、字节及查单头。
		util.Error("dataplane path=%s status=%d code=upstream_error detail=upstream %d", r.URL.Path, lastHTTPError.StatusCode, lastHTTPError.StatusCode)
		h.RememberExchange(callID, r, raw, lastHTTPError.Body)
		h.RecordSpend(w, p, callID, alias, op, nil, start, false, lastHTTPError.StatusCode, "")
		dptransport.WriteThrough(w, lastHTTPError)
		return
	}

	if lastErr != nil {
		reason, detail := dpdiagnostics.UpstreamFaultFeedback(lastDiagnostic, dpdiagnostics.SafeErr(lastErr))
		if lastDiagnostic != nil {
			lastDiagnostic.Outcome = &dpdiagnostics.GatewayOutcome{Code: reason}
		}
		util.Error("dataplane path=%s status=502 code=upstream_error detail=%s reason=%s", r.URL.Path, dpdiagnostics.SafeErr(lastErr), reason)
		h.RememberExchange(callID, r, raw, util.TypedReasonErrorBody(r.URL.Path, 502, "upstream_error", detail, "", reason))
		h.RecordSpend(w, p, callID, alias, op, nil, start, false, http.StatusBadGateway, "")
		util.WriteTypedReasonError(w, r.URL.Path, 502, "upstream_error", detail, "", reason)
		return
	}
	util.Error("dataplane path=%s status=502 code=upstream_error detail=all deployments failed", r.URL.Path)
	h.RememberExchange(callID, r, raw, []byte("all deployments failed"))
	h.RecordSpend(w, p, callID, alias, op, nil, start, false, http.StatusBadGateway, "")
	util.WriteTypedError(w, r.URL.Path, 502, "upstream_error", "all deployments failed")
}
