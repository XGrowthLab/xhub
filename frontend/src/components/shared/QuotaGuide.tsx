"use client";

import { ChevronDown, CircleHelp, Gauge, Wallet } from "lucide-react";
import { useT } from "@/i18n";

/** 按当前管理层级分组展示额度帮助；scope 决定主要说明，返回可折叠的多语言区域，不修改配置。 */
export function QuotaGuide({ scope }: { scope: "organization" | "team" | "person" | "key" }) {
  const t = useT();
  return (
    <aside aria-label={t("quotaGuide.title")} className="rounded-xl border bg-card text-sm shadow-sm">
      <details className="group p-4 sm:p-5">
        <summary className="flex cursor-pointer list-none items-center justify-between gap-3 font-medium [&::-webkit-details-marker]:hidden">
          <span className="flex items-center gap-2"><CircleHelp aria-hidden="true" className="size-4 shrink-0 text-primary" />{t("quotaGuide.title")}</span>
          <ChevronDown aria-hidden="true" className="size-4 shrink-0 transition-transform group-open:rotate-180" />
        </summary>
        <div className="mt-4 space-y-3">
          <p className="text-xs text-muted-foreground">{t("quotaGuide.chain")}</p>
          <div className="grid gap-3 md:grid-cols-2">
            <section className="rounded-lg border bg-muted/30 p-4">
              <h3 className="flex items-center gap-2 font-medium"><Wallet aria-hidden="true" className="size-4 text-primary" />{t("quotaGuide.budgetHeading")}</h3>
              <p className="mt-2 leading-relaxed text-muted-foreground">{t("quotaGuide." + scope)}</p>
              <p className="mt-2 leading-relaxed text-muted-foreground">{t("quotaGuide.shared")}</p>
            </section>
            <section className="rounded-lg border bg-muted/30 p-4">
              <h3 className="flex items-center gap-2 font-medium"><Gauge aria-hidden="true" className="size-4 text-primary" />{t("quotaGuide.rateHeading")}</h3>
              <ul className="mt-2 list-disc space-y-1.5 pl-4 leading-relaxed text-muted-foreground">
                <li>{t("quotaGuide.rateMeaning")}</li>
                <li>{t("quotaGuide.rateSharing")}</li>
                <li>{t("quotaGuide.rateAccounting")}</li>
              </ul>
            </section>
          </div>
          <details className="group/more rounded-lg border px-4 py-3">
            <summary className="flex cursor-pointer list-none items-center justify-between gap-2 font-medium [&::-webkit-details-marker]:hidden">
              {t("quotaGuide.moreHeading")}<ChevronDown aria-hidden="true" className="size-4 transition-transform group-open/more:rotate-180" />
            </summary>
            <div className="mt-3 space-y-2 leading-relaxed text-muted-foreground">
              <p>{t("quotaGuide.singleTeam")}</p>
              <p>{t("quotaGuide.business")}</p>
            </div>
          </details>
        </div>
      </details>
    </aside>
  );
}
