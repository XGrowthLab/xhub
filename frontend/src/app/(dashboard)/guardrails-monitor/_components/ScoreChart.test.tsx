import React from "react";
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
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
    expect(screen.getByText("通过")).toBeVisible();
    expect(screen.getByText("拦截")).toBeVisible();
  });

  it("should render the chart when data is provided", () => {
    const data = [
      { date: "2026-03-01", passed: 10, blocked: 2 },
      { date: "2026-03-02", passed: 15, blocked: 1 },
    ];

    const { container } = renderWithProviders(<ScoreChart data={data} />);

    expect(screen.queryByText("No chart data for this period")).not.toBeInTheDocument();
    expect(screen.getByText("Passed")).toBeInTheDocument();
    expect(screen.getByText("Blocked")).toBeInTheDocument();
    expect(screen.getAllByText(/2026-03-01/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/2026-03-02/).length).toBeGreaterThan(0);
    const bars = container.querySelectorAll(".recharts-bar");
    expect(bars).toHaveLength(2);
  });
});
