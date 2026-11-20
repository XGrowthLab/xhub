import { expect, test } from "@playwright/test";
import { runStorageSQL as runCanarySQL, storageReport } from "./storage-maintenance";
import { writeFileSync, rmSync } from "node:fs";
import path from "node:path";
import { GATEWAY, login, loginAdmin, sessionBearer, stableGoto } from "./helpers";
import { translate } from "../src/i18n/translate";

/** 读取新旧格式行数；调用方在每轮真实响应后比较当前运行增量。 */
function historyCounts(): number[] {
  return runCanarySQL(`SELECT (SELECT count(*) FROM conversation_history_manifests),
    (SELECT count(*) FROM kv WHERE kind='conversation_state' AND id LIKE 'session_route:v1:history:%');`)
    .split("|").map(Number);
}

/** 从实际容量快照中汇总 Responses 转 Chat 的 JSON 或 SSE 调用；仅比较本轮真实浏览器新增量。 */
function bridgeCount(report: ReturnType<typeof storageReport>, streamed: boolean): number {
  return report.routes.reduce((sum: number, row: { inbound: string; outbound: string; explicit_bypass: boolean; streamed: boolean; calls: number }) =>
    sum + (row.inbound === "responses" && row.outbound === "chat" && !row.explicit_bypass && row.streamed === streamed ? row.calls : 0), 0);
}

// 此场景使用专门的灰度套件。常规全量压缩和旧格式的套件保留各自的测试。
test.skip(process.env.XHUB_COMPACT_HISTORY !== "canary", "Requires account-scoped history storage");

// 前置独立 PostgreSQL、本地模型和真实浏览器。验证名单内外的 JSON/SSE 和历史格式。
// 固定 ID 用于启动时的名单。运行结束移除临时用户，独立 schema 由运行器清理。
for (const locale of ["en", "zh-CN"] as const) test("Compact history account canary " + locale, async ({ page }) => {
  await loginAdmin(page);
  const admin = await sessionBearer(page);
  const adminHeaders = { Authorization: "Bearer " + admin };
  const shortLocale = locale.toLowerCase();
  const email = `history-canary-${shortLocale}@example.test`;
  const password = "Canary-fixture-pass-42";
  const fixed = `history-canary-${shortLocale}`;
  // 只向测试 schema 插入普通用户夹具。密码摘要对应上面的公开测试密码。
  expect(runCanarySQL(`INSERT INTO users (id, email, name, password_hash)
    VALUES ('${fixed}', '${email}', '${fixed}', '$2y$10$enDIbnHBwB/IvCwgR9M9POTdk6uLsvKKgl7nf93ZgqSnir8aAwOJS')
    RETURNING id;`)).toBe(fixed);
  try {
    const [compact, legacy] = historyCounts();
    const snapshotArgs = ["snapshot", "--deployment=browser", "--days=2"];
    const before = storageReport(snapshotArgs);
    const outsider = await page.request.post(GATEWAY + "/v1/responses", {
      headers: adminHeaders, data: { model: "e2e-responses-history", input: "continuation-1", user: fixed, store: false },
    });
    expect(outsider.ok(), await outsider.text()).toBeTruthy();
    expect(historyCounts()).toEqual([compact, legacy + 1]);

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
    expect(historyCounts()).toEqual([compact + 2, legacy + 1]);
    const after = storageReport(snapshotArgs);
    expect(bridgeCount(after, false) - bridgeCount(before, false)).toBe(2);
    expect(bridgeCount(after, true) - bridgeCount(before, true)).toBe(1);
    // 生产旧格式的创建时间精确到秒，等待独占上界跨过本轮写入，再确认试算后仍可读。
    await expect.poll(() => runCanarySQL(`SELECT coalesce(max(created_at) < to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),true)
      FROM kv WHERE kind='conversation_state' AND id LIKE 'session_route:v1:history:%';`)).toBe("t");
    const cutoff = runCanarySQL(`SELECT to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"');`);
    const plan = storageReport(["plan", "--before=" + cutoff, "--rows=1000"]);
    expect(plan.has_more).toBe(false);
    expect(plan.scanned).toBe(legacy + 1);
    expect(historyCounts()).toEqual([compact + 2, legacy + 1]);
  } finally {
    const removed = await page.request.post(GATEWAY + "/user/delete", { headers: adminHeaders, data: { user_id: fixed } });
    expect(removed.ok(), await removed.text()).toBeTruthy();
  }
});

// 使用真实浏览器、网关和 PostgreSQL，只转换本例新创建的旧历史。切换格式及反向还原后均通过控制台续聊，
// 对照数据库原文字节摘要，确认还原逐字节一致；测试结束由独立 schema 回收历史与账单。
for (const locale of ["en", "zh-CN"] as const) test("Packed legacy history rollback " + locale, async ({ page }) => {
  await loginAdmin(page);
  if (locale === "en") await page.getByRole("button", { name: translate("zh-CN", "language.en"), exact: true }).click();
  await stableGoto(page, "/playground");
  await expect(page.getByPlaceholder(translate(locale, "Select a Model"), { exact: true })).toBeVisible();
  const bearer = await sessionBearer(page);
  const tag = "storage-" + locale.toLowerCase() + "-" + Date.now();
  const marker = tag + "-" + "amber-".repeat(130);
  const prompt = "Remember the project marker " + marker + ". Reply with only that marker.";
  /** 从已登录控制台走真正的代理及数据面。响应 ID 从网关获取，测试不构造任何路由或身份键。 */
  const send = (input: string, previous = "", stream = false) => page.evaluate(async ({ bearer, input, previous, stream }) => {
    const response = await fetch("/gw/v1/responses", { method: "POST", headers: {
      Authorization: "Bearer " + bearer, "Content-Type": "application/json",
    }, body: JSON.stringify({ model: "e2e-codex-agent", instructions: "Return the project marker.", input, stream, store: false, ...(previous ? { previous_response_id: previous } : {}) }) });
    return { status: response.status, text: await response.text() };
  }, { bearer, input, previous, stream });
  const first = await send(prompt);
  expect(first.status).toBe(200);
  const id = JSON.parse(first.text).id as string;
  expect(id).toBeTruthy();
  const [digest, original, created, encoded] = runCanarySQL(`SELECT split_part(id,':',4), encode(sha256(convert_to(body,'UTF8')),'hex'), created_at, replace(encode(convert_to(body,'UTF8'),'base64'),chr(10),'')
    FROM kv WHERE kind='conversation_state' AND id LIKE 'session_route:v1:history:%' AND position('${tag}' in body)>0;`).split("|");
  expect(digest).toMatch(/^[0-9a-f]{64}$/);
  expect(original).toMatch(/^[0-9a-f]{64}$/);
  await expect.poll(() => runCanarySQL(`SELECT to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"');`) > created).toBe(true);
  const cutoff = runCanarySQL(`SELECT to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"');`);
  const args = ["--execute", "--key-digest=" + digest, "--before=" + cutoff, "--reserve-bytes=64"];
  // 先以过期的备份摘要尝试，再换成已验证的原件。只把摘要存进当前测试运行的私有目录。
  const manifest = path.join(process.env.E2E_RUN_DIR!, tag + ".manifest");
  try {
    writeFileSync(manifest, digest + " " + "0".repeat(64) + "\n", { mode: 0o600, flag: "wx" });
    const pack = ["pack", ...args, "--backup-manifest=" + manifest];
    expect(storageReport(pack, "history-pack").not_backed_up_or_changed).toBe(1);
    writeFileSync(manifest, digest + " " + original + "\n");
    expect(storageReport(pack, "history-pack").selected).toBe(1);
  } finally {
    rmSync(manifest, { force: true });
  }
  const middle = await send("What project marker did I give you? Reply with only the marker.", id, true);
  expect(middle.status).toBe(200);
  expect(middle.text).toContain("response.completed");
  expect(middle.text).toContain(marker);
  expect(storageReport(["restore", ...args], "history-pack").restored).toBe(1);
  // 先验证正常反向展开，再验证帧被破坏时仍可从本测试已保存的原件恢复，后续对话继续使用同一身份。
  try {
    writeFileSync(manifest, digest + " " + original + "\n", { mode: 0o600, flag: "wx" });
    expect(storageReport(["pack", ...args, "--backup-manifest=" + manifest], "history-pack").selected).toBe(1);
    runCanarySQL(`UPDATE kv SET history_frame=decode('00','hex') WHERE kind='conversation_state'
      AND id='session_route:v1:history:${digest}' AND history_frame IS NOT NULL;`);
    expect(storageReport(["recover", "--execute", "--key-digest=" + digest, "--created-at=" + created,
      "--backup-manifest=" + manifest], "history-pack", Buffer.from(encoded, "base64").toString("utf8")).recovered).toBe(true);
  } finally {
    rmSync(manifest, { force: true });
  }
  expect(runCanarySQL(`SELECT encode(sha256(convert_to(body,'UTF8')),'hex') FROM kv WHERE kind='conversation_state'
    AND id='session_route:v1:history:${digest}' AND history_frame IS NULL;`)).toBe(original);
  const last = await send("What project marker did I give you? Reply with only the marker.", id);
  expect(last.status).toBe(200);
  expect(JSON.parse(last.text).id).toBeTruthy();
  expect(last.text).toContain(marker);
});
