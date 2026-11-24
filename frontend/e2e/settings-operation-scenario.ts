import { matchesGatewayResponse } from "./gateway-response.cjs";
import { expect } from "@playwright/test";
import { createBoundaryPolicy, test } from "./policy-config-service-scenario";
import { GATEWAY, UPSTREAM, loginAdmin, sessionBearer } from "./helpers";
import { translate, type Locale } from "../src/i18n/translate";

// 独立列举公开接口范围；不从服务端实现表读取，避免同时漏掉相同入口。
const settingsRoutes = [
  "GET /config/field/info",
  "GET /config/yaml",
  "GET /get/default_team_settings",
  "GET /get/internal_user_settings",
  "GET /get/ui_settings",
  "GET /get/user_banner",
  "GET /get_favicon",
  "GET /get_image",
  "GET /get_logo_url",
  "GET /settings",
  "GET /sso/get/ui_settings",
  "PATCH /update/default_team_settings",
  "PATCH /update/internal_user_settings",
  "PATCH /update/ui_settings",
  "PATCH /update/user_banner"
];

/** 注册C3设置入口的双语真实浏览器流程；locale选择语言，返回注册结果，无生产凭据。
 * 通过真实 API 建策略，验证旧设置接口返回明确错误并保留策略及上游状态，随后通过导航进入调试台进行真实对话。
 * 策略由独立夹具删除，数据库 schema 由运行器清理。
 */
export function registerSettingsOperationBoundary(locale: Locale) {
  test("unsupported settings preserve configuration and chat " + locale, async ({ page, policyResources: resources }) => {
    const tr = (key: string) => translate(locale, key);
    await loginAdmin(page);
    await page.getByRole("button", { name: locale === "en" ? "English" : "中文", exact: true }).click();
    const headers = resources.headers = { Authorization: "Bearer " + await sessionBearer(page) };
    const { policyID, readPath } = await createBoundaryPolicy(page, resources);
    const original = await page.request.get(readPath, { headers });
    expect(original.status()).toBe(200);
    const saved = await original.json();
    const before = await page.request.get(UPSTREAM + "/e2e/request-count");
    expect(before.status()).toBe(200);
    const calls = await before.json();
    for (const route of settingsRoutes) {
      const [method, template] = route.split(" ");
      const path = template.replace(/\{[^}]+\}/g, policyID);
      const response = await page.request.fetch(GATEWAY + path, { method, headers, data: { private_input: "do-not-echo" } });
      expect(response.status(), route + ": " + await response.text()).toBe(501);
      expect(response.headers()["cache-control"]).toBe("no-store");
      expect(await response.json()).toMatchObject({ error: { type: "unsupported_settings_operation" } });
      expect(await response.text()).not.toContain("do-not-echo");
    }
    const after = await page.request.get(UPSTREAM + "/e2e/request-count");
    expect(after.status()).toBe(200);
    expect(await after.json()).toEqual(calls);
    const preserved = await page.request.get(readPath, { headers });
    expect(preserved.status()).toBe(200);
    expect(await preserved.json()).toEqual(saved);
    await page.getByRole("link", { name: tr("nav.playground"), exact: true }).click();
    await expect(page).toHaveURL(/\/ui\/playground\/?$/);
    await page.getByPlaceholder(tr("Select a Model"), { exact: true }).click();
    await page.getByRole("option", { name: "gpt-4o-mini", exact: true }).click();
    await page.getByPlaceholder(tr("Select an endpoint"), { exact: true }).click();
    await page.getByRole("option").filter({ has: page.getByText("/v1/chat/completions", { exact: true }) }).click();
    const prompt = "Verify ordinary chat after unavailable legacy settings.";
    await page.getByTestId("chat-composer-input").fill(prompt);
    const chat = page.waitForResponse(r => ["/v1/chat/completions", "/chat/completions"].some(endpoint => matchesGatewayResponse(r.url(), r.request().method(), endpoint, "POST")));
    await page.getByTestId("chat-send-button").click();
    expect((await chat).status()).toBe(200);
    await expect(page.getByTestId("message-surface").filter({ hasText: "e2e-ok" })).toBeVisible();
    await expect(page.getByTestId("message-surface").filter({ hasText: prompt })).toBeVisible();
    const called = await page.request.get(UPSTREAM + "/e2e/request-count");
    expect(called.status()).toBe(200);
    expect(await called.json()).not.toEqual(calls);
  });
}
