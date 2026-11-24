import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { DashboardHeader } from "./DashboardHeader";

const { mockUsePluginMode, state } = vi.hoisted(() => {
  const state = {
    plugins: [] as { name: string; display_name: string; url: string }[],
    pathname: "/ui/logs",
  };
  return {
    state,
    mockUsePluginMode: vi.fn(() => ({ mode: "ai-gateway", setMode: vi.fn(), plugins: state.plugins })),
  };
});

vi.mock("@/contexts/PluginModeContext", () => ({ usePluginMode: mockUsePluginMode }));
vi.mock("next/navigation", () => ({
  usePathname: () => state.pathname,
  useRouter: () => ({ refresh: vi.fn(), push: vi.fn(), replace: vi.fn() }),
}));
vi.mock("@/hooks/useWorker", () => ({ useWorker: () => ({ isControlPlane: false, selectedWorker: null }) }));
vi.mock("@/app/(dashboard)/hooks/useDisableShowPrompts", () => ({ useDisableShowPrompts: () => false }));
vi.mock("@/components/Navbar/BlogDropdown/BlogDropdown", () => ({ BlogDropdown: () => null }));
vi.mock("@/components/Navbar/CommunityEngagementButtons/CommunityEngagementButtons", () => ({
  CommunityEngagementButtons: () => null,
}));
vi.mock("@/components/Navbar/WorkerDropdown/WorkerDropdown", () => ({ default: () => null }));

describe("DashboardHeader breadcrumb", () => {
  afterEach(() => {
    state.plugins = [];
    state.pathname = "/ui/logs";
  });

  it("titles the breadcrumb from the current route, not from a sidebar page id", () => {
    state.pathname = "/ui/models-and-endpoints";
    render(<DashboardHeader />);

    expect(screen.getByText("Models + Endpoints")).toBeInTheDocument();
  });

  it("titles the dashboard root as Virtual Keys", () => {
    state.pathname = "/ui/";
    render(<DashboardHeader />);

    expect(screen.getByText("Virtual Keys")).toBeInTheDocument();
  });

  /** 默认网关没有其它工作区可选。验证顶栏显示静态名称和当前路由，组件自动清理。 */
  it("shows the gateway and current route without a workspace dropdown", () => {
    render(<DashboardHeader />);

    expect(screen.getByText("AI Gateway", { exact: true })).toBeVisible();
    expect(screen.queryByRole("button", { name: "AI Gateway" })).not.toBeInTheDocument();
    expect(screen.getByText("Logs")).toBeInTheDocument();
  });

  // 目的：核对右上角文档入口；前置为真实 DocsLink，验证同站 /docs 链接，结束清理渲染树。
  it("opens the local documentation center", () => {
    render(<DashboardHeader />);

    expect(screen.getByRole("link", { name: "Documentation" })).toHaveAttribute("href", "/docs");
  });

  it("renders the tools divider centered rather than stretched to the top of the row", () => {
    const { container } = render(<DashboardHeader />);

    const separators = container.querySelectorAll('[data-slot="separator"][data-orientation="vertical"]');
    expect(separators).toHaveLength(1);
    expect(separators[0].className).not.toMatch(/self-stretch/);
    expect(separators[0].className).toContain("data-vertical:self-center");
  });
});
