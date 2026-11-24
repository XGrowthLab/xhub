"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import {
  ArrowDownUp,
  ArrowRight,
  Search,
  SlidersHorizontal,
  Wrench,
  Braces,
  Brain,
  ImageIcon,
  Database,
  CircleHelp,
} from "lucide-react";
import { ConsolePageHeader } from "@/components/console/ConsolePrimitives";
import { ConsoleSelect } from "@/components/console/ConsoleSelect";
import { Popover, PopoverTrigger, PopoverContent, PopoverTitle, PopoverDescription } from "@/components/ui/popover";
import { ModelAvatar } from "@/components/molecules/models/ModelAvatar";
import { resolveModelBrand } from "@/components/molecules/models/modelBrand";
import { t, useT, useI18n } from "@/i18n";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cva.config";
import { uiHref } from "@/utils/uiHref";
import type { MyModelCard } from "./grantedModelCards";
import { MyModelDetail, type ModelDetailTab } from "./MyModelDetail";
import { formatModelPrice, modelPriceRange } from "./modelPriceSummary";
import { displayModelCategory } from "./modelCategory";

export type { MyModelCard };

type SortOrder = "name" | "input" | "output";

/** 返回模型类别的展示名称；参数为后台类别，返回本地化名称，未知类别原样展示；供列表分组使用。 */
function categoryLabel(category: string): string {
  const labels: Record<string, string> = {
    chat: t("Chat"),
    vision: t("priceCatalog.vision"),
    embedding: t("Embeddings"),
    image: t("Image"),
    audio: t("Audio"),
    speech: t("Speech"),
    transcription: t("Transcription"),
    rerank: t("Rerank"),
    moderation: t("Moderation"),
    other: t("Other"),
  };
  const displayed = displayModelCategory(category);
  return labels[displayed] ?? displayed;
}

/** 展示授权模型摘要及详情、调试两个入口；参数为模型、展示品牌及打开回调，返回卡片。
 * 供我的模型列表调用；五个子网格行按相邻卡片的真实内容共同伸展，使价格与操作对齐而不写死高度。
 * 只按实际绑定展示可用状态，不因目录分类推断接口；分档价只读已确认快照。 */
function ModelCard({ model, brand, onOpen }: { model: MyModelCard; brand?: string; onOpen: (tab: ModelDetailTab) => void }) {
  const { t, locale } = useI18n();
  const capabilityIcons: Record<string, typeof Wrench> = {
    tools: Wrench,
    structured: Braces,
    reasoning: Brain,
    vision: ImageIcon,
    caching: Database,
  };
  const cacheRead = modelPriceRange(model, "cache_read");
  const cacheWrite = modelPriceRange(model, "cache_write");
  const unavailable = Boolean(model.unavailable_reason) || model.endpoints?.length === 0;
  return (
    <article
      data-testid={"my-model-" + model.id}
      className="row-span-5 grid min-w-0 grid-rows-subgrid gap-y-0 rounded-xl border border-border bg-card p-5 transition-[border-color,box-shadow] duration-150 motion-reduce:transition-none hover:border-primary/40 hover:shadow-sm"
    >
      <div className="flex items-start gap-3">
        <ModelAvatar model={model.id} provider={model.provider} className="size-10 bg-background" />
        <div className="min-w-0 flex-1">
          <h2 title={model.id} className="truncate text-base font-semibold leading-6">
            {model.id}
          </h2>
          <p className="mt-0.5 truncate text-sm text-muted-foreground">
            {brand ?? t("myModels.unknownProvider")}
          </p>
        </div>
        <Badge
          className={cn(
            "shrink-0",
            unavailable
              ? "border-amber-500/20 bg-amber-500/10 text-amber-700 dark:text-amber-300"
              : "border-emerald-500/20 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
          )}
        >
          {t(unavailable ? "myModels.needsConfiguration" : "myModels.available")}
        </Badge>
      </div>
      <div className="mt-4 flex flex-wrap content-start gap-2">
        {model.price_tiers?.length ? <Badge variant="secondary">{t("consoleUi.tieredPrice")}</Badge> : null}
        <Badge variant="outline" className="font-mono text-xs">
          {categoryLabel(model.category)}
        </Badge>
        {model.capabilities.map((capability) => {
          const Icon = capabilityIcons[capability] ?? Braces;
          return (
            <span key={capability} className="inline-flex items-center gap-1 text-xs text-muted-foreground">
              <Icon className="size-3.5" aria-hidden />
              {t("priceCatalog." + capability)}
            </span>
          );
        })}
      </div>
      <div data-testid="model-limits" className="min-w-0">
        {(model.max_input_tokens || model.max_output_tokens) && (
          <div className="mt-4 flex flex-wrap gap-x-5 gap-y-1 text-xs text-muted-foreground">
            {model.max_input_tokens != null && (
              <span>
                {t("priceCatalog.maxInput")}{" "}
                <strong className="font-medium text-foreground">
                  {t("consoleUi.tokenLimit", {
                    count: new Intl.NumberFormat(locale, { notation: "compact", maximumFractionDigits: 1 }).format(
                      model.max_input_tokens,
                    ),
                  })}
                </strong>
              </span>
            )}
            {model.max_output_tokens != null && (
              <span>
                {t("priceCatalog.maxOutput")}{" "}
                <strong className="font-medium text-foreground">
                  {t("consoleUi.tokenLimit", {
                    count: new Intl.NumberFormat(locale, { notation: "compact", maximumFractionDigits: 1 }).format(
                      model.max_output_tokens,
                    ),
                  })}
                </strong>
              </span>
            )}
          </div>
        )}
      </div>
      <div data-testid="model-price-summary" className="mt-5 border-t border-border pt-4">
        <dl className="grid grid-cols-2 gap-3">
          {[
            { label: t("Input"), value: modelPriceRange(model, "input") },
            { label: t("Output"), value: modelPriceRange(model, "output") },
          ].map(({ label, value }) => (
            <div key={label} className="min-w-0">
              <dt className="text-xs text-muted-foreground">{label}</dt>
              <dd className="mt-1 flex flex-wrap items-baseline gap-1.5">
                <span
                  className={value == null ? "text-sm text-muted-foreground" : "text-xl font-semibold tabular-nums"}
                >
                  {formatModelPrice(value) || t("Price unavailable")}
                </span>
                {value != null && <span className="sr-only">{t("consoleUi.units")}</span>}
              </dd>
            </div>
          ))}
        </dl>
        {(cacheRead || cacheWrite) && (
          <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
            {cacheRead != null && (
              <span>
                {t("Cache Read")} {formatModelPrice(cacheRead)}
              </span>
            )}
            {cacheWrite != null && (
              <span>
                {t("Cache Write")} {formatModelPrice(cacheWrite)}
              </span>
            )}
          </div>
        )}
      </div>
      <div data-testid="model-actions" className="mt-5 flex items-center gap-2 border-t pt-4">
        <Button variant="outline" className="min-w-0 flex-1" onClick={() => onOpen("pricing")}>
          {t("consoleUi.modelDetails")}
        </Button>
        <Link href={uiHref("playground")} className={cn(buttonVariants({ variant: "default" }), "min-w-0 flex-1")}>
          {t("consoleUi.tryModel")} <ArrowRight aria-hidden="true" />
        </Link>
      </div>
    </article>
  );
}

/** 展示授权模型列表及接入详情；参数为模型、加载/失败状态和重试回调，返回可筛选页面。
 * 供我的模型路由调用；详情仅使用授权列表，不读取管理员部署或供应商凭据。 */
export function MyModels({
  models,
  isLoading = false,
  isError = false,
  onRetry,
}: {
  models: MyModelCard[];
  isLoading?: boolean;
  isError?: boolean;
  onRetry?: () => void;
}) {
  const t = useT();
  const [detail, setDetail] = useState<{ model: MyModelCard; tab: ModelDetailTab } | null>(null);
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState("all");
  const [provider, setProvider] = useState("all");
  const [sort, setSort] = useState<SortOrder>("name");
  const [showFilters, setShowFilters] = useState(false);
  const categories = useMemo(
    () => ["all", ...Array.from(new Set(models.map((model) => displayModelCategory(model.category)))).sort()],
    [models],
  );
  // 同一原厂名称用于卡片、搜索和筛选，兼容协议标识只作为旧数据输入。
  const brands = useMemo(
    () => new Map(models.map((model) => [model.id, resolveModelBrand({ model: model.id, provider: model.provider })?.name])),
    [models],
  );
  const providers = useMemo(
    () => Array.from(new Set(brands.values())).filter((value): value is string => Boolean(value)).sort(),
    [brands],
  );
  const activeFilters = Number(category !== "all") + Number(provider !== "all");
  const visible = useMemo(() => {
    const search = query.trim().toLowerCase();
    return models
      .filter((model) => {
        const brand = brands.get(model.id);
        if (category !== "all" && displayModelCategory(model.category) !== category) return false;
        if (provider !== "all" && (brand ?? "unknown") !== provider) return false;
        return (
          !search ||
          [model.id, brand ?? "", model.category, ...model.capabilities]
            .join(" ")
            .toLowerCase()
            .includes(search)
        );
      })
      .sort((a, b) => {
        if (sort === "input")
          return (
            (modelPriceRange(a, "input")?.min ?? Infinity) - (modelPriceRange(b, "input")?.min ?? Infinity) ||
            a.id.localeCompare(b.id)
          );
        if (sort === "output")
          return (
            (modelPriceRange(a, "output")?.min ?? Infinity) - (modelPriceRange(b, "output")?.min ?? Infinity) ||
            a.id.localeCompare(b.id)
          );
        return a.id.localeCompare(b.id);
      });
  }, [brands, category, models, provider, query, sort]);

  /** 清除搜索、类别和供应商条件；无参数和返回值，供清除按钮调用，只更新本地筛选状态。 */
  function resetFilters() {
    setQuery("");
    setCategory("all");
    setProvider("all");
  }

  return (
    <div className="min-h-full text-foreground">
      <div className="mx-auto max-w-[1600px] space-y-5">
        <ConsolePageHeader title={t("myModels.title")} description={t("consoleUi.modelsDescription")} />
        <section aria-label={t("myModels.title")}>
          <div className="flex flex-col gap-3 rounded-lg border border-border bg-card p-3 sm:flex-row">
            <div className="relative min-w-0 flex-1">
              <Search
                aria-hidden="true"
                className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
              />
              <Input
                aria-label={t("Search")}
                placeholder={t("myModels.search")}
                className="h-11 border-0 bg-transparent pl-10 shadow-none"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
            </div>
            <ConsoleSelect
              label={t("myModels.sort")}
              value={sort}
              onValueChange={(value) => setSort(value as SortOrder)}
              className="h-11 data-[size=default]:h-11 sm:w-60 sm:shrink-0"
              icon={<ArrowDownUp aria-hidden className="size-4 text-muted-foreground" />}
              options={[
                { value: "name", label: t("myModels.sortName") },
                { value: "input", label: t("myModels.sortInput") },
                { value: "output", label: t("myModels.sortOutput") },
              ]}
            />
            <Button
              type="button"
              variant="outline"
              className="h-11 px-4"
              aria-expanded={showFilters}
              aria-controls="my-model-filters"
              onClick={() => setShowFilters(!showFilters)}
            >
              <SlidersHorizontal aria-hidden="true" />
              {t("myModels.filters")}
              {activeFilters > 0 && <Badge variant="secondary">{activeFilters}</Badge>}
            </Button>
          </div>
          {showFilters && (
            <div
              id="my-model-filters"
              className="mt-3 grid items-start gap-4 rounded-lg border border-border bg-card p-4 sm:grid-cols-[minmax(0,1fr)_minmax(12rem,16rem)]"
            >
              <div>
                <p className="mb-2 text-xs font-medium text-muted-foreground">{t("Category")}</p>
                <div className="flex flex-wrap gap-2" role="group" aria-label={t("Category")}>
                  {categories.map((item) => (
                    <Button
                      key={item}
                      type="button"
                      size="sm"
                      variant={category === item ? "default" : "outline"}
                      aria-pressed={category === item}
                      onClick={() => setCategory(item)}
                    >
                      {item === "all" ? t("All") : categoryLabel(item)}
                    </Button>
                  ))}
                </div>
              </div>
              <div className="space-y-2">
                <p className="text-xs font-medium text-muted-foreground">{t("Provider")}</p>
                <ConsoleSelect
                  label={t("Provider")}
                  value={provider}
                  onValueChange={setProvider}
                  options={[
                    { value: "all", label: t("myModels.allProviders") },
                    ...providers.map((item) => ({ value: item, label: item })),
                    ...(models.some((model) => !brands.get(model.id))
                      ? [{ value: "unknown", label: t("myModels.unknownProvider") }]
                      : []),
                  ]}
                />
              </div>
            </div>
          )}
          <div className="my-5 flex flex-wrap items-center justify-between gap-3 text-sm">
            <p role="status" className="text-muted-foreground">
              {t("myModels.results", { count: visible.length, total: models.length })}
            </p>
            {(query || activeFilters > 0) && (
              <Button type="button" size="sm" variant="ghost" onClick={resetFilters}>
                {t("myModels.reset")}
              </Button>
            )}
          </div>
          <div className="mb-4 flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
            <span>{t("consoleUi.units")}</span>
            <Popover>
              <PopoverTrigger
                render={<Button variant="ghost" size="sm" className="h-8 text-xs text-muted-foreground" />}
              >
                <CircleHelp className="size-3.5" aria-hidden />
                {t("consoleUi.priceHelp")}
              </PopoverTrigger>
              <PopoverContent align="end" className="w-80 max-w-[calc(100vw-2rem)] gap-2">
                <PopoverTitle>{t("consoleUi.priceHelp")}</PopoverTitle>
                <PopoverDescription className="leading-relaxed">{t("myModels.units")}</PopoverDescription>
              </PopoverContent>
            </Popover>
          </div>
          {isError && (
            <div
              role="alert"
              className="mb-5 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/30 p-5 text-sm"
            >
              <span>{t("myModels.loadError")}</span>
              <Button variant="outline" onClick={onRetry}>
                {t("myModels.retry")}
              </Button>
            </div>
          )}
          {isLoading && (
            <div role="status" className="rounded-lg border border-border p-12 text-center text-muted-foreground">
              {t("myModels.loading")}
            </div>
          )}
          {!isLoading && visible.length > 0 && (
            <div className="space-y-6">
              {Array.from(
                visible.reduce((groups, model) => {
                  const key = displayModelCategory(model.category);
                  groups.set(key, [...(groups.get(key) ?? []), model]);
                  return groups;
                }, new Map<string, MyModelCard[]>()),
              ).map(([group, items]) => (
                <section key={group} aria-label={categoryLabel(group)}>
                  <h2 className="mb-3 text-sm font-semibold text-muted-foreground">{categoryLabel(group)}</h2>
                  <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
                    {items.map((model) => (
                      <ModelCard key={model.id} model={model} brand={brands.get(model.id)} onOpen={(tab) => setDetail({ model, tab })} />
                    ))}
                  </div>
                </section>
              ))}
            </div>
          )}
          {!isLoading && !isError && visible.length === 0 && (
            <div className="rounded-lg border border-dashed border-border p-12 text-center">
              <Search aria-hidden="true" className="mx-auto mb-4 size-7 text-muted-foreground" />
              <p className="font-medium">{t(models.length ? "myModels.noMatches" : "myModels.empty")}</p>
              <p className="mt-2 text-sm text-muted-foreground">
                {t(models.length ? "myModels.tryFilters" : "myModels.emptyHint")}
              </p>
            </div>
          )}
        </section>
      </div>
      {detail && (
        <MyModelDetail
          model={models.find((model) => model.id === detail.model.id) ?? detail.model}
          initialTab={detail.tab}
          onClose={() => setDetail(null)}
        />
      )}
    </div>
  );
}
