import { useState } from "react";
import { Check, ChevronDown, ChevronRight, Copy, Info } from "lucide-react";
import moment from "moment";

// 日志时间格式是 Moment 协议格式，不随界面翻译。
const LOG_TIME_FORMAT = "YYYY-MM-DD HH:mm:ss.SSS Z";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { UiLoadingSpinner } from "@/components/ui/ui-loading-spinner";
import { LogEntry } from "../columns";
import { logErrorSummary, logMissingReasonKey } from "../logPresentation";
import { fullErrorDetails, isRequestFailure } from "../logErrors";
import { formatNumberWithCommas } from "@/utils/dataUtils";
import { formatLogCost } from "../formatLogCost";
import GuardrailViewer from "../GuardrailViewer/GuardrailViewer";
import EvalViewer from "../EvalViewer/EvalViewer";
import {
  getBatchIdFromRequestId,
  getBatchModels,
  getBatchRequestCounts,
  getReasoningTokens,
  isBatchCallType,
} from "../batchLogUtils";
import { CostBreakdownViewer } from "../CostBreakdownViewer";
import { ConfigInfoMessage } from "../ConfigInfoMessage";
import { VectorStoreViewer } from "../VectorStoreViewer";
import { TruncatedValue } from "./TruncatedValue";
import { TokenFlow } from "./TokenFlow";
import { JsonViewer } from "./JsonViewer";
import { loggedResponse, requestBody, requestHeaders } from "./prettyMessagesUtils";
import { RoutingDecisionCard, type RoutingDecision } from "./RoutingDecisionCard";
import {
  formatData,
  checkHasMessages,
  checkHasResponse,
  normalizeGuardrailEntries,
  calculateTotalMaskedEntities,
  getGuardrailLabel,
  checkHasVectorStoreData,
} from "./utils";
import {
  DRAWER_CONTENT_PADDING,
  API_BASE_MAX_WIDTH,
} from "./constants";
import { ToolsSection } from "../ToolsSection";
import { PrettyMessagesView } from "./PrettyMessagesView";
import { ClassifierAuditView } from "./ClassifierAuditView";
import { AUTOROUTER_CLASSIFIER_ORIGIN } from "./ClassifyTag";
import { useT } from "@/i18n";
import { logPromptCacheTokens } from "./promptCacheMetrics";
import { ProtocolRejectionDetail } from "./ProtocolRejectionDetail";
import { RequestErrorAlert } from "./RequestErrorAlert";

export interface LogDetailContentProps {
  logEntry: LogEntry;
  /** When true, log details (messages/response) are still being lazy-loaded. */
  isLoadingDetails?: boolean;
  /** 详情请求失败时由抽屉展示错误和重试，不把失败误报为历史正文缺失。 */
  isDetailsError?: boolean;
  accessToken?: string | null;
}

/**
 * 在单请求或会话抽屉中展示日志详情；接收日志、加载状态和可选鉴权凭据，
 * 返回请求、响应、费用和护栏视图。失败请求有正文时展示完整响应，否则显示保留的摘要；
 * 加载详情时不显示缺失捕获提示。
 * 上游响应默认折叠，用户通过标题按钮展开；切换请求时重置折叠状态，复制始终保留完整诊断。
 * 推理强度只展示明确快照，缺省为未知；金额格式化不修改计费或用户内容。
 */
/** 日志抽屉正文：按 logEntry 展示原始协议证据与国际化诊断，读取失败时说明正文省略；返回详情组件，不修改请求或费用。 */
export function LogDetailContent({ logEntry, isLoadingDetails = false, isDetailsError = false, accessToken }: LogDetailContentProps) {
  const t = useT();
  const metadata = logEntry.metadata || {};
  const earlyClose = metadata.upstream_response?.usage_parse_error === "upstream body closed before EOF";
  const hasError = isRequestFailure(logEntry);
  const errorDetails = hasError ? fullErrorDetails(logEntry) : "";
  const errorSummary = hasError ? logErrorSummary(logEntry, (status) => t("logsUX.upstreamEmptyErrorBody", { status })) : "";
  const errorInfo = hasError ? metadata.error_information : null;
  const isClassifier =
    metadata.internal_call_origin === AUTOROUTER_CLASSIFIER_ORIGIN &&
    ["completion", "acompletion", "responses", "aresponses"].includes(logEntry.call_type);
  const rawRequest = formatData(logEntry.proxy_server_request || logEntry.messages);
  const hasClassifierAudit =
    isClassifier && (rawRequest?.classifier_input != null || rawRequest?.originating_request_masked != null);

  const hasMessages = checkHasMessages(logEntry.messages);
  const hasResponse = checkHasResponse(logEntry.response);
  // Don't show "missing data" warning while details are still loading
  const missingData = !hasMessages && !hasResponse && !isLoadingDetails && !isDetailsError;

  // Guardrail data
  const guardrailInfo = metadata?.guardrail_information;
  const guardrailEntries = normalizeGuardrailEntries(guardrailInfo);
  const hasGuardrailData = guardrailEntries.length > 0;
  const totalMaskedEntities = calculateTotalMaskedEntities(guardrailEntries);
  const primaryGuardrailLabel = getGuardrailLabel(guardrailEntries);

  // LLM Judge data
  const evalInfo = metadata?.eval_information;
  const hasEvalData = evalInfo != null;

  // Vector store data
  const hasVectorStoreData = checkHasVectorStoreData(metadata);

  const getFormattedResponse = () => {
    // 错误响应包含供应商扩展诊断字段，必须完整保留，不能被兼容摘要或流式成功解析覆盖。
    if (hasResponse) return hasError ? formatData(logEntry.response) : loggedResponse(formatData(logEntry.response));
    if (logEntry.error?.trim()) return formatData(logEntry.error);
    if (hasError && errorInfo) {
      return {
        error: {
          message: errorInfo.error_message || "An error occurred",
          type: errorInfo.error_class || "error",
          code: errorInfo.error_code || "unknown",
          param: null,
        },
      };
    }
    return null;
  };

  return (
    <div style={{ padding: `${DRAWER_CONTENT_PADDING} ${DRAWER_CONTENT_PADDING} 0` }}>
      {hasError && <RequestErrorAlert logEntry={logEntry} summary={errorSummary} />}
      {hasError && errorDetails && !isLoadingDetails && (
        <section aria-label={t("Full Error Details")} className="mb-6 rounded-lg border border-destructive/30">
          <div className="flex items-center justify-between border-b px-3 py-2">
            <h3 className="text-sm font-semibold">{t("Full Error Details")}</h3>
            <CopyButton getText={() => errorDetails} label={t("Copy error details")} />
          </div>
          <details className="p-3"><summary className="cursor-pointer text-sm">{t("logsUX.rawError")}</summary><pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-all text-xs">{errorDetails}</pre></details>
        </section>
      )}

      {/* Tags */}
      {logEntry.request_tags && Object.keys(logEntry.request_tags).length > 0 && (
        <TagsSection tags={logEntry.request_tags} />
      )}

      {logEntry.label && <section aria-label={t("logs.label")} className="mb-6 rounded-lg border p-4">
        <h3 className="font-semibold">{t("logs.label")}</h3>
        <p className="whitespace-pre-wrap break-all">{logEntry.label}</p>
        <p className="text-sm text-muted-foreground">{t("logs.labelHint")}</p>
      </section>}
      {/* Request Details */}
      <div className="bg-card rounded-lg shadow-sm w-full max-w-full overflow-hidden mb-6">
        <Card size="sm" style={{ marginBottom: 0 }}>
          <CardHeader>
            <CardTitle>{t("Request Details")}</CardTitle>
          </CardHeader>
          <CardContent>
            <DescriptionList>
              <DescriptionItem label={t("Model")}>{logEntry.model || t(logMissingReasonKey(logEntry, "model"))}</DescriptionItem>
              <DescriptionItem label={t("Internal User")}>{logEntry.user_name || t(logMissingReasonKey(logEntry, "user"))}</DescriptionItem>
              {logEntry.user && <DescriptionItem label={t("User ID")}><TruncatedValue value={logEntry.user} /></DescriptionItem>}
              <DescriptionItem label={t("Team Name")}>
                {metadata.user_api_key_team_alias || t(logMissingReasonKey(logEntry, "team"))}
              </DescriptionItem>
              <DescriptionItem label={t("Credential Name")}>{logEntry.credential_name || "-"}</DescriptionItem>
              <DescriptionItem label={t("Call Type")}>{logEntry.call_type}</DescriptionItem>
              <DescriptionItem label={t("logs.reasoningEffort")}>
                <span title={t("logs.reasoningEffortHint")}>
                  {logEntry.reasoning_effort
                    ? `${logEntry.reasoning_effort} (reasoning_effort=${logEntry.reasoning_effort})`
                    : t("logs.reasoningEffortUnknown")}
                </span>
              </DescriptionItem>
              {logEntry.model_id && <DescriptionItem label={t("Model ID")}>
                <TruncatedValue value={logEntry.model_id} />
              </DescriptionItem>}
              <DescriptionItem label={t("API Base")}>
                <TruncatedValue value={logEntry.api_base} maxWidth={API_BASE_MAX_WIDTH} />
              </DescriptionItem>
              {logEntry.requester_ip_address && (
                <DescriptionItem label={t("IP Address")}>{logEntry.requester_ip_address}</DescriptionItem>
              )}
              {hasGuardrailData && (
                <DescriptionItem label={t("Guardrail")}>
                  <GuardrailLabel label={primaryGuardrailLabel} maskedCount={totalMaskedEntities} />
                </DescriptionItem>
              )}
            </DescriptionList>
          </CardContent>
        </Card>
      </div>

      {/* Batch Results */}
      {isBatchCallType(logEntry.call_type) && <BatchResultsSection logEntry={logEntry} metadata={metadata} />}

      {/* Configuration Info Message */}
      {missingData && (
        <div className="mb-6">
          <ConfigInfoMessage show={missingData} status={logEntry.body_capture_status} />
        </div>
      )}

      {/* Request/Response JSON */}
      {isLoadingDetails ? (
        <div className="bg-card rounded-lg shadow-sm w-full max-w-full overflow-hidden mb-6 p-8 text-center">
          <UiLoadingSpinner className="inline-block size-5" />
          <div style={{ marginTop: 8, color: "var(--color-muted-foreground)" }}>
            {t("Loading request & response data...")}
          </div>
        </div>
      ) : null}
      {!isLoadingDetails && hasClassifierAudit && (
        <ClassifierAuditView request={rawRequest} response={getFormattedResponse()} />
      )}
      {!isLoadingDetails && !hasClassifierAudit && (
        <RequestResponseSection
          hasResponse={hasResponse}
          hasError={hasError}
          errorSummary={errorSummary}
          getRawRequest={() => rawRequest}
          getFormattedResponse={getFormattedResponse}
          logEntry={logEntry}
        />
      )}

      {/* Routing */}
      <RoutingDecisionCard decision={metadata?.routing_decision as RoutingDecision | undefined} />

      {/* Metrics */}
      <MetricsSection logEntry={logEntry} metadata={metadata} />

      {/* Cost Breakdown */}
      <CostBreakdownViewer
        costBreakdown={metadata?.cost_breakdown}
        totalSpend={logEntry.spend ?? 0}
        promptTokens={logEntry.prompt_tokens}
        completionTokens={logEntry.completion_tokens}
        cacheHit={logEntry.cache_hit}
        rawInputTokens={metadata?.additional_usage_values?.prompt_tokens_details?.text_tokens}
        cacheReadTokens={metadata?.additional_usage_values?.cache_read_input_tokens}
        cacheCreationTokens={metadata?.additional_usage_values?.cache_creation_input_tokens}
      />

      {/* Tools */}
      <ToolsSection log={logEntry} />

      {/* Guardrail Data */}
      {hasGuardrailData && (
        <div id="guardrail-section">
          <GuardrailViewer
            data={guardrailInfo}
            accessToken={accessToken ?? null}
            logEntry={{
              request_id: logEntry.request_id,
              user: logEntry.user,
              model: logEntry.model,
              startTime: logEntry.startTime,
              metadata: logEntry.metadata,
            }}
          />
        </div>
      )}

      {/* LLM Judge Results */}
      {hasEvalData && <EvalViewer data={evalInfo} />}

      {/* Vector Store Data */}
      {hasVectorStoreData && <VectorStoreViewer data={metadata.vector_store_request_metadata} />}

      <ProtocolRejectionDetail rejection={metadata.upstream_response?.protocol} />
      {(metadata.upstream_response?.failure || metadata.upstream_response?.routing || metadata.upstream_response?.rate_limit) && (
        <section aria-label={t("logDiagnostics.title")} className="mb-6 rounded-lg border p-4">
          <h3 className="font-semibold">{t("logDiagnostics.title")}</h3>
          {metadata.upstream_response.failure?.cancellation_source === "unknown" && <p>{t("logDiagnostics.cancelUnknown")}</p>}
          {metadata.upstream_response.failure?.kind === "client_write_error" && <p>{t("logDiagnostics.clientWriteError")}</p>}
          {metadata.upstream_response.failure?.request_body_omitted && <p>{t("logDiagnostics.bodyOmitted")}</p>}
          <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(metadata.upstream_response.failure ?? metadata.upstream_response.routing ?? metadata.upstream_response.rate_limit, null, 2)}</pre>
          <h4>{t("logDiagnostics.upstreamError")}</h4>
          {metadata.upstream_response.error
            ? <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(metadata.upstream_response.error, null, 2)}</pre>
            : <p>{t("logDiagnostics.noUpstreamError")}</p>}
        </section>
      )}
      {metadata.upstream_response && (
        <section aria-label={t("Upstream Response")} className="mb-6 rounded-lg border p-4">
          {/* 以请求 ID 隔离展开状态，避免查看下一条日志时继承上一条的展开状态。 */}
          <Collapsible key={logEntry.request_id} defaultOpen={false}>
            <div className="flex items-center justify-between gap-3">
              <CollapsibleTrigger className="group flex flex-1 items-center gap-2 rounded-sm text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                <ChevronRight aria-hidden="true" className="size-4 shrink-0 text-muted-foreground group-aria-expanded:hidden" />
                <ChevronDown aria-hidden="true" className="hidden size-4 shrink-0 text-muted-foreground group-aria-expanded:block" />
                <h3 className="font-semibold">{t("Upstream Response")}</h3>
              </CollapsibleTrigger>
              <CopyButton getText={() => JSON.stringify(metadata.upstream_response, null, 2)} label={t("Copy upstream response")} />
            </div>
            {Array.isArray(metadata.upstream_response.dropped_capabilities) && metadata.upstream_response.dropped_capabilities.length > 0 && (
              <p role="status" className="mt-2 text-sm text-muted-foreground">{t("providerGuideUI.searchDropped", { capabilities: metadata.upstream_response.dropped_capabilities.join(", ") })}</p>
            )}
            <CollapsibleContent className="pt-3">
              <p className="mb-3 text-sm text-muted-foreground">{t("logsUX.upstreamSummary")}</p>
              <p>{t("HTTP Status")}: {metadata.upstream_response.status_code || t("logDiagnostics.noResponse")}</p>
              {metadata.upstream_response.read_error && <p role="alert" className="my-2 break-words rounded-md border-l-2 border-amber-500 bg-muted px-3 py-2 text-sm">{metadata.upstream_response.read_error}</p>}
              {metadata.upstream_response.usage_parse_error && <p role="alert" className="my-2 break-words rounded-md border-l-2 border-amber-500 bg-muted px-3 py-2 text-sm">{earlyClose ? t("logsUX.earlyClose") : metadata.upstream_response.usage_parse_error}</p>}
              {upstreamRequestId(metadata.upstream_response.headers) && <p className="break-all">{t("logsUX.upstreamRequestId")}: {upstreamRequestId(metadata.upstream_response.headers)}</p>}
              <div className="mt-4 grid grid-cols-[repeat(auto-fit,minmax(min(100%,15rem),1fr))] gap-3">
                <UsageSummary title={t("Upstream Usage")} help={t("logsUX.upstreamUsageHelp")} usage={metadata.upstream_response.usage_reported ? metadata.upstream_response.usage : undefined} missing={metadata.upstream_response.usage_reported ? t("logsUX.usageUnavailable") : t("Not reported")} />
                <UsageSummary billing title={t("Billing Usage")} help={t("logsUX.billingUsageHelp")} usage={metadata.upstream_response.billing_usage} missing={t("logsUX.usageUnavailable")} />
              </div>
              <details className="mt-3 rounded border p-3">
                <summary className="cursor-pointer text-sm font-medium">{t("logsUX.upstreamRaw")}</summary>
                {earlyClose && <pre className="my-2 whitespace-pre-wrap text-xs">{metadata.upstream_response.usage_parse_error}</pre>}
                <h4>{t("Upstream Response Headers")}</h4>
                <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(metadata.upstream_response.headers ?? {}, null, 2)}</pre>
                {metadata.upstream_response.trailers && <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(metadata.upstream_response.trailers, null, 2)}</pre>}
                <h4>{t("Upstream Usage")}</h4>
                {metadata.upstream_response.usage_reported && <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(metadata.upstream_response.usage, null, 2)}</pre>}
                <h4>{t("Billing Usage")}</h4>
                <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(metadata.upstream_response.billing_usage ?? {}, null, 2)}</pre>
              </details>
            </CollapsibleContent>
          </Collapsible>
        </section>
      )}

      {/* Metadata */}
      {logEntry.metadata && Object.keys(logEntry.metadata).length > 0 && (
        <MetadataSection key={logEntry.request_id} metadata={logEntry.metadata} />
      )}

      {/* Bottom spacing */}
      <div style={{ height: DRAWER_CONTENT_PADDING }} />
    </div>
  );
}

// ============================================================================
// Helper Components
// ============================================================================

/** 日志详情从原始响应头取供应商查单 ID；返回第一个有效值或空值，避免通用追踪头遮住 New API 的请求号。 */
function upstreamRequestId(headers: unknown): string | undefined {
  if (!headers || typeof headers !== "object" || Array.isArray(headers)) return undefined;
  const entries = new Map(Object.entries(headers).map(([key, value]) => [key.toLowerCase(), value]));
  for (const key of ["x-oneapi-request-id", "request-id", "x-amzn-requestid", "x-request-id"]) {
    const value = entries.get(key);
    const id = Array.isArray(value) ? value[0] : value;
    if (typeof id === "string" && id) return id;
  }
  return undefined;
}

/** 上游详情按来源展示可比较的 Token 数值。title/help 为本地化标题与提示，usage 为原始对象；billing 标记计费区，missing 为缺失提示。保留真实零值与原始数据。 */
function UsageSummary({ title, help, usage, missing, billing = false }: { billing?: boolean; title: string; help: string; usage: unknown; missing: string }) {
  const t = useT();
  const data = usage && typeof usage === "object" && !Array.isArray(usage) ? usage as Record<string, unknown> : {};
  const rawDetails = data.input_tokens_details ?? data.prompt_tokens_details;
  const details = rawDetails && typeof rawDetails === "object" && !Array.isArray(rawDetails) ? rawDetails as Record<string, unknown> : {};
  const fields = [
    [t("logsUX.inputTokens"), data.input_tokens ?? data.prompt_tokens],
    [t("logsUX.outputTokens"), data.output_tokens ?? data.completion_tokens],
    [t("logsUX.totalTokens"), data.total_tokens],
    [t("logsUX.cacheReadTokens"), data.cache_read_input_tokens ?? details.cached_tokens],
    [t("logsUX.cacheWriteTokens"), data.cache_creation_input_tokens ?? details.cache_write_tokens],
  ].filter((entry): entry is [string, number] => typeof entry[1] === "number" && Number.isFinite(entry[1]));
  return <section aria-label={title} className="min-w-0 rounded-lg border bg-card p-3 text-sm">
    <h4 className="flex items-center gap-2 font-semibold"><span aria-hidden="true" className={billing ? "h-4 w-1 rounded-full bg-primary" : "h-4 w-1 rounded-full bg-muted-foreground"} />{title}</h4>
    <p className="my-2 text-xs leading-relaxed text-muted-foreground">{help}</p>
    {fields.length ? <dl className="divide-y divide-border">{fields.map(([label, value]) => <div key={label} className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-3 py-2"><dt className="text-muted-foreground">{label}</dt><dd className="font-semibold tabular-nums">{formatNumberWithCommas(value)}</dd></div>)}</dl> : <p>{missing}</p>}
  </section>;
}

/** 为请求详情与指标排版标签值对，按容器宽度排一至两列；children 由 DescriptionItem 组成，渲染不改变数据。 */
function DescriptionList({ children }: { children: React.ReactNode }) {
  return <dl className="grid grid-cols-[repeat(auto-fit,minmax(min(100%,max(12rem,45%)),1fr))] gap-x-6 gap-y-4 text-sm">{children}</dl>;
}

/** 用语义化标签和值展示单个指标；label 可含帮助按钮，children 可含复制按钮或长标识。紧凑宽度时值仍可换行。 */
function DescriptionItem({ label, children }: { label: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="min-w-0 space-y-1">
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd className="min-w-0 leading-snug tabular-nums [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

function CopyButton({
  getText,
  label,
  disabled = false,
}: {
  getText: () => string;
  label: string;
  disabled?: boolean;
}) {
  const t = useT();
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(getText());
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch {
      /* clipboard unavailable in non-secure contexts */
    }
  };

  return (
    <Button
      variant="ghost"
      size="icon-sm"
      onClick={handleCopy}
      disabled={disabled}
      aria-label={copied ? t("Copied!") : label}
    >
      {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
    </Button>
  );
}

function TagsSection({ tags }: { tags: Record<string, any> }) {
  const t = useT();
  return (
    <div className="bg-card rounded-lg shadow-sm w-full max-w-full overflow-hidden p-4 mb-6">
      <span className="font-semibold" style={{ display: "block", marginBottom: 8, fontSize: 16 }}>
        {t("Tags")}
      </span>
      <div className="flex flex-wrap items-center gap-2">
        {Object.entries(tags).map(([key, value]) => (
          <Badge key={key} variant="outline">
            {key}: {String(value)}
          </Badge>
        ))}
      </div>
    </div>
  );
}

function GuardrailLabel({ label, maskedCount }: { label: string; maskedCount: number }) {
  const t = useT();
  const handleClick = () => {
    const el = document.getElementById("guardrail-section");
    if (el) el.scrollIntoView({ behavior: "smooth" });
  };

  return (
    <span className="inline-flex items-center gap-2">
      <a onClick={handleClick} style={{ cursor: "pointer" }}>
        {label}
      </a>
      {maskedCount > 0 && <Badge variant="secondary">{maskedCount} masked</Badge>}
    </span>
  );
}

/**
 * Uncached input token count (billable non-cache prompt text), aligned with Cost Breakdown "Input".
 * Same sources as CostBreakdownViewer rawInputTokens.
 */
function getUncachedInputTextTokens(metadata: Record<string, any>): number | undefined {
  const raw =
    metadata?.additional_usage_values?.prompt_tokens_details?.text_tokens ??
    metadata?.usage_object?.prompt_tokens_details?.text_tokens;
  if (raw === undefined || raw === null) return undefined;
  const n = Number(raw);
  return Number.isFinite(n) ? n : undefined;
}

function MetricLabel({ label, tooltip }: { label: string; tooltip: string }) {
  const t = useT();
  return (
    <span className="inline-flex items-center gap-1">
      {label}
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger
            render={<span role="img" aria-label={t("{label} info", { label })} className="inline-flex text-muted-foreground" />}
          >
            <Info className="size-3.5" />
          </TooltipTrigger>
          <TooltipContent>{tooltip}</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </span>
  );
}

/**
 * Aggregate per-request outcomes for a batch cost row: batch id, success/failure counts
 * from the parsed output and error files, and the models the batch actually ran on.
 */
function BatchResultsSection({ logEntry, metadata }: { logEntry: LogEntry; metadata: Record<string, unknown> }) {
  const t = useT();
  const counts = getBatchRequestCounts(metadata);
  const batchId = getBatchIdFromRequestId(logEntry.request_id);
  const batchModels = getBatchModels(metadata);
  if (!counts && !batchId && !batchModels) return null;

  return (
    <div className="bg-card rounded-lg shadow-sm w-full max-w-full overflow-hidden mb-6">
      <Card size="sm" style={{ marginBottom: 0 }}>
        <CardHeader>
          <CardTitle>{t("Batch Results")}</CardTitle>
        </CardHeader>
        <CardContent>
          <DescriptionList>
            {batchId && (
              <DescriptionItem label={t("Batch ID")}>
                <TruncatedValue value={batchId} />
              </DescriptionItem>
            )}
            {counts && (
              <>
                <DescriptionItem label={t("Successful Requests")}>
                  {formatNumberWithCommas(counts.successful)}
                </DescriptionItem>
                <DescriptionItem label={t("Failed Requests")}>
                  {counts.failed > 0 ? (
                    <Badge variant="secondary" className="bg-destructive/15 text-destructive">
                      {formatNumberWithCommas(counts.failed)}
                    </Badge>
                  ) : (
                    formatNumberWithCommas(counts.failed)
                  )}
                </DescriptionItem>
              </>
            )}
            {batchModels && <DescriptionItem label={t("Models")}>{batchModels.join(", ")}</DescriptionItem>}
          </DescriptionList>
        </CardContent>
      </Card>
    </div>
  );
}

/** 展示实测指标；参数为日志和元数据，返回指标视图，由详情组件调用。
 * 金额保留 API 数值精度、未定价零值显示未知，不重新计费；缓存统计缺失保留未知状态，无后台写入。 */
function MetricsSection({ logEntry, metadata }: { logEntry: LogEntry; metadata: Record<string, any> }) {
  const t = useT();
  const ttftMs = logEntry.ttft_ms;

  const responseCacheValue = String(logEntry.cache_hit ?? "").toLowerCase();
  const responseCacheKey = logEntry.cache_key && logEntry.cache_key !== "Cache OFF" ? logEntry.cache_key : undefined;
  const isResponseCacheHit = responseCacheValue === "true";
  const showResponseCache = isResponseCacheHit || responseCacheValue === "false" || responseCacheKey != null;
  const { read: promptCacheReadTokens, creation: promptCacheCreationTokens } = logPromptCacheTokens(logEntry, metadata);

  const uncachedInputTokens = getUncachedInputTextTokens(metadata);
  const showAnthropicMessagesInputOutput =
    logEntry.call_type === "anthropic_messages" && uncachedInputTokens !== undefined;
  const reasoningTokens = getReasoningTokens(metadata);

  return (
    <div className="bg-card rounded-lg shadow-sm w-full max-w-full overflow-hidden mb-6">
      <Card size="sm" style={{ marginBottom: 0 }}>
        <CardHeader>
          <CardTitle>{t("Metrics")}</CardTitle>
        </CardHeader>
        <CardContent>
          <DescriptionList>
            {showAnthropicMessagesInputOutput ? (
              <>
                <DescriptionItem label={t("Input Tokens")}>{formatNumberWithCommas(uncachedInputTokens)}</DescriptionItem>
                <DescriptionItem label={t("Output Tokens")}>
                  {formatNumberWithCommas(logEntry.completion_tokens)}
                </DescriptionItem>
              </>
            ) : (
              <DescriptionItem label={t("Tokens")}>
                <TokenFlow
                  prompt={logEntry.prompt_tokens}
                  completion={logEntry.completion_tokens}
                  total={logEntry.total_tokens}
                />
              </DescriptionItem>
            )}
            {reasoningTokens !== undefined && reasoningTokens > 0 && (
              <DescriptionItem label={t("Reasoning Tokens")}>{formatNumberWithCommas(reasoningTokens)}</DescriptionItem>
            )}
            <DescriptionItem label={t("Cost")}>{formatLogCost(logEntry.spend, logEntry)}</DescriptionItem>
            <DescriptionItem label={t("requestMetrics.inbound")}>{logEntry.inbound_endpoint || "—"}</DescriptionItem>
 <DescriptionItem label={t("requestMetrics.outbound")}>{logEntry.outbound_endpoint || "—"}</DescriptionItem>
 <DescriptionItem label={t("requestMetrics.tps")}>{logEntry.tps != null ? logEntry.tps.toFixed(2) : "—"}</DescriptionItem>
 <DescriptionItem label={t("requestMetrics.duration")}>
              {logEntry.request_duration_ms != null ? (logEntry.request_duration_ms / 1000).toFixed(3) : "-"} s
            </DescriptionItem>
            {ttftMs != null && ttftMs >= 0 && (
              <DescriptionItem label={t("logsUX.firstResponse")}>{(ttftMs / 1000).toFixed(2)} s</DescriptionItem>
            )}

            {showResponseCache && (
              <DescriptionItem
                label={
                  <MetricLabel
                    label={t("Response Cache")}
                    tooltip={t("logsUX.responseCacheHelp")}
                  />
                }
              >
                <Badge variant="secondary" className={isResponseCacheHit ? "bg-success/15 text-success" : undefined}>
                  {isResponseCacheHit ? t("Hit") : t("Miss")}
                </Badge>
              </DescriptionItem>
            )}
            {responseCacheKey && (
              <DescriptionItem
                label={<MetricLabel label={t("Cache Key")} tooltip={t("logsUX.cacheKeyHelp")} />}
              >
                <TruncatedValue value={responseCacheKey} />
              </DescriptionItem>
            )}
            {(promptCacheReadTokens != null || !isResponseCacheHit) && (
              <DescriptionItem
                label={
                  <MetricLabel
                    label={t("Prompt Cache Read Tokens")}
                    tooltip={t("logsUX.promptCacheReadHelp")}
                  />
                }
              >
                {promptCacheReadTokens != null ? formatNumberWithCommas(promptCacheReadTokens) : t("Not reported")}
              </DescriptionItem>
            )}
            {promptCacheCreationTokens != null && promptCacheCreationTokens > 0 && (
              <DescriptionItem
                label={
                  <MetricLabel
                    label={t("Prompt Cache Creation Tokens")}
                    tooltip={t("logsUX.promptCacheWriteHelp")}
                  />
                }
              >
                {formatNumberWithCommas(promptCacheCreationTokens)}
              </DescriptionItem>
            )}

            {metadata?.litellm_overhead_time_ms !== undefined && metadata.litellm_overhead_time_ms !== null && (
              <DescriptionItem label={t("LiteLLM Overhead")}>
                {metadata.litellm_overhead_time_ms.toFixed(2)} ms
              </DescriptionItem>
            )}

            <DescriptionItem label={t("Retries")}>
              {metadata?.attempted_retries != null && metadata.attempted_retries > 0 && (
                <>
                  {metadata.attempted_retries}
                  {metadata.max_retries !== undefined && metadata.max_retries !== null
                    ? ` / ${metadata.max_retries}`
                    : ""}
                </>
              )}
              {metadata?.attempted_retries != null && metadata.attempted_retries <= 0 && (
                <Badge variant="secondary" className="bg-success/15 text-success">
                  {t("None")}
                </Badge>
              )}
              {metadata?.attempted_retries == null && "-"}
            </DescriptionItem>

            <DescriptionItem label={t("Start Time")}>
              {moment(logEntry.startTime).format(LOG_TIME_FORMAT)}
            </DescriptionItem>
            <DescriptionItem label={t("End Time")}>
              {moment(logEntry.endTime).format(LOG_TIME_FORMAT)}
            </DescriptionItem>
          </DescriptionList>
        </CardContent>
      </Card>
    </div>
  );
}

function JsonPanel({
  title,
  data,
  copyLabel,
  empty,
}: {
  title: string;
  data: unknown;
  copyLabel: string;
  empty?: string;
}) {
  const t = useT();
  return (
    <section className="rounded-md border">
      <div className="flex items-center justify-between border-b px-3 py-2">
        <h4 className="text-sm font-semibold text-foreground">{title}</h4>
        <CopyButton getText={() => JSON.stringify(data ?? {}, null, 2)} label={copyLabel} disabled={data == null} />
      </div>
      <div className="p-3">
        {data == null ? (
          <p className="text-sm italic text-muted-foreground">{empty}</p>
        ) : (
          <JsonViewer data={data} mode="formatted" />
        )}
      </div>
    </section>
  );
}

interface RequestResponseSectionProps {
  hasResponse: boolean;
  hasError: boolean;
  errorSummary: string;
  getRawRequest: () => any;
  getFormattedResponse: () => any;
  logEntry: LogEntry;
}

/** 展示调用记录的请求与响应；输入为日志及已格式化内容，返回可切换的详情区。JSON 模式明确标记未采集字段。 */
function RequestResponseSection({
  hasResponse,
  hasError,
  errorSummary,
  getRawRequest,
  getFormattedResponse,
  logEntry,
}: RequestResponseSectionProps) {
  const t = useT();
  const [open, setOpen] = useState(true);
  const [viewMode, setViewMode] = useState<"pretty" | "json">("pretty");
  const request = getRawRequest();
  const body = requestBody(request);
  const hasBody = body != null && (typeof body !== "object" || checkHasMessages(body));

  const totalSpend = logEntry.spend ?? 0;
  const promptTokens = logEntry.prompt_tokens || 0;
  const completionTokens = logEntry.completion_tokens || 0;
  const totalTokens = promptTokens + completionTokens;
  const costBreakdown = logEntry.metadata?.cost_breakdown;
  // 有明细就用明细，只在**真的没有**的时候才按 token 比例估。
  //
  // 用 && 要求两侧都在是错的：按秒、按张的模型只有输出侧，缓存全命中的调用
  // 只有缓存侧，这些都会被判成"没有明细"，然后显示一个按 token 摊出来的数字——
  // 编出来的数，但看起来和真的一模一样。
  const hasStoredBreakdown =
    costBreakdown?.input_cost !== undefined ||
    costBreakdown?.output_cost !== undefined ||
    costBreakdown?.cache_read_cost !== undefined ||
    costBreakdown?.cache_creation_cost !== undefined;
  const estimatedInputCost = totalTokens > 0 ? (totalSpend * promptTokens) / totalTokens : 0;
  const estimatedOutputCost = totalTokens > 0 ? (totalSpend * completionTokens) / totalTokens : 0;
  const inputCost = hasStoredBreakdown ? costBreakdown!.input_cost ?? 0 : estimatedInputCost;
  const outputCost = hasStoredBreakdown ? costBreakdown!.output_cost ?? 0 : estimatedOutputCost;

  return (
    <div className="bg-card rounded-lg shadow-sm w-full max-w-full overflow-hidden mb-6">
      <Collapsible open={open} onOpenChange={setOpen}>
        <Tabs value={viewMode} onValueChange={(value) => setViewMode(value as "pretty" | "json")}>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", width: "100%" }}>
            <CollapsibleTrigger className="flex flex-1 items-center gap-3 px-4 py-3 text-left">
              {open ? (
                <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
              ) : (
                <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
              )}
              <h3 className="text-lg font-medium text-foreground" style={{ margin: 0 }}>
                {t("Request & Response")}
              </h3>
            </CollapsibleTrigger>
            <TabsList className="mr-4">
              <TabsTrigger value="pretty">{t("Pretty")}</TabsTrigger>
              <TabsTrigger value="json">JSON</TabsTrigger>
            </TabsList>
          </div>
          <CollapsibleContent>
            <div>
              <TabsContent value="pretty">
                <PrettyMessagesView
                  request={request}
                  response={getFormattedResponse()}
                  responseError={errorSummary}
                  model={logEntry.model}
                  metrics={{
                    prompt_tokens: promptTokens,
                    completion_tokens: completionTokens,
                    input_cost: inputCost,
                    output_cost: outputCost,
                  }}
                />
              </TabsContent>
              <TabsContent value="json">
                <div className="space-y-4 p-4">
                  <JsonPanel title={t("Headers")} data={requestHeaders(request)} copyLabel={t("Copy headers")} empty={t("No headers recorded")} />
                  <JsonPanel title={t("Request")} data={hasBody ? body : null} copyLabel={t("Copy request")} empty={t("No request body recorded")} />
                  <JsonPanel
                    title={t("Response")}
                    data={hasResponse || hasError ? getFormattedResponse() : null}
                    copyLabel={t("Copy response")}
                    empty={t("Response data not available")}
                  />
                </div>
              </TabsContent>
            </div>
          </CollapsibleContent>
        </Tabs>
      </Collapsible>
    </div>
  );
}

const GUARDRAIL_JUMP_LINK_STYLE = {
  passed: { className: "border border-success/20 bg-success/10 text-success", glyph: "\u2713" },
  flagged: { className: "border border-warning/20 bg-warning/10 text-warning", glyph: "\u26A0" },
  failed: { className: "border border-destructive/20 bg-destructive/10 text-destructive", glyph: "\u2717" },
} as const;

const isPassedStatus = (status: unknown) => status === "pass" || status === "passed" || status === "success";
const isFlaggedStatus = (status: unknown) => status === "flagged" || status === "guardrail_flagged";

const guardrailJumpLinkOutcome = (statuses: unknown[]): keyof typeof GUARDRAIL_JUMP_LINK_STYLE => {
  if (statuses.every(isPassedStatus)) return "passed";
  if (statuses.every((s) => isPassedStatus(s) || isFlaggedStatus(s))) return "flagged";
  return "failed";
};

export function GuardrailJumpLink({ guardrailEntries }: { guardrailEntries: any[] }) {
  const t = useT();
  const outcome = guardrailJumpLinkOutcome(guardrailEntries.map((e) => e?.guardrail_status || e?.status));
  const { className, glyph } = GUARDRAIL_JUMP_LINK_STYLE[outcome];

  const handleClick = () => {
    const el = document.getElementById("guardrail-section");
    if (el) el.scrollIntoView({ behavior: "smooth" });
  };

  return (
    <div style={{ textAlign: "left", marginBottom: 12 }}>
      <div
        onClick={handleClick}
        className={className}
        style={{
          display: "inline-flex",
          alignItems: "center",
          gap: 6,
          padding: "4px 12px",
          borderRadius: 16,
          cursor: "pointer",
          fontSize: 13,
          fontWeight: 500,
        }}
      >
        {glyph} {guardrailEntries.length} {t("guardrail")}
        {guardrailEntries.length !== 1 ? "s" : ""} {t("evaluated")}
        <span style={{ fontSize: 11, opacity: 0.7 }}>{"\u2193"}</span>
      </div>
    </div>
  );
}

/** 在日志末尾提供单层披露控件；metadata 为后台授权返回的原始字段，首次展开时才序列化，复制使用同一数据。 */
function MetadataSection({ metadata }: { metadata: Record<string, unknown> }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <Collapsible open={open} onOpenChange={setOpen} className="mb-4 rounded-lg border bg-card p-4">
      <CollapsibleTrigger className="flex w-full items-center gap-2 text-left text-sm font-semibold">
        {open ? <ChevronDown aria-hidden="true" className="size-4" /> : <ChevronRight aria-hidden="true" className="size-4" />}
        {t("logsUX.rawMetadata")}
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="my-3 flex items-start justify-between gap-3">
          <p className="text-xs leading-relaxed text-muted-foreground">{t("logsUX.metadataHelp")}</p>
          <CopyButton getText={() => JSON.stringify(metadata, null, 2)} label={t("Copy Metadata")} />
        </div>
        {open && <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-3 font-mono text-xs">{JSON.stringify(metadata, null, 2)}</pre>}
      </CollapsibleContent>
    </Collapsible>
  );
}
