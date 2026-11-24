import { expect, test } from "@playwright/test";
import { GATEWAY, UPSTREAM, loginAdmin, sessionBearer, stableGoto } from "../../helpers";

/** 经真实网关验证空响应、出站断开、空错误正文和恢复后的账单，并在浏览器核对双语诊断。
 * 使用独立数据库与本地上游，不依赖生产密钥；finally 删除模型和密钥，运行器清理 schema。 */
test("empty native response fails without replay and leaves confirmed billing intact", async ({ page }) => {
  await loginAdmin(page);
  const headers = { Authorization: "Bearer " + await sessionBearer(page) };
  const name = "empty-response-" + Date.now();
  let deployment = "", token = "";
  try {
    const model = await page.request.post(GATEWAY + "/model/new", { headers, data: {
      model_name: name,
      litellm_params: { model: "e2e-empty-messages", api_base: UPSTREAM, api_key: "sk-fake", custom_llm_provider: "anthropic", input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 },
      model_info: { transport: "bypass_anthropic_messages", endpoint_types: ["bypass:anthropic-messages"], pricing_source: "manual" },
    } });
    expect(model.status(), await model.text()).toBe(200);
    deployment = (await model.json()).model_info.id;
    const minted = await page.request.post(GATEWAY + "/key/generate", { headers, data: { key_alias: name, models: [name] } });
    expect(minted.status(), await minted.text()).toBe(200);
    const key = await minted.json(); token = key.token_id;
    const count = async () => (await (await page.request.get(UPSTREAM + "/e2e/request-count")).json()).requests as number;
    const before = await count();
    const result = await page.evaluate(async ({ secret, name }) => {
      // 通过浏览器访问实际网关，返回线上的状态、关联 ID 和原始响应以便同数据库记录对照。
      const send = async (content: string, stream = false) => {
        const response = await fetch("/gw/bypass/anthropic/v1/messages", {
          method: "POST", headers: { "x-api-key": secret, "anthropic-version": "2023-06-01", "content-type": "application/json" },
          body: JSON.stringify({ model: name, messages: [{ role: "user", content }], max_tokens: 32, stream }),
        });
        return { status: response.status, id: response.headers.get("x-litellm-call-id"), body: await response.text() };
      };
      return {
        failed: await send("Trigger empty response."),
        recovered: await send("Recover manually."),
        disconnected: await send("Disconnect before response."),
        emptyError: await send("Return an empty 502."),
        interruptedStream: await send("End the stream early.", true),
      };
    }, { secret: key.key, name });
    expect(result.failed.status, result.failed.body).toBe(502);
    expect(result.failed.body).toContain("upstream_error");
    expect(result.recovered.status, result.recovered.body).toBe(200);
    expect(result.recovered.body).toContain("Recovered");
    expect(result.failed.id).toBeTruthy();
    expect(result.recovered.id).toBeTruthy();
    expect(result.disconnected.status, result.disconnected.body).toBe(502);
    expect(result.disconnected.body).toContain("upstream request failed");
    expect(result.emptyError).toMatchObject({ status: 502, body: "" });
    expect(result.interruptedStream).toMatchObject({ status: 200, body: expect.stringContaining("event: ping") });
    expect(await count() - before).toBe(5);
    const detail = async (id: string | null) => {
      expect(id).toBeTruthy();
      const response = await page.request.get(GATEWAY + "/spend/logs/ui/" + id, { headers });
      return response.ok() ? await response.json() : null;
    };
    await expect.poll(async () => (await detail(result.failed.id))?.status).toBe("error");
    await expect.poll(async () => Number((await detail(result.failed.id))?.spend)).toBe(0);
    await expect.poll(async () => Number((await detail(result.recovered.id))?.spend)).toBeCloseTo(0.000012, 10);
    await expect.poll(async () => (await detail(result.disconnected.id))?.metadata?.upstream_response).toMatchObject({
      endpoint: "/v1/messages", status_code: 0, failure: { phase: "upstream_request", transport_error: expect.stringContaining("EOF") },
    });
    await expect.poll(async () => (await detail(result.emptyError.id))?.error).toContain("upstream returned HTTP 502 without an error body");
    await expect.poll(async () => (await detail(result.interruptedStream.id))?.metadata).toMatchObject({
      http_status: 502, upstream_response: { status_code: 200, failure: { phase: "response_transfer" } },
    });
    await stableGoto(page, "/logs?log_id=" + result.failed.id);
    await expect(page.getByRole("dialog")).toContainText("502");
    await expect(page.getByRole("dialog")).toContainText("HTTP 状态码");
    await page.keyboard.press("Escape");
    await stableGoto(page, "/logs?log_id=" + result.disconnected.id);
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("alert")).toContainText("EOF");
    await expect(dialog.getByRole("region", { name: "完整错误详情" })).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "English", exact: true }).click();
    await stableGoto(page, "/logs?log_id=" + result.emptyError.id);
    await expect(dialog.getByRole("alert")).toContainText("Upstream returned HTTP 502 without an error body.");
    await expect(dialog).toContainText("HTTP Status");
    await dialog.getByRole("tab", { name: "JSON" }).click();
    const json = dialog.getByRole("tabpanel", { name: "JSON", exact: true });
    await expect(json.getByText("No headers recorded", { exact: true })).toBeVisible();
    await expect(json.getByText("No request body recorded", { exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    await stableGoto(page, "/logs?log_id=" + result.interruptedStream.id);
    await expect(dialog.getByRole("alert")).toContainText("native stream ended without successful completion");
  } finally {
    if (token) expect((await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [token] } })).status()).toBe(200);
    if (deployment) expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id: deployment } })).status()).toBe(200);
  }
});
