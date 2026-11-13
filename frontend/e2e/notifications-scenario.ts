import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { translate } from "../src/i18n/translate";
import { GATEWAY, UPSTREAM, login, loginAdmin, sessionBearer, stableGoto } from "./helpers";

/** 注册个人预算消息真实闭环；locale 为界面语言。隔离数据库中创建两个账号及密钥，
 * 使用真实推理、持久化与扫描验证提醒、跨账号隔离和已读，结束删除资源，外层回收 schema。 */
export function registerNotificationsScenario(locale: "en" | "zh-CN") {
  test("personal budget notifications are private " + locale, async ({ page }) => {
    await loginAdmin(page);
    const admin = { Authorization: "Bearer " + await sessionBearer(page) };
    const suffix = Date.now() + "-" + locale;
    const firstEmail = `notice-a-${suffix}@example.com`, secondEmail = `notice-b-${suffix}@example.com`;
    const password = "fixture-password";
    const control = JSON.parse(readFileSync(join(process.env.E2E_RUN_DIR!, "enterprise-fixture.json"), "utf8")).control;
    let first = "", second = "", keyID = "", modelID = "";
    try {
      // 共享默认模型未配置价格：为真实结算单独创建正价部署，避免依赖共享夹具的定价状态。
      const model = `notification-fixture-${suffix}`;
      const createdModel = await page.request.post(GATEWAY + "/model/new", { headers: admin, data: {
        model_name: model,
        litellm_params: { model, custom_llm_provider: "openai", api_base: UPSTREAM, api_key: "sk-fake", input_cost_per_token: 0.01, output_cost_per_token: 0.01 },
        model_info: { transport: "bypass_openai_chat", endpoint_types: ["chat"], pricing_source: "manual" },
      } });
      expect(createdModel.status(), await createdModel.text()).toBe(200);
      modelID = (await createdModel.json()).model_info.id;
      expect(modelID).toBeTruthy();
      const a = await page.request.post(GATEWAY + "/user/new", { headers: admin, data: { user_email: firstEmail, password, max_budget: 100 } });
      expect(a.status(), await a.text()).toBe(200); first = (await a.json()).user_id;
      const b = await page.request.post(GATEWAY + "/user/new", { headers: admin, data: { user_email: secondEmail, password, max_budget: 100 } });
      expect(b.status(), await b.text()).toBe(200); second = (await b.json()).user_id;
      const k = await page.request.post(GATEWAY + "/key/generate", { headers: admin, data: { user_id: first, key_alias: "notification-fixture" } });
      expect(k.status(), await k.text()).toBe(200); const key = await k.json(); keyID = key.token_id;
      const response = await page.request.post(GATEWAY + "/v1/chat/completions", { headers: { Authorization: "Bearer " + key.key }, data: { model, messages: [{ role: "user", content: "Hello" }] } });
      expect(response.status(), await response.text()).toBe(200);
      let spent = 0;
      await expect.poll(async () => {
        const info = await page.request.get(GATEWAY + "/user/info?user_id=" + first, { headers: admin });
        expect(info.status()).toBe(200); spent = Number((await info.json()).user_info.spend); return spent;
      }).toBeGreaterThan(0);
      // 将真实已记账消费置于80%–95%之间，提醒依然由生产扫描器生成。
      const changed = await page.request.post(GATEWAY + "/user/update", { headers: admin, data: { user_id: first, max_budget: spent / 0.9 } });
      expect(changed.status(), await changed.text()).toBe(200);
      expect((await page.request.post(control + "/notifications/scan")).status()).toBe(200);
      expect((await page.request.post(control + "/notifications/scan")).status()).toBe(200);
      if (locale === "en") await page.getByRole("button", { name: "English", exact: true }).click();
      await login(page, firstEmail, password, locale);
      // login 只负责提交表单；等待用户真实登录完成后再访问受保护页面。
      await expect(page.getByText(translate(locale, "nav.apiKeys")).first()).toBeVisible();
      await stableGoto(page, "/message-center");
      const tr = (key: string) => translate(locale, key);
      await expect(page.getByRole("heading", { name: tr("notifications.budgetTitle") })).toBeVisible();
      await expect(page.getByText(translate(locale, "notifications.budgetMessage", { threshold: 80 }))).toBeVisible();
      const firstToken = await sessionBearer(page);
      const list = await page.request.get(GATEWAY + "/notifications?limit=20&offset=0", { headers: { Authorization: "Bearer " + firstToken } });
      expect(list.status(), await list.text()).toBe(200); const result = await list.json();
      expect(result.total).toBe(1); const notification = result.items[0];
      expect(notification.data.threshold).toBe(80); expect(notification.data.spent).toBeCloseTo(spent, 9);
      await page.getByRole("button", { name: tr("notifications.markRead"), exact: true }).click();
      await expect(page.getByText(tr("notifications.read"), { exact: true })).toBeVisible();
      const read = await page.request.get(GATEWAY + "/notifications/unread", { headers: { Authorization: "Bearer " + firstToken } });
      expect((await read.json()).count).toBe(0);
      await login(page, secondEmail, password, locale);
      await expect(page.getByText(translate(locale, "nav.apiKeys")).first()).toBeVisible();
      await stableGoto(page, "/message-center");
      await expect(page.getByText(tr("notifications.empty"))).toBeVisible();
      const secondToken = await sessionBearer(page);
      const forbidden = await page.request.post(GATEWAY + "/notifications/read", { headers: { Authorization: "Bearer " + secondToken }, data: { id: notification.id } });
      expect(forbidden.status()).toBe(404);
      const keyAccess = await page.request.get(GATEWAY + "/notifications", { headers: { Authorization: "Bearer " + key.key } });
      expect([401, 403]).toContain(keyAccess.status());
    } finally {
      if (keyID) expect((await page.request.post(GATEWAY + "/key/delete", { headers: admin, data: { keys: [keyID] } })).status()).toBe(200);
      for (const id of [first, second]) if (id) expect((await page.request.post(GATEWAY + "/user/delete", { headers: admin, data: { user_id: id } })).status()).toBe(200);
      if (modelID) expect((await page.request.post(GATEWAY + "/model/delete", { headers: admin, data: { id: modelID } })).status()).toBe(200);
    }
  });
}
