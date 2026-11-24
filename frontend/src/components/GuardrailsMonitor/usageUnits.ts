import { t as runtimeT, type TFunction } from "@/i18n";
import { formatNumberWithCommas, getSpendString } from "@/utils/dataUtils";

export type UsageUnits = Readonly<Record<string, number>>;

export const formatCost = (cost: number | null | undefined): string => {
  if (cost == null) return "—";
  return cost === 0 ? `$${formatNumberWithCommas(0, 4)}` : getSpendString(cost, 4);
};

export const totalUnits = (units: UsageUnits): number => Object.values(units).reduce((sum, n) => sum + n, 0);

/** 显示计费用量项名称；已知计数器按当前语言显示，未知计数器保留可读的上游名称。 */
export function counterLabel(counter: string, t: TFunction = runtimeT): string {
  const key = `pages.guardrailsMonitor.counters.${counter}`;
  const label = t(key);
  if (label !== key) return label;
  return counter
    .replace(/Units$/, "")
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/^./, (c) => c.toUpperCase());
}

/** 汇总用量卡片中不计费的数量；输入为未定价项和可选译文函数，没有未定价用量时返回 null。 */
export const unpricedSummary = (untracked: UsageUnits, t: TFunction = runtimeT): string | null => {
  const total = totalUnits(untracked);
  return total > 0
    ? t(total === 1 ? "pages.guardrailsMonitor.unpricedOne" : "pages.guardrailsMonitor.unpricedMany", {
        count: total.toLocaleString(),
      })
    : null;
};

export interface CounterMath {
  readonly counter: string;
  readonly units: number;
  readonly unpriced: number;
  readonly cost: number | null;
}

export const pricedUnits = ({ units, unpriced }: Pick<CounterMath, "units" | "unpriced">): number =>
  Math.max(units - unpriced, 0);

export const unitPrice = (row: CounterMath): number | null => {
  const priced = pricedUnits(row);
  return row.cost != null && priced > 0 ? row.cost / priced : null;
};

export const formatUnitPrice = (price: number): string => {
  const fixed = price.toFixed(6).replace(/\.?0+$/, "");
  return price > 0 && Number(fixed) === 0 ? "< $0.000001" : `$${fixed}`;
};

export interface MathRow {
  readonly label: string;
  readonly parts: readonly string[];
  readonly note: string | null;
}

/** 返回单项费用计算的展示数据及未定价原因；参数为用量和当前译文，不修改费率。 */
export const counterMathRow = (row: CounterMath, t: TFunction = runtimeT): MathRow => {
  const label = counterLabel(row.counter, t);
  const price = unitPrice(row);
  if (price == null) {
    return {
      label,
      parts: [row.units.toLocaleString(), "× —", "= —"],
      note: t("pages.guardrailsMonitor.missingPrice"),
    };
  }
  return {
    label,
    parts: [pricedUnits(row).toLocaleString(), `× ${formatUnitPrice(price)}`, `= ${formatCost(row.cost)}`],
    note:
      row.unpriced > 0
        ? t(row.unpriced === 1 ? "pages.guardrailsMonitor.omittedOne" : "pages.guardrailsMonitor.omittedMany", {
            count: row.unpriced.toLocaleString(),
          })
        : null,
  };
};

/** 返回用量明细行；参数为各项用量及译文，不执行计价。 */
export const unitsMathRows = (units: UsageUnits, t: TFunction = runtimeT): readonly MathRow[] =>
  Object.entries(units).map(([counter, n]) => ({
    label: counterLabel(counter, t),
    parts: [n.toLocaleString()],
    note: null,
  }));

export const pricingIssueUrl = (unpriced: UsageUnits, provider?: string): string => {
  const subject = provider ? `${provider} guardrail` : "guardrail";
  const params = new URLSearchParams({
    template: "feature_request.yml",
    title: `[Feature]: add ${subject} pricing to the cost map`,
    "the-feature": `LiteLLM has no price for these ${subject} usage units, so the Guardrails Monitor leaves them out of the cost: ${Object.keys(unpriced).join(", ")}`,
  });
  return `https://github.com/BerriAI/litellm/issues/new?${params.toString()}`;
};
