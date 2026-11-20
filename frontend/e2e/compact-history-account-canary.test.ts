import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync, rmSync } from "node:fs";
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

/** 以当前浏览器私有 schema 运行真实运维 CLI。仅返回聚合 JSON，配置只传给子进程，构建由 Go 缓存复用。 */
function storageReport(args: string[], program: "storage-report" | "history-pack" = "storage-report") {
  const dir = process.env.E2E_RUN_DIR!;
  const schema = readFileSync(path.join(dir, "schema.txt"), "utf8").trim();
  expect(schema).toMatch(/^e2e_[0-9]+_[0-9]+$/);
  const dsn: string = JSON.parse(readFileSync(path.join(dir, "c.yaml"), "utf8")).general_settings.database_url;
  expect(new URL(dsn).searchParams.get("search_path") === schema).toBe(true);
  return JSON.parse(execFileSync("go", ["run", "./cmd/" + program, ...args], {
    cwd: path.resolve(__dirname, "../.."), env: { ...process.env, XHUB_DATABASE_URL: dsn },
    encoding: "utf8", timeout: 60_000,
  }));
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
    await expect.poll(() => {
      const cutoff = runCanarySQL(`SELECT to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"');`);
      return storageReport(["plan", "--before=" + cutoff, "--rows=1000"]).scanned;
    }).toBe(legacy + 1);
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
  const [digest, original, created] = runCanarySQL(`SELECT split_part(id,':',4), encode(sha256(convert_to(body,'UTF8')),'hex'), created_at
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
  expect(runCanarySQL(`SELECT encode(sha256(convert_to(body,'UTF8')),'hex') FROM kv WHERE kind='conversation_state'
    AND id='session_route:v1:history:${digest}' AND history_frame IS NULL;`)).toBe(original);
  const last = await send("What project marker did I give you? Reply with only the marker.", id);
  expect(last.status).toBe(200);
  expect(JSON.parse(last.text).id).toBeTruthy();
  expect(last.text).toContain(marker);
});
