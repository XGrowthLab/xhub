import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { GATEWAY, login, loginAdmin, sessionBearer, stableGoto } from "./helpers";
import { translate } from "../src/i18n/translate";

/** 在运行器创建的独立 schema 中执行固定测试 SQL，返回单行结果。避免读取其他运行的数据。 */
function runCanarySQL(statement: string): string {
  const schema = readFileSync(path.join(process.env.E2E_RUN_DIR!, "schema.txt"), "utf8").trim();
  expect(schema).toMatch(/^e2e_[0-9]+_[0-9]+$/);
  return execFileSync("docker", ["exec", "-i", "xhub-postgres", "psql", "-X", "-U", "xhub", "-d",
    process.env.E2E_DATABASE || "xhub", "-qAt", "-v", "ON_ERROR_STOP=1"], {
    input: `SET search_path TO "${schema}"; ${statement}`, encoding: "utf8",
  }).trim();
}

/** 读取新旧格式行数；调用方在每轮真实响应后比较当前运行增量。 */
function historyCounts(): number[] {
  return runCanarySQL(`SELECT (SELECT count(*) FROM conversation_history_manifests),
    (SELECT count(*) FROM kv WHERE kind='conversation_state' AND id LIKE 'session_route:v1:history:%');`)
    .split("|").map(Number);
}

// 前置独立 PostgreSQL、本地模型和真实浏览器。验证普通账号从控制台入口发起及续接 JSON/SSE，核对历史格式。
// 用固定测试 ID 让进程启动时可配置灰度名单。运行结束移除临时用户，隔离 schema 由运行器清理。
for (const locale of ["en", "zh-CN"] as const) test("Compact history account canary " + locale, async ({ page }) => {
  await loginAdmin(page);
  const admin = await sessionBearer(page);
  const adminHeaders = { Authorization: "Bearer " + admin };
  const shortLocale = locale.toLowerCase();
  const email = `history-canary-${shortLocale}@example.test`;
  const password = "Canary-fixture-pass-42";
  const fixed = `history-canary-${shortLocale}`;
  const created = await page.request.post(GATEWAY + "/user/new", {
    headers: adminHeaders, data: { user_email: email, user_alias: fixed, password, user_role: "internal_user" },
  });
  expect(created.ok(), await created.text()).toBeTruthy();
  let userID = (await created.json()).user_id;
  expect(userID).toMatch(/^[0-9a-f-]+$/);
  try {
    expect(runCanarySQL(`UPDATE users SET id='${fixed}' WHERE id='${userID}' AND lower(email)='${email}' RETURNING id;`)).toBe(fixed);
    userID = fixed;

    const [compact, legacy] = historyCounts();
    const mode = process.env.XHUB_COMPACT_HISTORY;
    const allowlist = (process.env.XHUB_COMPACT_HISTORY_USER_IDS ?? "").split(",").map(id => id.trim());
    const expectedCompact = mode === "1" || (mode === "canary" && allowlist.includes(fixed));
    const outsiderCompact = mode === "1";
    const outsider = await page.request.post(GATEWAY + "/v1/responses", {
      headers: adminHeaders, data: { model: "e2e-responses-history", input: "continuation-1", user: fixed, store: false },
    });
    expect(outsider.ok(), await outsider.text()).toBeTruthy();
    expect(historyCounts()).toEqual([compact + Number(outsiderCompact), legacy + Number(!outsiderCompact)]);

    if (locale === "en") await page.getByRole("button", { name: translate("zh-CN", "language.en"), exact: true }).click();
    await login(page, email, password, locale);
    await expect(page.getByText(translate(locale, "nav.apiKeys")).first()).toBeVisible();
    await stableGoto(page, "/playground");
    await expect(page.getByPlaceholder(translate(locale, "Select a Model"), { exact: true })).toBeVisible();
    const bearer = await sessionBearer(page);
    const result = await page.evaluate(async ({ bearer }) => {
      const next = (body: Record<string, unknown>) => fetch("/gw/v1/responses", {
        method: "POST", headers: { Authorization: "Bearer " + bearer, "Content-Type": "application/json" },
        body: JSON.stringify({ ...body, model: "e2e-responses-history", store: false }),
      });
      const first = await next({ input: "continuation-1" });
      const initial = await first.json();
      const followup = await next({ input: "continuation-2", previous_response_id: initial.id, stream: true });
      return { first: first.status, id: initial.id, followup: followup.status, sse: await followup.text() };
    }, { bearer });
    expect(result.first).toBe(200);
    expect(result.id).toBeTruthy();
    expect(result.followup).toBe(200);
    expect(result.sse).toContain("response.completed");
    expect(result.sse).toContain("history-ok-2");
    expect(historyCounts()).toEqual([
      compact + Number(outsiderCompact) + (expectedCompact ? 2 : 0),
      legacy + Number(!outsiderCompact) + (expectedCompact ? 0 : 2),
    ]);
  } finally {
    const removed = await page.request.post(GATEWAY + "/user/delete", { headers: adminHeaders, data: { user_id: userID } });
    expect(removed.ok(), await removed.text()).toBeTruthy();
  }
});
