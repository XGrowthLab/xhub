import { createServer } from "node:http";

/** 创建独占三协议上游；验证鉴权、模型字段、消息数组和工具关联，记录测试请求供浏览器验收断言。
 * 无参数；返回地址、调用证据、可控故障及关闭方法。仅接收 fixture Key，finally 关闭连接，无商业外部依赖。 */
export async function protocolProbeFixture() {
  const requests: { path: string; body: Record<string, any> }[] = [];
  const state = {
    url: "",
    requests,
    failed: false,
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((e) => (e ? reject(e) : resolve()));
        server.closeAllConnections();
      }),
  };
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    let b: Record<string, any>;
    try {
      b = JSON.parse(Buffer.concat(chunks).toString());
    } catch {
      res.writeHead(400).end();
      return;
    }
    const path = req.url ?? "";
    requests.push({ path, body: b });
    if (state.failed) {
      res.writeHead(503).end();
      return;
    }
    const messages = path === "/v1/messages",
      responses = path === "/v1/responses";
    if (!["/v1/chat/completions", "/v1/responses", "/v1/messages"].includes(path)) {
      res.writeHead(404).end();
      return;
    }
    if (
      messages
        ? req.headers["x-api-key"] !== "fixture-protocol-key" || !req.headers["anthropic-version"]
        : req.headers.authorization !== "Bearer fixture-protocol-key"
    ) {
      res.writeHead(401).end();
      return;
    }
    let turns = responses ? b.input : b.messages;
    if (responses && typeof turns === "string") turns = [{ role: "user", content: turns }];
    if (!Array.isArray(turns) || !turns.length || !b.model || (messages && !b.max_tokens)) {
      res.writeHead(400).end();
      return;
    }
    if (
      JSON.stringify(turns).includes("tool_result") ||
      turns.some((t: any) => t.role === "tool" || t.type === "function_call_output")
    ) {
      if (!JSON.stringify(turns).includes("probe-call") || !JSON.stringify(turns).includes("PROBE_OK")) {
        res.writeHead(400).end();
        return;
      }
    }
    const tool = !!b.tool_choice;
    const usage = { input_tokens: 8, output_tokens: 2 };
    const id = `probe-${requests.length}`;
    let reply: any;
    if (messages)
      reply = {
        id,
        type: "message",
        role: "assistant",
        model: b.model,
        content: tool
          ? [{ type: "tool_use", id: "probe-call", name: "xhub_echo", input: { value: "PROBE_OK" } }]
          : [{ type: "text", text: "PROBE_OK" }],
        stop_reason: tool ? "tool_use" : "end_turn",
        usage,
      };
    else if (responses)
      reply = {
        id,
        object: "response",
        status: "completed",
        model: b.model,
        output: tool
          ? [
              {
                type: "function_call",
                id: "fc-probe",
                call_id: "probe-call",
                name: "xhub_echo",
                arguments: '{"value":"PROBE_OK"}',
                status: "completed",
              },
            ]
          : [
              {
                type: "message",
                id: "msg-probe",
                role: "assistant",
                status: "completed",
                content: [{ type: "output_text", text: "PROBE_OK", annotations: [] }],
              },
            ],
        usage,
      };
    else
      reply = {
        id,
        object: "chat.completion",
        model: b.model,
        choices: [
          {
            index: 0,
            message: tool
              ? {
                  role: "assistant",
                  content: null,
                  tool_calls: [
                    {
                      id: "probe-call",
                      type: "function",
                      function: { name: "xhub_echo", arguments: '{"value":"PROBE_OK"}' },
                    },
                  ],
                }
              : { role: "assistant", content: "PROBE_OK" },
            finish_reason: tool ? "tool_calls" : "stop",
          },
        ],
        usage: { prompt_tokens: 8, completion_tokens: 2, total_tokens: 10 },
      };
    res.setHeader("Content-Type", b.stream ? "text/event-stream" : "application/json");
    if (!b.stream) {
      res.end(JSON.stringify(reply));
      return;
    }
    /** write 输出真实完整 SSE 帧；只编码测试对象，无外部写入或用户内容。 */
    const write = (data: any) => res.write(`data: ${JSON.stringify(data)}\n\n`);
    if (messages) {
      write({
        type: "message_start",
        message: { id, type: "message", role: "assistant", content: [], usage: { input_tokens: 8, output_tokens: 0 } },
      });
      write({ type: "content_block_start", index: 0, content_block: { type: "text", text: "" } });
      write({ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "PROBE_OK" } });
      write({ type: "content_block_stop", index: 0 });
      write({ type: "message_delta", delta: { stop_reason: "end_turn" }, usage: { output_tokens: 2 } });
      write({ type: "message_stop" });
    } else if (responses) {
      write({ type: "response.created", response: { id, object: "response", status: "in_progress" } });
      write({ type: "response.output_text.delta", delta: "PROBE_OK" });
      write({ type: "response.completed", response: reply });
    } else {
      write({
        id,
        object: "chat.completion.chunk",
        choices: [{ index: 0, delta: { role: "assistant", content: "PROBE_OK" }, finish_reason: null }],
      });
      write({
        id,
        object: "chat.completion.chunk",
        choices: [{ index: 0, delta: {}, finish_reason: "stop" }],
        usage: reply.usage,
      });
      res.write("data: [DONE]\n\n");
    }
    res.end();
  });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("fixture not listening");
  state.url = `http://127.0.0.1:${address.port}`;
  return state;
}
