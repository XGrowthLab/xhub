"use client";
import { useState } from "react";
import Link from "next/link";
import { Boxes, Filter, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useT } from "@/i18n";
import type { PriceCatalogRow } from "./priceCatalogRows";
import {
  CAPABILITIES,
  MODALITY_NAMES,
  marketCapability,
  marketModalities,
  marketMoney,
  marketPrices,
  marketFeatures,
  marketSale,
  marketRetired,
} from "./marketCatalog";
import type { PriceCatalogProps } from "./PriceCatalog";

/** 市场模型卡片供目录网格使用；参数含价格行与管理回调，返回简洁卡片；只展示首档两个计费项，完整规则在详情打开，无后台写入。 */
function MarketCard({ row, onDetail, onEdit, onDelete, onReset }: PriceCatalogProps & { row: PriceCatalogRow }) {
  const t = useT();
  // 首屏优先展示输入和输出价格；缓存和分辨率变体仍完整保留在详情，避免按接口字段顺序误导用户。
  const prices = marketPrices(row)
    .filter((price) => price.tier === 0)
    .sort((a, b) => {
      const priority = (key: string) => ({ ncache: 0, input: 0, output: 1 })[key] ?? 2;
      return priority(a.key) - priority(b.key);
    });
  return (
    <article
      data-testid={"price-row-" + row.id}
      className="flex h-full min-w-0 flex-col rounded-xl border border-border bg-card p-5 transition-colors hover:border-cyan-500/60"
    >
      <button
        className="flex items-start gap-3 text-left"
        onClick={() => onDetail?.(row)}
        aria-label={t("marketSync.detailLabel", { name: row.displayName || row.id })}
      >
        <span className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-cyan-500/10 text-cyan-600">
          <Boxes className="size-5" />
        </span>
        <span className="min-w-0">
          <h3 className="text-lg font-semibold">{row.displayName || row.id}</h3>
          <span className="text-xs text-muted-foreground">
            {String(row.raw.issuer || row.provider || t("marketSync.custom"))}
          </span>
        </span>
      </button>
      <p className="mt-3 break-all text-xs text-muted-foreground">{row.id}</p>
      <p className="mt-3 line-clamp-2 min-h-10 text-xs leading-5 text-muted-foreground">
        {String(row.raw.description || "")}
      </p>
      <div className="my-3 flex flex-wrap gap-1.5 text-xs">
        {marketFeatures(row)
          .slice(0, 3)
          .map((tag) => (
            <span key={tag} className="rounded border px-2 py-1">
              {tag}
            </span>
          ))}
        {row.raw.feed_unavailable === true && (
          <span className="rounded bg-muted px-2 py-1">{t("marketSync.sourceDown")}</span>
        )}
        {row.overridden && <span className="rounded border px-2 py-1">{t("marketSync.edited")}</span>}
        {row.raw.private === true && <span>{t("marketSync.private")}</span>}
        {marketRetired(row) && <span>{t("marketSync.retired")}</span>}
      </div>
      <div className="mb-4 flex flex-wrap gap-3 text-xs text-muted-foreground">
        {row.maxInput && <span>{t("marketSync.contextValue", { value: row.maxInput.toLocaleString() })}</span>}
        {row.maxOutput && <span>{t("marketSync.outputValue", { value: row.maxOutput.toLocaleString() })}</span>}
      </div>
      <dl className="mt-auto space-y-2 border-t pt-4">
        {prices.slice(0, 2).map((price) => (
          <div className="flex justify-between gap-2 text-xs" key={price.key}>
            <dt>{price.label}</dt>
            <dd className="shrink-0 font-semibold text-cyan-700 dark:text-cyan-300">
              {marketMoney(price.usd)} <span className="font-normal text-muted-foreground">/ {price.unit}</span>
            </dd>
          </div>
        ))}
        {!prices.length && <p className="text-sm text-muted-foreground">{t("marketSync.noPrice")}</p>}
      </dl>
      <button
        className="mt-3 text-left text-xs text-muted-foreground hover:text-foreground"
        onClick={() => onDetail?.(row)}
      >
        {t("marketSync.fullPrices")}
        {prices.length > 2 ? t("marketSync.extraItems", { count: prices.length - 2 }) : ""}
      </button>
      <div className="mt-4 space-y-1 border-t pt-3 text-xs text-muted-foreground">
        <p>
          {t("marketSync.side.input")}:
          {marketModalities(row, "input")
            .map((m) => (MODALITY_NAMES[m] ? t("marketSync.modality." + m) : m))
            .join("、") || t("marketSync.notDeclared")}
        </p>
        <p>
          {t("marketSync.side.output")}:
          {marketModalities(row, "output")
            .map((m) => (MODALITY_NAMES[m] ? t("marketSync.modality." + m) : m))
            .join("、") || t("marketSync.notDeclared")}
        </p>
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-3 text-xs">
        <Link
          className="text-cyan-700 dark:text-cyan-300"
          href={"/models-and-endpoints?catalog=" + encodeURIComponent(row.id)}
        >
          {t("priceCatalog.deployModel")}
        </Link>
        {onEdit && <button onClick={() => onEdit(row)}>{t("Edit")}</button>}
        {onDelete && <button onClick={() => onDelete(row)}>{t("marketSync.delist")}</button>}
        {onReset && row.overridden && <button onClick={() => onReset(row)}>{t("priceData.resetModel")}</button>}
      </div>
    </article>
  );
}

/** Modelink 风格价格目录；参数为完整价格行、加载状态和管理回调，返回侧栏与模型网格。
 * 只展示在售公开市场模型，排除非公开、退役、删除与内部补充条目；计数来自实际数据，无持久化副作用。 */
export function MarketPriceCatalog(props: PriceCatalogProps) {
  const t = useT();
  const rows = props.rows ?? [];
  const [showDown, setShowDown] = useState(false);
  const [query, setQuery] = useState("");
  const [providerSearch, setProviderSearch] = useState("");
  const [provider, setProvider] = useState("");
  const [input, setInput] = useState("");
  const [output, setOutput] = useState("");
  const [capability, setCapability] = useState("");
  const [feature, setFeature] = useState("");
  const [context, setContext] = useState(0);
  const [sort, setSort] = useState("rank");
  const [limit, setLimit] = useState(60);
  const market = rows.filter((row) => row.raw.market_catalog === true);
  const active = market.filter(marketSale);
  const pool = showDown
    ? market.filter(
        (row) =>
          row.raw.private !== true &&
          (row.raw.feed_unavailable === true || row.raw.delisted === true || marketRetired(row)),
      )
    : active;
  const suppliers = [
    ...new Set(pool.map((row) => String(row.raw.issuer || row.provider || t("marketSync.custom")))),
  ].sort();
  const features = [...new Set(pool.flatMap(marketFeatures))];
  const visible = pool
    .filter(
      (row) =>
        (!provider || String(row.raw.issuer || row.provider || t("marketSync.custom")) === provider) &&
        (!input || marketModalities(row, "input").includes(input)) &&
        (!output || marketModalities(row, "output").includes(output)) &&
        (!capability || marketCapability(row, capability)) &&
        (!feature || marketFeatures(row).includes(feature)) &&
        (!context || (row.maxInput ?? 0) >= context) &&
        [row.id, row.displayName, row.raw.description].join(" ").toLowerCase().includes(query.trim().toLowerCase()),
    )
    // 排序仅改变展示顺序；价格缺失排末尾，不当作免费。
    .sort((a, b) => {
      if (sort === "rank") return Number(b.raw.rank || 0) - Number(a.raw.rank || 0) || a.id.localeCompare(b.id);
      if (sort === "name") return a.id.localeCompare(b.id);
      return (a[sort as "input" | "output"] ?? Infinity) - (b[sort as "input" | "output"] ?? Infinity);
    });
  /** 清空本地筛选；参数无、返回无，供清空按钮调用，不修改目录和后台数据。 */
  function reset() {
    setQuery("");
    setShowDown(false);
    setProvider("");
    setInput("");
    setOutput("");
    setCapability("");
    setFeature("");
    setContext(0);
    setLimit(60);
  }
  return (
    <section aria-label={t("Price catalog")} className="space-y-5">
      <div className="flex flex-wrap items-center gap-3 rounded-xl border bg-card p-3">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={showDown}
            onChange={(e) => {
              setShowDown(e.target.checked);
              setLimit(60);
            }}
          />
          {t("marketSync.down")}
        </label>
        <Search className="size-4 text-muted-foreground" />
        <Input
          aria-label={t("Search")}
          className="min-w-48 flex-1"
          placeholder={t("marketSync.searchModels")}
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setLimit(60);
          }}
        />
        <select
          aria-label={t("priceCatalog.sort")}
          value={sort}
          onChange={(e) => setSort(e.target.value)}
          className="rounded border bg-background p-2 text-sm"
        >
          <option value="rank">{t("marketSync.rank")}</option>
          <option value="name">{t("marketSync.name")}</option>
          <option value="input">{t("marketSync.inputPrice")}</option>
          <option value="output">{t("marketSync.outputPrice")}</option>
        </select>
        {props.onAdd && (
          <Button data-testid="price-add-model" onClick={props.onAdd}>
            {t("priceData.addModel")}
          </Button>
        )}
      </div>
      <div className="grid items-start gap-6 lg:grid-cols-[260px_minmax(0,1fr)]">
        <aside aria-label={t("marketSync.filterModels")} className="space-y-6 rounded-xl border bg-card p-5">
          <h2 className="flex items-center gap-2 font-semibold">
            <Filter className="size-4" />
            {t("marketSync.filterModels")}
          </h2>

          {(["input", "output"] as const).map((side) => (
            <fieldset key={side} className="border-t pt-5">
              <legend className="text-sm text-muted-foreground">
                {t(side === "input" ? "marketSync.inputModalities" : "marketSync.outputModalities")}
              </legend>
              <div className="mt-3 flex flex-wrap gap-2">
                {Object.keys(MODALITY_NAMES).map((value) => (
                  <button
                    key={value}
                    aria-pressed={(side === "input" ? input : output) === value}
                    className="rounded-lg border px-2.5 py-2 text-xs aria-pressed:border-cyan-500 aria-pressed:bg-cyan-500/10"
                    onClick={() =>
                      side === "input"
                        ? setInput(input === value ? "" : value)
                        : setOutput(output === value ? "" : value)
                    }
                  >
                    {t("marketSync.modality." + value)}{" "}
                    <span className="text-muted-foreground">
                      {pool.filter((row) => marketModalities(row, side).includes(value)).length}
                    </span>
                  </button>
                ))}
              </div>
            </fieldset>
          ))}
          <label className="block space-y-3 border-t pt-5 text-sm text-muted-foreground">
            {t("marketSync.context")}
            <select
              aria-label={t("marketSync.context")}
              className="w-full rounded border bg-background p-2"
              value={context}
              onChange={(e) => setContext(Number(e.target.value))}
            >
              {[0, 32000, 128000, 256000, 1000000].map((n) => (
                <option value={n} key={n}>
                  {n ? n.toLocaleString() + "+" : t("marketSync.unlimited")}
                </option>
              ))}
            </select>
          </label>
          <fieldset className="border-t pt-5">
            <legend className="text-sm text-muted-foreground">{t("marketSync.capabilities")}</legend>
            <div className="mt-3 space-y-2">
              {Object.keys(CAPABILITIES).map((key) => (
                <button
                  key={key}
                  aria-pressed={capability === key}
                  className="flex w-full justify-between rounded-lg border p-2.5 text-sm aria-pressed:border-cyan-500"
                  onClick={() => setCapability(capability === key ? "" : key)}
                >
                  {t("marketSync.capability." + key)}
                  <span className="text-muted-foreground">
                    {pool.filter((row) => marketCapability(row, key)).length}
                  </span>
                </button>
              ))}
            </div>
          </fieldset>
          <div className="space-y-3 border-t pt-5">
            <h3 className="text-sm text-muted-foreground">{t("marketSync.provider")}</h3>
            <Input
              aria-label={t("marketSync.searchSuppliers")}
              placeholder={t("marketSync.searchSuppliers")}
              value={providerSearch}
              onChange={(e) => setProviderSearch(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              {t("marketSync.suppliersCount", {
                total: suppliers.length,
                count: suppliers.filter((p) => p.toLowerCase().includes(providerSearch.toLowerCase())).length,
              })}
            </p>
            <div className="max-h-64 space-y-2 overflow-y-auto">
              {suppliers
                .filter((p) => p.toLowerCase().includes(providerSearch.toLowerCase()))
                .map((p) => (
                  <button
                    key={p}
                    aria-pressed={provider === p}
                    className="flex w-full justify-between rounded-lg border p-2.5 text-sm aria-pressed:border-cyan-500"
                    onClick={() => setProvider(provider === p ? "" : p)}
                  >
                    {p}
                    <span className="text-muted-foreground">
                      {
                        pool.filter((row) => String(row.raw.issuer || row.provider || t("marketSync.custom")) === p)
                          .length
                      }
                    </span>
                  </button>
                ))}
            </div>
          </div>
          <fieldset className="border-t pt-5">
            <legend className="text-sm text-muted-foreground">{t("marketSync.features")}</legend>
            <div className="mt-3 flex flex-wrap gap-2">
              {features.map((tag) => (
                <button
                  key={tag}
                  aria-pressed={feature === tag}
                  className="rounded-lg border p-2 text-xs aria-pressed:border-cyan-500"
                  onClick={() => setFeature(feature === tag ? "" : tag)}
                >
                  {tag}{" "}
                  <span className="text-muted-foreground">
                    {pool.filter((row) => marketFeatures(row).includes(tag)).length}
                  </span>
                </button>
              ))}
            </div>
          </fieldset>
          <Button variant="ghost" onClick={reset}>
            {t("priceCatalog.reset")}
          </Button>
        </aside>
        <div className="min-w-0">
          <p role="status" className="mb-5 text-sm text-muted-foreground">
            {t("priceCatalog.results", { count: visible.length, total: pool.length })}
          </p>
          {props.isLoading && <p role="status">{t("priceCatalog.loading")}</p>}
          {props.isError && (
            <div role="alert">
              {t("priceCatalog.loadError")}
              <Button onClick={props.onRetry}>{t("priceCatalog.retry")}</Button>
            </div>
          )}
          <div className="grid items-stretch gap-4 md:grid-cols-2">
            {visible.slice(0, limit).map((row) => (
              <MarketCard {...props} key={row.id} row={row} />
            ))}
          </div>
          {!visible.length && !props.isLoading && !props.isError && (
            <p className="rounded-xl border border-dashed p-12 text-center">{t("priceCatalog.noMatches")}</p>
          )}
          {visible.length > limit && (
            <Button className="mt-6" variant="outline" onClick={() => setLimit(limit + 60)}>
              {t("priceCatalog.loadMore", { count: visible.length - limit })}
            </Button>
          )}
        </div>
      </div>
    </section>
  );
}
