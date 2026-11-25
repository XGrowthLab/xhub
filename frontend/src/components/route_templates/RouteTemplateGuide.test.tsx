import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { I18nProvider } from "@/i18n";
import RouteTemplateGuide from "./RouteTemplateGuide";

/** 目的：验证帮助入口和弹窗标题、描述完整接入双语；前置本地组件；点击只改变弹窗状态，DOM 自动清理。 */
it.each([
  ["zh-CN", "JSON 配置指南", "查字段、复制示例，或了解请求如何路由。"],
  ["en", "JSON configuration guide", "Look up fields, copy an example, or see how requests are routed."],
] as const)("opens localized route-template help in %s", async (locale, button, description) => {
  const user = userEvent.setup();
  render(<I18nProvider initialLocale={locale}><RouteTemplateGuide /></I18nProvider>);
  await user.click(screen.getByRole("button", { name: button }));
  const dialog = screen.getByRole("dialog", { name: button });
  expect(dialog).toHaveTextContent(description);
  expect(dialog).toHaveTextContent("retry_policy");
  await user.click(screen.getByRole("tab", { name: locale === "zh-CN" ? "执行顺序" : "Request flow" }));
  expect(screen.getByRole("list", { name: "" })).toHaveTextContent("max_attempts");
  await user.keyboard("{Escape}");
  expect(dialog).not.toBeInTheDocument();
});
