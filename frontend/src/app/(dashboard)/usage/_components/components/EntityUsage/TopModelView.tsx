import { usageModelLabel } from "@/components/UsagePage/usageIdentityLabel";
import { UsageRankingBars } from "@/components/UsagePage/components/UsageRankingBars";
import { DataTable } from "@/components/shared/DataTable";
import { MoneyCell } from "@/components/shared/table_cells";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useState } from "react";
import { formatNumberWithCommas } from "@/utils/dataUtils";
import { useT } from "@/i18n";

type TopModel = {
  key: string;
  spend: number;
  successful_requests: number;
  failed_requests: number;
  tokens: number;
};

interface TopModelViewProps {
  topModels: TopModel[];
  topModelsLimit: number;
  setTopModelsLimit: (limit: number) => void;
}

export const TOP_MODEL_LIMITS = [5, 10, 25, 50];

/** 展示模型排行；参数为授权统计及条数回调，返回表格或图表，无后台写操作。
 * 条数与视图控件按组换行，避免窄卡片将标签压为竖排；榜单标签和数值各占一侧，长名称不挤压数据区。 */
export default function TopModelView({ topModels, topModelsLimit, setTopModelsLimit }: TopModelViewProps) {
  const t = useT();
  const [modelViewMode, setModelViewMode] = useState<"chart" | "table">("table");

  const columns = [
    {
      header: t("Model"),
      accessorKey: "key",
      cell: (info: any) => usageModelLabel(info.getValue()),
    },
    {
      header: t("Spend (USD)"),
      accessorKey: "spend",
      meta: { numeric: true },
      cell: (info: any) => <MoneyCell value={info.getValue()} decimals={2} />,
    },
    {
      header: t("Successful"),
      accessorKey: "successful_requests",
      meta: { numeric: true },
      cell: (info: any) => <span className="text-success">{info.getValue()?.toLocaleString() || 0}</span>,
    },
    {
      header: t("Failed"),
      accessorKey: "failed_requests",
      meta: { numeric: true },
      cell: (info: any) => <span className="text-destructive">{info.getValue()?.toLocaleString() || 0}</span>,
    },
    {
      header: t("Tokens"),
      accessorKey: "tokens",
      meta: { numeric: true },
      cell: (info: any) => info.getValue()?.toLocaleString() || 0,
    },
  ];
  const processedTopModels = topModels.slice(0, topModelsLimit);

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <Tabs
          className="shrink-0"
          value={String(topModelsLimit)}
          onValueChange={(value: string) => setTopModelsLimit(Number(value))}
        >
          <TabsList aria-label={t("Number of models to show")}>
            {TOP_MODEL_LIMITS.map((limit) => (
              <TabsTrigger key={limit} value={String(limit)} className="h-8 flex-none px-3">
                {limit}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
        <Tabs
          className="shrink-0"
          value={modelViewMode}
          onValueChange={(value: string) => setModelViewMode(value as "chart" | "table")}
        >
          <TabsList aria-label={t("Top model view mode")}>
            <TabsTrigger value="table" className="h-8 flex-none px-3">
              {t("usagePolish.table")}
            </TabsTrigger>
            <TabsTrigger value="chart" className="h-8 flex-none px-3">
              {t("usagePolish.chart")}
            </TabsTrigger>
          </TabsList>
        </Tabs>
      </div>
      {modelViewMode === "chart" ? (
        <UsageRankingBars
          items={processedTopModels.map(model => ({ id: model.key, label: usageModelLabel(model.key), value: model.spend }))}
          formatValue={value => "$" + formatNumberWithCommas(value, 2)}
        />
      ) : (
        <DataTable columns={columns} data={processedTopModels} isLoading={false} maxBodyHeight={600} size="compact" />
      )}
    </>
  );
}
