"use client";

import { ChevronDown, CircleHelp } from "lucide-react";
import { useT } from "@/i18n";

const rules = [
  ["pages.projects.ownershipLabel", "pages.projects.guideWhat"],
  ["pages.projects.visibilityLabel", "pages.projects.guideWho"],
  ["pages.projects.modelsLabel", "pages.projects.guideModels"],
  ["pages.projects.budgetLabel", "pages.projects.guideBudget"],
  ["pages.projects.blockLabel", "pages.projects.guideBlock"],
  ["pages.projects.deleteLabel", "pages.projects.guideDelete"],
] as const;

/** 在项目列表按主题展示权限与计费规则；无参数，返回默认收起的原生披露控件，支持键盘操作。 */
export function ProjectGuide() {
  const t = useT();
  return (
    <details className="group mt-6 rounded-xl border bg-card text-sm">
      <summary className="flex cursor-pointer list-none items-center gap-2 rounded-xl px-5 py-4 font-medium focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <CircleHelp aria-hidden="true" className="size-4 text-primary" />
        {t("pages.projects.guideTitle")}
        <ChevronDown aria-hidden="true" className="ml-auto size-4 text-muted-foreground transition-transform group-open:rotate-180 motion-reduce:transition-none" />
      </summary>
      <dl className="grid gap-x-8 gap-y-4 border-t px-5 py-4 sm:grid-cols-2">
        {rules.map(([label, body]) => (
          <div key={label}>
            <dt className="font-semibold text-foreground">{t(label)}</dt>
            <dd className="mt-1 leading-relaxed text-muted-foreground">{t(body)}</dd>
          </div>
        ))}
      </dl>
    </details>
  );
}
