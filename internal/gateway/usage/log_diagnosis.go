package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sunqirui1987/xhub/internal/iam"
)

// logDiagnosis 只输出稳定原因码和已观测的诊断事实。列表不输出错误正文，详情的原有正文仍走原审计流程。
type logDiagnosis struct {
	Code                  string `json:"code,omitempty"`
	RequestMethod         string `json:"request_method,omitempty"`
	ModelMissingReason    string `json:"model_missing_reason,omitempty"`
	UserMissingReason     string `json:"user_missing_reason,omitempty"`
	TeamMissingReason     string `json:"team_missing_reason,omitempty"`
	UpstreamStatus        int    `json:"upstream_status,omitempty"`
	Phase                 string `json:"phase,omitempty"`
	CoolingDeployments    *int   `json:"cooling_deployments,omitempty"`
	CompatibleDeployments *int   `json:"compatible_deployments,omitempty"`
}

type savedLogFailure struct {
	Phase string `json:"phase"`
	Kind  string `json:"kind"`
}

type savedLogError struct {
	Error struct {
		Reason  string `json:"reason"`
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	Diagnostics savedLogFailure `json:"diagnostics"`
}

type savedLogDiagnostics struct {
	Status    int                        `json:"status_code"`
	Failure   savedLogFailure            `json:"failure"`
	ReadError string                     `json:"read_error"`
	Error     map[string]json.RawMessage `json:"error"`
	Protocol  struct {
		Stage string `json:"stage"`
	} `json:"protocol"`
	Routing struct {
		Compatible *int `json:"compatible"`
		Cooling    *int `json:"cooling"`
	} `json:"routing"`
	Outcome logDiagnosis `json:"outcome"`
}

// enrichLogRows 将当前页或已授权详情的请求映射为可读身份和安全诊断。
// ctx 可取消查询，events 与 rows 顺序一致；detail 传入已读取的详情时复用正文，否则只批查当前页失败摘要。
// 返回身份或存储查询错误，缺失或历史损坏的单条诊断按可见事实降级。不查询现有团队来替代历史归属。
func enrichLogRows(ctx context.Context, db *iam.DB, events []iam.UsageEvent, rows []map[string]any, detail *iam.RequestLog) error {
	userIDs := make([]string, 0, len(events))
	userSeen := make(map[string]bool, len(events))
	failureIDs := make([]string, 0, len(events))
	for _, event := range events {
		if event.UserID != "" && !userSeen[event.UserID] {
			userSeen[event.UserID] = true
			userIDs = append(userIDs, event.UserID)
		}
		if event.LogSeverity() != iam.UsageSeveritySuccess {
			failureIDs = append(failureIDs, event.RequestID)
		}
	}
	// 仅查询已经通过日志范围校验的身份；返回展示名，空姓名时保留已有 UID。
	users, err := db.UsersByIDs(ctx, userIDs)
	if err != nil {
		return err
	}
	var saved map[string]iam.RequestLog
	if detail != nil {
		saved = map[string]iam.RequestLog{detail.RequestID: *detail}
	} else {
		saved, err = db.LogSummariesByIDs(ctx, failureIDs)
		if err != nil {
			return err
		}
	}
	for i, event := range events {
		user, found := users[event.UserID]
		if name := strings.TrimSpace(user.Name); name != "" {
			rows[i]["user_name"] = name
		}
		rows[i]["severity"] = event.LogSeverity()
		rows[i]["diagnosis"] = diagnoseLog(event, saved[event.RequestID], user, found)
	}
	return nil
}

// diagnoseLog 根据账单快照、已保存错误和现有用户目录生成原因码，供列表和详情共用。
// event 保留历史归属，body 只提供诊断，user/found 用于显示名缺失说明。返回可安全序列化的有限字段，不写库。
func diagnoseLog(event iam.UsageEvent, body iam.RequestLog, user iam.User, found bool) logDiagnosis {
	var stored savedLogDiagnostics
	var envelope savedLogError
	// 对详情的长旧正文采用与列表一致的安全降级，避免超大历史输入增加诊断成本。
	if body.UpstreamResponse != "" && len(body.UpstreamResponse) <= 64<<10 {
		_ = json.Unmarshal([]byte(body.UpstreamResponse), &stored)
	}
	if body.Error != "" && len(body.Error) <= 64<<10 {
		_ = json.Unmarshal([]byte(body.Error), &envelope)
	}
	out := logDiagnosis{}
	if event.LogSeverity() != iam.UsageSeveritySuccess {
		out.Code = logFailureCode(event, stored, envelope)
		switch stored.Outcome.RequestMethod {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			out.RequestMethod = stored.Outcome.RequestMethod
		}
		if stored.Status >= 100 && stored.Status <= 599 {
			out.UpstreamStatus = stored.Status
		}
		out.Phase = knownLogPhase(stored.Failure.Phase, envelope.Diagnostics.Phase, stored.Protocol.Stage)
		if out.Code == "all_deployments_cooling_down" || out.Code == "routing_no_capacity" {
			out.CoolingDeployments = stored.Routing.Cooling
			out.CompatibleDeployments = stored.Routing.Compatible
		}
	}
	out.ModelMissingReason = logModelMissingReason(event, out.Code, stored.Outcome.ModelMissingReason)
	out.UserMissingReason = logUserMissingReason(event, out.Code, stored.Outcome.UserMissingReason, user, found)
	out.TeamMissingReason = logTeamMissingReason(event, stored.Outcome.TeamMissingReason)
	return out
}

// knownLogPhase 从存储阶段候选取首个协议已知值。用于摘要展示，未知、空值和任意上游字符串均不进入响应。
func knownLogPhase(phases ...string) string {
	for _, phase := range phases {
		switch phase {
		case "request_body", "before_upstream", "upstream_request", "response_transfer", "client_write",
			"request_validation", "request_conversion", "route_selection", "response_conversion":
			return phase
		}
	}
	return ""
}

// gatewayLogCode 确认 outcome 或 error.reason 中的原因码由控制台支持。未知值交给历史诊断和 HTTP 状态推导。
// 参数 code 为已保存的候选原因码；返回白名单内的字符串或空，不回传供应商自定义字段。
func gatewayLogCode(code string) string {
	switch code {
	case "model_not_allowed", "unsupported_method", "route_not_found", "model_required", "model_not_found", "model_disabled",
		"protocol_unsupported", "credentials_missing", "credentials_rejected", "request_canceled", "client_write_error",
		"all_deployments_cooling_down", "routing_no_capacity", "upstream_http_error", "upstream_timeout",
		"upstream_stream_interrupted", "upstream_transport_error", "upstream_invalid_response", "budget_exceeded", "rate_limit",
		"upstream_credentials_unavailable", "request_body_unreadable", "request_body_too_large", "request_body_timeout",
		"continuation_unavailable", "guardrail_rejected", "history_unavailable", "request_rejected", "service_failure":
		return code
	}
	return ""
}

// logFailureCode 从明确的网关原因、出站证据或固定的历史网关文案分类。
// 参数 event 为授权快照，stored 和 envelope 为有限解析结果。返回稳定码，缺少证据时不会把状态码当作故障来源。
func logFailureCode(event iam.UsageEvent, stored savedLogDiagnostics, envelope savedLogError) string {
	if code := gatewayLogCode(stored.Outcome.Code); code != "" {
		return code
	}
	phase := knownLogPhase(stored.Failure.Phase, envelope.Diagnostics.Phase)
	kind := stored.Failure.Kind
	if kind == "" {
		kind = envelope.Diagnostics.Kind
	}
	if event.HTTPStatus == 499 || kind == "request_canceled" {
		return "request_canceled"
	}
	if kind == "client_write_error" || envelope.Error.Type == "client_write_error" {
		return "client_write_error"
	}
	if stored.Status >= 400 && stored.Status <= 599 {
		return "upstream_http_error"
	}
	// 正常上游 HTTP 响应之后仍可能发生本地历史持久化故障，不能把它写成供应商断流。
	if envelope.Error.Type == "history_unavailable" && len(stored.Error) == 0 {
		return "history_unavailable"
	}
	if phase == "request_body" {
		switch kind {
		case "body_too_large":
			return "request_body_too_large"
		case "timeout":
			return "request_body_timeout"
		default:
			return "request_body_unreadable"
		}
	}
	if kind == "timeout" && (phase == "upstream_request" || phase == "response_transfer") {
		return "upstream_timeout"
	}
	if phase == "response_transfer" || stored.ReadError != "" || (stored.Status >= 200 && stored.Status < 300 && len(stored.Error) > 0) {
		return "upstream_stream_interrupted"
	}
	if phase == "upstream_request" && kind != "" {
		return "upstream_transport_error"
	}
	// 有出站记录但没有确定的状态或阶段时，不将上游的文案冒认为网关自己的拒绝原因。
	if event.OutboundEndpoint == "" {
		if code := gatewayLogCode(envelope.Error.Reason); code != "" {
			return code
		}
		if code := historicalGatewayLogCode(event.HTTPStatus, stored, envelope); code != "" {
			return code
		}
	}
	if event.LogSeverity() == iam.UsageSeverityWarning {
		return "request_rejected"
	}
	return "service_failure"
}

// historicalGatewayLogCode 识别只在既有网关响应中出现的标记和说明。
// 它由未观测到出站的诊断调用；仅使用 HTTP 状态、已知枚举或固定文案，返回原因码或空，不反射存储错误正文。
func historicalGatewayLogCode(status int, stored savedLogDiagnostics, envelope savedLogError) string {
	message := strings.ToLower(strings.TrimSpace(envelope.Error.Message))
	switch message {
	case "model not in allowed model list":
		return "model_not_allowed"
	case "authentication error, no api key passed in.":
		return "credentials_missing"
	case "your session has ended. sign in again.", "invalid api key":
		return "credentials_rejected"
	case "this model has no upstream api key configured.":
		return "upstream_credentials_unavailable"
	case "model required":
		return "model_required"
	}
	if status == http.StatusNotFound && (message == "not found" || envelope.Error.Type == "not_found" && message == "") {
		return "route_not_found"
	}
	if status == http.StatusBadRequest && (message == "model not found" || strings.HasPrefix(message, "model not found: ")) {
		return "model_not_found"
	}
	if status == http.StatusServiceUnavailable {
		if (stored.Routing.Compatible != nil && stored.Routing.Cooling != nil &&
			*stored.Routing.Compatible > 0 && *stored.Routing.Compatible == *stored.Routing.Cooling) ||
			strings.HasSuffix(message, " has all compatible deployments cooling down; retry later") {
			return "all_deployments_cooling_down"
		}
		if envelope.Error.Type == "model_unavailable" {
			return "routing_no_capacity"
		}
	}
	if stored.Protocol.Stage != "" || envelope.Error.Type == "unsupported_capability" ||
		(status == http.StatusBadRequest && envelope.Error.Type == "model_unavailable") {
		return "protocol_unsupported"
	}
	switch envelope.Error.Type {
	case "budget_exceeded":
		return "budget_exceeded"
	case "guardrail_failed":
		return "guardrail_rejected"
	case "continuation_unavailable":
		return "continuation_unavailable"
	case "model_disabled":
		return "model_disabled"
	}
	if status == http.StatusTooManyRequests {
		return "rate_limit"
	}
	return ""
}

// logModelMissingReason 在没有公开模型名时标明读正文、进入路由或保存模型的阶段。
// 参数 event 是历史快照，code 与 saved 分别是标准原因和明确写入的缺失原因。未知旧记录标为未保存。
func logModelMissingReason(event iam.UsageEvent, code, saved string) string {
	if event.Model != "" {
		return ""
	}
	switch {
	case saved == "not_reached" || code == "unsupported_method" || code == "route_not_found":
		return "model_not_applicable"
	case saved == "not_provided" || code == "model_required":
		return "model_not_provided"
	case saved == "body_unreadable" || code == "request_body_unreadable" || code == "request_body_too_large" || code == "request_body_timeout":
		return "body_unreadable"
	default:
		return "model_not_recorded"
	}
}

// logUserMissingReason 区分没有可信身份和已知用户缺少显示名。
// 参数 event 为授权日志，code/saved 来自诊断，user/found 为当前目录结果。返回原因码，保留历史用户 ID，不以邮箱替代姓名。
func logUserMissingReason(event iam.UsageEvent, code, saved string, user iam.User, found bool) string {
	if event.UserID != "" {
		if !found {
			return "user_unavailable"
		}
		if strings.TrimSpace(user.Name) == "" {
			return "user_name_not_configured"
		}
		return ""
	}
	switch {
	case saved == "identity_lookup_failed":
		return saved
	case saved == "credentials_missing" || code == "credentials_missing":
		return "credentials_missing"
	case saved == "credentials_rejected" || code == "credentials_rejected":
		return "credentials_rejected"
	case saved == "unattributed_credential" || event.KeyID != "":
		return "unattributed_credential"
	case event.HTTPStatus == http.StatusUnauthorized || event.HTTPStatus == http.StatusNotFound:
		return "unverified_identity"
	default:
		return "user_not_recorded"
	}
}

// logTeamMissingReason 以请求时保存的范围为准说明团队缺失。
// 参数 event 为单条历史记录，saved 为当时明确收集的归属诊断；返回原因码，无副作用，不按当前团队补写历史。
func logTeamMissingReason(event iam.UsageEvent, saved string) string {
	switch {
	case event.TeamAlias != "":
		return ""
	case event.TeamID != "":
		return "team_name_not_recorded"
	case saved == "no_team" || saved == "team_lookup_failed":
		return saved
	case saved == "identity_unverified" || (event.UserID == "" && event.KeyID == ""):
		return "unverified_identity"
	default:
		return "team_not_recorded"
	}
}
