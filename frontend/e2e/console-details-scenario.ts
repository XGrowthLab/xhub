import { expect, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { translate, type Locale } from "../src/i18n/translate";

/** 检查个人密钥的精简筛选与列菜单；page 已登录且位于密钥列表，locale 指定语言。
 * 验证普通单归属用户不出现冗余筛选、列菜单不开页面滚动锁，随后关闭浮层，不修改任何记录。 */
export async function expectKeyFiltersAndColumns(page: Page, locale: Locale) {
  const tr = (key: string) => translate(locale, key);
  const list = page.getByTestId("data-table-frame").first();
  for (const cell of [list.getByRole("columnheader").first(), list.getByRole("cell").first()]) {
    expect(await cell.evaluate((node) => parseFloat(getComputedStyle(node).paddingInlineStart)), "首列表头和数据应使用相同的内边距").toBeGreaterThanOrEqual(16);
  }
  await page.getByRole("button", { name: tr("Filters"), exact: true }).click();
  const filters = page.getByRole("dialog", { name: tr("consoleDetails.keyFilters") });
  await expect(filters.getByRole("textbox", { name: tr("Key ID"), exact: true })).toBeVisible();
  await expect(filters.getByPlaceholder(tr("Select a team…"))).toHaveCount(0);
  await expect(filters.getByPlaceholder(tr("Select an organization…"))).toHaveCount(0);
  await expect(filters).not.toContainText("Narrow");
  await page.keyboard.press("Escape");
  await expect(filters).toHaveCount(0);
  const before = await page.getByRole("button", { name: tr("Columns"), exact: true }).boundingBox();
  const scrollWidth = await page.evaluate(() => document.documentElement.clientWidth);
  await page.getByRole("button", { name: tr("Columns"), exact: true }).click();
  const menu = page.getByRole("menu", { name: tr("consoleDetails.columnsTitle") });
  await expect(menu).toBeVisible();
  const after = await page.getByRole("button", { name: tr("Columns"), exact: true }).boundingBox();
  expect(after!.x, "打开列选择不应让页面水平抖动").toBeCloseTo(before!.x, 0);
  expect(await page.evaluate(() => document.documentElement.clientWidth)).toBe(scrollWidth);
  await expect(menu.getByRole("menuitemcheckbox").first()).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
}

/** 检查真实调试台生成的代码并实际执行；page 已选择测试模型且有用户草稿，key 是隔离场景新建密钥。
 * 复制命令中的协议、模型和原始输入必须保持一致，不包含会话凭据；仅对测试网关调用，费用随外层测试数据清理。 */
export async function expectRunnablePlaygroundCode(page: Page, locale: Locale, model: string, key: string) {
  const tr = (name: string) => translate(locale, name);
  const draft = await page.getByTestId("chat-composer-input").inputValue();
  await page.getByRole("button", { name: tr("consoleDetails.viewCode"), exact: true }).click();
  const dialog = page.getByRole("dialog", { name: tr("consoleDetails.codeTitle"), exact: true });
  await expect(dialog).toBeVisible();
  const code = dialog
    .locator("pre")
    .filter({ has: page.locator("code") })
    .filter({ hasText: "curl --request POST" });
  await expect(code).toHaveCount(1);
  await expect(code).toContainText(model);
  if (draft) await expect(code).toContainText(draft);
  await expect(code).toContainText("$XHUB_API_KEY");
  await expect(code).not.toContainText(key);
  if (process.env.E2E_RUN_DIR) {
    await page.screenshot({ path: `${process.env.E2E_RUN_DIR}/screenshots/code-${locale}.png` });
  }
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await dialog.getByRole("button", { name: tr("myModels.copyExample"), exact: true }).click();
  const command = await page.evaluate(() => navigator.clipboard.readText());
  expect(command).toBe(await code.textContent());
  const url = command.match(/curl --request POST '([^']+)'/)?.[1];
  expect(url, "真实调用代码必须含 POST 网关地址").toBeTruthy();
  expect(["localhost", "127.0.0.1", "::1"], "代码实测只允许隔离网关").toContain(new URL(url!).hostname);
  const response = await promisify(execFile)("bash", ["-c", command], {
    env: { ...process.env, XHUB_API_KEY: key },
    timeout: 30_000,
    maxBuffer: 1024 * 1024,
  });
  expect(response.stdout, "从代码弹窗复制出的命令必须真实调用成功").toContain("e2e-ok");
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(page.getByTestId("chat-composer-input")).toHaveValue(draft);
}
