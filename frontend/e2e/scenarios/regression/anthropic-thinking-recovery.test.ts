import { expect, test } from "@playwright/test";
import { once } from "node:events";
import { createServer } from "node:http";
import { GATEWAY, loginAdmin, sessionBearer, stableGoto } from "../../helpers";

/** 用真实浏览器、网关及隔离 PostgreSQL 验证同一会话切模型。前置两个本地 Claude 部署，
 * 验收历史可补发、新工具续轮保留当前签名、每次成功只扣费一次。结束时删除部署、关闭上游，
 * 运行器回收数据库 schema 和账单。 */
test("Anthropic bypass recovers stale thinking after a model switch", async ({ page }) => {
  const signatureError = "messages.1.content.0: Invalid `signature` in `thinking` block";
  const received: Array<{ model: string; messages: unknown[] }> = [];
  const upstream = createServer(async (request, response) => {
    const parts: Buffer[] = [];
    for await (const part of request) parts.push(Buffer.from(part));
    const body = JSON.parse(Buffer.concat(parts).toString());
    received.push(body);
    response.setHeader("content-type", "application/json");
    if (request.url !== "/v1/messages" || request.headers["x-api-key"] !== "fixture-key") {
      response.writeHead(401).end(JSON.stringify({ error: { message: "Invalid test deployment" } }));
      return;
    }
    const blocks = [
      { type: "thinking", thinking: "Check the service.", signature: "OLD_SIGNATURE" },
      { type: "redacted_thinking", data: "OLD_REDACTED" },
      { type: "tool_use", id: "check_1", name: "check", input: {} },
    ];
    const history = JSON.stringify(body.messages);
    if (body.model === "vendor-b" && history.includes("OLD_SIGNATURE")) {
      response.writeHead(400).end(JSON.stringify({ type: "error", error: { type: "<nil>", message: signatureError } }));
      return;
    }
    const start = body.model === "vendor-a" && body.messages.length === 1;
    const startNewTool = body.model === "vendor-b" && body.messages.length === 5;
    let content: Record<string, unknown>[];
    if (start) {
      content = blocks;
    } else if (startNewTool) {
      content = [
        { type: "thinking", thinking: "Verify the connection.", signature: "NEW_SIGNATURE" },
        { type: "tool_use", id: "check_2", name: "check", input: {} },
      ];
    } else {
      content = [{ type: "text", text: body.model === "vendor-a" ? "The database timed out." : "Reconnected." }];
    }
    response.end(JSON.stringify({
      id: "msg_test_" + received.length, type: "message", role: "assistant", model: body.model,
      content, stop_reason: start || startNewTool ? "tool_use" : "end_turn", usage: { input_tokens: 8, output_tokens: 2 },
    }));
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  const port = (upstream.address() as { port: number }).port;
  const deploymentIDs: string[] = [];
  let headers: Record<string, string> = {};
  try {
    await loginAdmin(page);
    const bearer = await sessionBearer(page);
    headers = { Authorization: "Bearer " + bearer };
    const models = ["a", "b"].map(label => "thinking-switch-" + label + "-" + Date.now());
    for (const [index, vendor] of ["vendor-a", "vendor-b"].entries()) {
      const created = await page.request.post(GATEWAY + "/model/new", { headers, data: {
        model_name: models[index],
        litellm_params: {
          model: vendor, custom_llm_provider: "anthropic", api_base: `http://127.0.0.1:${port}/v1`,
          api_key: "fixture-key", input_cost_per_token: 0.000001, output_cost_per_token: 0.000002,
        },
        model_info: { transport: "bypass_anthropic_messages", endpoint_types: ["messages"], pricing_source: "manual" },
      } });
      expect(created.status(), await created.text()).toBe(200);
      deploymentIDs.push((await created.json()).model_info.id);
    }
    const history: Array<{ role: string; content: unknown }> = [{ role: "user", content: "Check the service." }];
    /** 用登录会话从真实浏览器发起原生 Messages；参数为目标模型，返回状态、内容和日志 ID。 */
    const invoke = (model: string) => page.evaluate(async ({ bearer, model, messages }) => {
      // eslint-disable-next-line no-restricted-syntax
      const response = await fetch("/gw/bypass/anthropic/v1/messages", {
        method: "POST",
        headers: { Authorization: "Bearer " + bearer, "Content-Type": "application/json", "X-Session-Id": "thinking-switch" },
        body: JSON.stringify({ model, messages, max_tokens: 128, thinking: { type: "adaptive" } }),
      });
      return { status: response.status, data: await response.json(), callID: response.headers.get("x-litellm-call-id") };
    }, { bearer, model, messages: history });

    const start = await invoke(models[0]);
    expect(start.status).toBe(200);
    history.push(
      { role: "assistant", content: start.data.content },
      { role: "user", content: [{ type: "tool_result", tool_use_id: "check_1", content: "Connection timed out." }] },
    );
    const blocked = await invoke(models[1]);
    expect(blocked.status).toBe(400);
    expect(blocked.data.error.message).toBe(signatureError);
    expect(received).toHaveLength(2);
    expect(received[1].messages).toEqual(history);
    await stableGoto(page, "/logs?log_id=" + blocked.callID);
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText("400");
    await page.keyboard.press("Escape");

    const finished = await invoke(models[0]);
    expect(finished.status).toBe(200);
    expect(received[2].messages).toEqual(history);
    history.push(
      { role: "assistant", content: finished.data.content },
      { role: "user", content: "What should I try next?" },
    );
    const newTool = await invoke(models[1]);
    expect(newTool.status).toBe(200);
    expect(newTool.data.content).toContainEqual(expect.objectContaining({ signature: "NEW_SIGNATURE" }));
    expect(received[3].messages).toEqual(history);
    expect(received[4].messages).toEqual(history.map((message, index) =>
      index === 1 ? { ...message, content: [start.data.content[2]] } : message,
    ));
    history.push(
      { role: "assistant", content: newTool.data.content },
      { role: "user", content: [{ type: "tool_result", tool_use_id: "check_2", content: "Connection restored." }] },
    );
    const recovered = await invoke(models[1]);
    expect(recovered.status).toBe(200);
    expect(recovered.data.content).toEqual([{ type: "text", text: "Reconnected." }]);
    expect(received.map(body => body.model)).toEqual([
      "vendor-a", "vendor-b", "vendor-a", "vendor-b", "vendor-b", "vendor-b", "vendor-b",
    ]);
    expect(received[5].messages).toEqual(history);
    expect(received[6].messages).toEqual(history.map((message, index) =>
      index === 1 ? { ...message, content: [start.data.content[2]] } : message,
    ));
    await expect.poll(async () =>
      (await page.request.get(GATEWAY + "/spend/logs/ui/" + recovered.callID, { headers })).status(),
    ).toBe(200);
    const log = await (await page.request.get(GATEWAY + "/spend/logs/ui/" + recovered.callID, { headers })).json();
    expect(log.status).toBe("success");
    expect(log.spend).toBeCloseTo(0.000012, 10);
    await stableGoto(page, "/logs?log_id=" + recovered.callID);
    await expect(page.getByRole("dialog")).toContainText(models[1]);
  } finally {
    try {
      for (const id of deploymentIDs) {
        expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id } })).status()).toBe(200);
      }
    } finally {
      upstream.closeAllConnections();
      await new Promise<void>((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
    }
  }
});
