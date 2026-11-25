import { formatNumberWithCommas } from "@/utils/dataUtils";
import { t } from "@/i18n";

export interface SummaryTile {
  title: string;
  value: string;
  className?: string;
  tooltipKey?: "usagePolish.totalCostTooltip" | "usagePolish.requestCostTooltip" | "usagePolish.flatCostTooltip";
  expandable?: boolean;
}

interface SpendSummaryMetadata {
  total_spend: number;
  total_flat_cost?: number;
  total_api_requests: number;
  total_successful_requests: number;
  total_failed_requests: number;
  total_tokens: number;
}

export const hasFlatCost = (metadata: SpendSummaryMetadata): boolean => (metadata.total_flat_cost ?? 0) > 0;

/** 将接口元数据转为总览卡片。预留费用非零时展示含明细入口的总费用，提示由界面按当前语言翻译。 */
export const buildSummaryTiles = (metadata: SpendSummaryMetadata, showFlatCost: boolean): SummaryTile[] => {
  const flatCost = metadata.total_flat_cost ?? 0;
  return [
    showFlatCost
      ? {
          title: t("Total Cost"),
          value: `$${formatNumberWithCommas(metadata.total_spend + flatCost, 2)}`,
          tooltipKey: "usagePolish.totalCostTooltip",
          expandable: true,
        }
      : { title: t("Total Spend"), value: `$${formatNumberWithCommas(metadata.total_spend, 2)}` },
    { title: t("Total Requests"), value: metadata.total_api_requests.toLocaleString() },
    {
      title: t("Successful Requests"),
      value: metadata.total_successful_requests.toLocaleString(),
      className: "text-success",
    },
    { title: t("Failed Requests"), value: metadata.total_failed_requests.toLocaleString(), className: "text-destructive" },
    { title: t("usagePolish.totalTokens"), value: metadata.total_tokens.toLocaleString() },
  ];
};

/** 为已展开的用量总览返回请求费用和预留费用卡片。保留原金额和缺失值处理，由界面翻译说明。 */
export const buildCostBreakdownTiles = (metadata: SpendSummaryMetadata): SummaryTile[] => [
  {
    title: t("Request Cost"),
    value: `$${formatNumberWithCommas(metadata.total_spend, 2)}`,
    className: "text-info",
    tooltipKey: "usagePolish.requestCostTooltip",
  },
  {
    title: t("Flat Cost"),
    value: `$${formatNumberWithCommas(metadata.total_flat_cost ?? 0, 2)}`,
    className: "text-violet-600",
    tooltipKey: "usagePolish.flatCostTooltip",
  },
];
