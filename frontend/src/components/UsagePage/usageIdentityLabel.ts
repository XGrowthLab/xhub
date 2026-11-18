import { t } from "@/i18n";

/** 标记后端保留的 unknown 与空模型归属；用量展示调用，原始模型名保持原样，且不影响聚合、账单或筛选键。 */
export function usageModelLabel(value: string | null | undefined): string {
  return !value?.trim() || value === "unknown" ? t("usagePolish.missingModel") : value;
}
