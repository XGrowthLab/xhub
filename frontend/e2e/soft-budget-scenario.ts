import { expect, test } from "@playwright/test";
import { translate } from "../src/i18n/translate";
import { GATEWAY, UPSTREAM, loginAdmin, sessionBearer, stableGoto } from "./helpers";

/** 按 locale 注册中英文软预算用例；先创建小额密钥，核对已成功调用的扣费、预算拒绝的零增量及页面调额后的新账单。
 * 前置隔离数据库、前端和本地供应商，调用方为两种语言的浏览器测试；无返回值，finally 删除本用例创建的密钥和模型。 */
export function registerSoftBudgetScenario(locale: "en" | "zh-CN") {
  test("soft budget completes requests and stops after exhaustion " + locale, async ({ page }) => {
    await loginAdmin(page);
    const headers = { Authorization: "Bearer " + await sessionBearer(page) };
    await page.getByRole("button", {name: locale === "en" ? "English" : translate("en", "language.zhCN"), exact: true}).click();
    const tr = (key: string) => translate(locale, key);
    const model = "soft-budget-" + locale.toLowerCase() + "-" + Date.now();
    let keyID = "", modelID = "";
    try {
      const created = await page.request.post(GATEWAY + "/model/new", {headers, data: {
        model_name: model, litellm_params: {model, custom_llm_provider: "openai", api_base: UPSTREAM, api_key: "sk-fake", input_cost_per_token: 0.00001, output_cost_per_token: 0.00002},
        model_info: {transport: "bypass_openai_chat", pricing_source: "manual", endpoint_types: ["chat"]},
      }});
      expect(created.status(), await created.text()).toBe(200);
      modelID = (await created.json()).model_info.id;
      await page.getByTestId("create-key-button").click();
      await page.getByLabel(tr("Key Name")).fill("软预算-test-" + locale);
      await page.getByLabel(tr("Max Budget (USD)"), {exact: true}).fill("0.000001");
      const dialog = page.getByRole("dialog");
      const budgetContract = dialog.getByText(tr("hardBudget.contract"), { exact: true });
      await expect(budgetContract).toBeHidden();
      // 创建页的补充说明默认收起；通过真实点击验证可展开，不绕过交互直接读取隐藏内容。
      await dialog.getByText(tr("consoleUi.budgetHelp"), { exact: true }).click();
      await expect(budgetContract).toBeVisible();
      const minted = page.waitForResponse(r => r.url().includes("/key/generate") && r.request().method() === "POST");
      await page.getByRole("button", {name: tr("pages.apiKeys.createSubmit"), exact:true}).click();
      const response = await minted; expect(response.status()).toBe(200);
      const key = await response.json(); keyID = key.token_id;
      await page.keyboard.press("Escape");
      // 各次使用不同正文，避免命中响应缓存而把免费命中误认为一次真实上游扣费。
      const invoke = (content: string) => page.request.post(GATEWAY + "/v1/chat/completions", {headers: {Authorization: "Bearer " + key.key}, data: {model, messages:[{role:"user",content}]}});
      const first = await invoke("Use the initial budget."); expect(first.status(), await first.text()).toBe(200);
      expect(first.headers()["x-xhub-budget-guarantee"]).toBe("soft");
      expect(first.headers()["x-xhub-budget-reservation-id"]).toBeUndefined();
      const info = await (await page.request.get(GATEWAY + "/key/info?key=" + keyID, {headers})).json();
      expect(info.info.spend).toBeGreaterThan(0.000001);
      expect(info.info.budget_pending).toBe(0);
      expect((await invoke("Check the exhausted budget.")).status()).toBe(403);
      const deniedInfo = await page.request.get(GATEWAY + "/key/info?key=" + keyID, {headers});
      expect(deniedInfo.status()).toBe(200);
      expect((await deniedInfo.json()).info.spend).toBe(info.info.spend);
      await stableGoto(page, "/api-keys?key=" + keyID);
      await expect(page.getByText(tr("hardBudget.contract"))).toBeVisible();
      await page.getByLabel(tr("Max Budget (USD)"), {exact: true}).fill("1");
      const updated = page.waitForResponse(r => r.url().includes("/key/update") && r.request().method() === "POST");
      await page.getByRole("button", {name: tr("Save Changes"), exact:true}).click();
      expect((await updated).status()).toBe(200);
      expect((await invoke("Use the increased budget.")).status()).toBe(200);
      // 核对同一把测试密钥的真实账单，允许后台提交稍后可见。
      await expect.poll(async () => {
        const settled = await page.request.get(GATEWAY + "/key/info?key=" + keyID, {headers});
        expect(settled.status()).toBe(200);
        return (await settled.json()).info.spend;
      }).toBeGreaterThan(info.info.spend);
    } finally {
      if (keyID) expect((await page.request.post(GATEWAY + "/key/delete", {headers,data:{keys:[keyID]}})).status()).toBe(200);
      if (modelID) expect((await page.request.post(GATEWAY + "/model/delete", {headers,data:{id:modelID}})).status()).toBe(200);
    }
  });
}
