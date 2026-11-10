import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { translate } from "../src/i18n/translate";
import { GATEWAY, UPSTREAM, loginAdmin, sessionBearer, stableGoto } from "./helpers";

// 验证冲突账单保留时，浏览器仍可看到后续真实流式请求和已结算费用；测试使用私有数据库与独占 Redis。
export function registerQueueConflictScenario(locale: "en" | "zh-CN") {
  for (const kind of ["owner", "missing"] as const)
  test("queue conflict does not block later logs " + locale + " " + kind, async ({ page }) => {
    test.setTimeout(150_000);
    if (process.env.E2E_REDIS !== "1") throw new Error("E2E_REDIS=1 is required for the isolated queue test");
    const control = JSON.parse(readFileSync(join(process.env.E2E_RUN_DIR!, "enterprise-fixture.json"), "utf8")).control;
    await loginAdmin(page);
    const headers = { Authorization: "Bearer " + await sessionBearer(page) };
    await page.getByRole("button", { name: locale === "en" ? "English" : translate("en", "language.zhCN"), exact: true }).click();
    const tr = (key: string) => translate(locale, key);
    const name = `queue-recovery-${locale}-${Date.now()}`;
    const conflictID = `e2e-queue-conflict-${locale}-${kind}-${Date.now()}`;
    let modelID = "", keyID = "", started = false;
    try {
      const model = await page.request.post(GATEWAY + "/model/new", { headers, data: {
        model_name: name,
        litellm_params: { model: name, custom_llm_provider: "openai", api_base: UPSTREAM, api_key: "sk-fake", input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 },
        model_info: { transport: "bypass_openai_chat", pricing_source: "manual" },
      } });
      expect(model.status(), await model.text()).toBe(200);
      modelID = (await model.json()).model_info.id;
      const keyResponse = await page.request.post(GATEWAY + "/key/generate", { headers, data: { key_alias: name } });
      expect(keyResponse.status(), await keyResponse.text()).toBe(200);
      const key = await keyResponse.json(); keyID = key.token_id;
      const setup = await page.request.post(control + "/usage/queue-conflict", { data: { action: kind === "missing" ? "start-missing" : "start", id: conflictID } });
      expect(setup.status(), await setup.text()).toBe(200); started = true;
      const reply = await page.request.post(GATEWAY + "/v1/chat/completions", { headers: { Authorization: "Bearer " + key.key }, data: {
        model: name, stream: true, messages: [{ role: "user", content: "Verify queue recovery." }],
      } });
      expect(reply.status(), await reply.text()).toBe(200);
      expect(await reply.text()).toContain("e2e-ok");
      const callID = reply.headers()["x-litellm-call-id"];
      await expect.poll(async () => {
        await page.request.post(control + "/usage/queue-conflict", { data: { action: "flush", id: conflictID } });
        return (await page.request.get(GATEWAY + "/spend/logs/ui/" + callID, { headers })).status();
      }).toBe(200);
      const detail = await (await page.request.get(GATEWAY + "/spend/logs/ui/" + callID, { headers })).json();
      expect(detail.status).toBe("success");
      expect(detail.spend).toBeGreaterThan(0);
      await stableGoto(page, "/logs?log_id=" + encodeURIComponent(callID));
      const dialog = page.getByRole("dialog");
      await expect(dialog.getByText(name, { exact: true }).first()).toBeVisible();
      await expect(dialog.getByText(tr("Cost"), { exact: true }).first()).toBeVisible();
    } finally {
      if (started) {
        const result = await page.request.post(control + "/usage/queue-conflict", { data: { action: kind === "missing" ? "resolve-missing" : "resolve", id: conflictID } });
        expect(result.status(), await result.text()).toBe(200);
      }
      if (keyID) expect((await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [keyID] } })).status()).toBe(200);
      if (modelID) expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id: modelID } })).status()).toBe(200);
    }
  });
}

// 验证真实同步响应已交付但数据库确认失败时，先执行过期恢复仍保留确认费用；中英文日志均能查看。
// 前置独占 Redis/私有 PG/本地供应商；finally 解除故障并删除密钥及模型，账单随私有 schema 回收。
export function registerConfirmedRecoveryScenario(locale: "en" | "zh-CN") {
  test("confirmed response keeps its charge during recovery " + locale, async ({ page }) => {
    if (process.env.E2E_REDIS !== "1") throw new Error("E2E_REDIS=1 is required");
    const control = JSON.parse(readFileSync(join(process.env.E2E_RUN_DIR!, "enterprise-fixture.json"), "utf8")).control;
    await loginAdmin(page);
    const headers = { Authorization: "Bearer " + await sessionBearer(page) };
    await page.getByRole("button", { name: locale === "en" ? "English" : translate("en", "language.zhCN"), exact: true }).click();
    const name = `confirmed-recovery-${locale}-${Date.now()}`;
    const id = `e2e-queue-conflict-confirmed-${locale}-${Date.now()}`;
    let modelID = "", keyID = "";
    try {
      const model = await page.request.post(GATEWAY + "/model/new", { headers, data: {
        model_name: name,
        litellm_params: { model: name, custom_llm_provider: "openai", api_base: UPSTREAM, api_key: "sk-fake", input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 },
        model_info: { transport: "bypass_openai_chat", pricing_source: "manual" },
      } });
      expect(model.status(), await model.text()).toBe(200);
      modelID = (await model.json()).model_info.id;
      const generated = await page.request.post(GATEWAY + "/key/generate", { headers, data: { key_alias: name } });
      expect(generated.status(), await generated.text()).toBe(200);
      const key = await generated.json(); keyID = key.token_id;
      const block = await page.request.post(control + "/usage/queue-conflict", { data: { action: "block-confirmation", id } });
      expect(block.status(), await block.text()).toBe(200);
      const response = await page.request.post(GATEWAY + "/v1/chat/completions", { headers: { Authorization: "Bearer " + key.key }, data: {
        model: name, stream: false, messages: [{ role: "user", content: "Verify confirmed billing recovery." }],
      } });
      expect(response.status(), await response.text()).toBe(200);
      expect(await response.text()).toContain("e2e-ok");
      const requestID = response.headers()["x-litellm-call-id"];
      // HTTP 正文已写出不代表异步确认步骤已完成；先观察真实 Redis 冻结快照，再推进过期恢复。
      await expect.poll(async () => {
        const status = await page.request.post(control + "/usage/queue-conflict", { data: { action: "confirmation-status", id, requestID } });
        expect(status.status(), await status.text()).toBe(200);
        return (await status.json()).queued;
      }).toBe(true);
      const recover = await page.request.post(control + "/usage/queue-conflict", { data: { action: "recover-confirmation", id, requestID } });
      expect(recover.status(), await recover.text()).toBe(200);
      const detailResponse = await page.request.get(GATEWAY + "/spend/logs/ui/" + requestID, { headers });
      expect(detailResponse.status(), await detailResponse.text()).toBe(200);
      const detail = await detailResponse.json();
      expect(detail.status).toBe("success");
      expect(detail.spend).toBeGreaterThan(0);
      await stableGoto(page, "/logs?log_id=" + encodeURIComponent(requestID));
      const dialog = page.getByRole("dialog");
      await expect(dialog.getByText(name, { exact: true }).first()).toBeVisible();
      await expect(dialog.getByText(translate(locale, "Cost"), { exact: true }).first()).toBeVisible();
    } finally {
      const unblock = await page.request.post(control + "/usage/queue-conflict", { data: { action: "unblock-confirmation", id } });
      expect(unblock.status(), await unblock.text()).toBe(200);
      if (keyID) expect((await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [keyID] } })).status()).toBe(200);
      if (modelID) expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id: modelID } })).status()).toBe(200);
    }
  });
}
