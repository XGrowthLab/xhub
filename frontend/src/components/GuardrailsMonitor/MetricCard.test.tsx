import { renderWithProviders, screen } from "../../../tests/test-utils";
import React from "react";
import { MetricCard } from "./MetricCard";

describe("MetricCard", () => {
  /** 前置窄版指标配置，验证辅助图标隐藏且数值与操作仍可访问；测试库负责卸载。 */
  it("keeps the compact metric named and the icon decorative", () => {
    const icon = <span data-testid="compact-metric-icon">icon</span>;
    renderWithProviders(<MetricCard compact label="Metric" value="—" icon={icon} />);
    const card = screen.getByRole("group", { name: "Metric" });
    expect(card).toHaveTextContent("—");
    expect(screen.getByTestId("compact-metric-icon").closest('[aria-hidden="true"]')).not.toBeNull();
  });
  it("should render", () => {
    renderWithProviders(<MetricCard label="Total Requests" value={1234} />);
    expect(screen.getByText("Total Requests")).toBeInTheDocument();
  });

  it("should display the label and value", () => {
    renderWithProviders(<MetricCard label="Success Rate" value="98.5%" />);
    expect(screen.getByText("Success Rate")).toBeInTheDocument();
    expect(screen.getByText("98.5%")).toBeInTheDocument();
  });

  it("should display numeric values", () => {
    renderWithProviders(<MetricCard label="Count" value={42} />);
    expect(screen.getByText("42")).toBeInTheDocument();
  });

  it("should render icon when provided", () => {
    renderWithProviders(<MetricCard label="Metric" value={100} icon={<span data-testid="test-icon">icon</span>} />);
    expect(screen.getByTestId("test-icon")).toBeInTheDocument();
  });

  it("should not render icon container when no icon provided", () => {
    renderWithProviders(<MetricCard label="Metric" value={100} />);
    expect(screen.queryByTestId("test-icon")).not.toBeInTheDocument();
  });

  it("should render subtitle when provided", () => {
    renderWithProviders(<MetricCard label="Metric" value={100} subtitle="Last 24 hours" />);
    expect(screen.getByText("Last 24 hours")).toBeInTheDocument();
  });

  it("should not render subtitle when not provided", () => {
    renderWithProviders(<MetricCard label="Metric" value={100} />);
    expect(screen.queryByText("Last 24 hours")).not.toBeInTheDocument();
  });
});
