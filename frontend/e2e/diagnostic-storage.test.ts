import { expect, test } from "@playwright/test";
import { GATEWAY, UPSTREAM, loginAdmin, sessionBearer, stableGoto } from "./helpers";
import { translate } from "../src/i18n/translate";
import { runStorageSQL, storageReport } from "./storage-maintenance";

// 创建一个只连接本地上游的测试模型。通过真实控制台调用、查询、压缩及回滚日志，仅操作本次请求 ID。
// 两种语言分别从日志抽屉核对完整响应头与费用；结束时删除模型，运行器清理私有 schema 中的用量。
for (const locale of ["en", "zh-CN"] as const) test("Read packed diagnostic in log details " + locale, async ({ page }) => {
  await loginAdmin(page);
  if (locale === "en") await page.getByRole("button", { name: translate("zh-CN", "language.en"), exact: true }).click();
  const headers = { Authorization: "Bearer " + await sessionBearer(page) };
  const model = "e2e-packed-log-" + locale.toLowerCase() + "-" + Date.now();
  const created = await page.request.post(GATEWAY + "/model/new", { headers, data: {
    model_name: model,
    litellm_params: { model, custom_llm_provider: "openai", api_base: UPSTREAM, api_key: "sk-fake",
      input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 },
    model_info: { transport: "bypass_openai_chat", pricing_source: "manual" },
  } });
  expect(created.status(), await created.text()).toBe(200);
  const deploymentID = (await created.json()).model_info.id;
  try {
    const call = await page.request.post(GATEWAY + "/v1/chat/completions", { headers, data: {
      model, messages: [{ role: "user", content: "Return a sample counter report." }],
    } });
    expect(call.status(), await call.text()).toBe(200);
    const id = call.headers()["x-litellm-call-id"];
    expect(id).toMatch(/^[A-Za-z0-9_-]+$/);
    const detailURL = GATEWAY + "/spend/logs/ui/" + id;
    await expect.poll(async () => (await page.request.get(detailURL, { headers })).status()).toBe(200);
    const before = await (await page.request.get(detailURL, { headers })).json();
    const trace: string = before.metadata.upstream_response.headers["X-Storage-Proof"][0];
    expect(trace.length).toBeGreaterThan(4096);
    const query = `SELECT id::text || '|' || to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM usage_events
      WHERE request_id='${id}' AND ts < date_trunc('second', now());`;
    await expect.poll(() => runStorageSQL(query)).not.toBe("");
    const [event, cutoff] = runStorageSQL(query).split("|");
    const args = ["--before=" + cutoff, "--from-event=" + event, "--through=" + event, "--rows=1", "--min-bytes=1024"];
    expect(storageReport(["plan", ...args], "diagnostic-pack").selected).toBe(1);
    expect(storageReport(["pack", "--execute", ...args], "diagnostic-pack").selected).toBe(1);
    const after = await (await page.request.get(detailURL, { headers })).json();
    expect(after.metadata.upstream_response).toEqual(before.metadata.upstream_response);
    expect(after.spend).toEqual(before.spend);
    await stableGoto(page, "/logs?log_id=" + encodeURIComponent(id));
    const region = page.getByRole("dialog").getByRole("region", { name: translate(locale, "Upstream Response"), exact: true });
    await region.getByRole("button", { name: translate(locale, "Upstream Response"), exact: true }).click();
    await region.getByText(translate(locale, "logsUX.upstreamRaw"), { exact: true }).click();
    await expect(region).toContainText(trace);
    expect(storageReport(["restore", "--execute", ...args], "diagnostic-pack").restored).toBe(1);
    expect((await (await page.request.get(detailURL, { headers })).json()).metadata.upstream_response).toEqual(before.metadata.upstream_response);
  } finally {
    expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id: deploymentID } })).status()).toBe(200);
  }
});

/** 在私有 PG、真实网关和浏览器中查看三种失败：上游 JSON 字符串、原生 Responses 无效终态、历史表写入失败。
 * 同一条日志对照实际 HTTP 响应、数据库错误筛选以及中英详情；finally 移除测试约束、恢复配置和日志选项并删除临时模型。 */
test("Failure details match the gateway response and survive scalar upstream errors", async ({ page }) => {
  await loginAdmin(page);
  const headers = { Authorization: "Bearer " + await sessionBearer(page) };
  const cookie = (await page.context().cookies()).find(({ name }) => name === "token")!.value;
  const { user_id: userID } = JSON.parse(Buffer.from(cookie.split(".")[1], "base64url").toString());
  expect(userID).toBeTruthy();
  const capture = await page.request.get(GATEWAY + "/account/log-capture", { headers });
  expect(capture.ok()).toBeTruthy();
  const { enabled } = await capture.json();
  const suffix = Date.now();
  const models = ["e2e-json-null-error", "e2e-native-log-failure"].map((upstream) => ({
    upstream, alias: upstream + "-" + suffix,
  }));
  const deployments: string[] = [];
  let nativeEnabled = false;
  let rejectHistoryWrites = false;
  try {
    expect((await page.request.put(GATEWAY + "/account/log-capture", { headers, data: { enabled: true } })).ok()).toBeTruthy();
    for (const [index, { alias, upstream }] of models.entries()) {
      const created = await page.request.post(GATEWAY + "/model/new", { headers, data: {
        model_name: alias,
        litellm_params: { model: upstream, api_base: UPSTREAM, api_key: "local-fixture", custom_llm_provider: "openai",
          input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 },
        model_info: { transport: index === 0 ? "bypass_openai_chat" : "bypass_openai_responses",
          endpoint_types: [index === 0 ? "chat" : "responses"], pricing_source: "manual" },
      } });
      expect(created.ok(), await created.text()).toBeTruthy();
      deployments.push((await created.json()).model_info.id);
    }

    const policy = await page.request.post(GATEWAY + "/config/update", { headers, data: {
      general_settings: { native_responses: { enabled: true, user_ids: [userID] } },
    } });
    expect(policy.ok(), await policy.text()).toBeTruthy();
    nativeEnabled = true;

    const dialog = page.getByRole("dialog");
    const failures = [
      { status: 503, model: models[0].alias, query: "null", path: "/v1/chat/completions",
        payload: { messages: [{ role: "user", content: "Check scalar error." }] } },
      { status: 502, model: models[1].alias, query: "upstream response is not complete", path: "/v1/responses",
        payload: { input: "Check invalid terminal response." } },
      { status: 502, model: models[1].alias, query: "history_unavailable", path: "/v1/responses",
        payload: { input: "Check history failure after completed response." } },
    ];
    for (const failure of failures) {
      if (failure.query === "history_unavailable") {
        // 只拒绝当前隔离 schema 的新历史写入。旧格式、压缩格式及 CI 的账号灰度使用同一夹具。
        runStorageSQL(`BEGIN;
          ALTER TABLE kv ADD CONSTRAINT e2e_reject_history CHECK (kind <> 'conversation_state' OR id NOT LIKE 'session_route:v1:history:%') NOT VALID;
          ALTER TABLE conversation_history_manifests ADD CONSTRAINT e2e_reject_history CHECK (false) NOT VALID;
          COMMIT;`);
        rejectHistoryWrites = true;
      }
      const reply = await page.request.post(GATEWAY + failure.path, {
        headers, data: { model: failure.model, ...failure.payload },
      });
      const raw = await reply.text();
      expect(reply.status(), raw).toBe(failure.status);
      if (failure.status === 503) expect(raw).toBe('"null"');
      else expect(raw).toContain(failure.query);
      const id = reply.headers()["x-litellm-call-id"];
      expect(id).toBeTruthy();
      await expect.poll(async () => {
        const response = await page.request.get(GATEWAY + "/spend/logs/ui/" + id, { headers });
        if (!response.ok()) return null;
        const row = await response.json();
        return { ...row, error: row.error?.trim() };
      }).toMatchObject({ status: "error", error: raw.trim(), body_capture_status: "saved" });
      const query = new URLSearchParams({ request_id: id, error_code: String(failure.status), status_filter: "error" })
      if (failure.status === 502) query.set("error_message", failure.query);
      const search = await page.request.get(GATEWAY + "/spend/logs/ui?" + query, { headers });
      expect((await search.json()).data).toEqual([expect.objectContaining({ request_id: id })]);
      if (failure.query === "history_unavailable") await page.getByRole("button", { name: "English", exact: true }).click();
      await stableGoto(page, "/logs?log_id=" + encodeURIComponent(id));
      const locale = failure.query === "history_unavailable" ? "en" : "zh-CN";
      await expect(dialog.getByRole("alert")).toContainText(String(failure.status));
      const details = dialog.getByRole("region", { name: translate(locale, "Full Error Details"), exact: true });
      await details.getByText(translate(locale, "logsUX.rawError"), { exact: true }).click();
      await expect(details).toContainText(failure.query);
      await dialog.getByRole("tab", { name: "JSON", exact: true }).click();
      const response = dialog.getByRole("tabpanel", { name: "JSON", exact: true });
      await expect(response.getByRole("heading", { name: translate(locale, "Response"), exact: true })).toBeVisible();
      await expect(response).toContainText(failure.query);
      await page.keyboard.press("Escape");
    }
  } finally {
    if (rejectHistoryWrites) runStorageSQL(`BEGIN;
      ALTER TABLE kv DROP CONSTRAINT e2e_reject_history;
      ALTER TABLE conversation_history_manifests DROP CONSTRAINT e2e_reject_history;
      COMMIT;`);
    if (nativeEnabled) expect((await page.request.post(GATEWAY + "/config/field/delete", {
      headers, data: { field_name: "native_responses" },
    })).ok()).toBeTruthy();
    expect((await page.request.put(GATEWAY + "/account/log-capture", { headers, data: { enabled } })).ok()).toBeTruthy();
    for (const id of deployments) expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id } })).ok()).toBeTruthy();
  }
});
