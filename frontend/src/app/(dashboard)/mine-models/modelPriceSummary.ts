import type { MyModelCard, ModelPriceRate } from "./grantedModelCards";

export type PriceSide = "input" | "output" | "cache_read" | "cache_write";
export type PriceRange = { min: number; max: number };

/** 计算卡片每百万 Token 的真实费率区间；model 来自授权接口，side 指定输入、输出或缓存。
 * 分档和已确认费率优先于旧平面字段；任一档缺少该侧价格时返回 null，防止将不完整价格显示为统一报价。
 * 仅服务展示与排序，不改计费；搜索等非 Token 费用不混入 Token 单价。
 */
export function modelPriceRange(model: MyModelCard, side: PriceSide): PriceRange | null {
  let groups: ModelPriceRate[][] | null = null;
  if (model.price_tiers?.length) groups = model.price_tiers.map((tier) => tier.rates);
  else if (model.rates?.length) groups = [model.rates];
  if (groups) {
    const prices: number[] = [];
    for (const group of groups) {
      const matches = group.filter((rate: ModelPriceRate) => rate.measure === "token" && rate.side === side);
      if (
        !matches.length ||
        matches.some((rate) => !Number.isFinite(rate.usd) || rate.usd < 0 || !Number.isFinite(rate.usd * 1_000_000))
      )
        return null;
      prices.push(...matches.map((rate) => rate.usd * 1_000_000));
    }
    return { min: Math.min(...prices), max: Math.max(...prices) };
  }
  const field = {
    input: "input_price",
    output: "output_price",
    cache_read: "cache_read_price",
    cache_write: "cache_write_price",
  } as const;
  const value = model[field[side]];
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? { min: value, max: value } : null;
}

/** 将明确的价格区间格式化为美元金额；null 由调用方本地化缺价提示，返回空串。
 * 用于模型卡片，不将最低档伪装为所有请求单价，也不截断小额有效位。
 */
export function formatModelPrice(range: PriceRange | null): string {
  if (!range) return "";
  // 美元价格保留有效位；仅格式化展示数值，不进行结算或币种换算。
  const money = (value: number) => "$" + new Intl.NumberFormat("en-US", { maximumSignificantDigits: 8 }).format(value);
  return range.min === range.max ? money(range.min) : `${money(range.min)}–${money(range.max)}`;
}
