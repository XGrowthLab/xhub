const assert = require("node:assert/strict");
const { chromium } = require("../frontend/node_modules/playwright");
const { expect } = require("../frontend/node_modules/@playwright/test");

/** 登录隔离的首装实例，验证默认检查入口、真实执行器响应和匿名访问权限，结束关闭所有页面。 */
async function run(consoleURL, gatewayURL, version) {
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({ locale: "zh-CN" });
    await page.goto(consoleURL + "/ui/login");
    const username = page.getByPlaceholder("请输入用户名");
    await username.fill("admin@xhub.local");
    const password = page.getByPlaceholder("请输入密码");
    await password.fill("admin-pass-1234");
    await password.press("Enter");
    await expect(page.getByRole("link", { name: "管理员设置" })).toBeVisible({ timeout: 20000 });
    await page.goto(consoleURL + "/ui/admin-panel");
    await page.getByRole("tab", { name: "版本更新", exact: true }).click();
    await expect(page.getByText(version || "未识别", { exact: true })).toBeVisible({ timeout: 20000 });
    await expect(page.getByText("正式版渠道", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "检查更新", exact: true }).click();
    const current = page.getByText("当前渠道没有更新版本。", { exact: true });
    const candidate = page.getByRole("heading", { name: /可更新到 v/ });
    await expect(current.or(candidate)).toBeVisible({ timeout: 25000 });
    if (!version && await candidate.isVisible()) {
      await expect(page.getByRole("button", { name: "立即更新", exact: true })).toBeDisabled();
    }
    const unauthorized = await fetch(gatewayURL + "/admin/version-update");
    assert.equal(unauthorized.status, 401);
    await page.getByRole("button", { name: "English", exact: true }).click();
    await page.getByRole("tab", { name: "Version updates", exact: true }).click();
    await expect(page.getByRole("button", { name: "Check for updates", exact: true })).toBeVisible();
    console.log(JSON.stringify({ passed: 1, version, authenticatedCheck: true, anonymousStatus: unauthorized.status, locales: ["zh-CN", "en"] }));
  } finally {
    await browser.close();
  }
}

run(...process.argv.slice(2)).catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
