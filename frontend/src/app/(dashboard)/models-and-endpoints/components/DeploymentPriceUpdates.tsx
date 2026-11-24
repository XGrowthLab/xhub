"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "@/components/networking";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog";
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

type PriceLine = {
  rate: DeploymentRate;
  tier?: DeploymentTier;
  dimension: string;
};

type PriceComparison = { current?: PriceLine; proposed?: PriceLine };

/** 将部署快照中的平价或分档展开为明细行，供只读价格对比使用。
 * 单位由预览接口按实际计费规则返回；平价和分档各有独立比较维度，来源费率对象保持原样。
 */
function priceLines(rates: DeploymentRate[] | null, tiers?: DeploymentTier[]): PriceLine[] {
  /** 把费率与可选档位映射到展示维度；返回新行，供新旧费率配对，不改变原快照。 */
  function toLine(rate: DeploymentRate, tier?: DeploymentTier): PriceLine {
    const dimension = JSON.stringify([rate.side, rate.variant || "", rate.window, tier?.input_range, tier?.output_range]);
    return { rate, tier, dimension };
  }
  return tiers?.length
    ? tiers.flatMap((tier) => tier.rates.map((rate) => toLine(rate, tier)))
    : (rates ?? []).map((rate) => toLine(rate));
}

/** 按档位、计价项、时段与单位把选中的部署价格配对，返回对照行供列表和确认框显示。
 * 优先匹配同单位，只有同一维度双方各剩一项时才视为单位改变；全部未匹配项保留，避免对照时隐去新增或移除价。
 */
function comparePrices(row: DeploymentPrice): PriceComparison[] {
  const current = priceLines(row.current, row.current_tiers);
  const remaining = priceLines(row.proposed, row.proposed_tiers);
  const pairs = current.map((line): PriceComparison => {
    const index = remaining.findIndex(
      (candidate) => candidate.dimension === line.dimension && candidate.rate.measure === line.rate.measure,
    );
    return { current: line, proposed: index >= 0 ? remaining.splice(index, 1)[0] : undefined };
  });
  for (const pair of pairs) {
    if (pair.proposed || !pair.current) continue;
    const dimension = pair.current.dimension;
    if (pairs.filter((item) => !item.proposed && item.current?.dimension === dimension).length !== 1) continue;
    if (remaining.filter((line) => line.dimension === dimension).length !== 1) continue;
    const index = remaining.findIndex((line) => line.dimension === dimension);
    pair.proposed = remaining.splice(index, 1)[0];
  }
  return [...pairs, ...remaining.map((proposed) => ({ proposed }))];
}

/** 返回部署的并排价格表，参数是只读预览快照；由候选列表和确认对话框复用。
 * 每行展示明确的费率单位、输入和输出范围与变更状态，无候选时保留真实现价和原因，不写入后台。
 */
function DeploymentPriceComparisonTable({ row }: { row: DeploymentPrice }) {
  const t = useT();
  const comparisons = comparePrices(row);

  /** 根据展示费率的一侧渲染美元金额和计价单位；无费率时保留占位符，供比较行调用。 */
  function amount(line: PriceLine | undefined) {
    if (!line) return <span className="text-muted-foreground">—</span>;
    const { rate } = line;
    return (
      <span className="tabular-nums">
        {displayRate(rate)} / {measures.includes(rate.measure) ? t("marketSync.unit." + rate.measure) : rate.measure}
      </span>
    );
  }

  /** 把费率及可选范围转成可阅读的行标题；返回本地化标签和档位说明，不推断未知维度。 */
  function itemLabel({ rate, tier }: PriceLine) {
    const parts =
      rate.variant === "search" && rate.measure === "query"
        ? [t("marketSync.variant.search")]
        : [
            sides.includes(rate.side) ? t("marketSync.side." + rate.side) : rate.side,
            rate.variant && (variants.includes(rate.variant) ? t("marketSync.variant." + rate.variant) : rate.variant),
          ];
    if (rate.window !== "all")
      parts.push(windows.includes(rate.window) ? t("marketSync.window." + rate.window) : rate.window);
    return (
      <>
        <span>{parts.filter(Boolean).join(" · ")}</span>
        {tier && (
          <div className="mt-1 space-y-0.5 text-xs font-normal text-muted-foreground">
            <p>
              {t("marketSync.inputTier", {
                lower: tier.input_range[0].toLocaleString("en-US"),
                upper: tier.input_range[1].toLocaleString("en-US"),
              })}
            </p>
            <p>
              {t("marketSync.outputTier", {
                lower: tier.output_range[0].toLocaleString("en-US"),
                upper: tier.output_range[1].toLocaleString("en-US"),
              })}
            </p>
          </div>
        )}
      </>
    );
  }

  return (
    <div className="space-y-2">
      {comparisons.length > 0 && (
        <table className="w-full table-fixed text-left text-sm">
          <caption className="sr-only">{t("marketSync.compareRates", { name: row.name })}</caption>
          <thead className="bg-muted/40">
            <tr>
              <th scope="col" className="w-2/5 break-words p-2 font-medium">
                {t("marketSync.priceItem")}
              </th>
              <th scope="col" className="w-[30%] break-words p-2 font-medium">
                {t("marketSync.current")}
              </th>
              <th scope="col" className="w-[30%] break-words p-2 font-medium">
                {t("marketSync.proposed")}
              </th>
            </tr>
          </thead>
          <tbody>
            {comparisons.map(({ current, proposed }, index) => {
              let change: string | undefined;
              if (!row.reason) {
                if (!current) change = t("marketSync.priceAdded");
                else if (!proposed) change = t("marketSync.priceRemoved");
                else if (current.rate.measure !== proposed.rate.measure) change = t("marketSync.unitChanged");
                else if (current.rate.usd !== proposed.rate.usd) change = t("marketSync.priceChanged");
              }
              return (
                <tr key={index} className="border-t align-top">
                  <th scope="row" className="break-words p-2 font-medium">
                    {itemLabel((current ?? proposed)!)}
                    {change && (
                      <span className="mt-1 block text-xs font-medium text-amber-700 dark:text-amber-300">
                        {change}
                      </span>
                    )}
                  </th>
                  <td className="break-words p-2">{amount(current)}</td>
                  <td className="break-words p-2">{amount(proposed)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {!comparisons.length && <p className="text-muted-foreground">{t("marketSync.noPrice")}</p>}
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
        onOpenChange={(open) => {
          if (!open && !busy) setConfirm([]);
        }}
      >
        <DialogContent className="max-h-[85dvh] overflow-auto sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>{t("marketSync.confirm", { count: confirm.length })}</DialogTitle>
            <DialogDescription>{t("marketSync.confirmHelp")}</DialogDescription>
          </DialogHeader>
          <ul className="space-y-4">
            {confirm.map((row) => (
              <li key={row.id} className="rounded-lg border p-4">
                <strong>{row.name}</strong>
                {row.manual && <p className="mt-1 text-sm">{t("marketSync.manualWarning")}</p>}
                <div className="mt-3">
                  <DeploymentPriceComparisonTable row={row} />
                </div>
              </li>
            ))}
          </ul>
          {failed && <p role="alert">{message}</p>}
          <div className="flex justify-end gap-3">
            <Button variant="outline" disabled={busy} onClick={() => setConfirm([])}>
              {t("marketSync.cancel")}
            </Button>
            <Button disabled={busy} onClick={() => void apply()}>
              {t(busy ? "marketSync.applying" : "marketSync.apply")}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </section>
  );
}
