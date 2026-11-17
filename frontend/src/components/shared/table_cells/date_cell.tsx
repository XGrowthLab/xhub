"use client";

import { CellTooltip } from "./cell_tooltip";
import { useI18n } from "@/i18n";
export type DatePrecision = "datetime" | "date";
interface DateCellProps {
  value: string | null | undefined;
  precision?: DatePrecision;
  fallback?: string;
}

/** 按当前界面语言格式化日期；precision 控制日期/日期时间，默认英语兼容独立调用，非法日期返回占位。 */
export const formatCellDate = (date: Date, precision: DatePrecision, locale = "en"): string => {
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(
    locale,
    precision === "date"
      ? { year: "numeric", month: "short", day: "numeric" }
      : { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" },
  ).format(date);
};

/** 悬浮提示保留年份、秒和时区；参数为日期与语言，返回完整时间，不改变存储或用户时区。 */
export const formatFullTimestamp = (date: Date, locale = "en"): string => {
  if (Number.isNaN(date.getTime())) return "—";
  const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return (
    new Intl.DateTimeFormat(locale, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
    }).format(date) + ` (${timeZone})`
  );
};

/** 为表格提供本地化时间；日期与占位均随语言切换，空值或非法值不渲染 Invalid Date。 */
export function DateCell({ value, precision = "datetime", fallback = "-" }: DateCellProps) {
  const { t, locale } = useI18n();
  const date = value ? new Date(value) : null;
  if (!date || Number.isNaN(date.getTime())) return <span className="text-muted-foreground">{t(fallback)}</span>;
  return (
    <CellTooltip
      content={formatFullTimestamp(date, locale)}
      trigger={
        <time dateTime={date.toISOString()} className="whitespace-nowrap tabular-nums">
          {formatCellDate(date, precision, locale)}
        </time>
      }
    />
  );
}
