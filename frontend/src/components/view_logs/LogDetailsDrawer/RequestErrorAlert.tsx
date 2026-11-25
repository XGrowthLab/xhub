import { CircleAlert } from "lucide-react";
import { useT } from "@/i18n";
import type { LogEntry } from "../columns";
import { logErrorSummary, logOutcomeKey } from "../logPresentation";

/** 日志详情的失败摘要。接收失败日志与已整理的摘要，返回状态、明确原因及取消时观察到的上游证据。
 * 由日志抽屉在失败时展示；未记录证据时提示原因未知，不推断断开方，不发起请求或修改日志。 */
export function RequestErrorAlert({ logEntry, summary }: { logEntry: LogEntry; summary: string }) {
  const t = useT();
  const metadata = logEntry.metadata || {};
  const upstream = metadata.upstream_response;
  const cancellation = Number(metadata.http_status) === 499 ? upstream?.failure : undefined;
  const upstreamStatus = Number(upstream?.status_code);
  const observedError = cancellation && upstream?.error ? logErrorSummary({ response: upstream.error }) : "";
  const errorInfo = metadata.error_information;
  return (
    <div role="alert" className="mb-6 flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm">
      <CircleAlert className="size-4 shrink-0 text-destructive" />
      <div>
        <div className="font-medium text-destructive">{t(logOutcomeKey(logEntry))}</div>
        {metadata.http_status != null && <div>{t("HTTP Status")}: {metadata.http_status}</div>}
        {cancellation?.phase === "upstream_request" && upstream?.status_code === 0 && (
          <p>{t("logDiagnostics.canceledWaitingForUpstream")}</p>
        )}
        {cancellation?.phase === "response_transfer" && upstreamStatus > 0 && (
          <p>{t("logDiagnostics.canceledDuringUpstreamResponse", { status: upstreamStatus })}</p>
        )}
        {observedError && (
          <p className="break-words"><strong>{t("logDiagnostics.upstreamError")}</strong> · {observedError}</p>
        )}
        {errorInfo ? <ErrorDescription errorInfo={errorInfo} /> : summary && <p className="mt-1 break-words">{summary}</p>}
        {(!summary || summary === "upstream request failed") && !errorInfo && (
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
