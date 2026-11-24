import { expect, test, type Page } from "@playwright/test";
import { copyFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { loginAdmin, stableGoto } from "./helpers";

/** 访问实际替换的两个 Compose 服务并核对版本，无拦截请求或模拟响应。 */
async function expectRunningVersions(page: Page, version: string) {
  for (const url of [process.env.E2E_VERSION_GATEWAY_URL, process.env.E2E_VERSION_CONSOLE_URL]) {
    expect(url).toBeTruthy();
    const response = await page.request.get(url!);
    expect(response.status()).toBe(200);
    expect(await response.text()).toBe(version);
  }
}

/** 真实浏览器经单独网关及 Unix 执行器检查、升级、刷新、双语切换和回退。动态清单由本地 Registry 提供。
 * 需要 version_update_fixture.py 运行；脚本负责回收 Compose、镜像和状态文件。
 */
test("version update switches both application images and restores a failed release", async ({ page }) => {
  test.skip(process.env.E2E_VERSION_UPDATE !== "1", "requires independent Compose and release fixtures");
  test.setTimeout(150000);
  // Node 的直接 HTTP 探针不使用浏览器的测试域名映射，调用执行器同样使用的回环地址。
  const consolePort = process.env.E2E_UI_PORT ?? "3100";
  const consoleLogin = await page.request.get(`http://127.0.0.1:${consolePort}/login`, { maxRedirects: 0 });
  expect(consoleLogin.status()).toBe(200);
  await loginAdmin(page);
  await stableGoto(page, "/admin-panel");
  await page.getByRole("tab", { name: "版本更新", exact: true }).click();
  await expect(page.getByText("v0.1.0-beta.1", { exact: true })).toBeVisible();
  const feed = process.env.E2E_VERSION_FEED!;
  copyFileSync(join(dirname(feed), "invalid.json"), feed);
  await page.getByRole("button", { name: "检查更新", exact: true }).click();
  await expect(page.getByRole("alert").filter({ hasText: "检查更新失败，请检查发布源和运行中的镜像。" })).toBeVisible();
  await expectRunningVersions(page, "v0.1.0-beta.1");
  copyFileSync(join(dirname(feed), "update.json"), feed);
  await page.getByRole("button", { name: "检查更新", exact: true }).click();
  await expect(page.getByRole("heading", { name: /可更新到 v0\.1\.0-beta\.2/ })).toBeVisible();
  await expect(page.getByText("测试版", { exact: true })).toBeVisible();

  const update = page.getByRole("button", { name: "立即更新", exact: true });
  await update.click();
  const confirmation = page.getByRole("dialog", { name: "更新到 v0.1.0-beta.2" });
  await confirmation.getByRole("button", { name: "取消", exact: true }).click();
  await expect(confirmation).toBeHidden();
  await expectRunningVersions(page, "v0.1.0-beta.1");
  await update.click();
  await confirmation.getByRole("button", { name: "确认执行", exact: true }).click();
  await expect(page.getByText("操作完成，当前版本 v0.1.0-beta.2", { exact: true })).toBeVisible({ timeout: 30000 });
  await expectRunningVersions(page, "v0.1.0-beta.2");

  await page.reload();
  await page.getByRole("button", { name: "English", exact: true }).click();
  await page.getByRole("tab", { name: "Version updates", exact: true }).click();
  await expect(page.getByText("Operation complete. Current version v0.1.0-beta.2.", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Roll back", exact: true }).click();
  await page.getByRole("dialog", { name: "Roll back to v0.1.0-beta.1" }).getByRole("button", { name: "Confirm", exact: true }).click();
  await expect(page.getByText("Operation complete. Current version v0.1.0-beta.1.", { exact: true })).toBeVisible({ timeout: 30000 });
  await expectRunningVersions(page, "v0.1.0-beta.1");

  copyFileSync(join(dirname(feed), "incompatible.json"), feed);
  const check = page.getByRole("button", { name: "Check for updates", exact: true });
  await check.click();
  await expect(page.getByRole("heading", { name: /Update to v0\.1\.0-beta\.4 available/ })).toBeVisible();
  await expect(page.getByText(/The database compatibility identifiers differ/)).toBeVisible();
  await expect(page.getByRole("button", { name: "Update now", exact: true })).toBeDisabled();

  copyFileSync(join(dirname(feed), "broken.json"), feed);
  await check.click();
  await expect(page.getByRole("heading", { name: /Update to v0\.1\.0-beta\.3 available/ })).toBeVisible();
  await page.getByRole("button", { name: "Update now", exact: true }).click();
  await page.getByRole("dialog", { name: "Update to v0.1.0-beta.3" }).getByRole("button", { name: "Confirm", exact: true }).click();
  await expect(page.getByRole("alert").filter({ hasText: "The new version failed its health checks. The original version was restored." })).toBeVisible({ timeout: 40000 });
  await expectRunningVersions(page, "v0.1.0-beta.1");
});
