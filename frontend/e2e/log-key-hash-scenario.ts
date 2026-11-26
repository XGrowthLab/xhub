import { createHash } from "node:crypto";

import { expect, test, type Page } from "@playwright/test";

import { GATEWAY, UPSTREAM, loginAdmin, selectFailureSeverity, sessionBearer, stableGoto } from "./helpers";

/** 核对密钥指纹前打开对应表格列。参数是已定位到日志页的浏览器，返回列显示后的 Promise；仅变更临时表格列状态。 */
async function showKeyHashColumn(page: Page) {
  const label = /^(密钥哈希|Key Hash)$/;
  const header = page.getByRole("columnheader", { name: label });
  if (await header.count()) return;
  await page.getByTestId("view-options-trigger").filter({ visible: true }).click();
  await page.getByRole("menuitemcheckbox", { name: label }).click();
  await page.keyboard.press("Escape");
  await expect(header).toBeVisible();
}
/** 用途：注册独立场景「日志密钥 Hash 仅展示且删除密钥后仍可查看日志：普通日志」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC051() {
const failure: boolean = false;

test("日志密钥 Hash 仅展示且删除密钥后仍可查看日志：普通日志", async ({ page }) => {
    await loginAdmin(page);
    const headers = { Authorization: "Bearer " + await sessionBearer(page) };
    const generated = await page.request.post(GATEWAY + "/key/generate", {
      headers, data: { key_alias: "e2e-log-hash-" + Date.now() },
    });
    expect(generated.status(), await generated.text()).toBe(200);
    const key = await generated.json();
    const model = "e2e-log-hash-" + key.token_id;
    const deploymentID = "log-hash-dep-" + key.token_id;
    let modelCreated = false;
    let deleted = false;
    try {
      if (!failure) {
        const created = await page.request.post(GATEWAY + "/model/new", { headers, data: {
          model_name: model,
          litellm_params: { model: "log-hash-upstream", api_base: UPSTREAM, api_key: "sk-fake", deployment_id: deploymentID, custom_llm_provider: "custom", input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 },
          model_info: { id: deploymentID, transport: "bypass_openai_chat", endpoint_types: ["chat"], pricing_source: "manual" },
        } });
        expect(created.status(), await created.text()).toBe(200);
        modelCreated = true;
      }
      const initial = await page.request.get(GATEWAY + "/key/info?key=" + key.token_id, { headers });
      expect(initial.status(), await initial.text()).toBe(200);
      const beforeSpend = Number((await initial.json()).info.spend);
      let usage: { prompt_tokens: number; completion_tokens: number } | undefined;
      const inference = await page.request.post(GATEWAY + "/v1/chat/completions", {
        headers: { Authorization: "Bearer " + key.key },
        data: { model: failure ? "e2e-log-hash-missing-model" : model, messages: [{ role: "user", content: "log hash regression" }] },
      });
      if (failure) {
        expect(inference.status()).toBeGreaterThanOrEqual(400);
      } else {
        expect(inference.status(), await inference.text()).toBe(200);
        const body = await inference.json();
        expect(body.choices[0].message.content).toContain("ok");
        usage = body.usage;
      }
      const responseCost = Number(inference.headers()["x-litellm-response-cost"] ?? 0);
      if (failure) expect(responseCost, "失败请求不得扣费").toBe(0);
      else {
        expect(responseCost, "本地计价上游成功请求应产生费用").toBeGreaterThan(0);
        expect(responseCost, "响应费用必须按专用模型价格和实际用量计算").toBeCloseTo(usage!.prompt_tokens * 0.000001 + usage!.completion_tokens * 0.000002, 10);
      }
      const id = inference.headers()["x-litellm-call-id"];
      expect(id).toBeTruthy();
      await expect.poll(async () => (await page.request.get(GATEWAY + "/spend/logs/ui/" + id, { headers })).status()).toBe(200);
      await expect.poll(async () => {
        const info = await page.request.get(GATEWAY + "/key/info?key=" + key.token_id, { headers });
        expect(info.status()).toBe(200);
        return Number((await info.json()).info.spend) - beforeSpend;
      }, { message: "密钥累计费用必须与本次响应金额一致" }).toBeCloseTo(responseCost, 10);
      const hash = createHash("sha256").update(key.key).digest("hex");
      const keyInfoRequests: string[] = [];
      page.on("request", (request) => {
        // 只观察浏览器发出的请求；测试的 APIRequestContext 用于验证真实契约，不会进入此事件。
        if (/\/(?:v2\/)?key\/info(?:\?|$)/.test(request.url())) keyInfoRequests.push(request.url());
      });

      for (const revoke of [false, true]) {
        if (revoke) {
          const removed = await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [key.token_id] } });
          expect(removed.status(), await removed.text()).toBe(200);
          deleted = true;
          const missing = await page.request.get(GATEWAY + "/key/info?key=" + key.token_id, { headers });
          expect(missing.status(), "删除后的密钥详情必须返回 404").toBe(404);
        }
        const stored = await page.request.get(GATEWAY + "/spend/logs/ui/" + id, { headers });
        expect(stored.status()).toBe(200);
        const bill = await stored.json();
        expect(bill.metadata.user_api_key, "历史日志必须保留原密钥 Hash").toBe(hash);
        expect(bill.api_key, "账单必须保留原凭证身份").toBe(key.token_id);
        expect(Number(bill.spend), "刷新或删除凭证不得重新定价或重复扣费").toBeCloseTo(responseCost, 10);
        if (usage) {
          expect(bill.prompt_tokens, "日志输入用量必须等于响应").toBe(usage.prompt_tokens);
          expect(bill.completion_tokens, "日志输出用量必须等于响应").toBe(usage.completion_tokens);
        }
        await stableGoto(page, "/logs");
        if (failure) {
          await page.getByRole("tab", { name: /错误日志|Error Logs/, exact: true }).click();
          await selectFailureSeverity(page, "warning");
        }
        await showKeyHashColumn(page);
        await page.getByPlaceholder(/按 ID 搜索日志|Search logs by ID/).fill(id);
        const row = page.getByRole("row").filter({ hasText: id });
        await expect(row).toHaveCount(1);
        const cell = row.getByRole("cell", { name: hash, exact: true });
        await expect(cell).toBeVisible();
        await expect(cell.getByRole("button")).toHaveCount(0);
        await expect(cell.getByRole("link")).toHaveCount(0);
        await cell.getByText(hash, { exact: true }).click();
        await expect(page.getByRole("dialog")).toBeVisible();
        await expect(page).toHaveURL(new RegExp("log_id=" + id));
        expect(keyInfoRequests, "日志页不得请求密钥详情").toEqual([]);
        await expect(page.getByText(/获取密钥信息失败|Failed to fetch key info/)).toHaveCount(0);
      }
    } finally {
      try {
        if (!deleted) {
          const cleanup = await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [key.token_id] } });
          expect(cleanup.status(), await cleanup.text()).toBe(200);
        }
      } finally {
        if (modelCreated) {
          const cleanup = await page.request.post(GATEWAY + "/model/delete", { headers, data: { id: deploymentID } });
          expect(cleanup.status(), await cleanup.text()).toBe(200);
        }
      }
    }
  });
}
/** 用途：注册独立场景「日志密钥 Hash 仅展示且删除密钥后仍可查看日志：错误日志」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC052() {
const failure: boolean = true;

test("日志密钥 Hash 仅展示且删除密钥后仍可查看日志：错误日志", async ({ page }) => {
    await loginAdmin(page);
    const headers = { Authorization: "Bearer " + await sessionBearer(page) };
    const generated = await page.request.post(GATEWAY + "/key/generate", {
      headers, data: { key_alias: "e2e-log-hash-" + Date.now() },
    });
    expect(generated.status(), await generated.text()).toBe(200);
    const key = await generated.json();
    const model = "e2e-log-hash-" + key.token_id;
    const deploymentID = "log-hash-dep-" + key.token_id;
    let modelCreated = false;
    let deleted = false;
    try {
      if (!failure) {
        const created = await page.request.post(GATEWAY + "/model/new", { headers, data: {
          model_name: model,
          litellm_params: { model: "log-hash-upstream", api_base: UPSTREAM, api_key: "sk-fake", deployment_id: deploymentID, custom_llm_provider: "custom", input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 },
          model_info: { id: deploymentID, transport: "bypass_openai_chat", endpoint_types: ["chat"], pricing_source: "manual" },
        } });
        expect(created.status(), await created.text()).toBe(200);
        modelCreated = true;
      }
      const initial = await page.request.get(GATEWAY + "/key/info?key=" + key.token_id, { headers });
      expect(initial.status(), await initial.text()).toBe(200);
      const beforeSpend = Number((await initial.json()).info.spend);
      let usage: { prompt_tokens: number; completion_tokens: number } | undefined;
      const inference = await page.request.post(GATEWAY + "/v1/chat/completions", {
        headers: { Authorization: "Bearer " + key.key },
        data: { model: failure ? "e2e-log-hash-missing-model" : model, messages: [{ role: "user", content: "log hash regression" }] },
      });
      if (failure) {
        expect(inference.status()).toBeGreaterThanOrEqual(400);
      } else {
        expect(inference.status(), await inference.text()).toBe(200);
        const body = await inference.json();
        expect(body.choices[0].message.content).toContain("ok");
        usage = body.usage;
      }
      const responseCost = Number(inference.headers()["x-litellm-response-cost"] ?? 0);
      if (failure) expect(responseCost, "失败请求不得扣费").toBe(0);
      else {
        expect(responseCost, "本地计价上游成功请求应产生费用").toBeGreaterThan(0);
        expect(responseCost, "响应费用必须按专用模型价格和实际用量计算").toBeCloseTo(usage!.prompt_tokens * 0.000001 + usage!.completion_tokens * 0.000002, 10);
      }
      const id = inference.headers()["x-litellm-call-id"];
      expect(id).toBeTruthy();
      await expect.poll(async () => (await page.request.get(GATEWAY + "/spend/logs/ui/" + id, { headers })).status()).toBe(200);
      await expect.poll(async () => {
        const info = await page.request.get(GATEWAY + "/key/info?key=" + key.token_id, { headers });
        expect(info.status()).toBe(200);
        return Number((await info.json()).info.spend) - beforeSpend;
      }, { message: "密钥累计费用必须与本次响应金额一致" }).toBeCloseTo(responseCost, 10);
      const hash = createHash("sha256").update(key.key).digest("hex");
      const keyInfoRequests: string[] = [];
      page.on("request", (request) => {
        // 只观察浏览器发出的请求；测试的 APIRequestContext 用于验证真实契约，不会进入此事件。
        if (/\/(?:v2\/)?key\/info(?:\?|$)/.test(request.url())) keyInfoRequests.push(request.url());
      });

      for (const revoke of [false, true]) {
        if (revoke) {
          const removed = await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [key.token_id] } });
          expect(removed.status(), await removed.text()).toBe(200);
          deleted = true;
          const missing = await page.request.get(GATEWAY + "/key/info?key=" + key.token_id, { headers });
          expect(missing.status(), "删除后的密钥详情必须返回 404").toBe(404);
        }
        const stored = await page.request.get(GATEWAY + "/spend/logs/ui/" + id, { headers });
        expect(stored.status()).toBe(200);
        const bill = await stored.json();
        expect(bill.metadata.user_api_key, "历史日志必须保留原密钥 Hash").toBe(hash);
        expect(bill.api_key, "账单必须保留原凭证身份").toBe(key.token_id);
        expect(Number(bill.spend), "刷新或删除凭证不得重新定价或重复扣费").toBeCloseTo(responseCost, 10);
        if (usage) {
          expect(bill.prompt_tokens, "日志输入用量必须等于响应").toBe(usage.prompt_tokens);
          expect(bill.completion_tokens, "日志输出用量必须等于响应").toBe(usage.completion_tokens);
        }
        await stableGoto(page, "/logs");
        if (failure) {
          await page.getByRole("tab", { name: /错误日志|Error Logs/, exact: true }).click();
          await selectFailureSeverity(page, "warning");
        }
        await showKeyHashColumn(page);
        await page.getByPlaceholder(/按 ID 搜索日志|Search logs by ID/).fill(id);
        const row = page.getByRole("row").filter({ hasText: id });
        await expect(row).toHaveCount(1);
        const cell = row.getByRole("cell", { name: hash, exact: true });
        await expect(cell).toBeVisible();
        await expect(cell.getByRole("button")).toHaveCount(0);
        await expect(cell.getByRole("link")).toHaveCount(0);
        await cell.getByText(hash, { exact: true }).click();
        await expect(page.getByRole("dialog")).toBeVisible();
        await expect(page).toHaveURL(new RegExp("log_id=" + id));
        expect(keyInfoRequests, "日志页不得请求密钥详情").toEqual([]);
        await expect(page.getByText(/获取密钥信息失败|Failed to fetch key info/)).toHaveCount(0);
      }
    } finally {
      try {
        if (!deleted) {
          const cleanup = await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [key.token_id] } });
          expect(cleanup.status(), await cleanup.text()).toBe(200);
        }
      } finally {
        if (modelCreated) {
          const cleanup = await page.request.post(GATEWAY + "/model/delete", { headers, data: { id: deploymentID } });
          expect(cleanup.status(), await cleanup.text()).toBe(200);
        }
      }
    }
  });
}
