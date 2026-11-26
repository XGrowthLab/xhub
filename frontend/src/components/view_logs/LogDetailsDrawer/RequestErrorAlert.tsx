import { CircleAlert, TriangleAlert } from "lucide-react";
import { useT } from "@/i18n";
import type { LogEntry } from "../columns";
import { logErrorSummary, logOutcomeKey } from "../logPresentation";
import { RequestLogDiagnosticFacts } from "../RequestLogDiagnosticFacts";
import { requestLogStatus } from "../taskStatus";

/** 日志详情的未成功摘要。接收失败日志与已整理的摘要，返回警告或错误及确切原因。
 * 由日志抽屉在失败时展示；未记录证据时提示原因未知，不推断断开方，不发起请求或修改日志。 */
export function RequestErrorAlert({ logEntry, summary }: { logEntry: LogEntry; summary: string }) {
  const t = useT();
  const metadata = logEntry.metadata || {};
  const upstream = metadata.upstream_response;
  const canceled = logEntry.diagnosis?.code === "request_canceled" || Number(metadata.http_status) === 499;
  const cancellationPhase = canceled ? logEntry.diagnosis?.phase ?? upstream?.failure?.phase : undefined;
  const upstreamStatus = Number(logEntry.diagnosis?.upstream_status ?? upstream?.status_code ?? 0);
  const observedError = canceled && upstream?.error ? logErrorSummary({ response: upstream.error }) : "";
  const errorInfo = metadata.error_information;
  const code = logEntry.diagnosis?.code;
  const warning = requestLogStatus(logEntry).tone === "warning";
  const Icon = warning ? TriangleAlert : CircleAlert;
  return (
    <div role="alert" className={`mb-6 flex items-start gap-2 rounded-lg border p-3 text-sm ${warning ? "border-warning/30 bg-warning/5" : "border-destructive/30 bg-destructive/5"}`}>
      <Icon aria-hidden className={`size-4 shrink-0 ${warning ? "text-warning" : "text-destructive"}`} />
      <div className="min-w-0">
        <div className={`font-medium ${warning ? "text-warning" : "text-destructive"}`}>{t(logOutcomeKey(logEntry))}</div>
        {metadata.http_status != null && <div>{t("HTTP Status")}: {metadata.http_status}</div>}
        {code === "model_not_allowed" && <p>{t("logsUX.chooseAllowedModel")}</p>}
        {code === "unsupported_method" && <p>{t("logsUX.useSupportedMethod")}</p>}
        {cancellationPhase === "upstream_request" && upstreamStatus === 0 && (
          <p>{t("logDiagnostics.canceledWaitingForUpstream")}</p>
        )}
        {cancellationPhase === "response_transfer" && upstreamStatus > 0 && (
          <p>{t("logDiagnostics.canceledDuringUpstreamResponse", { status: upstreamStatus })}</p>
        )}
        {!cancellationPhase && <RequestLogDiagnosticFacts log={logEntry} />}
        {observedError && (
          <p className="break-words"><strong>{t("logDiagnostics.upstreamError")}</strong> · {observedError}</p>
        )}
        {errorInfo ? <ErrorDescription errorInfo={errorInfo} /> : summary && <p className="mt-1 break-words">{summary}</p>}
        {(!summary || summary === "upstream request failed") && !errorInfo && (!code || code === "request_rejected" || code === "service_failure") && (
          <p className="text-muted-foreground">{t("logsUX.causeUnknown")}</p>
        )}
      </div>
    </div>
  );
}

/** 摘要展示已有错误代码与消息；长正文在原始错误中完整保留，避免顶部摘要撑满抽屉。参数是日志元数据，无副作用。 */
function ErrorDescription({ errorInfo }: { errorInfo: any }) {
  const t = useT();
  return (
    <div>
      {errorInfo.error_code && (
        <div>
          <span className="font-semibold">{t("Error Code:")}</span> {errorInfo.error_code}
        </div>
      )}
      {errorInfo.error_message && (
        <div className="line-clamp-3 break-all">
          <span className="font-semibold">{t("Message:")}</span> {errorInfo.error_message}
        </div>
      )}
    </div>
  );
}
