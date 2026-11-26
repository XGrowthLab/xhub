import type { StatusTone } from "@/components/shared/table_cells";
import type { LogEntry } from "./columns";
import { isRequestFailure } from "./logErrors";

/** 表格和详情共享生命周期及失败级别。接收日志，返回翻译键和颜色。旧数据仅在有 4xx 证据时标警告，不改变请求结果。 */
export function requestLogStatus(log: Pick<LogEntry, "status" | "metadata" | "error" | "severity">): { label: string; tone: StatusTone } {
  if (isRequestFailure(log)) {
    const status = Number(log.metadata?.http_status);
    if (log.severity === "warning" || (!log.severity && status >= 400 && status < 500)) {
      return { label: "logsUX.severityWarning", tone: "warning" };
    }
    return { label: log.severity === "error" ? "logsUX.severityError" : "Failure", tone: "error" };
  }
  switch (log.status) {
    case "executing": return { label: "Executing", tone: "info" };
    case "polling": return { label: "Polling", tone: "warning" };
    case "completed": return { label: "Completed", tone: "success" };
    default: return { label: "Success", tone: "success" };
  }
}
