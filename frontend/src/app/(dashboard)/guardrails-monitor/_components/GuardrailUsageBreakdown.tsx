import type { ColumnDef } from "@tanstack/react-table";
import { CircleDollarSign } from "lucide-react";
import React from "react";
import type { GuardrailUsageDetail } from "@/app/(dashboard)/hooks/guardrails/useGuardrailsUsage";
import { CalcPopover, MathTable } from "@/components/GuardrailsMonitor/CalcPopover";
import { MetricCard } from "@/components/GuardrailsMonitor/MetricCard";
import { UnpricedNote } from "@/components/GuardrailsMonitor/UnpricedNote";
import {
  counterLabel,
  counterMathRow,
  formatCost,
  totalUnits,
  unitsMathRows,
  unpricedSummary,
} from "@/components/GuardrailsMonitor/usageUnits";
import { DataTable } from "@/components/shared/DataTable";
import { IdCell } from "@/components/shared/table_cells/id_cell";
import { MoneyCell } from "@/components/shared/table_cells/money_cell";
import { useT, t } from "@/i18n";

interface CounterRow {
  counter: string;
  units: number;
  cost: number | null;
  unpriced: number;
}

interface GroupRow {
  id: string;
  units: number;
  cost: number | null;
  unpriced: number;
}

const counterRows = (detail: GuardrailUsageDetail): CounterRow[] =>
  Object.entries(detail.usage_units).map(([counter, units]) => ({
    counter,
    units,
    cost: detail.cost_by_unit[counter] ?? null,
    unpriced: detail.untracked_usage_units[counter] ?? 0,
  }));

const groupRows = (
  unitsByGroup: GuardrailUsageDetail["usage_units_by_team"],
  costByGroup: GuardrailUsageDetail["cost_by_team"],
  untrackedByGroup: GuardrailUsageDetail["untracked_usage_units_by_team"],
): GroupRow[] =>
  Object.entries(unitsByGroup)
    .map(([id, units]) => ({
      id,
      units: totalUnits(units),
      cost: costByGroup[id] ?? null,
      unpriced: totalUnits(untrackedByGroup[id] ?? {}),
    }))
    .sort((a, b) => b.units - a.units);

const UnpricedUnitsCell = ({ unpriced }: { unpriced: number }) =>
  { const t = useT(); return (unpriced > 0 ? (
    <span className="text-warning">{unpriced.toLocaleString()}</span>
  ) : (
    <span className="text-muted-foreground">—</span>
  )); };

const unpricedColumn = <TRow extends { unpriced: number }>(): ColumnDef<TRow> => ({
  header: () => t("Unpriced Units"),
  accessorKey: "unpriced",
  meta: { numeric: true },
  cell: ({ row }) => <UnpricedUnitsCell unpriced={row.original.unpriced} />,
});

const counterColumns: ColumnDef<CounterRow>[] = [
  { header: () => t("Counter"), accessorKey: "counter", cell: ({ row }) => counterLabel(row.original.counter) },
  {
    header: () => t("Units"),
    accessorKey: "units",
    meta: { numeric: true },
    cell: ({ row }) => row.original.units.toLocaleString(),
  },
  {
    header: () => t("Cost"),
    accessorKey: "cost",
    meta: { numeric: true },
    cell: ({ row }) => <MoneyCell value={row.original.cost} emptyText="—" showZero />,
  },
  unpricedColumn<CounterRow>(),
];

const groupColumns = (label: string, emptyLabel: string): ColumnDef<GroupRow>[] => [
  {
    header: () => t(label),
    accessorKey: "id",
    cell: ({ row }) =>
      row.original.id ? (
        <IdCell value={row.original.id} variant="plain" copyable />
      ) : (
        <span className="text-muted-foreground">{t(emptyLabel)}</span>
      ),
  },
  {
    header: () => t("Units"),
    accessorKey: "units",
    meta: { numeric: true },
    cell: ({ row }) => row.original.units.toLocaleString(),
  },
  {
    header: () => t("Cost"),
    accessorKey: "cost",
    meta: { numeric: true },
    cell: ({ row }) => <MoneyCell value={row.original.cost} emptyText="—" showZero />,
  },
  unpricedColumn<GroupRow>(),
];

const teamColumns = groupColumns("Team", "No team");
const keyColumns = groupColumns("Key", "No key");

/** 展示用量与计价相乘的计算明细；参数为后端分项及总额，不修改账单。 */
const CostMath = ({ counters, detail }: { counters: CounterRow[]; detail: GuardrailUsageDetail }) => { const t = useT(); return ((
  <CalcPopover title={t("How this cost is calculated")} formula={t("pages.guardrailsMonitor.counterCostFormula")}>
    <MathTable rows={counters.map((counter) => counterMathRow(counter, t))} total={formatCost(detail.cost)} />
    <p className="text-xs text-muted-foreground">{t("pages.guardrailsMonitor.unitPriceSource")}</p>
    <UnpricedNote unpriced={detail.untracked_usage_units} provider={detail.provider} />
  </CalcPopover>
)); };

/** 展示各用量计数器及合计；参数为真实计数，按当前语言展示。 */
const UnitsMath = ({ units }: { units: GuardrailUsageDetail["usage_units"] }) => { const t = useT(); return ((
  <CalcPopover title={t("How usage units add up")} formula={t("pages.guardrailsMonitor.counterUnitsFormula")}>
    <MathTable rows={unitsMathRows(units, t)} total={totalUnits(units).toLocaleString()} />
    <p className="text-xs text-muted-foreground">
      {t("Units are the billable counters the provider reported for this guardrail, added up over every call.")}
    </p>
  </CalcPopover>
)); };

const TableHeading = ({ title }: { title: string }) => { const t = useT(); return ((
  <h6 className="text-sm font-semibold text-foreground">{title}</h6>
)); };

/** 展示已选护栏的用量与费用分项；接收详情接口响应，空用量时显示空状态，不改动计费记录。 */
export function GuardrailUsageBreakdown({ detail }: { detail: GuardrailUsageDetail }) {
  const t = useT();
  const counters = counterRows(detail);
  const unpriced = unpricedSummary(detail.untracked_usage_units, t);

  return (
    <section className="space-y-4" aria-label={t("Usage and cost")}>
      <div>
        <h5 className="mb-0 text-base font-semibold text-foreground">{t("Usage & Cost")}</h5>
        <p className="mt-0.5 text-xs text-muted-foreground">
          {t("pages.guardrailsMonitor.billedUsageDescription")}
        </p>
      </div>

      {counters.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("No billable usage units were recorded in this period.")}</p>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-4 md:grid-cols-3">
            <MetricCard
              label={t("Cost")}
              value={formatCost(detail.cost)}
              valueColor={detail.cost != null ? "text-foreground" : "text-muted-foreground"}
              icon={<CircleDollarSign className="size-4" />}
              subtitle={unpriced ?? undefined}
              hint={<CostMath counters={counters} detail={detail} />}
            />
            <MetricCard
              label={t("Usage Units")}
              value={totalUnits(detail.usage_units).toLocaleString()}
              subtitle={`${counters.length} ${counters.length === 1 ? t("counter") : t("counters")}`}
              hint={<UnitsMath units={detail.usage_units} />}
            />
          </div>

          <DataTable
            columns={counterColumns}
            data={counters}
            getRowId={(row) => row.counter}
            size="compact"
            toolbar={() => <TableHeading title={t("By counter")} />}
          />

          <div className="grid gap-4 lg:grid-cols-2">
            <DataTable
              columns={teamColumns}
              data={groupRows(detail.usage_units_by_team, detail.cost_by_team, detail.untracked_usage_units_by_team)}
              getRowId={(row) => row.id || "no-team"}
              size="compact"
              toolbar={() => <TableHeading title={t("By team")} />}
            />
            <DataTable
              columns={keyColumns}
              data={groupRows(detail.usage_units_by_key, detail.cost_by_key, detail.untracked_usage_units_by_key)}
              getRowId={(row) => row.id || "no-key"}
              size="compact"
              toolbar={() => <TableHeading title={t("By key")} />}
            />
          </div>
        </>
      )}
    </section>
  );
}
