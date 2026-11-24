import React from "react";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { I18nProvider } from "@/i18n";
import AdminPanel from "./AdminPanel";
vi.mock("@/components/Settings/AdminSettings/DirectorySettings", () => ({
  default: () => <div data-testid="settings-tabs" />,
}));
afterEach(cleanup);

/** 使用双语目录验证管理员指引的实际用户页路径；无后台调用，统一清理 DOM。 */
it.each([
  ["zh-CN", "前往用户"],
  ["en", "Open Users"],
] as const)("links to the real Users page in %s", (locale, label) => {
  render(
    <I18nProvider initialLocale={locale}>
      <AdminPanel />
    </I18nProvider>,
  );
  expect(screen.getByRole("link", { name: label })).toHaveAttribute("href", "/ui/users");
});

/** 在中文控制台渲染独立管理页，验证页头和设置页签共用同一布局；测试由 cleanup 回收 DOM。 */
it("管理员页面以统一页头显示标题和用户入口", () => {
  render(<I18nProvider initialLocale="zh-CN"><AdminPanel /></I18nProvider>);
  const canvas = screen.getByTestId("admin-settings-canvas");
  expect(within(canvas).getByRole("heading", { level: 1, name: "管理员设置" })).toBeVisible();
  expect(within(canvas).getByRole("link", { name: "前往用户" })).toBeVisible();
  expect(canvas).toContainElement(screen.getByTestId("settings-tabs"));
});
