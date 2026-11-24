import React from "react";
import { pricingIssueUrl, totalUnits, type UsageUnits } from "./usageUnits";
import { useT } from "@/i18n";

/** 说明供应商报告但未计价的用量；无未定价项时不渲染，反馈链接沿用当前计数器。 */
export function UnpricedNote({ unpriced, provider }: { unpriced: UsageUnits; provider?: string }) {
  const t = useT();
  const total = totalUnits(unpriced);
  if (total === 0) return null;
  const note = t(total === 1 ? "pages.guardrailsMonitor.costExcludedOne" : "pages.guardrailsMonitor.costExcludedMany", {
    count: total.toLocaleString(),
  });
  return (
    <p className="text-xs text-warning">
      {note}{" "}
      <a
        href={pricingIssueUrl(unpriced, provider)}
        target="_blank"
        rel="noreferrer"
        className="underline underline-offset-2"
      >
        {t("Request pricing on GitHub")}
      </a>
    </p>
  );
}
