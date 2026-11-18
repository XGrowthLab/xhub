import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { I18nProvider, setActiveLocale } from "@/i18n";
import { AreaChart } from "./area_chart";
import { LineChart } from "./line_chart";
import { SinglePointSummary, singlePointValue } from "./SinglePointSummary";
import { formatCategoryName } from "./chart_tooltip";

/** 每例后清理组件与语言，防止单日降级及翻译状态影响其他图表测试。 */
afterEach(() => { cleanup(); setActiveLocale("en"); });

describe("single-date charts", () => {
  /** 输入嵌套和含点号字段，验证按原始键优先读取；缺失、空值、Infinity、NaN 均不能伪装成零，无持久化。 */
  it("reads finite metrics without inventing missing values", () => {
    expect(singlePointValue({ metrics: { spend: 0 } }, "metrics.spend")).toBe(0);
    expect(singlePointValue({ "model.v1": 4, model: { v1: 2 } }, "model.v1")).toBe(4);
    for (const value of [null, undefined, Infinity, NaN, "12"]) expect(singlePointValue({ metric: value }, "metric")).toBeUndefined();
    expect(singlePointValue({}, "metrics.missing")).toBeUndefined();
  });

  /** 单日面积图与折线图在两语言均展示日期、真实输入输出值和缺失状态，不绘制虚假趋势；结束自动清理 DOM。 */
  it.each(["zh-CN", "en"] as const)("renders readable values for a single date in %s", (locale) => {
    render(<I18nProvider initialLocale={locale}><AreaChart data={[{ date: "2026-11-18", metrics: { prompt_tokens: 123, completion_tokens: 0 } }]} index="date" categories={["metrics.prompt_tokens", "metrics.completion_tokens", "metrics.total_tokens"]} /></I18nProvider>);
    const summary = screen.getByRole("region", { name: locale === "en" ? "One recorded date" : "当前仅有一天的记录" });
    expect(summary).toHaveTextContent("2026-11-18");
    expect(within(summary).getByText(locale === "en" ? "Input tokens" : "输入 Token")).toBeVisible();
    expect(within(summary).getByText("123")).toBeVisible();
    expect(within(summary).getByText("0")).toBeVisible();
    expect(within(summary).getByText(locale === "en" ? "Not recorded" : "未记录")).toBeVisible();
    expect(summary.querySelector("svg.recharts-surface")).toBeNull();
  });

  /** 单日端点折线保留请求路径，不将用户标识标题化或翻译；显式格式化金额，组件卸载完成清理。 */
  it("preserves endpoint names and formatters", () => {
    render(<LineChart data={[{ date: "Nov 18", "/v1/chat/completions": 0.25 }]} index="date" categories={["/v1/chat/completions"]} valueFormatter={(value) => `$${value.toFixed(2)}`} />);
    expect(screen.getByText("/v1/chat/completions")).toBeVisible();
    expect(screen.getByText("$0.25")).toBeVisible();
    expect(formatCategoryName("gpt-6.1-terra")).toBe("gpt-6.1-terra");
  });

  /** 支持调用方提供监控连接名称，非法数值明确为未记录；不增加数据点，结束清理 DOM。 */
  it("uses explicit labels and preserves unavailable samples", () => {
    render(<SinglePointSummary row={{ day: "2026-11-18", opaque: NaN }} index="day" categories={["opaque"]} labels={{ opaque: "supplier-a / gpt-6.1-terra" }} />);
    expect(screen.getByText("supplier-a / gpt-6.1-terra")).toBeVisible();
    expect(screen.getByText("Not recorded")).toBeVisible();
  });
});
