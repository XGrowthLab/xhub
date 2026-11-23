import React from "react";
import { describe, it, expect } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "../../../../../tests/test-utils";
import { I18nProvider } from "@/i18n";
import { ScoreChart } from "./ScoreChart";

describe("ScoreChart", () => {
  it("should render the title", () => {
    renderWithProviders(<ScoreChart />);

    expect(screen.getByText("Request Outcomes Over Time")).toBeInTheDocument();
  });

  it("should show empty state when no data is provided", () => {
    renderWithProviders(<ScoreChart />);

    expect(screen.getByText("No chart data for this period")).toBeInTheDocument();
  });

  it("should show empty state when data is an empty array", () => {
    renderWithProviders(<ScoreChart data={[]} />);

    expect(screen.getByText("No chart data for this period")).toBeInTheDocument();
  });

  /** 中文图例使用本地化标签，生成两组实际柱形；组件卸载由测试框架清理。 */
  it("本地化中文数据系列", () => {
    render(
      <I18nProvider initialLocale="zh-CN">
        <ScoreChart data={[{ date: "2026-03-01", passed: 10, blocked: 2 }]} />
      </I18nProvider>,
    );
    expect(screen.getByText("请求处理结果趋势")).toBeVisible();
    expect(screen.getAllByText("通过").some((item) => item.closest("table") == null)).toBe(true);
    expect(screen.getAllByText("拦截").some((item) => item.closest("table") == null)).toBe(true);
  });

  it("should render the chart when data is provided", () => {
    const data = [
      { date: "2026-03-01", passed: 10, blocked: 2 },
      { date: "2026-03-02", passed: 15, blocked: 1 },
    ];

    const { container } = renderWithProviders(<ScoreChart data={data} />);

    expect(screen.queryByText("No chart data for this period")).not.toBeInTheDocument();
    expect(screen.getAllByText("Passed").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Blocked").length).toBeGreaterThan(0);
    expect(screen.getAllByText(/2026-03-01/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/2026-03-02/).length).toBeGreaterThan(0);
    const bars = container.querySelectorAll(".recharts-bar");
    expect(bars).toHaveLength(2);
  });

  /** 前置有通过和拦截值的图表数据；验证展开后两种语言的等价表格与原数据，自动卸载。 */
  it.each(["en", "zh-CN"] as const)("provides a daily totals table in %s", async (locale) => {
    const user = userEvent.setup();
    render(
      <I18nProvider initialLocale={locale}>
        <ScoreChart data={[{ date: "2026-03-01", passed: 10, blocked: 2 }]} />
      </I18nProvider>,
    );
    const summary = screen.getByText(locale === "en" ? "View daily totals" : "查看每日统计");
    expect(summary.parentElement).not.toHaveAttribute("open");
    await user.click(summary);
    expect(summary.parentElement).toHaveAttribute("open");
    const table = within(screen.getByRole("table"));
    expect(table.getByRole("columnheader", { name: locale === "en" ? "Passed" : "通过" })).toBeVisible();
    expect(table.getByRole("rowheader", { name: "2026-03-01" })).toBeVisible();
    expect(table.getByRole("cell", { name: "10", exact: true })).toBeVisible();
    expect(table.getByRole("cell", { name: "2", exact: true })).toBeVisible();
  });
});
