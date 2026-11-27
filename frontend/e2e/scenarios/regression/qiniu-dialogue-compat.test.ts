import { expect, test } from "@playwright/test";
import { once } from "node:events";
import { createServer, type ServerResponse } from "node:http";
import { GATEWAY, loginAdmin, sessionBearer, stableGoto } from "../../helpers";

const prompt = "Reply with exactly OK.";
const reply = "OK";
const plan = "Check the prompt.";
const googleUsage = { promptTokenCount: 12, candidatesTokenCount: 3, totalTokenCount: 15 };
const googleText = { candidates: [{ index: 0, content: { role: "model", parts: [{ text: reply }] } }] };
const googleComplete = {
  candidates: [{ ...googleText.candidates[0], finishReason: "stop" }],
  usageMetadata: googleUsage,
};

const messages = [
  { type: "message_start", message: {
    id: "msg_fixture", type: "message", role: "assistant", model: "fixture-messages", content: [],
    stop_reason: null, stop_sequence: null, usage: { input_tokens: 12, output_tokens: 0 },
  } },
  { type: "content_block_start", index: 0, content_block: { type: "thinking", thinking: "" } },
  { type: "content_block_delta", index: 0, delta: { type: "thinking_delta", thinking: plan } },
  { type: "content_block_delta", index: 0, delta: { type: "signature_delta", signature: "fixture-signature" } },
  { type: "content_block_stop", index: 0 },
  { type: "content_block_start", index: 2, content_block: { type: "text", text: "" } },
  { type: "content_block_delta", index: 2, delta: { type: "text_delta", text: reply } },
  { type: "content_block_stop", index: 2 },
  { type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 3 } },
  { type: "message_stop" },
];

const responseObject = {
  id: "resp_qiniu_fixture", object: "response", status: "completed", model: "fixture-responses",
  output: [
    { id: "rs_fixture", type: "reasoning", summary: [], content: [{ type: "reasoning_text", text: plan }] },
    { id: "msg_fixture", type: "message", role: "assistant", status: "completed", content: [
      { type: "output_text", text: reply, annotations: [] }, { type: "reasoning_text", text: plan },
    ] },
  ],
  usage: { input_tokens: 12, output_tokens: 3, total_tokens: 15 },
};

/** 发送指定供应商风格的事件；无 type 的 Google 内容用普通 SSE 数据帧，Chat/Responses 可附带 DONE。 */
function sendSSE(response: ServerResponse, frames: Array<Record<string, unknown>>, done = false) {
  response.writeHead(200, { "Content-Type": "text/event-stream" });
  for (const frame of frames) {
    if (frame.type) response.write("event: " + frame.type + "\n");
    response.write("data: " + JSON.stringify(frame) + "\n\n");
  }
  response.end(done ? "data: [DONE]\n\n" : "");
}

/** 从真实网关 SSE 文本读取 JSON 帧，返回可按协议检查的索引、正文、终态和错误字段。 */
function readSSE(body: string) {
  return body.split("\n").filter(line => line.startsWith("data: ") && line !== "data: [DONE]")
    .map(line => JSON.parse(line.slice("data: ".length)));
}

/** 使用真实浏览器会话、网关及隔离 PostgreSQL 复演七牛流形状；核对标准端点、透传及日志。
 * 本地上游模拟已观测到的协议帧，供应商真实调用矩阵单独验证。结束时删除部署并关闭上游。 */
test("Qiniu dialogue compatibility preserves reasoning, completion and upstream errors", async ({ page }) => {
  const upstream = createServer(async (request, response) => {
    const url = request.url ?? "";
    const parts: Buffer[] = [];
    for await (const part of request) parts.push(Buffer.from(part));
    const body = JSON.parse(Buffer.concat(parts).toString());
    if (url.endsWith("/messages") && body.model === "fixture-messages") {
      sendSSE(response, messages);
      return;
    }
    if (url.endsWith("/responses") && body.model === "fixture-responses") {
      if (body.stream) {
        sendSSE(response, [
          { type: "response.created", response: { ...responseObject, status: "in_progress", output: [] } },
          { type: "response.reasoning_text.delta", item_id: "rs_fixture", output_index: 0, content_index: 0, delta: plan },
          { type: "response.output_text.delta", item_id: "msg_fixture", output_index: 1, content_index: 0, delta: reply },
          { type: "response.completed", response: responseObject },
        ], true);
      } else {
        response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify(responseObject));
      }
      return;
    }
    if (url.endsWith("/chat/completions") && body.model === "fixture-chat") {
      sendSSE(response, [
        { id: "chatcmpl_fixture", choices: [{ index: 0, delta: { reasoning_content: plan } }] },
        { id: "chatcmpl_fixture", choices: [{ index: 0, delta: { content: reply } }] },
        { id: "chatcmpl_fixture", choices: [{ index: 0, delta: {}, finish_reason: "stop" }],
          usage: { prompt_tokens: 12, completion_tokens: 3, total_tokens: 15 } },
      ], true);
      return;
    }
    if (url.includes("/models/fixture-gemini:") || url.includes("/models/fixture-vertex:")) {
      if (url.includes(":streamGenerateContent")) {
        sendSSE(response, [googleText, { usageMetadata: googleUsage }]);
      } else {
        response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify(googleComplete));
      }
      return;
    }
    response.writeHead(400, { "Content-Type": "application/json" }).end(JSON.stringify({ error: { message: "Unexpected fixture endpoint." } }));
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  const port = (upstream.address() as { port: number }).port;
  const model = (name: string) => "qiniu-dialogue-" + name;
  const deployments = [
    { name: "messages", transport: "bypass_anthropic_messages", protocols: ["chat", "messages", "bypass:anthropic-messages"] },
    { name: "responses", transport: "bypass_openai_responses", protocols: ["responses", "chat"] },
    { name: "chat", transport: "bypass_openai_chat", protocols: ["gemini", "vertex"] },
    { name: "gemini", transport: "gemini_generate_content", protocols: ["gemini"] },
    { name: "vertex", transport: "vertex_generate_content", protocols: ["vertex"] },
  ];
  const ids: string[] = [];
  let headers: Record<string, string> = {};
  try {
    await loginAdmin(page);
    const bearer = await sessionBearer(page);
    headers = { Authorization: "Bearer " + bearer };
    for (const deployment of deployments) {
      const created = await page.request.post(GATEWAY + "/model/new", { headers, data: {
        model_name: model(deployment.name),
        litellm_params: {
          model: "fixture-" + deployment.name, custom_llm_provider: "openai",
          api_base: `http://127.0.0.1:${port}`, api_key: "fixture-key",
          input_cost_per_token: 0.000001, output_cost_per_token: 0.000002,
        },
        model_info: { transport: deployment.transport, endpoint_types: deployment.protocols, pricing_source: "manual" },
      } });
      expect(created.status(), await created.text()).toBe(200);
      ids.push((await created.json()).model_info.id);
    }

    /** 浏览器经 /gw 发起推理请求，返回原始响应和日志关联 ID。 */
    const invoke = (path: string, body: Record<string, unknown>) =>
      page.evaluate(async ({ bearer, path, body }) => {
        // eslint-disable-next-line no-restricted-syntax
        const response = await fetch("/gw" + path, {
          method: "POST",
          headers: { Authorization: "Bearer " + bearer, "Content-Type": "application/json", "anthropic-version": "2023-06-01" },
          body: JSON.stringify(body),
        });
        return { status: response.status, text: await response.text(), id: response.headers.get("x-litellm-call-id") };
      }, { bearer, path, body });

    const messageBody = { model: model("messages"), max_tokens: 96, stream: true, messages: [{ role: "user", content: prompt }] };
    const standard = await invoke("/v1/messages", messageBody);
    expect(standard.status, standard.text).toBe(200);
    const normalized = readSSE(standard.text);
    expect(normalized).toContainEqual({ type: "content_block_delta", index: 1, delta: { type: "text_delta", text: reply } });
    expect(normalized).toContainEqual(messages[3]);
    expect(normalized.at(-1)).toMatchObject({ type: "message_stop" });
    const bypass = await invoke("/bypass/anthropic/v1/messages", messageBody);
    expect(bypass.status, bypass.text).toBe(200);
    expect(readSSE(bypass.text)).toEqual(messages);
    const opaque = await invoke("/v1/chat/completions", messageBody);
    expect(opaque.status, opaque.text).toBe(502);
    expect(opaque.text).toContain("Messages content_block.type thinking requires native Messages");
    expect(opaque.text).toContain("unsupported_response_bridge");
    expect(opaque.text).not.toContain("fixture-signature");


    const responsesBody = { model: model("responses"), input: prompt, max_output_tokens: 96 };
    const responses = await invoke("/v1/responses", responsesBody);
    expect(responses.status, responses.text).toBe(200);
    const responseData = JSON.parse(responses.text);
    expect(responseData).toMatchObject({
      status: "completed",
      output: responseObject.output.map(({ type, content }) => ({ type, content })),
    });
    expect(responseData.id).toBeTruthy();
    const followUp = await invoke("/v1/responses", { ...responsesBody, previous_response_id: responseData.id });
    expect(followUp.status, followUp.text).toBe(200);
    const responsesStream = await invoke("/v1/responses", { ...responsesBody, stream: true });
    expect(responsesStream.status, responsesStream.text).toBe(200);
    expect(readSSE(responsesStream.text)).toContainEqual(expect.objectContaining({ type: "response.completed" }));
    const chat = await invoke("/v1/chat/completions", { ...messageBody, model: model("responses") });
    expect(chat.status, chat.text).toBe(200);
    const chatDeltas = readSSE(chat.text).flatMap(frame => (frame.choices ?? [])
      .map((choice: { delta?: { reasoning_content?: string; content?: string } }) => choice.delta));
    expect(chatDeltas.map(delta => delta?.reasoning_content ?? "").join("")).toBe(plan);
    expect(chatDeltas.map(delta => delta?.content ?? "").join("")).toBe(reply);
    expect(chat.text).toContain("data: [DONE]");

    const googleBody = { contents: [{ role: "user", parts: [{ text: prompt }] }] };
    for (const [protocol, prefix] of [["gemini", "/v1beta/models/"], ["vertex", "/vertex/v1/models/"]]) {
      const upstreamURL = prefix + model(protocol);
      const google = await invoke(upstreamURL + ":generateContent", googleBody);
      expect(google.status, google.text).toBe(200);
      expect(JSON.parse(google.text)).toMatchObject({ candidates: [{ finishReason: "STOP", content: { parts: [{ text: reply }] } }] });

      const converted = await invoke(prefix + model("chat") + ":streamGenerateContent?alt=sse", googleBody);
      expect(converted.status, converted.text).toBe(200);
      const candidates = readSSE(converted.text).flatMap(frame => frame.candidates ?? []);
      expect(candidates.flatMap(candidate => candidate.content?.parts ?? [])).toEqual([
        { text: plan, thought: true }, { text: reply },
      ]);
      expect(candidates.at(-1)).toMatchObject({ finishReason: "STOP" });

      const failed = await invoke(upstreamURL + ":streamGenerateContent?alt=sse", googleBody);
      expect(failed.text).toContain("finishReason");
      expect(failed.text).not.toContain('"finishReason":"STOP"');
      expect(failed.id).toBeTruthy();
      const params = new URLSearchParams({ request_id: failed.id! });
      await expect.poll(async () => (await (await page.request.get(GATEWAY + "/spend/logs/ui?" + params, { headers })).json()).data)
        .toMatchObject([{ request_id: failed.id, status: "error", spend: 0 }]);
      await stableGoto(page, "/logs?log_id=" + failed.id);
      await expect(page.getByRole("dialog")).toContainText("finishReason");
    }
  } finally {
    try {
      for (const id of ids) {
        const deleted = await page.request.post(GATEWAY + "/model/delete", { headers, data: { id } });
        expect(deleted.status(), await deleted.text()).toBe(200);
      }
    } finally {
      await new Promise<void>((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
    }
  }
});
