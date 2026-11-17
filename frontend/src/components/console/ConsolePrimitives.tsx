"use client";

import type { ReactNode } from "react";
import { ChevronDown, CircleHelp, Wallet } from "lucide-react";
import { useI18n } from "@/i18n";
import { formatConsoleMoney } from "./money";

/** 控制台页面的统一标题区；接收已翻译标题、简短描述及操作，返回响应式页头，不读写业务状态。 */
export function ConsolePageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-center justify-between gap-4">
      <div className="min-w-0">
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1.5 max-w-2xl text-sm leading-6 text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex min-w-0 max-w-full flex-wrap items-center gap-2">{actions}</div>}
    </header>
  );
}

/** 将补充说明收进可键盘操作的原生折叠区；title 与 children 由调用方国际化，不隐藏表单错误或关键状态。 */
export function HelpDisclosure({ title, children }: { title: string; children: ReactNode }) {
  return (
    <details className="group/help text-sm">
      <summary className="flex w-fit cursor-pointer list-none items-center gap-2 rounded-md py-1 text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <CircleHelp className="size-4 shrink-0" aria-hidden />
        {title}
        <ChevronDown
          className="size-3.5 shrink-0 transition-transform motion-reduce:transition-none group-open/help:rotate-180"
          aria-hidden
        />
      </summary>
      <div className="mt-2 space-y-2 rounded-lg bg-muted/50 p-3 text-sm leading-6 text-muted-foreground">
        {children}
      </div>
    </details>
  );
}

/** 显示已经确认的预算快照；spent/limit 为账面美元数据，null 上限表示未设置，非法值明确显示不可用。
 * 用于用量页、密钥创建与消息详情。只计算展示进度，保留实际超额，不改变准入或账单。 */
export function BudgetOverview({
  spent,
  limit,
  remaining,
  title,
  status,
  note,
}: {
  spent: number;
  limit: number | null;
  remaining?: number;
  title: string;
  status?: ReactNode;
  note?: ReactNode;
}) {
  const { t, locale } = useI18n();
  const valid =
    Number.isFinite(spent) &&
    spent >= 0 &&
    (limit === null || (Number.isFinite(limit) && limit >= 0)) &&
    (remaining === undefined || Number.isFinite(remaining));
  const balance = remaining ?? (limit === null ? null : Math.max(0, limit - spent));
  let percent: number | null = null;
  if (limit !== null) percent = limit === 0 ? 100 : Math.min(100, Math.max(0, (spent / limit) * 100));
  return (
    <section aria-label={title} className="rounded-xl border bg-card p-4 sm:p-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="flex items-center gap-2 text-sm font-semibold">
          <Wallet className="size-4 text-primary" aria-hidden />
          {title}
        </h2>
        {status && <div className="text-xs leading-5 text-muted-foreground">{status}</div>}
      </div>
      {!valid ? (
        <p className="mt-3 text-sm" role="status">
          {t("quotaGuide.personalUnavailable")}
        </p>
      ) : (
        <>
          <dl className="mt-5 grid grid-cols-1 gap-4 min-[420px]:grid-cols-3">
            {[
              ["consoleUi.balance", balance],
              ["consoleUi.spent", spent],
              ["consoleUi.limit", limit],
            ].map(([label, value], index) => (
              <div key={label as string} className="min-w-0">
                <dt className="text-sm text-muted-foreground">{t(label as string)}</dt>
                <dd
                  className={`mt-1 break-words font-semibold tracking-tight tabular-nums ${index === 0 ? "text-2xl" : "text-xl"}`}
                >
                  {value === null ? t("quotaGuide.personalUnlimited") : formatConsoleMoney(value as number, locale)}
                </dd>
              </div>
            ))}
          </dl>
          {percent !== null && (
            <div
              role="progressbar"
              aria-label={t("consoleUi.budgetProgress")}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(percent)}
              aria-valuetext={t("consoleUi.budgetProgressValue", {
                spent: formatConsoleMoney(spent, locale),
                limit: formatConsoleMoney(limit!, locale),
              })}
              className="mt-4 h-1.5 overflow-hidden rounded-full bg-muted"
            >
              <div
                className={`h-full rounded-full ${percent >= 95 ? "bg-amber-500" : "bg-primary"}`}
                style={{ width: `${percent}%` }}
              />
            </div>
          )}
          {note && (
            <div className="mt-3">
              <HelpDisclosure title={t("consoleUi.balanceDetails")}>{note}</HelpDisclosure>
            </div>
          )}
        </>
      )}
    </section>
  );
}
