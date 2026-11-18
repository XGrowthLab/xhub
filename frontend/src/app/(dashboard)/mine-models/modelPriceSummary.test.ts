import { describe, expect, it } from "vitest";
import { grantedModelCards, type MyModelCard, type ModelPriceRate } from "./grantedModelCards";
import { formatModelPrice, modelPriceRange } from "./modelPriceSummary";

/** 构造最小授权卡片供纯函数测试；覆盖项不触及网络或存储，测试结束由 GC 回收。 */
function card(overrides: Partial<MyModelCard> = {}): MyModelCard {
  return { ...grantedModelCards(["tiered"], [])[0], ...overrides };
}

/** 构造基础单位费率；amount 为每百万 Token 金额，测试按实际 API 单位验证换算。 */
function rate(amount: number, side = "input"): ModelPriceRate {
  return { measure: "token", side, window: "all", usd: amount / 1_000_000 };
}

describe("confirmed model price summary", () => {
  // 多档和时段都应纳入区间；搜索按次费用不得混进 Token 单价，无外部数据。
  it("summarizes every confirmed tier instead of a stale flat or search price", () => {
    const model = card({
      input_price: 999,
      price_tiers: [
        { input_range: [0, 200000], output_range: [0, 99999999], rates: [rate(1), rate(5, "output")] },
        {
          input_range: [200000, 99999999],
          output_range: [0, 99999999],
          rates: [rate(2), rate(8, "output"), { ...rate(999), measure: "query" }],
        },
      ],
    });
    expect(modelPriceRange(model, "input")).toEqual({ min: 1, max: 2 });
    expect(formatModelPrice(modelPriceRange(model, "output"))).toBe("$5–$8");
    expect(modelPriceRange(model, "cache_read")).toBeNull();
  });

  // 免费、旧平面字段、小额缓存与不同缓存时长均保留精度；不读取最新市场候选。
  it("keeps zero and tiny rates and supports flat deployments and cache variants", () => {
    expect(formatModelPrice(modelPriceRange(card({ input_price: 0 }), "input"))).toBe("$0");
    expect(formatModelPrice(modelPriceRange(card({ output_price: 0.00000125 }), "output"))).toBe("$0.00000125");
    const model = card({
      rates: [
        rate(0.0029, "cache_read"),
        { ...rate(2.5, "cache_write"), variant: "1h" },
        { ...rate(5, "cache_write"), variant: "12h" },
      ],
    });
    expect(formatModelPrice(modelPriceRange(model, "cache_read"))).toBe("$0.0029");
    expect(modelPriceRange(model, "cache_write")).toEqual({ min: 2.5, max: 5 });
  });

  // 任一档缺少价格或含非法值时显示缺价，不能回退为首档或免费；纯内存测试无需清理。
  it("rejects missing, negative, non-finite and overflowing prices", () => {
    for (const value of [-1, NaN, Infinity, Number.MAX_VALUE]) {
      expect(modelPriceRange(card({ rates: [{ ...rate(1), usd: value }] }), "input")).toBeNull();
    }
    for (const value of [null, -1, NaN, Infinity]) {
      expect(modelPriceRange(card({ input_price: value }), "input")).toBeNull();
    }
    expect(
      modelPriceRange(
        card({
          input_price: 99,
          price_tiers: [
            { input_range: [0, 10], output_range: [0, 100], rates: [rate(1)] },
            { input_range: [10, 100], output_range: [0, 100], rates: [] },
          ],
        }),
        "input",
      ),
    ).toBeNull();
    expect(formatModelPrice(null)).toBe("");
  });
});
