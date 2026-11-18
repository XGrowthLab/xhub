"use client";

import * as React from "react";
import { Bar, BarChart as RechartsBarChart, CartesianGrid, Cell, XAxis, YAxis } from "recharts";
import { ChartContainer, ChartLegend, ChartLegendContent, ChartTooltip, type ChartConfig } from "@/components/ui/chart";
import { cn } from "@/lib/cva.config";
import { ValueTooltip, type ChartTooltipComponent } from "./chart_tooltip";
import { categoryFills, type ChartColor } from "./colors";
import { useT } from "@/i18n";

export type BarChartProps<TDatum extends Record<string, unknown>> = {
  data: readonly TDatum[];
  index: string;
  categories: readonly string[];
  colors?: readonly ChartColor[];
  colorByDatum?: boolean;
  maxBarSize?: number;
  valueFormatter?: (value: number) => string;
  stack?: boolean;
  layout?: "horizontal" | "vertical";
  yAxisWidth?: number | "auto";
  tickGap?: number;
  showLegend?: boolean;
  showXAxis?: boolean;
  showGridLines?: boolean;
  showTooltip?: boolean;
  customTooltip?: ChartTooltipComponent;
  onValueChange?: (item: TDatum & { categoryClicked: string }) => void;
  className?: string;
  style?: React.CSSProperties;
};

/** 根据堆叠与方向返回柱体末端圆角；共享柱图调用，堆叠保持直角避免内部接缝，不改变数据或交互。 */
function barCornerRadius(stack: boolean, vertical: boolean): number | [number, number, number, number] {
  if (stack) return 0;
  return vertical ? [0, 4, 4, 0] : [4, 4, 0, 0];
}

/** 展示分类柱状图；参数包含真实数据、维度、交互与轴宽，返回图表或无数据状态。
 * vertical + auto 由 Recharts 按实际标签测量轴宽，避免排行短名称预留数百像素空白；不改变数据和费用。
 */
export function BarChart<TDatum extends Record<string, unknown>>({
  data,
  index,
  categories,
  colors,
  colorByDatum = false,
  maxBarSize = 40,
  valueFormatter,
  stack = false,
  layout = "horizontal",
  yAxisWidth = 56,
  tickGap = 5,
  showLegend = true,
  showXAxis = true,
  showGridLines = true,
  showTooltip = true,
  customTooltip,
  onValueChange,
  className,
  style,
}: BarChartProps<TDatum>) {
  const t = useT();
  if (data.length === 0) {
    return (
      <div
        className={cn("flex h-80 w-full items-center justify-center rounded-lg border border-dashed", className)}
        style={style}
      >
        <p className="text-sm text-muted-foreground">{t("No data")}</p>
      </div>
    );
  }

  const fills = categoryFills(colorByDatum ? data.length : categories.length, colors);
  const config: ChartConfig = Object.fromEntries(categories.map((category) => [category, { label: category }]));
  const vertical = layout === "vertical";
  const TooltipContent = customTooltip ?? ValueTooltip;
  const barRadius = barCornerRadius(stack, vertical);

  return (
    <ChartContainer config={config} className={cn("aspect-auto h-80 w-full", className)} style={style}>
      <RechartsBarChart data={[...data]} layout={layout}>
        {showGridLines && <CartesianGrid horizontal={!vertical} vertical={vertical} />}
        {vertical ? (
          <XAxis
            type="number"
            hide={!showXAxis}
            tickLine={false}
            axisLine={false}
            minTickGap={tickGap}
            tickFormatter={valueFormatter}
          />
        ) : (
          <XAxis
            dataKey={index}
            hide={!showXAxis}
            tickLine={false}
            axisLine={false}
            minTickGap={tickGap}
            interval="equidistantPreserveStart"
          />
        )}
        {vertical ? (
          <YAxis type="category" dataKey={index} width={yAxisWidth} tickLine={false} axisLine={false} interval={0} />
        ) : (
          <YAxis width={yAxisWidth} tickLine={false} axisLine={false} tickFormatter={valueFormatter} />
        )}
        {showTooltip && (
          <ChartTooltip
            content={({ active, payload, label }) => (
              <TooltipContent
                active={active}
                payload={payload}
                label={label}
                {...(customTooltip ? {} : { valueFormatter })}
              />
            )}
          />
        )}
        {showLegend && (
          <ChartLegend
            verticalAlign="top"
            content={<ChartLegendContent className="justify-end text-muted-foreground" />}
          />
        )}
        {categories.map((category, i) => (
          <Bar
            key={category}
            dataKey={category}
            fill={fills[i]}
            stackId={stack ? "stack" : undefined}
            isAnimationActive={false}
            maxBarSize={maxBarSize}
            radius={barRadius}
            onClick={
              onValueChange
                ? (item: { payload?: TDatum }) => {
                    if (item.payload) onValueChange({ ...item.payload, categoryClicked: category });
                  }
                : undefined
            }
          >
            {colorByDatum && data.map((_, dataIndex) => <Cell key={dataIndex} fill={fills[dataIndex]} />)}
          </Bar>
        ))}
      </RechartsBarChart>
    </ChartContainer>
  );
}
