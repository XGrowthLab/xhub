/** @vitest-environment jsdom */
import { render, screen, within } from "@testing-library/react";
import { beforeEach, expect, it } from "vitest";
import { I18nProvider, LOCALE_COOKIE } from "@/i18n";
import { setActiveLocale } from "@/i18n/runtime";
import { grantedModelCards, type MyModelCard } from "./grantedModelCards";
import { MyModels } from "./MyModels";
import { ModelPriceDetails } from "./ModelPriceDetails";

const model: MyModelCard = {
  ...grantedModelCards(["tiered-model"], [])[0],
  price_tiers: [1, 2].map((factor) => ({
    input_range: factor === 1 ? [0, 200000] : [200000, 99999999],
    output_range: [0, 99999999],
    rates: [
      { measure: "token", side: "input", window: "all", usd: 0.000001 * factor },
      { measure: "token", side: "output", window: "all", usd: 0.000005 * factor },
      { measure: "token", side: "cache_write", window: "peak", variant: "1h", usd: 0.000002 * factor },
      { measure: "query", side: "output", window: "all", variant: "search", usd: 0.01 },
    ],
  })),
};

/** 每个语言用例清空偏好，避免 DOM 与运行时语言不一致；只操作测试 jsdom，无用户数据。 */
beforeEach(() => {
  localStorage.removeItem(LOCALE_COOKIE);
  document.cookie = `${LOCALE_COOKIE}=;path=/;max-age=0`;
});

// 真实组件渲染两档、搜索、缓存时长与时段；验证中文和英文完整展示，DOM 由测试框架清理。
it.each(["zh-CN", "en"] as const)("shows confirmed tier prices and dimensions in %s", (locale) => {
  setActiveLocale(locale);
  render(
    <I18nProvider initialLocale={locale}>
      <MyModels models={[model]} />
      <ModelPriceDetails model={model} />
    </I18nProvider>,
  );
  const card = screen.getByRole("article");
  expect(within(card).getByText("$1–$2")).toBeInTheDocument();
  expect(within(card).getByText("$5–$10")).toBeInTheDocument();
  expect(within(card).queryByText(locale === "en" ? "Price unavailable" : "价格未提供")).not.toBeInTheDocument();
  const tables = screen.getAllByRole("table");
  expect(tables).toHaveLength(2);
  expect(tables[0]).toHaveAccessibleName(
    locale === "en" ? "Input tokens: 0 < amount ≤ 200,000" : "输入 Token：0 < 数量 ≤ 200,000",
  );
  expect(tables[1]).toHaveAccessibleName(
    locale === "en" ? "Input tokens: 200,000 < amount ≤ 99,999,999" : "输入 Token：200,000 < 数量 ≤ 99,999,999",
  );
  expect(within(tables[0]).getByText(locale === "en" ? "1 hour" : "1 小时")).toBeInTheDocument();
  expect(within(tables[0]).getByText(locale === "en" ? "Peak" : "高峰")).toBeInTheDocument();
  expect(within(tables[0]).getByText("$0.01")).toBeInTheDocument();
});

// 普通费率表免费价格必须显示为零，非法金额不能显示免费或 Infinity；无后端副作用。
it("shows zero and missing-rate states without inventing a price", () => {
  setActiveLocale("en");
  render(
    <I18nProvider initialLocale="en">
      <ModelPriceDetails
        model={{
          ...model,
          price_tiers: undefined,
          rates: [
            { measure: "token", side: "input", window: "all", usd: 0 },
            { measure: "token", side: "output", window: "all", usd: NaN },
          ],
        }}
      />
    </I18nProvider>,
  );
  expect(screen.getByText("$0")).toBeInTheDocument();
  expect(screen.getByText("Price unavailable")).toBeInTheDocument();
});
