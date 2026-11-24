import React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { DataTable } from "@/components/shared/DataTable";
import { MoneyCell } from "@/components/shared/table_cells";
import { MetricWithMetadata } from "@/components/UsagePage/types";
import { useI18n } from "@/i18n";

interface EndpointUsageTableProps {
  endpointData: Record<string, MetricWithMetadata>;
}

interface EndpointRow {
  key: string;
  endpoint: string;
  successful_requests: number;
  failed_requests: number;
  api_requests: number;
  total_tokens: number;
  spend: number;
  successRate: number;
}

/** 渲染所选范围按端点聚合的实测用量；参数为端点用量映射，返回可读表格。
 * 成功与失败各自按列对齐，零请求成功率为零；缺少端点时显示本地化提示，不触发请求。 */
const EndpointUsageTable: React.FC<EndpointUsageTableProps> = ({ endpointData }) => {
  const { t, locale } = useI18n();
  const count = new Intl.NumberFormat(locale);
  const percent = new Intl.NumberFormat(locale, { style: "percent", minimumFractionDigits: 2, maximumFractionDigits: 2 });

  const dataSource: EndpointRow[] = Object.entries(endpointData).map(([endpoint, data]) => ({
    key: endpoint,
    endpoint,
    successful_requests: data.metrics.successful_requests,
    failed_requests: data.metrics.failed_requests,
    api_requests: data.metrics.api_requests,
    total_tokens: data.metrics.total_tokens,
    spend: data.metrics.spend,
    successRate: data.metrics.api_requests === 0 ? 0 : data.metrics.successful_requests / data.metrics.api_requests * 100,
  }));

  const columns: ColumnDef<EndpointRow>[] = [
    {
      header: t("Endpoint"),
      accessorKey: "endpoint",
      cell: ({ row }) => <span className="block min-w-24 max-w-80 whitespace-normal font-medium [overflow-wrap:anywhere]">{row.original.endpoint}</span>,
    },
    {
      header: t("Successful"),
      accessorKey: "successful_requests",
      meta: { numeric: true },
      cell: ({ row }) => <span className="font-medium tabular-nums text-success">{count.format(row.original.successful_requests)}</span>,
    },
    {
      header: t("Failed"),
      accessorKey: "failed_requests",
      meta: { numeric: true },
      cell: ({ row }) => <span className="font-medium tabular-nums text-destructive">{count.format(row.original.failed_requests)}</span>,
    },
    {
      header: t("Total Request"),
      accessorKey: "api_requests",
      meta: { numeric: true },
      cell: ({ row }) => count.format(row.original.api_requests),
    },
    {
      header: t("Success Rate"),
      accessorKey: "successRate",
      meta: { numeric: true },
      cell: ({ row }) => {
        const value = row.original.successRate;
        const successRateStr = percent.format(value / 100);
        return (
          <span
            className={
              value >= 95
                ? "text-success font-medium"
                : value >= 80
                  ? "text-warning font-medium"
                  : "text-destructive font-medium"
            }
          >
            {successRateStr}
          </span>
        );
      },
    },
    {
      header: t("Total Tokens"),
      accessorKey: "total_tokens",
      meta: { numeric: true },
      cell: ({ row }) => count.format(row.original.total_tokens),
    },
    {
      header: t("Spend"),
      accessorKey: "spend",
      meta: { numeric: true },
      cell: ({ row }) => <MoneyCell value={row.original.spend} decimals={2} />,
    },
  ];

  return (
    <DataTable
      columns={columns}
      data={dataSource}
      getRowId={(row) => row.key}
      noDataMessage={t("usagePolish.noEndpointData")}
      size="compact"
    />
  );
};

export default EndpointUsageTable;
