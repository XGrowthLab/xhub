const assert = require("node:assert/strict");
const { chromium } = require("../frontend/node_modules/playwright");
const { expect } = require("../frontend/node_modules/@playwright/test");

/** 登录隔离的首装实例，验证运行构建、真实发布检查和管理员权限，结束关闭浏览器。 */
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
    const revision = process.env.E2E_BUILD_REVISION;
    const currentBuild = revision ? `main · ${revision.slice(0, 7)}` : "未识别";
    await expect(page.getByText(version || currentBuild, { exact: true })).toBeVisible({ timeout: 20000 });
    if (revision) await expect(page.getByText(currentBuild, { exact: true })).toHaveAttribute("title", revision);
    await expect(page.getByText("正式版渠道", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "切换到测试版渠道", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: "检查更新", exact: true }).click();
    const current = page.getByText("当前渠道没有更新版本。", { exact: true });
    const candidate = page.getByRole("heading", { name: version ? /可更新到 v/ : /渠道最新发布 v/ });
    await expect(current.or(candidate)).toBeVisible({ timeout: 25000 });
    if (!version && await candidate.isVisible()) {
      await expect(page.getByRole("button", { name: "立即更新", exact: true })).toBeDisabled();
    }
    if (revision) {
      await page.getByRole("button", { name: "切换到测试版渠道", exact: true }).click();
      await expect(page.getByText("测试版渠道", { exact: true })).toBeVisible();
      await page.getByRole("button", { name: "检查更新", exact: true }).click();
      await expect(candidate).toBeVisible({ timeout: 25000 });
      const update = page.getByRole("button", { name: "立即更新", exact: true });
      await expect(update).toBeDisabled();
      await expect(update).toHaveAccessibleDescription(/当前部署缺少可回退的发布版本/);
      await expect(page.getByText(/数据库兼容标识不同/)).toHaveCount(0);
      await page.getByRole("button", { name: "切换到正式版渠道", exact: true }).click();
      await expect(page.getByText("正式版渠道", { exact: true })).toBeVisible();
    }
    const unauthorized = await fetch(gatewayURL + "/admin/version-update");
    assert.equal(unauthorized.status, 401);
    await page.getByRole("button", { name: "English", exact: true }).click();
    await page.getByRole("tab", { name: "Version updates", exact: true }).click();
    await expect(page.getByRole("button", { name: "Check for updates", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Switch to beta channel", exact: true })).toBeEnabled();
    console.log(JSON.stringify({ passed: 1, version, revision, authenticatedCheck: true, anonymousStatus: unauthorized.status, locales: ["zh-CN", "en"] }));
  } finally {
    await browser.close();
  }
}

run(...process.argv.slice(2)).catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
