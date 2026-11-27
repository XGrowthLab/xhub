import { expect, test } from "@playwright/test";
import { once } from "node:events";
import { createServer } from "node:http";
import { GATEWAY, loginAdmin, sessionBearer, stableGoto } from "../../helpers";

const beta = "context-management-2025-06-27, thinking-display-updates-2026-08-18";
const rejection = {
  type: "error",
  error: { type: "invalid_request_error", message: "context_management: Extra inputs are not permitted" },
};

/** 验证登录后从浏览器调用标准 Messages，缺少 beta 头时日志可查，带头时成功并按用量入账。
 * 参数 page 为真实浏览器页面，无返回值。使用隔离网关和本地上游，结束删除部署、关闭夹具，运行器回收数据库。 */
test("standard Messages forwards Anthropic beta features", async ({ page }) => {
  const upstream = createServer(async (request, response) => {
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(Buffer.from(chunk));
    const body = JSON.parse(Buffer.concat(chunks).toString());
    response.setHeader("Content-Type", "application/json");
    if (request.url !== "/v1/messages" || body.model !== "vendor-beta" ||
        body.context_management?.edits?.[0]?.type !== "clear_tool_uses_20250919" ||
        body.thinking?.display !== "updates") {
      response.writeHead(422).end(JSON.stringify({ error: { message: "Unexpected upstream request" } }));
      return;
    }
    if (request.headers["anthropic-beta"] !== beta) {
      response.writeHead(400).end(JSON.stringify(rejection));
      return;
    }
    response.end(JSON.stringify({
      id: "msg_beta", type: "message", role: "assistant", model: body.model,
      content: [{ type: "text", text: "OK" }], stop_reason: "end_turn", usage: { input_tokens: 3, output_tokens: 2 },
    }));
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  let deploymentID: string | undefined;
  let headers: Record<string, string> = {};
  try {
    await loginAdmin(page);
    const bearer = await sessionBearer(page);
    headers = { Authorization: "Bearer " + bearer };
    const model = "messages-beta-" + Date.now();
    const port = (upstream.address() as { port: number }).port;
    const created = await page.request.post(GATEWAY + "/model/new", {
      headers,
      data: {
        model_name: model,
        litellm_params: {
          model: "vendor-beta", custom_llm_provider: "anthropic", api_base: "http://127.0.0.1:" + port,
          api_key: "fixture-key", input_cost_per_token: 0.000001, output_cost_per_token: 0.000002,
        },
        model_info: { transport: "bypass_anthropic_messages", endpoint_types: ["messages"], pricing_source: "manual" },
      },
    });
    expect(created.status(), await created.text()).toBe(200);
    deploymentID = (await created.json()).model_info.id;

    /** 通过控制台代理调用 Messages。betaHeader 为可选 beta 值，返回 HTTP 状态、正文及用于打开日志的 ID。 */
    const invoke = (betaHeader?: string) => page.evaluate(async ({ bearer, model, betaHeader }) => {
      // eslint-disable-next-line no-restricted-syntax
      const response = await fetch("/gw/v1/messages", {
        method: "POST",
        headers: {
          Authorization: "Bearer " + bearer,
          "Content-Type": "application/json",
          "Cache-Control": "no-store",
          ...(betaHeader ? { "Anthropic-Beta": betaHeader } : {}),
        },
        body: JSON.stringify({
          model, max_tokens: 64,
          messages: [{ role: "user", content: "Reply with OK." }],
          context_management: { edits: [{ type: "clear_tool_uses_20250919" }] },
          thinking: { type: "adaptive", display: "updates" },
        }),
      });
      return { status: response.status, body: await response.json(), callID: response.headers.get("x-litellm-call-id") };
    }, { bearer, model, betaHeader });

    const failed = await invoke();
    expect(failed.status).toBe(400);
    expect(failed.body).toEqual(rejection);
    expect(failed.callID).toBeTruthy();
    await stableGoto(page, "/logs?log_id=" + failed.callID);
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText(rejection.error.message);

    const accepted = await invoke(beta);
    expect(accepted.status).toBe(200);
    expect(accepted.body).toMatchObject({ model, content: [{ type: "text", text: "OK" }] });
    expect(accepted.callID).toBeTruthy();
    const logURL = GATEWAY + "/spend/logs/ui/" + accepted.callID;
    await expect.poll(async () => (await page.request.get(logURL, { headers })).status()).toBe(200);
    const log = await (await page.request.get(logURL, { headers })).json();
    expect(log.status).toBe("success");
    expect(log.spend).toBeCloseTo(0.000007, 10);
  } finally {
    try {
      if (deploymentID) {
        const deleted = await page.request.post(GATEWAY + "/model/delete", { headers, data: { id: deploymentID } });
        expect(deleted.status()).toBe(200);
      }
    } finally {
      upstream.closeAllConnections();
      await new Promise<void>((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
    }
  }
});
