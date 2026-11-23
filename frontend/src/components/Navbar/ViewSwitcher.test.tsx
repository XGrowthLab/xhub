import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@/i18n";
import ViewSwitcher from "./ViewSwitcher";

const state = vi.hoisted(() => ({
  mode: "ai-gateway",
  setMode: vi.fn(),
  plugins: [] as { name: string; display_name: string; url: string }[],
}));

vi.mock("@/contexts/PluginModeContext", () => ({ usePluginMode: () => state }));

describe("ViewSwitcher", () => {
  afterEach(() => {
    state.mode = "ai-gateway";
    state.plugins = [];
    state.setMode.mockClear();
  });

  /** 没有插件时，在两种语言下只显示网关名称。测试不创建会话，组件由测试框架卸载。 */
  it.each([
    ["en", "AI Gateway"],
    ["zh-CN", "AI 网关"],
  ] as const)("shows the gateway label in %s", (locale, name) => {
    render(
      <I18nProvider initialLocale={locale}>
        <ViewSwitcher />
      </I18nProvider>,
    );
    expect(screen.getByText(name, { exact: true })).toBeVisible();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  /** 安装插件后可在配置的插件和网关之间切换。使用内存状态，验证选项和回调，结束时清理。 */
  it("lists only the gateway and configured plugins", async () => {
    const user = userEvent.setup();
    state.plugins = [
      { name: "obs", display_name: "Observability", url: "http://localhost:9000" },
      { name: "audit", display_name: "Audit plugin", url: "http://localhost:9001" },
    ];
    state.mode = "obs";
    render(<ViewSwitcher />);

    await user.click(screen.getByRole("button", { name: "Observability" }));
    const menu = await screen.findByRole("menu");
    expect(within(menu).getAllByRole("menuitem")).toHaveLength(3);
    await user.click(within(menu).getByRole("menuitem", { name: "AI Gateway" }));
    expect(state.setMode).toHaveBeenCalledWith("ai-gateway");

    await user.click(screen.getByRole("button", { name: "Observability" }));
    await user.click(await screen.findByRole("menuitem", { name: "Audit plugin" }));
    expect(state.setMode).toHaveBeenLastCalledWith("audit");
  });
});
