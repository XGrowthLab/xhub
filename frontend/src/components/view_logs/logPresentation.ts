import type { LogEntry } from "./columns";

/** 从已记录的错误正文提取短摘要；参数为日志和可选的空正文翻译回调，返回至多 240 字符和截断标记，供详情首屏使用。
 * 有具体错误正文时优先展示；正文缺失或只有网关通用错误时查已记录的传输诊断。不推断责任方。
 */
export function logErrorSummary(
  log: Pick<LogEntry, "error" | "response" | "metadata">,
  formatMissingBody?: (status: number) => string,
): string {
  let payload: unknown = log.error?.trim() || log.response;
  if (typeof payload === "string") {
    try {
      payload = JSON.parse(payload);
    } catch {
      /* 纯文本错误保留原文。 */
    }
  }
  const object = payload && typeof payload === "object" ? (payload as Record<string, unknown>) : {};
  const error = object.error && typeof object.error === "object" ? (object.error as Record<string, unknown>) : {};
  if (formatMissingBody && error.type === "upstream_empty_error_body" && typeof error.status_code === "number") {
    return formatMissingBody(error.status_code);
  }
  const textError = typeof object.error === "string" ? object.error : undefined;
  const candidate = log.metadata?.error_information?.error_message ?? error.message ?? textError ?? object.message ?? payload;
  const summary = typeof candidate === "string" ? candidate.replace(/\s+/g, " ").trim() : "";
  const upstream = log.metadata?.upstream_response;
  const recordedTransportError = upstream?.failure?.transport_error || upstream?.read_error;
  const text = (!summary || summary === "upstream request failed") && typeof recordedTransportError === "string"
    ? recordedTransportError.replace(/\s+/g, " ").trim() || summary
    : summary;
  return text.length > 240 ? `${text.slice(0, 240)}…` : text;
}

const outcomeKeys: Readonly<Record<string, string>> = {
  model_not_allowed: "modelNotAllowed",
  unsupported_method: "methodNotSupported",
  route_not_found: "routeNotFound",
  model_required: "modelNotProvided",
  model_not_found: "modelNotFound",
  model_disabled: "modelDisabled",
  protocol_unsupported: "protocolUnsupported",
  request_body_unreadable: "unreadableRequestBody",
  request_body_too_large: "requestBodyTooLarge",
  request_body_timeout: "requestBodyTimedOut",
  credentials_missing: "credentialsMissing",
  credentials_rejected: "credentialsRejected",
  request_canceled: "interrupted",
  client_write_error: "clientWriteFailed",
  all_deployments_cooling_down: "deploymentsCooling",
  routing_no_capacity: "routingNoCapacity",
  upstream_http_error: "upstreamReturnedError",
  upstream_timeout: "upstreamTimeout",
  upstream_stream_interrupted: "upstreamStreamInterrupted",
  upstream_transport_error: "upstreamTransportError",
  upstream_invalid_response: "upstreamInvalidResponse",
  upstream_credentials_unavailable: "upstreamCredentialsUnavailable",
  continuation_unavailable: "continuationUnavailable",
  history_unavailable: "historyUnavailable",
  guardrail_rejected: "guardrailRejected",
  budget_exceeded: "budgetExceeded",
  rate_limit: "limited",
};

/** 根据服务端诊断选取原因翻译键，旧日志回退到已记录的传输事实和 HTTP 状态。
 * 列表和详情调用；不基于状态猜测是哪一端中断，也不展示未知的内部诊断代码。 */
export function logOutcomeKey(log: Pick<LogEntry, "metadata" | "diagnosis">): string {
  const code = log.diagnosis?.code;
  if (code && Object.hasOwn(outcomeKeys, code)) return `logsUX.${outcomeKeys[code]}`;
  // 客户端写入失败不等于上游服务错误，状态码不能覆盖更具体的已观测事实。
  if (log.metadata?.upstream_response?.failure?.kind === "client_write_error") return "logsUX.clientWriteFailed";
  const status = Number(log.metadata?.http_status);
  if (status === 499) return "logsUX.interrupted";
  const failure = log.metadata?.upstream_response?.failure;
  if (failure?.kind === "timeout" && ["upstream_request", "response_transfer"].includes(failure.phase)) {
    return "logsUX.upstreamTimeout";
  }
  if (status === 429) return "logsUX.limited";
  if (status === 413) return "logsUX.bodyRejected";
  if (status === 401 || status === 403) return "logsUX.denied";
  if (status >= 500) return "logsUX.serviceFailed";
  return "logsUX.requestFailed";
}

const missingKeys = {
  model: {
    model_not_applicable: "modelNotApplicable",
    model_not_provided: "modelNotProvided",
    model_not_recorded: "modelNotRecorded",
    body_unreadable: "modelBodyUnreadable",
  },
  user: {
    unverified_identity: "identityUnverified",
    user_not_recorded: "userNotRecorded",
    user_name_not_configured: "userNameNotConfigured",
    user_unavailable: "userUnavailable",
    credentials_missing: "credentialsMissing",
    credentials_rejected: "credentialsRejected",
    unattributed_credential: "credentialHasNoUser",
    identity_lookup_failed: "identityLookupFailed",
  },
  team: {
    unverified_identity: "identityUnverified",
    team_not_recorded: "teamUnknown",
    team_name_not_recorded: "teamNameNotRecorded",
    no_team: "teamNotAssigned",
    team_lookup_failed: "teamLookupFailed",
  },
} satisfies Record<string, Readonly<Record<string, string>>>;

const missingFallback = { model: "modelNotRecorded", user: "userNotRecorded", team: "teamUnknown" } as const;

/** 生成缺失字段原因翻译键。接收日志与字段名，由列表和详情在字段缺失时调用。
 * 旧记录或未知原因只标注未记录，不能根据用户当前团队推断历史归属。 */
export function logMissingReasonKey(log: Pick<LogEntry, "diagnosis">, field: keyof typeof missingKeys): string {
  const code = log.diagnosis?.[`${field}_missing_reason`];
  const keys: Readonly<Record<string, string>> = missingKeys[field];
  return `logsUX.${code && Object.hasOwn(keys, code) ? keys[code] : missingFallback[field]}`;
}

/** 生成列表初始列可见性；参数是错误视图标志，返回列映射。只影响展示，所有字段仍可通过列选择器恢复。 */
export function logColumnVisibility(errorsOnly: boolean): Record<string, boolean> {
  return {
    endpoints: false,
    type: false,
    session_id: false,
    ttft_ms: false,
    tps: false,
    team_alias: errorsOnly,
    key_hash: false,
    end_user: false,
    request_tags: false,
    spend: !errorsOnly,
    total_tokens: !errorsOnly,
    diagnosis: errorsOnly,
    key_alias: false,
  };
}
