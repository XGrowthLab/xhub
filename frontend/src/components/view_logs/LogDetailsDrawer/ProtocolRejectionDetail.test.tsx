import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { I18nProvider } from "@/i18n";
import { ProtocolRejectionDetail } from "./ProtocolRejectionDetail";

afterEach(cleanup);

/** 前置后端已保存精确字段；中英文展示协议、阶段和原始技术细节，卸载时清理组件。 */
it.each([
  ["zh-CN", "协议兼容诊断", "尚未发送给供应商", "目标协议"],
  ["en", "Protocol compatibility", "before contacting the provider", "Target protocol"],
] as const)("renders the localized rejection in %s", (locale, title, reason, target) => {
  const rejection = {
    stage: "request_conversion",
    source_protocol: "openai-responses",
    problems: [{ field: "tools[0].format.type", target_protocol: "openai-chat", details: "Unsupported custom format." }],
  };
  render(<I18nProvider initialLocale={locale}><ProtocolRejectionDetail rejection={rejection} /></I18nProvider>);
  const region = screen.getByRole("region", { name: title });
  expect(region).toHaveTextContent(reason);
  expect(region).toHaveTextContent(target);
  expect(within(region).getByText("tools[0].format.type", { exact: true })).toBeVisible();
  expect(within(region).getByText("openai-chat", { exact: true })).toBeVisible();
  expect(within(region).getByText(rejection.problems[0].details)).toBeVisible();
});

/** 前置旧日志缺少协议字段；无证据时隐藏诊断，卸载后无需额外清理。 */
it("renders nothing for a historical entry without protocol diagnostics", () => {
  render(<I18nProvider initialLocale="en"><ProtocolRejectionDetail /></I18nProvider>);
  expect(screen.queryByRole("region")).not.toBeInTheDocument();
});
