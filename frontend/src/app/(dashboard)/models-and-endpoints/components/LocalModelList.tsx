"use client";
import { useT } from "@/i18n";
import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import type { PriceCatalogRow } from "./priceCatalogRows";
import { marketRetired } from "./marketCatalog";

/** 本地模型档案列表；参数含完整目录及详情、上下架回调，返回可搜索表格。
 * 价格页管理标签调用；全部记录保留，只允许改变广场展示，退役、非公开和上游撤回条目不能强制上架。 */
export function LocalModelList({
  rows,
  onDetail,
  onListing,
}: {
  rows: PriceCatalogRow[];
  onDetail: (row: PriceCatalogRow) => void;
  onListing: (row: PriceCatalogRow, delisted: boolean) => void;
}) {
  const t = useT();
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("all");
  const visible = rows.filter(
    (row) =>
      [row.id, row.displayName, row.provider].join(" ").toLowerCase().includes(query.trim().toLowerCase()) &&
      (status === "all" ||
        (status === "down"
          ? !!row.raw.delisted || !!row.raw.feed_unavailable
          : !row.raw.delisted && !row.raw.feed_unavailable)),
  );
  return (
    <section aria-label={t("marketSync.local")} className="space-y-4">
      <p className="text-sm text-muted-foreground">{t("marketSync.archiveHelp")}</p>
      <div className="flex gap-3">
        <Input
          aria-label={t("marketSync.searchLocal")}
          placeholder={t("marketSync.searchPlaceholder")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <select
          className="rounded border bg-background px-3"
          aria-label={t("marketSync.localStatus")}
          value={status}
          onChange={(e) => setStatus(e.target.value)}
        >
          <option value="all">{t("marketSync.all")}</option>
          <option value="up">{t("marketSync.up")}</option>
          <option value="down">{t("marketSync.down")}</option>
        </select>
      </div>
      <p role="status">{t("marketSync.localCount", { total: rows.length, count: visible.length })}</p>
      <div className="overflow-x-auto rounded-xl border">
        <table className="w-full text-left text-sm">
          <caption className="sr-only">{t("marketSync.localCaption")}</caption>
          <thead>
            <tr className="border-b bg-muted/40">
              <th className="p-3">{t("marketSync.model")}</th>
              <th className="p-3">{t("marketSync.provider")}</th>
              <th className="p-3">{t("marketSync.state")}</th>
              <th className="p-3">{t("marketSync.action")}</th>
            </tr>
          </thead>
          <tbody>
            {visible.map((row) => {
              const unavailable =
                row.raw.private === true ||
                marketRetired(row) ||
                row.raw.feed_unavailable === true;
              let state = t("marketSync.up");
              if (row.raw.feed_unavailable) state = t("marketSync.sourceDown");
              else if (row.raw.delisted) state = t("marketSync.down");
              return (
                <tr key={row.id} className="border-b last:border-0" data-testid={"local-model-" + row.id}>
                  <td className="p-3">
                    <button className="text-left font-medium hover:underline" onClick={() => onDetail(row)}>
                      {row.displayName || row.id}
                    </button>
                    <code className="block text-xs text-muted-foreground">{row.id}</code>
                  </td>
                  <td className="p-3">{String(row.raw.issuer || row.provider || t("marketSync.custom"))}</td>
                  <td className="p-3">
                    {state}
                    {row.raw.private === true && <span> · {t("marketSync.private")}</span>}
                    {marketRetired(row) && <span> · {t("marketSync.retired")}</span>}
                    {row.raw.market_catalog !== true && <span> · {t("marketSync.custom")}</span>}
                  </td>
                  <td className="p-3">
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={row.raw.feed_unavailable === true || (!!row.raw.delisted && unavailable)}
                      onClick={() => onListing(row, !row.raw.delisted)}
                    >
                      {t(row.raw.delisted ? "marketSync.list" : "marketSync.delist")}
                    </Button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {!visible.length && <p>{t("marketSync.noMatches")}</p>}
    </section>
  );
}
