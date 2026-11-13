import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { translate } from "../src/i18n/translate";
import { GATEWAY, UPSTREAM, login, loginAdmin, sessionBearer, stableGoto } from "./helpers";

/** 注册双语市场消息浏览器闭环；通过本地来源、真实同步和生产扫描器产生数据，隔离 schema 自动清理。 */
export function registerMarketNotificationsScenario(locale: "en" | "zh-CN") {
  test("market notifications are admin only " + locale, async ({ page }) => {
    await loginAdmin(page);
    const admin = { Authorization: "Bearer " + await sessionBearer(page) };
    const control = JSON.parse(readFileSync(join(process.env.E2E_RUN_DIR!, "enterprise-fixture.json"), "utf8")).control;
    const email = `market-notice-${Date.now()}-${locale}@example.com`, password = "fixture-password";
    const newModel = `e2e-market-new-${locale}`;
    let userID = "";
    const setVariant = async (enabled: boolean) => {
      expect((await page.request.post(UPSTREAM + "/e2e/market-variant", { data: { enabled, model: newModel } })).status()).toBe(200);
    };
    const refresh = async () => expect((await page.request.post(GATEWAY + "/reload/model_cost_map", { headers: admin })).status()).toBe(200);
    const scan = async () => expect((await page.request.post(control + "/notifications/market-scan")).status()).toBe(200);
    try {
      const created = await page.request.post(GATEWAY + "/user/new", { headers: admin, data: { user_email: email, password } });
      expect(created.status(), await created.text()).toBe(200); userID = (await created.json()).user_id;
      await setVariant(false); await refresh(); await scan();
      const beforeResponse = await page.request.get(GATEWAY + "/notifications?limit=100", { headers: admin });
      expect(beforeResponse.status()).toBe(200);
      const before = new Set((await beforeResponse.json()).items.map((item: { id: string }) => item.id));
      await setVariant(true); await refresh(); await scan(); await scan();
      const response = await page.request.get(GATEWAY + "/notifications?limit=100", { headers: admin });
      expect(response.status()).toBe(200);
      const createdMessages = (await response.json()).items.filter((item: { id: string; type: string }) => item.type === "market_update" && !before.has(item.id));
      expect(createdMessages).toHaveLength(1);
      expect(createdMessages[0].data.changes).toEqual(expect.arrayContaining([
        expect.objectContaining({ kind: "added", model: newModel }),
        expect.objectContaining({ kind: "price_changed", model: "e2e-market-chat" }),
      ]));
      if (locale === "en") await page.getByRole("button", { name: "English", exact: true }).click();
      await stableGoto(page, "/message-center");
      const tr = (key: string) => translate(locale, key);
      await expect(page.getByRole("heading", { name: tr("notifications.marketTitle") }).first()).toBeVisible();
      await expect(page.getByText(`${tr("notifications.marketKind.added")}: ${newModel}`).first()).toBeVisible();
      await expect(page.getByText(`${tr("notifications.marketKind.price_changed")}: e2e-market-chat`).first()).toBeVisible();
      await expect(page.getByText(tr("notifications.marketPriceHelp")).first()).toBeVisible();
      await expect(page.getByRole("link", { name: tr("notifications.marketLink") }).first()).toHaveAttribute("href", "/ui/price-data");
      const changedPrice = page.getByRole("listitem").filter({ hasText: `${tr("notifications.marketKind.price_changed")}: e2e-market-chat` }).first();
      await changedPrice.getByText(tr("notifications.marketPriceDetails")).click();
      await expect(changedPrice.getByText('"unit_price_usd": 2')).toBeVisible();
      await expect(changedPrice.getByText('"unit_price_usd": 3')).toBeVisible();
      await login(page, email, password, locale);
      await expect(page.getByText(translate(locale, "nav.apiKeys")).first()).toBeVisible();
      const token = await sessionBearer(page);
      const ordinary = { Authorization: "Bearer " + token };
      const list = await page.request.get(GATEWAY + "/notifications?limit=100", { headers: ordinary });
      expect((await list.json()).items.filter((item: { type: string }) => item.type === "market_update")).toHaveLength(0);
      expect((await page.request.post(GATEWAY + "/notifications/read", { headers: ordinary, data: { id: createdMessages[0].id } })).status()).toBe(404);
    } finally {
      await setVariant(false);
      if (userID) expect((await page.request.post(GATEWAY + "/user/delete", { headers: admin, data: { user_id: userID } })).status()).toBe(200);
    }
  });
}
