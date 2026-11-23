import React from "react";
import { BarChart } from "@/components/shared/charts";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useT } from "@/i18n";

/**
 * Overview chart: Request Outcomes Over Time (passed vs blocked).
 * Stacked bar chart. Data from usage/overview API (chart array).
 */
interface ScoreChartProps {
  data?: Array<{ date: string; passed: number; blocked: number }>;
}

/** 展示监控接口的通过和拦截趋势。接收按日期汇总的计数，返回双语图例、数据或空状态，不修改接口数据。 */
export function ScoreChart({ data }: ScoreChartProps) {
  const t = useT();
  const passedLabel = t("pages.guardrailsMonitor.passed");
  const blockedLabel = t("pages.guardrailsMonitor.blocked");
  const chartData = (data ?? []).map(({ date, passed, blocked }) => ({
    date,
    [passedLabel]: passed,
    [blockedLabel]: blocked,
  }));

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base font-semibold">{t("Request Outcomes Over Time")}</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="h-80 min-h-[280px]">
          {chartData.length > 0 ? (
            <BarChart
              data={chartData}
              index="date"
              categories={[passedLabel, blockedLabel]}
              colors={["green", "red"]}
              valueFormatter={(v) => v.toLocaleString()}
              yAxisWidth={48}
              showLegend={true}
              stack={true}
              className="h-full"
            />
          ) : (
            <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
              {t("No chart data for this period")}
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
