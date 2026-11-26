import { expect, test } from "@playwright/test";
import { once } from "node:events";
import { createServer } from "node:http";
import { GATEWAY, loginAdmin, sessionBearer, stableGoto } from "../../helpers";

/** 用真实浏览器、网关和数据库验证 Chat/Responses 迁移及错误保真。前置本地协议供应商、
 * 手工价格与独立模型。验证实际外发次数和调用日志，finally 删除模型并关闭供应商，运行器回收 schema。
 */
test("Chat and Responses preserve public history during reasoning recovery", async ({ page }) => {
  const received: Array<{ path: string; body: string }> = [];
  const upstream = createServer(async (request, response) => {
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(Buffer.from(chunk));
    const raw = Buffer.concat(chunks).toString();
    const path = request.url ?? "";
    received.push({ path, body: raw });
    response.setHeader("content-type", "application/json");
    if (request.headers.authorization !== "Bearer history-test-key") {
      response.writeHead(401).end(JSON.stringify({ error: { message: "Invalid fixture key." } }));
      return;
    }
    const parsed = JSON.parse(raw);
    if (raw.includes("STALE_CIPHER") || raw.includes("COMPACT_CIPHER")) {
      const id = raw.includes("COMPACT_CIPHER") ? "cmp_old" : "rs_old";
      response.writeHead(400).end(JSON.stringify({
        error: { code: "invalid_encrypted_content", message: `The encrypted content for item ${id} could not be verified.` },
      }));
      return;
    }
    if (path === "/v1/chat/completions") {
      response.end(JSON.stringify({
        id: "chat_" + received.length, model: parsed.model, object: "chat.completion",
        choices: [{ index: 0, message: { role: "assistant", content: "History retained." }, finish_reason: "stop" }],
        usage: { prompt_tokens: 8, completion_tokens: 2 },
      }));
      return;
    }
    response.end(JSON.stringify({
      id: "resp_" + received.length, model: parsed.model, object: "response", status: "completed",
      output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "History retained." }] }],
      usage: { input_tokens: 8, output_tokens: 2 },
    }));
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  const port = (upstream.address() as { port: number }).port;
  const modelIDs: string[] = [];
  let headers: Record<string, string> = {};
  try {
    await loginAdmin(page);
    const key = await sessionBearer(page);
    headers = { Authorization: "Bearer " + key };
    const chat = "history-chat-" + Date.now();
    const responses = "history-responses-" + Date.now();
    for (const [model, endpoint, transport] of [
      [chat, "chat", "bypass_openai_chat"],
      [responses, "responses", "bypass_openai_responses"],
    ]) {
      const created = await page.request.post(GATEWAY + "/model/new", { headers, data: {
        model_name: model,
        litellm_params: {
          model, custom_llm_provider: "openai", api_base: `http://127.0.0.1:${port}/v1`,
          api_key: "history-test-key", input_cost_per_token: 0.000001, output_cost_per_token: 0.000002,
        },
        model_info: { transport, endpoint_types: [endpoint], pricing_source: "manual" },
      } });
      expect(created.status(), await created.text()).toBe(200);
      modelIDs.push((await created.json()).model_info.id);
    }
    /** 从登录后的浏览器发起 JSON 请求。参数为入口和正文，返回错误/结果及稳定调用 ID。 */
    const invoke = (path: string, body: Record<string, unknown>) => page.evaluate(async ({ path, body, key }) => {
      // eslint-disable-next-line no-restricted-syntax
      const reply = await fetch("/gw" + path, {
        method: "POST",
        headers: { Authorization: "Bearer " + key, "Content-Type": "application/json", "X-Session-Id": "browser-history-recovery" },
        body: JSON.stringify(body),
      });
      return { status: reply.status, text: await reply.text(), callID: reply.headers.get("x-litellm-call-id") };
    }, { path, body, key });
    const reasoning = {
      type: "reasoning", id: "rs_old", encrypted_content: "STALE_CIPHER",
      summary: [{ type: "summary_text", text: "Check first." }],
    };
    const before = { role: "user", content: "Read the service." };
    const earlier = { role: "assistant", content: "The service is offline." };
    const next = { role: "user", content: "Continue. 保留用户原文" };
    const messages = [before, { ...earlier, reasoning_content: "Check first.", reasoning_items: [reasoning] }, next];
    const input = [before, reasoning, earlier, next];
    for (const [path, body, calls] of [
      ["/bypass/openai/v1/chat/completions", { model: chat, messages }, 2],
      ["/v1/chat/completions", { model: chat, messages }, 1],
      ["/bypass/openai/v1/responses", { model: responses, input }, 2],
      ["/v1/responses", { model: responses, input }, 1],
    ] as const) {
      const start = received.length;
      const reply = await invoke(path, body);
      expect(reply.status, reply.text).toBe(200);
      expect(reply.text).toContain("History retained.");
      expect(received).toHaveLength(start + calls);
      if (calls === 2) expect(received[start].body).toContain("STALE_CIPHER");
      const finalWire = received.at(-1)!;
      expect(finalWire.body).not.toContain("STALE_CIPHER");
      expect(finalWire.body).toContain("The service is offline.");
      expect(finalWire.body).toContain("保留用户原文");
      if (finalWire.path.endsWith("/chat/completions")) expect(finalWire.body).toContain("Check first.");
      await expect.poll(async () => (await page.request.get(GATEWAY + "/spend/logs/ui/" + reply.callID, { headers })).status()).toBe(200);
      const log = await (await page.request.get(GATEWAY + "/spend/logs/ui/" + reply.callID, { headers })).json();
      expect(log.status).toBe("success");
      expect(log.spend).toBeCloseTo(0.000012, 10);
    }
    const count = received.length;
    const refused = await invoke("/bypass/openai/v1/responses", {
      model: responses, input: [before, { type: "compaction", id: "cmp_old", encrypted_content: "COMPACT_CIPHER" }, next],
    });
    expect(refused.status).toBe(400);
    expect(received).toHaveLength(count + 1);
    expect(received.at(-1)!.body).toContain("COMPACT_CIPHER");
    await stableGoto(page, "/logs?log_id=" + refused.callID);
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText("400");
  } finally {
    try {
      for (const id of modelIDs) {
        expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id } })).status()).toBe(200);
      }
    } finally {
      upstream.closeAllConnections();
      await new Promise<void>((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
    }
  }
});
