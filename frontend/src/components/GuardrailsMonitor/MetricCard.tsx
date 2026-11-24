import React, { type ReactNode } from "react";
import { cn } from "@/lib/cva.config";

interface MetricCardProps {
  label: string;
  value: string | number;
  valueColor?: string;
  icon?: ReactNode;
  subtitle?: string;
  hint?: ReactNode;
  compact?: boolean;
}

/** 展示命名指标、数值与可选解释；compact 用于窄列监控布局，返回可访问的指标组。 */
export function MetricCard({ label, value, valueColor = "text-foreground", icon, subtitle, hint, compact = false }: MetricCardProps) {
  return (
    <div role="group" aria-label={label} className={cn("flex h-full flex-col border border-border bg-card", compact ? "min-w-0 rounded-xl p-3.5" : "rounded-lg p-5")}>
      <div className={cn("flex justify-between", compact ? "mb-1.5 items-start gap-2" : "mb-1 items-center")}>
        <span className={cn("text-sm font-medium text-muted-foreground", compact && "min-w-0 [overflow-wrap:anywhere]")}>{label}</span>
        {icon && <span aria-hidden={compact || undefined} className={cn("text-muted-foreground", compact && "hidden shrink-0 rounded-md bg-muted p-1.5 @lg:block")}>{icon}</span>}
      </div>
      <div className={cn("font-semibold tracking-tight", compact ? "break-words text-2xl tabular-nums" : "text-3xl", valueColor)}>{value}</div>
      {subtitle && <p className="text-xs text-muted-foreground mt-1">{subtitle}</p>}
      {hint}
    </div>
  );
}
