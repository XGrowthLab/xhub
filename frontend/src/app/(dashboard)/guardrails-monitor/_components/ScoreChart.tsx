import React from "react";
import { ChartNoAxesCombined } from "lucide-react";
import { BarChart } from "@/components/shared/charts";
import { useI18n } from "@/i18n";

/**
 * Overview chart: Request Outcomes Over Time (passed vs blocked).
 * Stacked bar chart. Data from usage/overview API (chart array).
 */
interface ScoreChartProps {
  data?: Array<{ date: string; passed: number; blocked: number }>;
}

/** 展示通过与拦截趋势；接收按日汇总的计数，返回图表和可键盘展开的同源数据表，空时段显示简短提示。 */
export function ScoreChart({ data }: ScoreChartProps) {
  const { t, locale } = useI18n();
  const title = t("Request Outcomes Over Time");
  const passedLabel = t("pages.guardrailsMonitor.passed");
  const blockedLabel = t("pages.guardrailsMonitor.blocked");
  const numberFormat = new Intl.NumberFormat(locale);
  const chartData = (data ?? []).map(({ date, passed, blocked }) => ({
    date,
    [passedLabel]: passed,
    [blockedLabel]: blocked,
  }));

  return (
    <section aria-label={title} className="min-w-0 rounded-xl border border-border bg-card p-4 sm:p-5">
      <h2 className="text-base font-semibold text-foreground">{title}</h2>
      {data?.length ? (
        <>
          <div className="mt-3 h-52 max-w-full overflow-hidden sm:h-64">
            <BarChart
              data={chartData}
              index="date"
              categories={[passedLabel, blockedLabel]}
              colors={["green", "red"]}
              valueFormatter={(value) => numberFormat.format(value)}
              yAxisWidth={48}
              showLegend={true}
              stack={true}
              className="h-full"
            />
          </div>
          <details className="mt-2 border-t border-border pt-3 text-sm">
            <summary className="w-fit cursor-pointer font-medium text-primary">{t("pages.guardrailsMonitor.dailyTotals")}</summary>
            <table className="mt-2 w-full border-collapse text-start text-sm tabular-nums">
              <thead>
                <tr className="border-b border-border text-muted-foreground">
                  <th scope="col" className="py-2 text-start font-medium">{t("Date")}</th>
                  <th scope="col" className="py-2 text-end font-medium">{passedLabel}</th>
                  <th scope="col" className="py-2 text-end font-medium">{blockedLabel}</th>
                </tr>
              </thead>
              <tbody>
                {data.map(({ date, passed, blocked }) => (
                  <tr key={date} className="border-b border-border/60 last:border-0">
                    <th scope="row" className="py-2 text-start font-medium">{date}</th>
                    <td className="py-2 text-end">{numberFormat.format(passed)}</td>
                    <td className="py-2 text-end">{numberFormat.format(blocked)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </details>
        </>
      ) : (
        <div className="mt-3 flex min-h-28 flex-col items-center justify-center gap-2 rounded-lg bg-muted/40 px-4 text-center text-sm text-muted-foreground">
          <ChartNoAxesCombined className="size-5" aria-hidden="true" />
          {t("No chart data for this period")}
        </div>
      )}
    </section>
  );
}
