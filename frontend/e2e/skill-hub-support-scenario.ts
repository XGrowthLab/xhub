import { expect, test } from "@playwright/test";
import { translate, type Locale } from "../src/i18n/translate";
import { GATEWAY, UPSTREAM, loginAdmin, stableGoto } from "./helpers";

/** 注册旧 Skill 目录退役后的双语模型调试流程；locale 指定界面语言，真实网关与控制台返回各自的状态。
 * 检查旧目录维持明确失败，旧页面无内容；随后真实模型请求抵达本地上游。
 * 不创建Skill配置，运行器负责测试会话和schema清理；不调用生产或外部插件仓库。
 */
export function registerSkillHubSupport(locale: Locale) {
  test("retired skill hub preserves playground inference " + locale, async ({ page }) => {
    const tr = (key: string) => translate(locale, key);
    await loginAdmin(page);
    await page.getByRole("button", { name: locale === "en" ? "English" : "中文", exact: true }).click();
    const discovery = await page.request.get(GATEWAY + "/public/skill_hub");
    expect(discovery.status()).toBe(404);
    const writes: string[] = [];
    page.on("request", req => {
      if (req.method() !== "GET" && new URL(req.url()).pathname.includes("claude-code")) writes.push(req.url());
    });
    const retired = await page.goto("/ui/model_hub");
    expect(retired?.status()).toBe(410);
    expect(await retired?.text()).toBe("");
    expect(writes).toEqual([]);
    await stableGoto(page, "/playground");
    await page.getByPlaceholder(tr("Select a Model"), { exact: true }).click();
    await page.getByRole("option", { name: "gpt-4o-mini", exact: true }).click();
    await page.getByPlaceholder(tr("Select an endpoint"), { exact: true }).click();
    await page.getByRole("option").filter({ has: page.getByText("/v1/chat/completions", { exact: true }) }).click();
    const before = await page.request.get(UPSTREAM + "/e2e/request-count");
    expect(before.status()).toBe(200);
    const count = (await before.json()).requests;
    const prompt = "Verify the Skill support notice does not prevent this conversation.";
    await page.getByTestId("chat-composer-input").fill(prompt);
    const inference = page.waitForResponse(response => new URL(response.url()).pathname.endsWith("/chat/completions") && response.request().method() === "POST");
    await page.getByTestId("chat-send-button").click();
    expect((await inference).status()).toBe(200);
    await expect(page.getByTestId("message-surface").filter({ hasText: "e2e-ok" })).toBeVisible();
    await expect(page.getByTestId("message-surface").filter({ hasText: prompt })).toBeVisible();
    const after = await page.request.get(UPSTREAM + "/e2e/request-count");
    expect(after.status()).toBe(200);
    expect((await after.json()).requests).toBe(count + 1);
  });
}
