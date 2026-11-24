import type { ColumnDef, OnChangeFn, SortingState } from "@tanstack/react-table";
import { Activity, CircleDollarSign, HeartPulse, Shield, Timer, TrendingUp, TriangleAlert } from "lucide-react";
import React, { useMemo, useState } from "react";
import { DataTable, DataTableSortHeader } from "@/components/shared/DataTable";
import { MoneyCell } from "@/components/shared/table_cells/money_cell";
import { CellTooltip } from "@/components/shared/table_cells/cell_tooltip";
import {
  type GuardrailUsageOverviewRow,
  useGuardrailsUsageOverview,
} from "@/app/(dashboard)/hooks/guardrails/useGuardrailsUsage";
import { CalcPopover, MathTable } from "@/components/GuardrailsMonitor/CalcPopover";
import { UnpricedNote } from "@/components/GuardrailsMonitor/UnpricedNote";
import {
  counterLabel,
  formatCost,
  totalUnits,
  unpricedSummary,
  type UsageUnits,
} from "@/components/GuardrailsMonitor/usageUnits";
import { useT } from "@/i18n";
import { scrubBrand } from "@/i18n/brand";
import { PageHeader } from "@/components/shared/PageHeader";
import { UiLoadingSpinner } from "@/components/ui/ui-loading-spinner";
import { MetricCard } from "@/components/GuardrailsMonitor/MetricCard";
import { ScoreChart } from "./ScoreChart";

interface GuardrailsOverviewProps {
  accessToken?: string | null;
  startDate: string;
  endDate: string;
  onSelectGuardrail: (id: string) => void;
  dateRangeControl?: React.ReactNode;
}

type SortKey = "failRate" | "requestsEvaluated" | "avgLatency" | "cost";

const providerColors: Record<string, string> = {
  Bedrock: "bg-warning/15 text-warning border-warning/20",
  "Google Cloud": "bg-info/15 text-info border-info/20",
  LiteLLM:
    "bg-indigo-100 text-indigo-700 border-indigo-200 dark:bg-indigo-950 dark:text-indigo-300 dark:border-indigo-800",
  XHub:
    "bg-indigo-100 text-indigo-700 border-indigo-200 dark:bg-indigo-950 dark:text-indigo-300 dark:border-indigo-800",
  Custom: "bg-muted text-muted-foreground border-border",
};

const EMPTY_METRICS = {
  totalRequests: 0,
  totalBlocked: 0,
  passRate: "0",
  avgLatency: null as number | null,
  count: 0,
  totalCost: null as number | null,
  untracked: {} as UsageUnits,
};

/** 显示护栏行的用量汇总和分项提示；参数为真实计数，空数据以占位符呈现。 */
function UsageUnitsCell({ units }: { units: GuardrailUsageOverviewRow["usageUnits"] }) {
  const t = useT();
  const counters = Object.entries(units);
  if (counters.length === 0) return <span className="text-muted-foreground">—</span>;
  return (
    <CellTooltip
      content={
        <ul className="space-y-0.5">
          {counters.map(([counter, n]) => (
            <li key={counter}>
              {counterLabel(counter, t)}: {n.toLocaleString()}
            </li>
          ))}
        </ul>
      }
      trigger={<span className="tabular-nums">{totalUnits(units).toLocaleString()}</span>}
    />
  );
}

/** 解释已计价总额；参数为用量行与汇总金额，展示分项和未计价提示，不改变计费数据。 */
function TotalCostMath({
  rows,
  total,
  untracked,
}: {
  rows: GuardrailUsageOverviewRow[];
  total: number | null;
  untracked: UsageUnits;
}) {
  const t = useT();
  return (
    <CalcPopover title={t("How this cost is calculated")} formula={t("pages.guardrailsMonitor.costFormula")}>
      <MathTable
        rows={rows
          .filter((row) => row.cost != null)
          .map((row) => ({ label: row.name, parts: [formatCost(row.cost)], note: null }))}
        total={formatCost(total)}
      />
      <p className="text-xs text-muted-foreground">
        {t("pages.guardrailsMonitor.costExplanation")}
      </p>
      <UnpricedNote unpriced={untracked} />
    </CalcPopover>
  );
}

/** 显示护栏行的已计价金额，以及未计价用量的可访问提示；不重新计算账单。 */
function CostCell({ row }: { row: GuardrailUsageOverviewRow }) {
  const t = useT();
  const unpriced = unpricedSummary(row.untrackedUsageUnits, t);
  return (
    <span className="inline-flex w-full items-center justify-end gap-1">
      {unpriced && (
        <CellTooltip
          content={t("pages.guardrailsMonitor.costExcludedHint")}
          trigger={<TriangleAlert aria-label={unpriced} className="size-3.5 shrink-0 text-warning" />}
        />
      )}
      <MoneyCell value={row.cost} emptyText="—" showZero />
    </span>
  );
}

/** 展示所选时间窗内的护栏统计和性能表。参数包含会话、日期和详情回调；不修改护栏设置。 */
export function GuardrailsOverview({
  accessToken = null,
  startDate,
  endDate,
  onSelectGuardrail,
  dateRangeControl,
}: GuardrailsOverviewProps) {
  const t = useT();
  const [sortBy, setSortBy] = useState<SortKey>("failRate");
  const [sortDir, setSortDir] = useState<"asc" | "desc">("desc");

  const {
    data: guardrailsData,
    isLoading: guardrailsLoading,
    error: guardrailsError,
  } = useGuardrailsUsageOverview({ accessToken, startDate, endDate });

  const activeData: GuardrailUsageOverviewRow[] = useMemo(() => guardrailsData?.rows ?? [], [guardrailsData]);
  const metrics = useMemo(() => {
    if (!guardrailsData) return EMPTY_METRICS;
    // 不把未采集到延迟的护栏算作 0ms，真实的 0ms 仍参与平均。
    const recordedLatencies = activeData.flatMap(({ avgLatency }) => avgLatency == null ? [] : [avgLatency]);
    return {
      totalRequests: guardrailsData.totalRequests,
      totalBlocked: guardrailsData.totalBlocked,
      passRate: String(guardrailsData.passRate),
      avgLatency: recordedLatencies.length
        ? Math.round(recordedLatencies.reduce((sum, value) => sum + value, 0) / recordedLatencies.length)
        : null,
      count: activeData.length,
      totalCost: guardrailsData.totalCost,
      untracked: guardrailsData.totalUntrackedUsageUnits,
    };
  }, [guardrailsData, activeData]);
  const chartData = guardrailsData?.chart;
  const sorted = useMemo(() => {
    const mult = sortDir === "desc" ? -1 : 1;
    return [...activeData].sort((a, b) => {
      const aVal = a[sortBy];
      const bVal = b[sortBy];
      if (aVal == null || bVal == null) return Number(aVal == null) - Number(bVal == null);
      return (aVal - bVal) * mult;
    });
  }, [activeData, sortBy, sortDir]);
  const isLoading = guardrailsLoading;
  const error = guardrailsError;
  const showMetrics = !isLoading && !error;
  let latencyColor = "text-muted-foreground";
  if (metrics.avgLatency != null) {
    latencyColor = "text-success";
    if (metrics.avgLatency > 50) latencyColor = "text-warning";
    if (metrics.avgLatency > 150) latencyColor = "text-destructive";
  }

  const columns: ColumnDef<GuardrailUsageOverviewRow>[] = [
    {
      header: t("Status"),
      accessorKey: "status",
      enableSorting: false,
      cell: ({ row }) => (
        <span className="inline-flex items-center gap-1.5">
          <span
              aria-hidden="true"
              className={`size-2 shrink-0 rounded-full ${
              row.original.status === "healthy"
                ? "bg-success"
                : row.original.status === "warning"
                  ? "bg-warning"
                  : "bg-destructive"
            }`}
          />
          <span className="text-xs text-muted-foreground capitalize">{t(`pages.guardrailsMonitor.status.${row.original.status}`)}</span>
        </span>
      ),
    },
    {
      header: t("Guardrail"),
      accessorKey: "name",
      enableSorting: false,
      cell: ({ row }) => (
        <button
          type="button"
          className="rounded-sm text-start text-sm font-medium text-primary underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
          onClick={() => onSelectGuardrail(row.original.id)}
        >
          {row.original.name}
        </button>
      ),
    },
    {
      header: t("Provider"),
      accessorKey: "provider",
      enableSorting: false,
      cell: ({ row }) => (
        <span
          className={`inline-flex items-center px-2 py-0.5 text-xs font-medium rounded border ${
            providerColors[row.original.provider] ?? providerColors[scrubBrand(row.original.provider)] ?? providerColors.Custom
          }`}
        >
          {scrubBrand(row.original.provider)}
        </span>
      ),
    },
    {
      header: ({ column }) => <DataTableSortHeader column={column} title={t("Requests")} />,
      accessorKey: "requestsEvaluated",
      meta: { numeric: true },
      sortDescFirst: false,
      cell: ({ row }) => row.original.requestsEvaluated.toLocaleString(),
    },
    {
      header: ({ column }) => <DataTableSortHeader column={column} title={t("Fail Rate")} />,
      accessorKey: "failRate",
      meta: { numeric: true },
      sortDescFirst: false,
      cell: ({ row }) => (
        <span
          className={
            row.original.failRate > 15
              ? "text-destructive"
              : row.original.failRate > 5
                ? "text-warning"
                : "text-success"
          }
        >
          {row.original.failRate}%
          {row.original.trend === "up" && <span className="ms-1 text-xs text-destructive"><span aria-hidden="true">↑</span><span className="sr-only">{t("pages.guardrailsMonitor.trendUp")}</span></span>}
          {row.original.trend === "down" && <span className="ms-1 text-xs text-success"><span aria-hidden="true">↓</span><span className="sr-only">{t("pages.guardrailsMonitor.trendDown")}</span></span>}
        </span>
      ),
    },
    {
      header: ({ column }) => <DataTableSortHeader column={column} title={t("Avg. latency added")} />,
      accessorKey: "avgLatency",
      meta: { numeric: true },
      sortDescFirst: false,
      cell: ({ row }) => (
        <span
          className={
            row.original.avgLatency == null
              ? "text-muted-foreground"
              : row.original.avgLatency > 150
                ? "text-destructive"
                : row.original.avgLatency > 50
                  ? "text-warning"
                  : "text-success"
          }
        >
          {row.original.avgLatency != null ? `${row.original.avgLatency}ms` : "—"}
        </span>
      ),
    },
    {
      header: t("Usage Units"),
      accessorKey: "usageUnits",
      enableSorting: false,
      meta: { numeric: true },
      cell: ({ row }) => <UsageUnitsCell units={row.original.usageUnits} />,
    },
    {
      header: ({ column }) => <DataTableSortHeader column={column} title={t("Cost")} />,
      accessorKey: "cost",
      meta: { numeric: true },
      sortDescFirst: false,
      cell: ({ row }) => <CostCell row={row.original} />,
    },
  ];

  const sortableKeys: SortKey[] = ["failRate", "requestsEvaluated", "avgLatency", "cost"];
  const sorting = useMemo<SortingState>(() => [{ id: sortBy, desc: sortDir === "desc" }], [sortBy, sortDir]);
  const handleSortingChange: OnChangeFn<SortingState> = (updater) => {
    const nextSorting = typeof updater === "function" ? updater(sorting) : updater;
    const primarySort = nextSorting[0];
    if (primarySort && sortableKeys.includes(primarySort.id as SortKey)) {
      setSortBy(primarySort.id as SortKey);
      setSortDir(primarySort.desc ? "desc" : "asc");
    }
  };

  return (
    <div className="@container min-w-0 space-y-5">
      <PageHeader
        icon={<HeartPulse />}
        title={t("pages.guardrailsMonitor.title")}
        subtitle={t("pages.guardrailsMonitor.subtitle")}
      />
      {dateRangeControl && <div className="flex w-full min-w-0 justify-end">{dateRangeControl}</div>}

      {isLoading && (
        <div role="status" aria-busy="true" aria-label={t("Loading")} className="flex items-center justify-center rounded-xl border bg-card py-10">
          <UiLoadingSpinner className="size-5 text-primary" />
        </div>
      )}
      {error && <p role="alert" className="rounded-xl border border-destructive/20 bg-destructive/5 p-4 text-sm text-destructive">{t("Failed to load data. Try again.")}</p>}

      {showMetrics && (
        <section aria-label={t("pages.guardrailsMonitor.metrics")} className="grid grid-cols-2 gap-3 @xl:grid-cols-3 @5xl:grid-cols-6">
          <MetricCard compact label={t("Total Evaluations")} value={metrics.totalRequests.toLocaleString()} icon={<Activity className="size-4" />} />
          <MetricCard
            compact
            label={t("Blocked Requests")}
            value={metrics.totalBlocked.toLocaleString()}
            valueColor={metrics.totalBlocked > 0 ? "text-destructive" : undefined}
            icon={<TriangleAlert className="size-4 text-destructive" />}
          />
          <MetricCard
            compact
            label={t("Pass Rate")}
            value={metrics.totalRequests > 0 ? `${metrics.passRate}%` : "—"}
            icon={<TrendingUp className="size-4 text-success" />}
          />
          <MetricCard
            compact
            label={t("Avg. latency added")}
            value={metrics.avgLatency != null ? `${metrics.avgLatency}ms` : "—"}
            icon={<Timer className="size-4" />}
            valueColor={latencyColor}
          />
          <MetricCard
            compact
            label={t("Guardrail Cost")}
            value={formatCost(metrics.totalCost)}
            valueColor={metrics.totalCost != null ? "text-foreground" : "text-muted-foreground"}
            icon={<CircleDollarSign className="size-4" />}
            subtitle={unpricedSummary(metrics.untracked, t) ?? undefined}
            hint={<TotalCostMath rows={activeData} total={metrics.totalCost} untracked={metrics.untracked} />}
          />
          <MetricCard compact label={t("Active Guardrails")} value={metrics.count} icon={<Shield className="size-4" />} />
        </section>
      )}

      {showMetrics && <ScoreChart data={chartData} />}

      <section aria-label={t("Guardrail Performance")} className="min-w-0">
        <DataTable
          columns={columns}
          data={sorted}
          getRowId={(row) => row.id}
          isLoading={isLoading}
          noDataMessage={error ? "—" : t("pages.guardrailsMonitor.emptyPerformance")}
          onRowClick={(row) => onSelectGuardrail(row.id)}
          rowClassName={() => "cursor-pointer"}
          sortingMode="server"
          sorting={sorting}
          onSortingChange={handleSortingChange}
          enableSortingRemoval={false}
          size="compact"
          toolbar={() => (
            <div>
              <h2 className="text-base font-semibold text-foreground">{t("Guardrail Performance")}</h2>
              <p className="mt-0.5 text-sm text-muted-foreground">
                {t("pages.guardrailsMonitor.performanceHint")}
              </p>
            </div>
          )}
        />
      </section>

    </div>
  );
}
