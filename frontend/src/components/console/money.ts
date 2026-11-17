/** 格式化控制台美元金额；value 为实际账面数，locale 决定数字格式。
 * 预算卡、用量指标和提示框共用；非有限数返回占位，小额保留精度，不参与计费或修改原值。 */
export function formatConsoleMoney(value: number, locale: string): string {
  if (!Number.isFinite(value)) return "—";
  return new Intl.NumberFormat(locale, {
    style: "currency",
    currency: "USD",
    currencyDisplay: "narrowSymbol",
    minimumFractionDigits: 2,
    maximumFractionDigits: Math.abs(value) > 0 && Math.abs(value) < 0.0001 ? 12 : 4,
  }).format(value);
}
