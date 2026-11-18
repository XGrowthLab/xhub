import type { Page } from "@playwright/test";

/** 使用可访问角色选择监控页的统一下拉框；参数为页面、已翻译字段名和显示选项，返回交互完成 Promise。
 * 各监控真实 E2E 调用；不依赖原生 select 或 CSS，未找到选项时保留 Playwright 的诊断，无写入与清理副作用。 */
export async function selectMonitorOption(page: Page, label: string, option: string): Promise<void> {
  await page.getByRole("combobox", { name: label, exact: true }).click();
  await page.getByRole("option", { name: option, exact: true }).click();
}
