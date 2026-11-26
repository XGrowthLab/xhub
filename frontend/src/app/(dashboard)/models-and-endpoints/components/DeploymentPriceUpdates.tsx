"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { ConsoleSelect } from "@/components/console/ConsoleSelect";
import { apiClient } from "@/components/networking";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog";
import { useT } from "@/i18n";

export type DeploymentRate = {
  measure: string;
  side: string;
  variant?: string;
  window: string;
  usd: number;
  source_key: string;
};
export type DeploymentTier = {
  input_range: [number, number];
  output_range: [number, number];
  rates: DeploymentRate[];
};
export type DeploymentPrice = {
  id: string;
  name: string;
  base_model: string;
  manual: boolean;
  current: DeploymentRate[] | null;
  proposed: DeploymentRate[] | null;
  current_tiers?: DeploymentTier[];
  proposed_tiers?: DeploymentTier[];
  changed: boolean;
  reason: string;
  version: string;
};

// 已知计价维度使用翻译；未识别的历史配置保留技术标识，避免显示不存在的翻译键。
const sides = ["input", "output", "cache_read", "cache_write", "batch_input", "batch_output"];
const variants = ["uncached", "cached", "1h", "30m", "12h", "search"];
const windows = ["peak", "offpeak"];
const measures = ["token", "query", "second", "picture"];

/** 将每基础单位价格格式化为可读美元金额；输入费率，返回金额文本，不改变后端计价。
 * token 统一显示每百万，其他单位维持原值；保留小额有效位，不把缺价显示成零。 */
export function displayRate(rate: DeploymentRate): string {
  return (
    "$" +
    new Intl.NumberFormat("en-US", { maximumSignificantDigits: 8 }).format(
      rate.usd * (rate.measure === "token" ? 1_000_000 : 1),
    )
  );
}

type PriceLine = { rate: DeploymentRate; tier?: DeploymentTier };
type PriceItem = { key: string; current: PriceLine[]; proposed: PriceLine[]; measure?: string };

/** 将预览按计费项、变体和时段分组；同一项在新旧任一侧有多种单位时，各单位独立展示。
 * 参数为只读部署快照，返回对照表的逻辑行。分档仍留在各自价格列中，不把范围不同的费率误判成新增或移除。
 */
function groupPrices(row: DeploymentPrice): PriceItem[] {
  const groups = new Map<string, PriceItem>();
  const sources = [
    { side: "current", rates: row.current, tiers: row.current_tiers },
    { side: "proposed", rates: row.proposed, tiers: row.proposed_tiers },
  ] as const;
  for (const { side, rates, tiers } of sources) {
    // 两列按相同的区间顺序呈现，即使历史部署和目录的档位顺序不同，读者也能直接定位。
    const lines = tiers?.length
      ? [...tiers].sort((a, b) => a.input_range[0] - b.input_range[0] || a.output_range[0] - b.output_range[0])
          .flatMap((tier) => tier.rates.map((rate): PriceLine => ({ rate, tier })))
      : (rates ?? []).map((rate): PriceLine => ({ rate }));
    for (const line of lines) {
      const { rate } = line;
      const key = JSON.stringify([rate.side, rate.variant || "", rate.window]);
      const group = groups.get(key) ?? { key, current: [], proposed: [] };
      group[side].push(line);
      groups.set(key, group);
    }
  }
  return [...groups.values()].flatMap((item) => {
    const currentUnits = new Set(item.current.map(({ rate }) => rate.measure));
    const proposedUnits = new Set(item.proposed.map(({ rate }) => rate.measure));
    if (currentUnits.size <= 1 && proposedUnits.size <= 1) return [item];
    return [...new Set([...currentUnits, ...proposedUnits])].map((measure) => ({
      key: JSON.stringify([item.key, measure]),
      measure,
      current: item.current.filter(({ rate }) => rate.measure === measure),
      proposed: item.proposed.filter(({ rate }) => rate.measure === measure),
    }));
  });
}

/** 把旧版开放上界统一到 -1；供范围比较和显示使用，不改写服务端价目快照。 */
function normalizedUpper(value: number): number {
  return value === 99_999_999 ? -1 : value;
}

/** 取得计费项的状态文案键。任一侧完全没有该项才记作新增或移除；只有范围可对照时才判定金额变化。
 * 参数为一行分组价格，结果供表格展示，忽略来源字段名和档位排列顺序。
 */
function priceChanges({ current, proposed }: PriceItem): string[] {
  if (!current.length) return ["priceAdded"];
  if (!proposed.length) return ["priceRemoved"];

  /** 获取各档的实际范围。统一价没有区间；开放上界两种表示方式等价。 */
  const scope = ({ tier }: PriceLine) => tier
    ? [tier.input_range[0], normalizedUpper(tier.input_range[1]), tier.output_range[0], normalizedUpper(tier.output_range[1])]
    : null;
  const currentScopes = new Set(current.map((line) => JSON.stringify(scope(line))));
  const nextScopes = new Set(proposed.map((line) => JSON.stringify(scope(line))));
  const changes: string[] = [];
  if (currentScopes.size !== nextScopes.size || [...currentScopes].some((value) => !nextScopes.has(value))) {
    if (!current[0].tier) changes.push("nowTiered");
    else if (!proposed[0].tier) changes.push("nowFlat");
    else changes.push("tierChanged");
  }
  const beforeUnits = new Set(current.map(({ rate }) => rate.measure));
  const afterUnits = new Set(proposed.map(({ rate }) => rate.measure));
  if (beforeUnits.size !== afterUnits.size || [...beforeUnits].some((unit) => !afterUnits.has(unit))) {
    changes.push("unitChanged");
  }
  if (!changes.length) {
    /** 组成档位和金额的无序对照快照，保持相同金额的重复条目，不使用显示精度判断涨跌。 */
    const amounts = (lines: PriceLine[]) => JSON.stringify(lines.map((line) =>
      JSON.stringify([scope(line), line.rate.measure, line.rate.usd]),
    ).sort());
    if (amounts(current) !== amounts(proposed)) changes.push("priceChanged");
  }
  return changes;
}

/** 以计费项为行、现价和市场价为列显示预览。两侧独立说明档位；不合并不同输入或输出区间，也不参与计费写入。 */
function DeploymentPriceComparisonTable({ row }: { row: DeploymentPrice }) {
  const t = useT();
  const items = groupPrices(row);

  /** 翻译标准单位，未知单位保留后台标识；用于费率和不同单位的独立行标签。 */
  const unitLabel = (measure: string) => measures.includes(measure) ? t("marketSync.unit." + measure) : measure;

  /** 生成输入或输出区间的可读说明，开放范围不显示占位上限。 */
  function rangeLabel(side: "input" | "output", [lower, ceiling]: [number, number]) {
    const values = { lower: lower.toLocaleString("en-US"), upper: ceiling.toLocaleString("en-US") };
    const suffix = normalizedUpper(ceiling) === -1 ? "TierOpen" : "Tier";
    return t("marketSync." + side + suffix, values);
  }

  /** 绘制价格列，统一费率在必要时加标识；各档金额与其计费区间绑定，缺价用占位符。 */
  function prices(lines: PriceLine[], showFlat: boolean) {
    if (!lines.length) return <span className="text-muted-foreground">—</span>;
    return (
      <ul className="divide-y divide-border/60">
        {lines.map(({ rate, tier }, index) => (
          <li key={index} className="py-2 first:pt-0 last:pb-0">
            <div className="tabular-nums">
              <span className="font-medium">{displayRate(rate)}</span>{" "}
              <span className="text-xs text-muted-foreground">/ {unitLabel(rate.measure)}</span>
            </div>
            {tier && (
              <div className="mt-1 space-y-0.5 text-xs leading-5 text-muted-foreground">
                <p>{rangeLabel("input", tier.input_range)}</p>
                <p>{rangeLabel("output", tier.output_range)}</p>
              </div>
            )}
            {!tier && showFlat && <span className="mt-1 block text-xs text-muted-foreground">{t("marketSync.flatRate")}</span>}
          </li>
        ))}
      </ul>
    );
  }

  /** 返回计费项名称与独立的时段说明；搜索按次保持原有命名，历史单位和变体可见。 */
  function itemLabel(rate: DeploymentRate, measure?: string) {
    const parts = rate.variant === "search" && rate.measure === "query"
      ? [t("marketSync.variant.search")]
      : [
          sides.includes(rate.side) ? t("marketSync.side." + rate.side) : rate.side,
          rate.variant && (variants.includes(rate.variant) ? t("marketSync.variant." + rate.variant) : rate.variant),
        ];
    return (
      <>
        <span>{parts.filter(Boolean).join(" · ")}</span>
        {measure && <p className="mt-1 text-xs font-normal text-muted-foreground">{unitLabel(measure)}</p>}
        {rate.window !== "all" && (
          <p className="mt-1 text-xs font-normal text-muted-foreground">
            {windows.includes(rate.window) ? t("marketSync.window." + rate.window) : rate.window}
          </p>
        )}
      </>
    );
  }

  return (
    <div className="space-y-2">
      {items.length > 0 && (
        <div className="overflow-hidden rounded-lg border">
          <table className="w-full table-fixed text-left text-sm">
            <caption className="sr-only">{t("marketSync.compareRates", { name: row.name })}</caption>
            <thead className="bg-muted/55">
              <tr className="divide-x divide-border">
                <th scope="col" className="w-[28%] wrap-anywhere p-2 font-semibold sm:w-1/4 sm:px-3">{t("marketSync.priceItem")}</th>
                <th scope="col" className="w-[36%] wrap-anywhere p-2 font-semibold sm:w-[37.5%] sm:px-3">{t("marketSync.current")}</th>
                <th scope="col" className="wrap-anywhere p-2 font-semibold sm:px-3">{t("marketSync.proposed")}</th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => {
                const { key, current, proposed, measure } = item;
                const changes = row.reason ? [] : priceChanges(item);
                const hasTiers = Boolean(current[0]?.tier || proposed[0]?.tier);
                return (
                  <tr key={key} className="divide-x divide-border border-t align-top even:bg-muted/20">
                    <th scope="row" className="wrap-anywhere p-2 font-medium sm:px-3">
                      {itemLabel((current[0] ?? proposed[0]).rate, measure)}
                      {changes.length > 0 && (
                        <ul className="mt-1 space-y-0.5 text-xs text-amber-700 dark:text-amber-300">
                          {changes.map((change) => <li key={change}>{t("marketSync." + change)}</li>)}
                        </ul>
                      )}
                    </th>
                    <td className="wrap-anywhere p-2 sm:px-3">{prices(current, hasTiers)}</td>
                    <td className="wrap-anywhere p-2 sm:px-3">{prices(proposed, hasTiers)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {!items.length && <p className="text-muted-foreground">{t("marketSync.noPrice")}</p>}
      {row.reason && <p className="text-muted-foreground">{t("marketSync.reason." + row.reason)}</p>}
    </div>
  );
}

/** 展示部署现价与市场候选并提供单个/批量确认；参数为管理会话，返回完整操作区。
 * 所有价格与权限由后台重查；默认不勾选任何部署，失败保持预览并提示刷新，无自动调价。 */
export function DeploymentPriceUpdates({ accessToken }: { accessToken: string | null }) {
  const t = useT();
  const cache = useQueryClient();
  const [selected, setSelected] = useState<string[]>([]);
  const [confirm, setConfirm] = useState<DeploymentPrice[]>([]);
  const [reviewIndex, setReviewIndex] = useState(0);
  const [busy, setBusy] = useState(false);
  const [updatedCount, setUpdatedCount] = useState<number | null>(null);
  const [failed, setFailed] = useState(false);
  let message = "";
  if (failed) message = t("marketSync.applyFailed");
  else if (updatedCount !== null) message = t("marketSync.updated", { count: updatedCount });

  /** 打开选定部署的确认窗口；参数为当前预览行，无返回值，仅重置提示并冻结版本，不写后台。 */
  function review(items: DeploymentPrice[]) {
    setUpdatedCount(null);
    setFailed(false);
    setReviewIndex(0);
    setConfirm(items);
  }
  const query = useQuery<{ deployments: DeploymentPrice[] }>({
    queryKey: ["deploymentPrices", accessToken],
    enabled: !!accessToken,
    queryFn: () => apiClient.get("/price/deployments", { accessToken: accessToken! }),
  });
  const rows = query.data?.deployments ?? [];
  const candidates = rows.filter((row) => row.changed && !row.reason);
  const chosen = candidates.filter((row) => selected.includes(row.id));
  const reviewed = confirm[reviewIndex];

  /** 提交确认窗口中冻结的版本；无参数，返回异步结果。
   * 冲突或网络失败不会报告成功；成功刷新部署和价格查询，关闭窗口并清空选择。 */
  async function apply() {
    setBusy(true);
    setUpdatedCount(null);
    setFailed(false);
    try {
      const result = await apiClient.post<{ updated: number }>("/price/deployments/apply", {
        body: { items: confirm.map((row) => ({ id: row.id, version: row.version })) },
        accessToken: accessToken!,
      });
      setConfirm([]);
      setSelected([]);
      setUpdatedCount(result.updated);
      await cache.invalidateQueries({ queryKey: ["deploymentPrices"] });
      await cache.invalidateQueries({ queryKey: ["models"] });
    } catch {
      setFailed(true);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section aria-label={t("marketSync.prices")} className="space-y-4 rounded-xl border bg-card p-5 sm:p-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold">{t("marketSync.prices")}</h2>
          <p className="mt-2 max-w-3xl text-sm leading-6 text-muted-foreground">{t("marketSync.priceHelp")}</p>
        </div>
        <Button
          variant="outline"
          onClick={() => {
            setSelected([]);
            void query.refetch();
          }}
        >
          {t("marketSync.refresh")}
        </Button>
      </div>
      {query.isPending && <p role="status">{t("marketSync.loading")}</p>}
      {query.isError && <p role="alert">{t("marketSync.loadFailed")}</p>}
      {message && <p role={failed ? "alert" : "status"}>{message}</p>}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={candidates.length > 0 && chosen.length === candidates.length}
            disabled={!candidates.length}
            onChange={(e) => setSelected(e.target.checked ? candidates.map((row) => row.id) : [])}
          />
          {t("marketSync.selectAll")}
        </label>
        <Button disabled={!chosen.length || query.isError} onClick={() => review(chosen)}>
          {t("marketSync.reviewSelected", { count: chosen.length })}
        </Button>
      </div>
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full min-w-[680px] text-left text-sm">
          <caption className="sr-only">{t("marketSync.prices")}</caption>
          <thead className="bg-muted/40">
            <tr>
              {["deployment", "comparison", "action"].map((key) => (
                <th scope="col" className={key === "comparison" ? "w-[65%] p-4" : "p-4"} key={key}>
                  {t("marketSync." + key)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.id} className="border-t align-top">
                <td className="p-4">
                  <label className="flex items-start gap-2">
                    <input
                      type="checkbox"
                      aria-label={t("marketSync.select", { name: row.name })}
                      checked={chosen.some((item) => item.id === row.id)}
                      disabled={!row.changed || !!row.reason}
                      onChange={(e) =>
                        setSelected(e.target.checked ? [...selected, row.id] : selected.filter((id) => id !== row.id))
                      }
                    />
                    <span className="min-w-0">
                      <span className="block font-medium">{row.name}</span>
                      <code className="mt-1 block break-all text-xs text-muted-foreground">{row.id}</code>
                      <span className="mt-1 block text-xs text-muted-foreground">
                        {row.base_model || t("marketSync.reason.unmapped")}
                      </span>
                      {row.manual && (
                        <span className="mt-2 inline-block rounded bg-amber-500/10 px-2 py-1 text-xs">
                          {t("marketSync.manual")}
                        </span>
                      )}
                    </span>
                  </label>
                </td>
                <td className="p-4">
                  <DeploymentPriceComparisonTable row={row} />
                </td>
                <td className="p-4">
                  {row.changed && !row.reason && (
                    <Button size="sm" variant="outline" onClick={() => review([row])}>
                      {t("marketSync.reviewOne")}
                    </Button>
                  )}
                  {!row.changed && !row.reason && t("marketSync.unchanged")}
                  {!!row.reason && "—"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {!query.isPending && !query.isError && !rows.length && <p>{t("marketSync.emptyDeployments")}</p>}
      <Dialog
        open={confirm.length > 0}
        disablePointerDismissal={busy}
        onOpenChange={(open) => {
          if (!open && !busy) setConfirm([]);
        }}
      >
        <DialogContent className="flex max-h-[90dvh] min-w-0 flex-col gap-0 overflow-hidden p-0 sm:max-w-4xl" showCloseButton={!busy}>
          <DialogHeader className="shrink-0 border-b py-4 pl-4 pr-12 sm:pl-6">
            <DialogTitle className="text-lg font-semibold">{t("marketSync.confirm", { count: confirm.length })}</DialogTitle>
            <DialogDescription className="max-w-3xl">{t("marketSync.confirmHelp")}</DialogDescription>
          </DialogHeader>
          {reviewed && confirm.length > 1 && (
            <div className="flex shrink-0 flex-wrap items-center gap-2 border-b bg-muted/25 px-4 py-2 sm:px-6">
              <div className="min-w-0 flex-[1_1_12rem] sm:max-w-sm">
                <ConsoleSelect
                  label={t("marketSync.reviewDeployment")}
                  value={reviewed.id}
                  options={confirm.map((row, index) => ({
                    value: row.id,
                    label: t("marketSync.reviewOption", { index: index + 1, name: row.name }),
                  }))}
                  onValueChange={(id) => setReviewIndex(confirm.findIndex((row) => row.id === id))}
                  disabled={busy}
                />
              </div>
              <div className="flex items-center gap-2">
                <Button size="icon-sm" variant="outline" aria-label={t("marketSync.previousDeployment")}
                  disabled={busy || reviewIndex === 0} onClick={() => setReviewIndex(reviewIndex - 1)}>
                  <ChevronLeft aria-hidden="true" />
                </Button>
                <span role="status" className="text-xs tabular-nums text-muted-foreground">
                  {t("marketSync.reviewPosition", { index: reviewIndex + 1, total: confirm.length })}
                </span>
                <Button size="icon-sm" variant="outline" aria-label={t("marketSync.nextDeployment")}
                  disabled={busy || reviewIndex === confirm.length - 1} onClick={() => setReviewIndex(reviewIndex + 1)}>
                  <ChevronRight aria-hidden="true" />
                </Button>
              </div>
            </div>
          )}
          {reviewed && (
            // 按部署重挂滚动区，切换时从表头开始；只替换预览，提交仍使用完整的冻结批次。
            <div key={reviewed.id} className="min-h-0 min-w-0 flex-1 space-y-3 overflow-y-auto overscroll-contain px-4 py-4 sm:px-6">
              <div>
                <h3 className="wrap-anywhere font-semibold">{reviewed.name}</h3>
                <code className="block wrap-anywhere text-xs text-muted-foreground">{reviewed.id}</code>
              </div>
              {reviewed.manual && <p className="rounded-md bg-amber-500/10 px-3 py-2 text-xs text-amber-800 dark:text-amber-300">
                {t("marketSync.manualWarning")}
              </p>}
              <DeploymentPriceComparisonTable row={reviewed} />
            </div>
          )}
          <div className="shrink-0 border-t px-4 py-3 sm:px-6">
            {failed && <p role="alert" className="mb-3 text-sm text-destructive">{message}</p>}
            <DialogFooter className="flex-row flex-wrap justify-end">
              <Button variant="outline" disabled={busy} onClick={() => setConfirm([])}>
                {t("marketSync.cancel")}
              </Button>
              <Button disabled={busy} onClick={() => void apply()}>
                {t(busy ? "marketSync.applying" : "marketSync.apply")}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>
    </section>
  );
}
