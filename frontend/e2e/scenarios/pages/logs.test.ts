/** 目的：独立验证 /logs 的页面与读接口；前置隔离网关与管理员，异常独立报告；上下文和 schema 由运行器清理。 */
import { discoverPages } from "../../routes";
import { registerPageScenario } from "../../page-scenario";
registerPageScenario(discoverPages().find(page => page.route === '/logs')!, '/logs');

import { expect, test } from "@playwright/test";
import { loginAdmin, stableGoto, watchGateway } from "../../helpers";
import { translate } from "../../../src/i18n/translate";

/** 目的：验证普通和错误日志的后台状态隔离及双语缓存筛选；使用隔离后台，运行器清理上下文和 schema。 */
test("请求日志 Hook 回归：中英文状态与缓存筛选", async ({ page }) => {
  const guard = watchGateway(page);
  await loginAdmin(page);
  await stableGoto(page, "/logs");
  await page.getByRole("button", { name: "English", exact: true }).click();
  await page.getByTestId("datatable-filters-trigger").click();
  await page.getByText(translate("en", "logsUX.advanced"), { exact: true }).click();
  await page.getByRole("combobox", { name: translate("en", "logsUX.responseCache") }).click();
  await page.getByRole("option", { name: translate("en", "Cache Miss"), exact: true }).click();
  const read = page.waitForResponse(res => res.url().includes("/spend/logs/ui?") && new URL(res.url()).searchParams.get("cache_hit_filter") === "miss");
  // 共享抽屉仍通过稳定测试标识提交；本用例聚焦日志选项及后台协议。
  await page.getByTestId("filter-drawer-apply").click();
  const response = await read;
  expect(response.status()).toBe(200);
  expect(new URL(response.url()).searchParams.get("status_filter")).toBe("non_error");
  await page.getByRole("button", { name: translate("en", "language.zhCN"), exact: true }).click();
  await page.getByTestId("datatable-filters-trigger").click();
  await page.getByText(translate("zh-CN", "logsUX.advanced"), { exact: true }).click();
  await expect(page.getByRole("combobox", { name: translate("zh-CN", "logsUX.responseCache") })).toContainText(translate("zh-CN", "Cache Miss"));
  await page.keyboard.press("Escape");
  const errorRead = page.waitForResponse(res => res.url().includes("/spend/logs/ui?") && new URL(res.url()).searchParams.get("status_filter") === "error");
  await page.getByRole("tab", { name: translate("zh-CN", "Error Logs"), exact: true }).click();
  expect((await errorRead).status()).toBe(200);
  await guard.assertOk();
});
