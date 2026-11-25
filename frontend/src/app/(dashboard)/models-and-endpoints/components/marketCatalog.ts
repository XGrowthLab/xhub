import { t as translate, type TFunction } from "@/i18n";
import { resolveModelBrand } from "@/components/molecules/models/modelBrand";
import type { PriceCatalogRow } from "./priceCatalogRows";

export const MODALITY_NAMES: Record<string, string> = {
  text: "文本",
  image: "图片",
  audio: "音频",
  video: "视频",
  file: "文件",
};
export const CAPABILITIES: Record<string, string> = {
  function_calling: "函数调用",
  schema_output: "结构化输出",
  reasoning: "推理能力",
  content_cache: "内容缓存",
};

/** 返回广场卡片、详情与筛选共用的原厂名称；目录未提供原厂且型号无法识别时使用本地化默认文本。 */
export function marketBrandLabel(row: PriceCatalogRow, t: TFunction): string {
  return resolveModelBrand({ model: row.id, issuer: row.raw.issuer, provider: row.provider })?.name ?? t("marketSync.custom");
}

/** 读取市场字符串数组；参数为未知字段，返回有效字符串，供筛选和详情使用；缺失或非法字段返回空数组，无副作用。 */
export function marketStrings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string" && !!item) : [];
}
/** 合并市场能力特性和运营标签；参数为价格行，返回去重后的标签，供侧栏筛选与卡片展示；缺失字段不产生猜测标签。 */
export function marketFeatures(row: PriceCatalogRow): string[] {
  return [...new Set([...marketStrings(row.raw.features), ...marketStrings(row.raw.hot_tags)])];
}
/** 读取明确声明的输入或输出模态；参数为目录行及方向，返回完整模态集合，不根据模型名猜测能力。 */
export function marketModalities(row: PriceCatalogRow, side: "input" | "output"): string[] {
  return marketStrings(row.raw[side + "_modalities"]);
}
/** 判断市场是否明确声明能力；参数为目录行和能力键，返回布尔值，缺失能力视为未声明，供左侧筛选使用。 */
export function marketCapability(row: PriceCatalogRow, key: string): boolean {
  const architecture = row.raw.architecture as Record<string, { supported?: boolean }> | undefined;
  return architecture?.[key]?.supported === true;
}
/** 按退役日期确定状态；参数为目录行及可注入的当前时间，返回是否退役；未来退役日期仍为在售，无效日期保留为在售。 */
export function marketRetired(row: PriceCatalogRow, now = Date.now()): boolean {
  return typeof row.raw.retirement_at === "string" && Date.parse(row.raw.retirement_at) <= now;
}
export type MarketPrice = {
  key: string;
  label: string;
  usd: number | null;
  cny: number | null;
  unit: string;
  tier: number;
  range: string;
};
/** 展开目录行的档位和计费变体，返回双币价格及本地化区间。旧版及新版开放上界统一显示，价格和排序沿用原规则。 */
export function marketPrices(row: PriceCatalogRow, t: TFunction = translate): MarketPrice[] {
  if (!Array.isArray(row.raw.pricing_rules_v2)) return [];
  return row.raw.pricing_rules_v2.flatMap((rule, tier) => {
    if (!rule || typeof rule !== "object") return [];
    const ranges = [rule.input_range, rule.output_range]
      .map((range, i) => {
        if (!Array.isArray(range) || range.length !== 2 ||
          !range.every((n) => typeof n === "number" && Number.isFinite(n))) return "";
        const [from, to] = range as [number, number];
        const start = from.toLocaleString();
        // Modelink 旧数据使用 99999999，新数据使用 -1，两者都表示没有有限上界。
        const formatted = to === -1 || to === 99_999_999
          ? t("marketSync.detail.openRange", { start })
          : `${start}–${to.toLocaleString()}`;
        return t(i ? "marketSync.detail.outputRange" : "marketSync.detail.inputRange", { range: formatted });
      })
      .filter(Boolean)
      .join(" · ");
    const details = rule.details_v2;
    if (!details || typeof details !== "object") return [];
    return Object.entries(details).flatMap(([key, raw]) => {
      if (!raw || typeof raw !== "object") return [];
      const item = raw as Record<string, unknown>;
      const size = typeof item.unit_size === "number" && item.unit_size > 0 ? item.unit_size : 1;
      const token = item.unit_name === "token";
      const factor = token ? 1_000_000 / size : 1;
      const knownUnit: Record<string, string> = { pic: "picture", second: "second", time: "query" };
      const rawUnit = typeof item.unit_name === "string" ? item.unit_name : "";
      const price = (value: unknown) =>
        typeof value === "number" && Number.isFinite(value) && value >= 0 ? value * factor : null;
      return [
        {
          key,
          label: typeof item.name === "string" && item.name ? item.name : key,
          usd: price(item.unit_price_usd),
          cny: price(item.unit_price),
          unit: token
            ? t("marketSync.unit.token")
            : t("marketSync.detail.unitAmount", {
                size,
                unit: Object.hasOwn(knownUnit, rawUnit)
                  ? t("marketSync.unit." + knownUnit[rawUnit])
                  : rawUnit || t("marketSync.detail.unit"),
              }),
          tier,
          range: ranges,
        },
      ];
    });
  });
}
/** 格式化市场价格；参数为金额、币种符号及当前界面翻译函数，返回最多六位有效数字或未提供提示。供卡片与详情统一使用。 */
export function marketMoney(value: number | null, currency = "$", t: TFunction = translate): string {
  return value == null
    ? t("marketSync.detail.missingPrice")
    : currency + new Intl.NumberFormat("en-US", { maximumSignificantDigits: 6 }).format(value);
}

/** 判断模型是否可在广场展示；参数为本地价格行，返回在售公开状态。
 * 广场和刷新提示共同调用；下架、退役和上游撤回均隐藏，本地列表仍保留记录。 */
export function marketSale(row: PriceCatalogRow): boolean {
  return (
    row.raw.market_catalog === true &&
    row.raw.private !== true &&
    !row.raw.delisted &&
    !row.raw.feed_unavailable &&
    !marketRetired(row)
  );
}
