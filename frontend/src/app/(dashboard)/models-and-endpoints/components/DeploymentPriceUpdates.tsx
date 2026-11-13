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
export type DeploymentPrice = {
  id: string;
  name: string;
  base_model: string;
  manual: boolean;
  current: DeploymentRate[] | null;
  proposed: DeploymentRate[] | null;
  changed: boolean;
  reason: string;
  version: string;
};

// 已知计价维度使用翻译；未识别的历史配置保留技术标识，避免显示不存在的翻译键。
const sides = ["input", "output", "cache_read", "cache_write", "batch_input", "batch_output"];
const variants = ["uncached", "cached", "1h", "search"];
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

  /** 按真实费率维度显示可比较的明细，参数为空时明确缺价；返回 JSX，无写操作。 */
  function rates(items: DeploymentRate[] | null) {
    if (!items?.length) return <span className="text-muted-foreground">{t("marketSync.noPrice")}</span>;
    return (
      <ul className="space-y-1.5">
        {items.map((rate, index) => (
          <li key={index} className="flex flex-wrap justify-between gap-x-4 gap-y-1">
            <span>
              {sides.includes(rate.side) ? t("marketSync.side." + rate.side) : rate.side}
              {rate.variant
                ? ` · ${variants.includes(rate.variant) ? t("marketSync.variant." + rate.variant) : rate.variant}`
                : ""}
              {rate.window !== "all"
                ? ` · ${windows.includes(rate.window) ? t("marketSync.window." + rate.window) : rate.window}`
                : ""}
            </span>
            <span className="tabular-nums">
              {displayRate(rate)} /{" "}
              {measures.includes(rate.measure) ? t("marketSync.unit." + rate.measure) : rate.measure}
            </span>
          </li>
        ))}
      </ul>
    );
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
              {["deployment", "current", "proposed", "action"].map((key) => (
                <th className="p-4" key={key}>
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
                <td className="p-4">{rates(row.current)}</td>
                <td className="p-4">{row.reason ? t("marketSync.reason." + row.reason) : rates(row.proposed)}</td>
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
        <DialogContent className="max-h-[85dvh] overflow-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t("marketSync.confirm", { count: confirm.length })}</DialogTitle>
            <DialogDescription>{t("marketSync.confirmHelp")}</DialogDescription>
          </DialogHeader>
          <ul className="space-y-4">
            {confirm.map((row) => (
              <li key={row.id} className="rounded-lg border p-4">
                <strong>{row.name}</strong>
                {row.manual && <p className="mt-1 text-sm">{t("marketSync.manualWarning")}</p>}
                <div className="mt-3 grid gap-4 sm:grid-cols-2">
                  <div>
                    <h3 className="mb-2 text-sm font-medium">{t("marketSync.current")}</h3>
                    {rates(row.current)}
                  </div>
                  <div>
                    <h3 className="mb-2 text-sm font-medium">{t("marketSync.proposed")}</h3>
                    {rates(row.proposed)}
                  </div>
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
