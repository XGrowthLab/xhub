"use client";

import * as React from "react";
import { CartesianGrid, Line, LineChart as RechartsLineChart, XAxis, YAxis } from "recharts";
import { ChartContainer, ChartLegend, ChartLegendContent, ChartTooltip, type ChartConfig } from "@/components/ui/chart";
import { SinglePointSummary } from "./SinglePointSummary";
import { cn } from "@/lib/cva.config";
import { ValueTooltip, type ChartTooltipComponent } from "./chart_tooltip";
import { categoryFills, type ChartColor } from "./colors";

export type LineChartCurveType = "linear" | "natural" | "monotone" | "step";

export type LineChartProps<TDatum extends Record<string, unknown>> = {
  data: readonly TDatum[];
  index: string;
  categories: readonly string[];
  colors?: readonly ChartColor[];
  valueFormatter?: (value: number) => string;
  yAxisWidth?: number;
  tickGap?: number;
  showLegend?: boolean;
  showXAxis?: boolean;
  showGridLines?: boolean;
  showTooltip?: boolean;
  customTooltip?: ChartTooltipComponent;
  connectNulls?: boolean;
  curveType?: LineChartCurveType;
  className?: string;
  style?: React.CSSProperties;
};

/** 展示真实多日序列；参数为数据与显示选项，返回图表。单日记录显示数值摘要，避免孤立小点被误认作趋势；不补造日期。 */
export function LineChart<TDatum extends Record<string, unknown>>({
  data,
  index,
  categories,
  colors,
  valueFormatter,
  yAxisWidth = 56,
  tickGap = 5,
  showLegend = true,
  showXAxis = true,
  showGridLines = true,
  showTooltip = true,
  customTooltip,
  connectNulls = false,
  curveType = "linear",
  className,
  style,
}: LineChartProps<TDatum>) {
  if (data.length === 1) return <SinglePointSummary row={data[0]} index={index} categories={categories} colors={colors} valueFormatter={valueFormatter} className={className} style={style} />;

  const fills = categoryFills(categories.length, colors);
  const config: ChartConfig = Object.fromEntries(categories.map((category) => [category, { label: category }]));
  const TooltipContent = customTooltip ?? ValueTooltip;

  return (
    <ChartContainer config={config} className={cn("aspect-auto h-80 w-full", className)} style={style}>
      <RechartsLineChart data={[...data]}>
        {showGridLines && <CartesianGrid vertical={false} />}
        <XAxis
          dataKey={index}
          hide={!showXAxis}
          tickLine={false}
          axisLine={false}
          minTickGap={tickGap}
          interval="equidistantPreserveStart"
        />
        <YAxis width={yAxisWidth} tickLine={false} axisLine={false} tickFormatter={valueFormatter} />
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
          <Line
            key={category}
            type={curveType}
            dataKey={category}
            stroke={fills[i]}
            strokeWidth={2}
            dot={false}
            isAnimationActive={false}
            connectNulls={connectNulls}
          />
        ))}
      </RechartsLineChart>
    </ChartContainer>
  );
}
