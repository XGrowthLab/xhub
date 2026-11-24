import { expect, test } from "@playwright/test";
import { translate, type Locale } from "../src/i18n/translate";
import { CONSOLE_HTTP, GATEWAY, loginAdmin, sessionBearer, stableGoto, watchGateway } from "./helpers";

const retiredConsolePaths = [
  "/ui/agents",
  "/ui/workflows",
  "/ui/memory",
  "/ui/tool-policies",
  "/ui/budgets",
  "/ui/policies",
  "/ui/vector-stores",
  "/ui/transform-request",
  "/ui/ui-theme",
  "/ui/chat",
  "/ui/chat/api-keys",
  "/ui/chat/credentials",
  "/ui/chat/integrations",
  "/ui/chat/logs",
  "/ui/chat/usage",
  "/ui/connect",
  "/ui/retired",
  "/ui/tag-management",
];

/** 以两种界面语言验证真实控制台的旧入口退役；locale 为界面语言，无返回。
 * 前置隔离网关与真实管理员，覆盖未登录直达、已登录的书签和导航、用量与文档。运行器清理上下文和 schema。 */
export function registerLegacyRoutes(locale: Locale) {
  test("旧工作区下线并保留文档和用量入口 " + locale, async ({ page }) => {
    const retired = await page.goto("/ui/chat");
    expect(retired?.status()).toBe(410);
    expect(await retired?.text()).toBe("");

    await loginAdmin(page);
    const monitor = watchGateway(page);
    const label = (key: string) => translate(locale, key);
    if (locale === "en") await page.getByRole("button", { name: "English", exact: true }).click();
    await expect(page.getByRole("link", { name: label("nav.playground"), exact: true })).toBeVisible();
    await expect(page.getByText(label("header.viewGateway"), { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: label("header.viewGateway"), exact: true })).toHaveCount(0);
    await expect(page.getByRole("link", { name: label("Chat"), exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: label("Chat"), exact: true })).toHaveCount(0);

    await stableGoto(page, "/?page=chat&from=legacy");
    await expect(page).toHaveURL(/\/ui\/playground\/?\?from=legacy$/);
    await expect(page.getByRole("link", { name: label("nav.playground"), exact: true, disabled: false })).toHaveAttribute(
      "aria-current",
      "page",
    );

    for (const route of retiredConsolePaths) {
      const response = await page.request.get(CONSOLE_HTTP + route, { maxRedirects: 0 });
      expect(response.status(), route).toBe(410);
      expect(response.headers()["cache-control"], route).toBe("no-store");
      expect(response.headers()["location"], route).toBeUndefined();
      expect(await response.text(), route).toBe("");
    }
    for (const route of ["/chat", "/chat/integrations", "/connect", "/tag-management"]) {
      const response = await page.request.head(CONSOLE_HTTP + route, { maxRedirects: 0 });
      expect(response.status(), route).toBe(410);
    }

    await monitor.assertOk();
    await stableGoto(page, "/api-reference?from=legacy");
    await expect(page).toHaveURL(/\/docs\/api\/models\?from=legacy$/);
    await expect(page.getByRole("heading", { name: label("docs.articles.models.title"), exact: true })).toBeVisible();
    await monitor.assertOk();

    await stableGoto(page, "/old-usage");
    await expect(page).toHaveURL(/\/ui\/usage\/?$/);
    await expect(page.getByRole("tab", { name: label("pages.usage.cost"), exact: true })).toBeVisible();
    await monitor.assertOk();
  });
}

/** 注册浏览器同源 API 契约；无参数和返回值。用真实登录后的页面访问 API，前置隔离网关。
 * 验证同名接口方法不被迁移重写，真实策略写入可读回；finally 删除策略。 */
export function registerLegacyRouteGateway() {
  test("旧界面退役后同名网关 API 保持可用", async ({ page, request }) => {
    await loginAdmin(page);
    const headers = { Authorization: `Bearer ${await sessionBearer(page)}` };
    for (const path of ["/budgets", "/gw/budgets"]) {
      const read = await page.request.get(CONSOLE_HTTP + path, { headers });
      expect(read.status(), await read.text()).toBe(200);
      expect(read.headers()["content-type"]).toMatch(/json/);
      expect((await read.json()).data).toEqual(expect.any(Array));
    }

    const id = `console-alias-${Date.now()}`;
    const created = await page.request.post(CONSOLE_HTTP + "/ui/policies", {
      headers,
      data: { policy_id: id, name: "Console alias test" },
    });
    expect(created.status(), await created.text()).toBe(200);
    try {
      const saved = await page.request.get(GATEWAY + "/policies/" + id, { headers });
      expect(saved.status(), await saved.text()).toBe(200);
      expect(await saved.json()).toMatchObject({ policy_id: id });
    } finally {
      const removed = await page.request.delete(CONSOLE_HTTP + "/ui/policies/" + id, { headers });
      expect(removed.status(), await removed.text()).toBe(200);
    }

    for (const endpoint of ["/chat/completions", "/ui/chat/completions", "/gw/v1/chat/completions"]) {
      const unauthenticated = await request.post(CONSOLE_HTTP + endpoint, {
        data: {
          model: "gpt-4o-mini",
          messages: [{ role: "user", content: "Verify gateway authentication is still required." }],
        },
      });
      expect(unauthenticated.status(), await unauthenticated.text()).toBe(401);
      expect((await unauthenticated.json()).error).toBeDefined();
    }
  });
}
