import { expect, test } from "@playwright/test";

import { GATEWAY, loginAdmin, sessionBearer, stableGoto, t } from "./helpers";
/** 用途：注册独立场景「Playground fetch chat/completions SSE」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC104() {
const stream: boolean = true;
const protocol: string = "chat/completions";

test("Playground fetch chat/completions SSE", async ({ page }) => {
      await loginAdmin(page);
      await page.evaluate((value) => sessionStorage.setItem("streamingEnabled", JSON.stringify(value)), stream);
      await stableGoto(page, "/playground");
      await page.getByPlaceholder(t("Select a Model"), { exact: true }).click();
      await page.getByRole("option", { name: "gpt-4o-mini", exact: true }).click();
      await page.getByPlaceholder(t("Select an endpoint"), { exact: true }).click();
      await page
        .getByRole("option")
        .filter({ hasText: protocol === "v1/messages" ? /^Anthropic Messages\s*\/v1\/messages$/ : /^OpenAI Chat\s*\/v1\/chat\/completions$/ })
        .click();
      await page.getByPlaceholder(t("Type your message... (Shift+Enter for new line)")).filter({ visible: true }).fill("transport-check");
      const pending = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith("/" + protocol) && response.request().method() === "POST",
      );
      await page.getByRole("button", { name: t("Send message") }).click();
      const response = await pending;
      // SSE 响应会被页面持续读取，Chromium 不保证测试端还能二次取得响应体。
      expect(response.status(), "Playground 客户接口应成功返回").toBe(200);
      expect(response.headers()["content-type"], "响应格式应与页面所选流式模式一致").toContain(stream ? "text/event-stream" : "application/json");
      expect(response.request().postDataJSON()).toMatchObject({ model: "gpt-4o-mini", stream });
      expect(response.request().headers().authorization).toMatch(/^Bearer /);
      await expect(page.getByText("e2e-ok", { exact: true })).toBeVisible();
      const callId = response.headers()["x-litellm-call-id"];
      expect(callId, "实际模型调用必须有关联账单").toBeTruthy();
      const headers = { Authorization: "Bearer " + (await sessionBearer(page)) };
      await expect
        .poll(async () => {
          const detail = await page.request.get(GATEWAY + "/spend/logs/ui/" + callId, { headers });
          if (!detail.ok()) return null;
          const log = await detail.json();
          return {
            model: log.model,
            status: log.metadata?.upstream_response?.status_code,
            usageReported: log.metadata?.upstream_response?.usage_reported,
            billingUsage: {
              prompt_tokens: log.metadata?.upstream_response?.billing_usage?.prompt_tokens ?? log.metadata?.upstream_response?.billing_usage?.input_tokens,
              completion_tokens: log.metadata?.upstream_response?.billing_usage?.completion_tokens ?? log.metadata?.upstream_response?.billing_usage?.output_tokens,
            },
          };
        })
        .toMatchObject({ model: "gpt-4o-mini", status: 200, usageReported: true, billingUsage: { prompt_tokens: expect.any(Number), completion_tokens: expect.any(Number) } });
    });
}
/** 用途：注册独立场景「Playground fetch v1/messages SSE」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC105() {
const stream: boolean = true;
const protocol: string = "v1/messages";

test("Playground fetch v1/messages SSE", async ({ page }) => {
      await loginAdmin(page);
      await page.evaluate((value) => sessionStorage.setItem("streamingEnabled", JSON.stringify(value)), stream);
      await stableGoto(page, "/playground");
      await page.getByPlaceholder(t("Select a Model"), { exact: true }).click();
      await page.getByRole("option", { name: "gpt-4o-mini", exact: true }).click();
      await page.getByPlaceholder(t("Select an endpoint"), { exact: true }).click();
      await page
        .getByRole("option")
        .filter({ hasText: protocol === "v1/messages" ? /^Anthropic Messages\s*\/v1\/messages$/ : /^OpenAI Chat\s*\/v1\/chat\/completions$/ })
        .click();
      await page.getByPlaceholder(t("Type your message... (Shift+Enter for new line)")).filter({ visible: true }).fill("transport-check");
      const pending = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith("/" + protocol) && response.request().method() === "POST",
      );
      await page.getByRole("button", { name: t("Send message") }).click();
      const response = await pending;
      // SSE 响应会被页面持续读取，Chromium 不保证测试端还能二次取得响应体。
      expect(response.status(), "Playground 客户接口应成功返回").toBe(200);
      expect(response.headers()["content-type"], "响应格式应与页面所选流式模式一致").toContain(stream ? "text/event-stream" : "application/json");
      expect(response.request().postDataJSON()).toMatchObject({ model: "gpt-4o-mini", stream });
      expect(response.request().headers().authorization).toMatch(/^Bearer /);
      await expect(page.getByText("e2e-ok", { exact: true })).toBeVisible();
      const callId = response.headers()["x-litellm-call-id"];
      expect(callId, "实际模型调用必须有关联账单").toBeTruthy();
      const headers = { Authorization: "Bearer " + (await sessionBearer(page)) };
      await expect
        .poll(async () => {
          const detail = await page.request.get(GATEWAY + "/spend/logs/ui/" + callId, { headers });
          if (!detail.ok()) return null;
          const log = await detail.json();
          return {
            model: log.model,
            status: log.metadata?.upstream_response?.status_code,
            usageReported: log.metadata?.upstream_response?.usage_reported,
            billingUsage: {
              prompt_tokens: log.metadata?.upstream_response?.billing_usage?.prompt_tokens ?? log.metadata?.upstream_response?.billing_usage?.input_tokens,
              completion_tokens: log.metadata?.upstream_response?.billing_usage?.completion_tokens ?? log.metadata?.upstream_response?.billing_usage?.output_tokens,
            },
          };
        })
        .toMatchObject({ model: "gpt-4o-mini", status: 200, usageReported: true, billingUsage: { prompt_tokens: expect.any(Number), completion_tokens: expect.any(Number) } });
    });
}
/** 用途：注册独立场景「Playground fetch chat/completions JSON」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC106() {
const stream: boolean = false;
const protocol: string = "chat/completions";

test("Playground fetch chat/completions JSON", async ({ page }) => {
      await loginAdmin(page);
      await page.evaluate((value) => sessionStorage.setItem("streamingEnabled", JSON.stringify(value)), stream);
      await stableGoto(page, "/playground");
      await page.getByPlaceholder(t("Select a Model"), { exact: true }).click();
      await page.getByRole("option", { name: "gpt-4o-mini", exact: true }).click();
      await page.getByPlaceholder(t("Select an endpoint"), { exact: true }).click();
      await page
        .getByRole("option")
        .filter({ hasText: protocol === "v1/messages" ? /^Anthropic Messages\s*\/v1\/messages$/ : /^OpenAI Chat\s*\/v1\/chat\/completions$/ })
        .click();
      await page.getByPlaceholder(t("Type your message... (Shift+Enter for new line)")).filter({ visible: true }).fill("transport-check");
      const pending = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith("/" + protocol) && response.request().method() === "POST",
      );
      await page.getByRole("button", { name: t("Send message") }).click();
      const response = await pending;
      // SSE 响应会被页面持续读取，Chromium 不保证测试端还能二次取得响应体。
      expect(response.status(), "Playground 客户接口应成功返回").toBe(200);
      expect(response.headers()["content-type"], "响应格式应与页面所选流式模式一致").toContain(stream ? "text/event-stream" : "application/json");
      expect(response.request().postDataJSON()).toMatchObject({ model: "gpt-4o-mini", stream });
      expect(response.request().headers().authorization).toMatch(/^Bearer /);
      await expect(page.getByText("e2e-ok", { exact: true })).toBeVisible();
      const callId = response.headers()["x-litellm-call-id"];
      expect(callId, "实际模型调用必须有关联账单").toBeTruthy();
      const headers = { Authorization: "Bearer " + (await sessionBearer(page)) };
      await expect
        .poll(async () => {
          const detail = await page.request.get(GATEWAY + "/spend/logs/ui/" + callId, { headers });
          if (!detail.ok()) return null;
          const log = await detail.json();
          return {
            model: log.model,
            status: log.metadata?.upstream_response?.status_code,
            usageReported: log.metadata?.upstream_response?.usage_reported,
            billingUsage: {
              prompt_tokens: log.metadata?.upstream_response?.billing_usage?.prompt_tokens ?? log.metadata?.upstream_response?.billing_usage?.input_tokens,
              completion_tokens: log.metadata?.upstream_response?.billing_usage?.completion_tokens ?? log.metadata?.upstream_response?.billing_usage?.output_tokens,
            },
          };
        })
        .toMatchObject({ model: "gpt-4o-mini", status: 200, usageReported: true, billingUsage: { prompt_tokens: expect.any(Number), completion_tokens: expect.any(Number) } });
    });
}
/** 用途：注册独立场景「Playground fetch v1/messages JSON」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC107() {
const stream: boolean = false;
const protocol: string = "v1/messages";

test("Playground fetch v1/messages JSON", async ({ page }) => {
      await loginAdmin(page);
      await page.evaluate((value) => sessionStorage.setItem("streamingEnabled", JSON.stringify(value)), stream);
      await stableGoto(page, "/playground");
      await page.getByPlaceholder(t("Select a Model"), { exact: true }).click();
      await page.getByRole("option", { name: "gpt-4o-mini", exact: true }).click();
      await page.getByPlaceholder(t("Select an endpoint"), { exact: true }).click();
      await page
        .getByRole("option")
        .filter({ hasText: protocol === "v1/messages" ? /^Anthropic Messages\s*\/v1\/messages$/ : /^OpenAI Chat\s*\/v1\/chat\/completions$/ })
        .click();
      await page.getByPlaceholder(t("Type your message... (Shift+Enter for new line)")).filter({ visible: true }).fill("transport-check");
      const pending = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith("/" + protocol) && response.request().method() === "POST",
      );
      await page.getByRole("button", { name: t("Send message") }).click();
      const response = await pending;
      // SSE 响应会被页面持续读取，Chromium 不保证测试端还能二次取得响应体。
      expect(response.status(), "Playground 客户接口应成功返回").toBe(200);
      expect(response.headers()["content-type"], "响应格式应与页面所选流式模式一致").toContain(stream ? "text/event-stream" : "application/json");
      expect(response.request().postDataJSON()).toMatchObject({ model: "gpt-4o-mini", stream });
      expect(response.request().headers().authorization).toMatch(/^Bearer /);
      await expect(page.getByText("e2e-ok", { exact: true })).toBeVisible();
      const callId = response.headers()["x-litellm-call-id"];
      expect(callId, "实际模型调用必须有关联账单").toBeTruthy();
      const headers = { Authorization: "Bearer " + (await sessionBearer(page)) };
      await expect
        .poll(async () => {
          const detail = await page.request.get(GATEWAY + "/spend/logs/ui/" + callId, { headers });
          if (!detail.ok()) return null;
          const log = await detail.json();
          return {
            model: log.model,
            status: log.metadata?.upstream_response?.status_code,
            usageReported: log.metadata?.upstream_response?.usage_reported,
            billingUsage: {
              prompt_tokens: log.metadata?.upstream_response?.billing_usage?.prompt_tokens ?? log.metadata?.upstream_response?.billing_usage?.input_tokens,
              completion_tokens: log.metadata?.upstream_response?.billing_usage?.completion_tokens ?? log.metadata?.upstream_response?.billing_usage?.output_tokens,
            },
          };
        })
        .toMatchObject({ model: "gpt-4o-mini", status: 200, usageReported: true, billingUsage: { prompt_tokens: expect.any(Number), completion_tokens: expect.any(Number) } });
    });
}
