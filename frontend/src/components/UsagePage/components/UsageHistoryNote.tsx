import { History } from "lucide-react";
import { useT } from "@/i18n";

/** 向用量页解释请求快照与当前密钥列表的区别；无参数，返回默认收起的说明，不判断密钥当前状态或修改历史。 */
export function UsageHistoryNote() {
  const t = useT();
  return <details className="rounded-lg border border-dashed bg-muted/20 px-3 py-2 text-sm text-muted-foreground">
    <summary className="flex cursor-pointer items-center gap-2 font-medium"><History className="size-4 shrink-0" aria-hidden />{t("usagePolish.historyTitle")}</summary>
    <p className="mt-2 max-w-3xl leading-relaxed">{t("usagePolish.historyHint")}</p>
  </details>;
}
