import { expect, test, type APIResponse, type Page } from "@playwright/test";
import { GATEWAY, UPSTREAM, loginAdmin, selectFailureSeverity, sessionBearer, stableGoto } from "../../helpers";
import { translate } from "../../../src/i18n/translate";

type Locale = "zh-CN" | "en";
type Severity = "warning" | "error";

/** 以请求 ID 查询已授权的真实列表；参数为当前页面、ID 和分级，返回网关响应。调用方先开始等待再改变搜索项或分级，忽略其他面板与轮询。 */
function waitForLogSearch(page: Page, callId: string, severity: Severity) {
  return page.waitForResponse(response => {
    const url = new URL(response.url());
    return url.pathname.endsWith("/spend/logs/ui") && url.searchParams.get("search") === callId
      && url.searchParams.get("severity") === severity;
  });
}

/** 等待请求落库并校验原始客户端状态及结构化诊断；响应需已收到且带调用 ID。返回已关联日志，仅读独立 schema。 */
async function storedFailure(
  page: Page, headers: Record<string, string>, response: APIResponse, expected: { severity: Severity; code: string },
) {
  const id = response.headers()["x-litellm-call-id"];
  expect(id, "客户端应拿到可关联诊断的网关调用 ID").toBeTruthy();
  const path = GATEWAY + "/spend/logs/ui/" + encodeURIComponent(id);
  await expect.poll(async () => (await page.request.get(path, { headers })).status()).toBe(200);
  const row = await (await page.request.get(path, { headers })).json();
  const facts = {
    request_id: id, severity: expected.severity, diagnosis: { code: expected.code },
    metadata: { http_status: response.status() }, spend: 0,
  };
  expect(row).toMatchObject(facts);
  return row;
}

/** 通过当前失败面板的可访问按钮打开真实日志；参数为当前语言和调用 ID，返回详情弹窗，调用方关闭弹窗以继续筛选。 */
async function showFailure(page: Page, locale: Locale, id: string) {
  const panel = page.getByRole("tabpanel", { name: translate(locale, "Error Logs"), exact: true });
  await panel.getByPlaceholder(translate(locale, "Search logs by ID…"), { exact: true }).fill(id);
  await panel.getByRole("button", { name: translate(locale, "logsUX.openDetails", { id }), exact: true }).click();
  const dialog = page.getByRole("dialog", { name: translate(locale, "Request {value0} details", { value0: id }) });
  await expect(dialog).toBeVisible();
  return dialog;
}

/** 用真实隔离网关、数据库、用户密钥与本地上游验证日志分级、分页、身份归属及中英文原因。
 * Redis 模式验证唯一兼容部署连续失败后仍出站，核对 HTTP 原始错误与供应商查单 ID；finally 清理测试资源，运行器回收 Redis 及专属 schema。 */
test("request logs distinguish actionable errors and warnings", async ({ page }) => {
  test.setTimeout(120_000);
  await loginAdmin(page);
  const admin = { Authorization: "Bearer " + await sessionBearer(page) };
  const name = "log-severity-" + Date.now();
  const userName = "日志验收员 " + name;
  const teamName = name + "-team";
  const deniedModel = name + "-denied";
  const cleanup: Array<{ path: string; data: object; method?: "DELETE" }> = [];

  /** 从真实管理 API 创建夹具；接收路由和数据，返回响应 JSON，失败时保留具体接口诊断。调用方登记已创建资源以供清理。 */
  const create = async (path: string, data: object) => {
    const response = await page.request.post(GATEWAY + path, { headers: admin, data });
    expect(response.status(), path + " " + await response.text()).toBe(200);
    return response.json();
  };

  try {
    for (const [alias, upstreamModel] of [[name, "e2e-error-log-json-" + name], [deniedModel, name + "-unreachable"]]) {
      const deployment = await create("/model/new", {
        model_name: alias,
        litellm_params: { model: upstreamModel, api_base: UPSTREAM, api_key: "sk-fake", custom_llm_provider: "openai",
          input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 },
        model_info: { transport: "bypass_openai_chat", pricing_source: "manual", endpoint_types: ["chat"] },
      });
      cleanup.push({ path: "/model/delete", data: { id: deployment.model_info.id } });
    }

    const org = await create("/organization/new", { organization_alias: name, models: [name] });
    cleanup.push({ path: "/organization/delete", data: { organization_id: org.organization_id }, method: "DELETE" });
    const team = await create("/team/new", { organization_id: org.organization_id, team_alias: teamName });
    cleanup.push({ path: "/team/delete", data: { team_id: team.team_id } });
    const userInput = {
      user_email: name + "@example.test", user_alias: userName, password: "diagnostic-fixture-password",
      team_id: team.team_id, team_role: "user",
    };
    const user = await create("/user/new", userInput);
    cleanup.push({ path: "/user/delete", data: { user_id: user.user_id } });
    const template = await create("/route_template/new", { name, body: {
      model_routes: [], routing_groups: [], fallbacks: [], context_window_fallbacks: [], content_policy_fallbacks: [],
      retry_policy: { max_attempts: 1, timeout_seconds: 20, failure_threshold: 1, cooldown_seconds: 60 },
    } });
    cleanup.push({ path: "/route_template/" + template.id + "/delete", data: {} });
    const key = await create("/key/generate", { user_id: user.user_id, key_alias: name, route_template_id: template.id });
    cleanup.push({ path: "/key/delete", data: { keys: [key.token_id] } });
    const caller = { Authorization: "Bearer " + key.key };

    /** 使用测试密钥经标准 Chat 入口发送真实请求；参数为模型及英文提示，返回原始网关响应供诊断断言。 */
    const call = (model: string, content: string) => page.request.post(GATEWAY + "/v1/chat/completions", {
      headers: caller, data: { model, messages: [{ role: "user", content }] },
    });

    const denied = await call(deniedModel, "Check model permission diagnostics.");
    expect(denied.status(), await denied.text()).toBe(401);
    expect(await denied.json()).toMatchObject({
      error: { reason: "model_not_allowed", param: "model", message: expect.stringContaining("allowed model list") },
    });
    const warning = await storedFailure(page, admin, denied, { severity: "warning", code: "model_not_allowed" });
    const warningIdentity = {
      model: deniedModel, user: user.user_id, user_name: userName, team_id: team.team_id,
      metadata: { user_api_key_team_alias: teamName },
    };
    expect(warning).toMatchObject(warningIdentity);

    const wrongMethod = await page.request.get(GATEWAY + "/responses");
    expect(wrongMethod.status(), await wrongMethod.text()).toBe(404);
    expect(await wrongMethod.json()).toMatchObject({ error: { reason: "unsupported_method", message: expect.stringContaining("POST") } });
    const missing = await storedFailure(page, admin, wrongMethod, { severity: "warning", code: "unsupported_method" });
    const missingReasons = {
      request_method: "GET", model_missing_reason: "model_not_applicable", user_missing_reason: "credentials_missing",
      team_missing_reason: "unverified_identity",
    };
    expect(missing.diagnosis).toMatchObject(missingReasons);

    const badGateway = await call(name, "Check the upstream error body.");
    expect(badGateway.status(), await badGateway.text()).toBe(502);
    expect(await badGateway.json()).toMatchObject({ error: { code: "E2E_UPSTREAM_502" } });
    const upstream = await storedFailure(page, admin, badGateway, { severity: "error", code: "upstream_http_error" });
    expect(upstream.diagnosis.upstream_status).toBe(502);

    let repeatedId = "";
    let supplierId = "";
    if (process.env.E2E_REDIS === "1") {
      // 上游用它实际收到的网关调用 ID 生成查单头，每次都不同，避免把第一次错误误当成第二次出站。
      const repeated = await call(name, "Check that the only compatible deployment still forwards after failure.");
      expect(repeated.status(), await repeated.text()).toBe(502);
      expect(await repeated.body()).toEqual(await badGateway.body());
      const repeatedFailure = await storedFailure(page, admin, repeated, { severity: "error", code: "upstream_http_error" });
      expect(repeatedFailure.diagnosis.upstream_status).toBe(502);
      expect(repeatedFailure.outbound_endpoint).toContain("/chat/completions");
      repeatedId = repeatedFailure.request_id;
      supplierId = "e2e-supplier-" + repeatedId;
      expect(repeated.headers()["x-oneapi-request-id"]).toBe(supplierId);
      expect(repeated.headers()["x-oneapi-request-id"]).not.toBe(badGateway.headers()["x-oneapi-request-id"]);
    }

    // 同一 Key 的新故障比警告更新。页大小 1 证明必须先筛选再分页，旧失败筛选仍可拿到两种严重程度。
    const list = GATEWAY + "/spend/logs/ui?" + new URLSearchParams({ status_filter: "error", key_alias: name, page_size: "1" }).toString();
    const legacy = await page.request.get(list, { headers: admin });
    expect(legacy.status()).toBe(200);
    expect(await legacy.json()).toMatchObject({ total: repeatedId ? 3 : 2 });
    const warnings = await page.request.get(list + "&severity=warning", { headers: admin });
    expect(warnings.status()).toBe(200);
    expect(await warnings.json()).toMatchObject({ total: 1, data: [{ request_id: warning.request_id }] });
    const faults = await page.request.get(list + "&severity=error", { headers: admin });
    expect(faults.status()).toBe(200);
    expect(await faults.json()).toMatchObject({ total: repeatedId ? 2 : 1, data: [{ severity: "error" }] });

    await stableGoto(page, "/logs");
    await page.getByRole("tab", { name: translate("zh-CN", "Error Logs"), exact: true }).click();
    const panel = page.getByRole("tabpanel", { name: translate("zh-CN", "Error Logs"), exact: true });
    const search = panel.getByPlaceholder(translate("zh-CN", "Search logs by ID…"), { exact: true });
    const absent = waitForLogSearch(page, warning.request_id, "error");
    await search.fill(warning.request_id);
    expect(await (await absent).json()).toMatchObject({ total: 0, data: [] });
    await expect(panel.getByRole("row").filter({ hasText: warning.request_id })).toHaveCount(0);
    if (repeatedId) {
      const details = await showFailure(page, "zh-CN", repeatedId);
      const upstreamResponse = details.getByRole("region", { name: translate("zh-CN", "Upstream Response") });
      await upstreamResponse.getByRole("button", { name: translate("zh-CN", "Upstream Response"), exact: true }).click();
      await expect(upstreamResponse).toContainText(translate("zh-CN", "logsUX.upstreamRequestId") + ": " + supplierId);
      await page.keyboard.press("Escape");
      await search.fill(warning.request_id);
    }
    const found = waitForLogSearch(page, warning.request_id, "warning");
    await selectFailureSeverity(page, "warning");
    expect(await (await found).json()).toMatchObject({ total: 1, data: [{ request_id: warning.request_id }] });
    const row = panel.getByRole("row").filter({ hasText: warning.request_id });
    await expect(row.getByText(translate("zh-CN", "logsUX.severityWarning"), { exact: true })).toBeVisible();
    await expect(row).toContainText("模型不在可用模型名单内");
    await expect(row).toContainText(userName);
    const denialDetails = await showFailure(page, "zh-CN", warning.request_id);
    await expect(denialDetails).toContainText(teamName);
    await page.keyboard.press("Escape");

    const warningDetails = await showFailure(page, "zh-CN", missing.request_id);
    await expect(warningDetails).toContainText("此接口不支持该请求方法");
    await expect(warningDetails).toContainText("未进入模型处理");
    await expect(warningDetails).toContainText("请求未提供凭据");
    await expect(warningDetails).toContainText("未能验证请求身份");
    await page.keyboard.press("Escape");

    await page.getByRole("button", { name: "English", exact: true }).click();
    const englishWarning = await showFailure(page, "en", missing.request_id);
    await expect(englishWarning).toContainText("This endpoint does not support the request method");
    await expect(englishWarning).toContainText("Did not reach model processing");
    await expect(englishWarning).toContainText("Request credentials are missing");
    await expect(englishWarning).toContainText("Request identity could not be verified");
    await page.keyboard.press("Escape");

    await selectFailureSeverity(page, "error");
    const upstreamDetails = await showFailure(page, "en", upstream.request_id);
    await expect(upstreamDetails).toContainText("Upstream returned an error response");
    await expect(upstreamDetails).toContainText("Received HTTP 502 from upstream");
    await expect(upstreamDetails).toContainText(userName);
    await page.keyboard.press("Escape");
    if (repeatedId) {
      const details = await showFailure(page, "en", repeatedId);
      await expect(details).toContainText("Received HTTP 502 from upstream");
      const upstreamResponse = details.getByRole("region", { name: "Upstream Response" });
      await upstreamResponse.getByRole("button", { name: "Upstream Response", exact: true }).click();
      await expect(upstreamResponse).toContainText(translate("en", "logsUX.upstreamRequestId") + ": " + supplierId);
      await page.keyboard.press("Escape");
    }
  } finally {
    // 删除顺序从 Key 和模板到用户、团队、组织及模型，单项失败仍继续清理其他夹具。
    const failures: string[] = [];
    for (const { path, data, method } of cleanup.reverse()) {
      try {
        const removed = await page.request.fetch(GATEWAY + path, { method: method || "POST", headers: admin, data });
        if (!removed.ok()) failures.push(path + " " + removed.status());
      } catch (error) {
        failures.push(path + " " + String(error));
      }
    }
    expect(failures, "应清理本例创建的资源").toEqual([]);
  }
});
