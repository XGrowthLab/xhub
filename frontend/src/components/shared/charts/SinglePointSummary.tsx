"use client";

import type { CSSProperties } from "react";
import { CalendarDays } from "lucide-react";
import { useT } from "@/i18n";
import { cn } from "@/lib/cva.config";
import { categoryFills, type ChartColor } from "./colors";
import { formatCategoryName } from "./chart_tooltip";

/** 从单条记录读取指标；优先保留含点号的原始键，随后解析嵌套路径。图表降级展示调用，缺失与非有限数返回 undefined。 */
export function singlePointValue(row: Record<string, unknown>, key: string): number | undefined {
  const value = Object.hasOwn(row, key) ? row[key] : key.split(".").reduce<unknown>((current, part) =>
    current && typeof current === "object" ? (current as Record<string, unknown>)[part] : undefined, row);
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

/** 将只有一个日期的曲线降级为可读数值；参数为真实记录、指标及格式，返回日期和各项值，不虚构零点或趋势。
 * AreaChart、LineChart 和监控页调用；缺失指标明确标记为未记录，颜色与多日图表一致。
 */
export function SinglePointSummary({ row, index, categories, colors, valueFormatter, className, style, labels }: {
  row: Record<string, unknown>; index: string; categories: readonly string[]; colors?: readonly ChartColor[];
  valueFormatter?: (value: number) => string; className?: string; style?: CSSProperties;
  labels?: Readonly<Record<string, string>>;
}) {
  const t = useT();
  const fills = categoryFills(categories.length, colors);
  return (
    <section className={cn("flex min-h-48 flex-col justify-center gap-5 rounded-xl bg-muted/30 p-5", className)} style={style} aria-label={t("usagePolish.singleDate")}>
      <div className="flex items-start gap-2.5 text-muted-foreground">
        <CalendarDays className="mt-0.5 size-4 shrink-0" aria-hidden />
        <p className="text-sm leading-relaxed">{t("usagePolish.singleDateHint", { date: String(row[index] ?? "—") })}</p>
      </div>
      <dl className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {categories.map((category, i) => {
          const value = singlePointValue(row, category);
          let displayValue = t("usagePolish.unavailable");
          if (value !== undefined) displayValue = valueFormatter ? valueFormatter(value) : value.toLocaleString();
          return <div key={category} className="min-w-0 border-l-2 pl-3" style={{ borderColor: fills[i] }}>
            <dt className="truncate text-sm text-muted-foreground" title={labels?.[category] ?? formatCategoryName(category)}>{labels?.[category] ?? formatCategoryName(category)}</dt>
            <dd className="mt-1 break-words text-xl font-semibold tabular-nums tracking-tight">{displayValue}</dd>
          </div>;
        })}
      </dl>
    </section>
  );
}
