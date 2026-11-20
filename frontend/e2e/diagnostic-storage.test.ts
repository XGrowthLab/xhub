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
    const args = ["--before=" + cutoff, "--after=" + (BigInt(event) - BigInt(1)).toString(), "--through=" + event, "--rows=1", "--min-bytes=1024"];
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
