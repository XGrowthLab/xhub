import { expect, test } from "@playwright/test";
import { translate, type Locale } from "../src/i18n/translate";
import { GATEWAY, loginAdmin, sessionBearer, stableGoto, watchGateway } from "./helpers";
import { recordPage } from "./report";

const retiredAliases = [
  "/agents",
  "/workflows",
  "/memory",
  "/tool-policies",
  "/budgets",
  "/policies",
  "/vector-stores",
  "/transform-request",
  "/ui-theme",
];

/** 注册旧控制台路由的双语浏览器场景；locale 为界面语言，无返回。
 * 前置隔离网关和管理员，依次检查真实路由、旧书签保护、有效链接及 MCP 不可用状态；浏览器和 schema 由运行器清理。 */
export function registerLegacyRoutes(locale: Locale) {
  test("旧控制台书签和 MCP 连接指引 " + locale, async ({ page }) => {
    await page.goto("/ui/agents");
    await expect(page).toHaveURL(/\/ui\/login/);

    await loginAdmin(page);
    const monitor = watchGateway(page);
    await page.evaluate((language) => {
      localStorage.setItem("xhub_locale", language);
      document.cookie = `xhub_locale=${language}; path=/`;
    }, locale);
    const label = (key: string) => translate(locale, key);

    for (const route of ["/retired", ...retiredAliases]) {
      await stableGoto(page, route);
      await expect(page.getByRole("heading", { name: label("legacyRoutes.retiredTitle") })).toBeVisible();
      expect(new URL(page.url()).pathname).toBe(`/ui${route}`);
      await expect(page.getByRole("link", { name: label("legacyRoutes.openConsole") })).toHaveAttribute(
        "href",
        /^\/ui\/?$/,
      );
      // 等完页面发起的可选配置读取，防止下一次文档导航取消还未提取的响应正文。
      await monitor.assertOk();
    }
    recordPage("/retired", "pass", "/retired");

    await page.getByRole("link", { name: label("legacyRoutes.openDocs"), exact: true }).click();
    await expect(page).toHaveURL(/\/docs\/?$/);
    await monitor.assertOk();

    await stableGoto(page, "/api-reference?from=legacy");
    await expect(page).toHaveURL(/\/docs\/api\/models\?from=legacy$/);
    await expect(page.getByRole("heading", { name: label("docs.articles.models.title"), exact: true })).toBeVisible();
    await monitor.assertOk();

    await stableGoto(page, "/tag-management");
    await expect(page.getByRole("heading", { name: label("legacyRoutes.tagsTitle"), exact: true })).toBeVisible();
    await expect(page.getByText(label("legacyRoutes.tagsBody"))).toBeVisible();
    await monitor.assertOk();

    await stableGoto(page, "/chat/credentials");
    await expect(page.getByText(label("connect.credentialsUnavailable"))).toBeVisible();
    await monitor.assertOk();
    await page.getByRole("link", { name: label("connect.openConnections"), exact: true }).click();
    await expect(page).toHaveURL(/\/ui\/connect\/?$/);
    await expect(page.getByRole("status").filter({ hasText: label("connect.serversUnavailable") })).toBeVisible();
    await page.getByRole("tab", { name: label("connect.connected"), exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: label("connect.serversUnavailable") })).toBeVisible();

    await monitor.assertOk();
    await stableGoto(page, "/chat/integrations?from=legacy");
    await expect(page).toHaveURL(/\/ui\/connect\?from=legacy$/);
    await expect(page.getByRole("heading", { name: label("connect.mcpServers"), exact: true })).toBeVisible();
    await expect(page.getByRole("status").filter({ hasText: label("connect.serversUnavailable") })).toBeVisible();
    await monitor.assertOk();
  });
}

/** 注册浏览器同源 API 契约；无参数和返回值。用真实登录后的页面访问 API，前置隔离网关。
 * 验证同名接口方法不被迁移重写，真实策略写入可读回；finally 删除策略。 */
export function registerLegacyRouteGateway() {
  test("旧界面退役后同名网关 API 保持可用", async ({ page }) => {
    await loginAdmin(page);
    const headers = { Authorization: `Bearer ${await sessionBearer(page)}` };
    for (const path of ["/budgets", "/gw/budgets", "/gw/model_hub?page_size=1"]) {
      const read = await page.request.get(path, { headers });
      expect(read.status(), await read.text()).toBe(200);
      expect(read.headers()["content-type"]).toMatch(/json/);
      expect((await read.json()).data).toEqual(expect.any(Array));
    }

    const id = `console-alias-${Date.now()}`;
    const created = await page.request.post("/ui/policies", {
      headers,
      data: { policy_id: id, name: "Console alias test" },
    });
    expect(created.status(), await created.text()).toBe(200);
    try {
      const saved = await page.request.get(GATEWAY + "/policies/" + id, { headers });
      expect(saved.status(), await saved.text()).toBe(200);
      expect(await saved.json()).toMatchObject({ policy_id: id });
    } finally {
      const removed = await page.request.delete("/ui/policies/" + id, { headers });
      expect(removed.status(), await removed.text()).toBe(200);
    }
  });
}
