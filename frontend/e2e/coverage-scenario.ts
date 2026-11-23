import { chooseKeyTeam } from "./helpers";

import { GATEWAY } from "./helpers";

import { execFileSync } from "child_process";

import path from "path";

import { expect, test, type Page } from "@playwright/test";

import { MASTER, login, loginAdmin, t, uiPath, watchGateway } from "./helpers";

import { recordChain, recordPage, reportDir } from "./report";

import { discoverPages } from "./routes";


const pages = discoverPages();


/** 用途：从已登录控制台 cookie 提取会话 bearer；参数为当前页面；返回会话 key，缺少 cookie 或非法 JWT 时抛错；供真实后台读接口使用，无写副作用。 */
async function sessionBearer(page: Page): Promise<string> {
  const cookies = await page.context().cookies();
  const token = cookies.find((c) => c.name === "token")?.value;
  const payload = JSON.parse(Buffer.from(token!.split(".")[1], "base64url").toString());
  return payload.key as string;
}


/** 用途：注册独立场景「discovers every page.tsx route」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC005() {


test("discovers every page.tsx route", async () => {
  expect(pages.length).toBeGreaterThan(0);
  expect(pages.map((p) => p.route)).toContain("/login");
  expect(pages.map((p) => p.route)).toContain("/api-keys");
  expect(pages.map((p) => p.route)).not.toContain("/model_hub");
  expect(pages.map((p) => p.route)).toContain("/mcp/oauth/callback");
  console.log(`discovered pages=${pages.length}`);
});
}
/** 用途：注册独立场景「fresh isolated user has empty usage and logs」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC006() {


test("fresh isolated user has empty usage and logs", async ({ page }) => {
  const suffix = Date.now().toString();
  const email = `coverage-empty-${suffix}@xhub.local`;
  const password = "coverage-empty-password";
  await loginAdmin(page);
  const adminHeaders = { Authorization: "Bearer " + (await sessionBearer(page)) };
  const created = await page.request.post(GATEWAY + "/user/new", {
    headers: adminHeaders,
    data: { user_email: email, password, user_role: "user" },
  });
  expect(created.status(), await created.text()).toBe(200);
  const userID = (await created.json()).user_id as string;

  try {
    await login(page, email, password);
    // login() 在提交表单后即返回；等待登录后导航出现，确保会话 cookie 已完成写入再读取 bearer。
    await expect(page.getByText(t("nav.apiKeys")).first()).toBeVisible({ timeout: 20_000 });
    const userHeaders = { Authorization: "Bearer " + (await sessionBearer(page)) };
    const usage = await page.request.get(GATEWAY + "/user/daily/activity/aggregated", { headers: userHeaders });
    expect(usage.status(), await usage.text()).toBe(200);
    expect(Number((await usage.json()).metadata?.total_api_requests ?? 0)).toBe(0);
    const logs = await page.request.get(GATEWAY + "/spend/logs/ui?page=1&page_size=10", { headers: userHeaders });
    expect(logs.status(), await logs.text()).toBe(200);
    expect((await logs.json()).data ?? []).toHaveLength(0);

    await page.goto(uiPath("/usage"));
    await expect(page.getByTestId("daily-spend-settled").getByText(t("No data"), { exact: true })).toBeVisible();
    await page.goto(uiPath("/logs"));
    await expect(page.getByText(t("logsUX.empty")).first()).toBeVisible();
  } finally {
    const removed = await page.request.post(GATEWAY + "/user/delete", { headers: adminHeaders, data: { user_id: userID } });
    expect(removed.status(), await removed.text()).toBe(200);
  }
});
}
/** 注册旧模型市场路径与网关 API 的真实契约验证；无参数、无返回值。
 * 浏览器和 HTTP 客户端共同确认原页面不渲染，公开目录各别名分页一致；不读取客户数据，无持久化写入。 */
export function registerSC007() {
  test("retired model hub pages preserve the public catalog API", async ({ page }) => {
    const query = "?page=1&page_size=1";
    for (const path of ["/model_hub_table", "/ui/model_hub_table", "/model-hub-table", "/ui/model-hub-table", "/ui/model_hub"]) {
      const response = await page.request.get(path + query, { maxRedirects: 0 });
      expect(response.status(), path).toBe(410);
      expect(response.headers()["cache-control"], path).toBe("no-store");
      expect(await response.text(), path).toBe("");
    }
    const browser = await page.goto("/ui/model_hub");
    expect(browser?.status()).toBe(410);
    const reference = await page.request.get(GATEWAY + "/public/v1/model_hub" + query);
    expect(reference.ok(), await reference.text()).toBeTruthy();
    const expected = await reference.json();
    expect(expected.data).toHaveLength(1);

    for (const path of ["/model_hub", "/gw/model_hub", "/public/model_hub", "/public/v1/model_hub"]) {
      const catalog = await page.request.get(path + query, { headers: { Accept: "application/json" } });
      expect(catalog.status(), await catalog.text()).toBe(200);
      expect(catalog.headers()["content-type"]).toMatch(/json/);
      const payload = await catalog.json();
      expect(payload.data).toEqual(expected.data);
      expect(payload.meta).toEqual(expected.meta);
      expect(payload.links.self).toBe(path.replace(/^\/gw(?=\/)/, "") + query);
    }
  });
}
/** 用途：注册独立场景「a created user can sign in with the initial password」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC008() {


test("a created user can sign in with the initial password", async ({ page }) => {
  test.setTimeout(90_000);
  const guard = watchGateway(page);
  await loginAdmin(page);
  await page.goto(uiPath("/users"));
  await page.getByRole("button", { name: `+ ${t("pages.users.invite")}` }).click();
  await page.getByLabel(t("pages.users.userEmail")).fill("e2e-claim@example.com");
  await page.getByLabel(t("Initial password")).fill("claimed-pass");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: t("pages.users.invite") })
    .click();
  await expect(page.getByText("e2e-claim@example.com").first()).toBeVisible({ timeout: 15_000 });
  await login(page, "e2e-claim@example.com", "claimed-pass");
  await expect(page.getByText(t("nav.apiKeys")).first()).toBeVisible({ timeout: 20_000 });
  await guard.assertOk();
  console.log("created resource name=e2e-claim@example.com");
  recordChain("created resource name=e2e-claim@example.com");
  await guard.assertOk();
});
}
/** 用途：注册独立场景「an administrator resets a password and the account signs in with it」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC009() {


test("an administrator resets a password and the account signs in with it", async ({ page }) => {
  test.setTimeout(120_000);
  const guard = watchGateway(page);
  await loginAdmin(page);
  await page.goto(uiPath("/users"));

  // A fresh address per run, so a leftover account from an earlier attempt
  // cannot turn the create into a conflict that leaves the dialog open.
  const email = `e2e-reset-${Date.now()}@example.com`;
  await page.getByRole("button", { name: `+ ${t("pages.users.invite")}` }).click();
  await page.getByLabel(t("pages.users.userEmail")).fill(email);
  await page.getByLabel(t("Initial password")).fill("initial-pass");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: t("pages.users.invite") })
    .click();
  // The dialog closing is what says the create was accepted; the address also
  // appears inside the form while it is open.
  await expect(page.getByRole("dialog")).toHaveCount(0, { timeout: 15_000 });
  await expect(page.getByText(email).first()).toBeVisible({ timeout: 15_000 });

  // The row's own actions menu, so the click does not open whichever row is first.
  await page
    .getByRole("row", { name: new RegExp(email) })
    .locator('[data-testid^="user-actions-"]')
    .first()
    .click();
  await page.getByTestId("user-action-reset-password").click();
  // Exact, because "New password" is a substring of "Confirm the new password".
  await page.getByLabel(t("New password"), { exact: true }).fill("replaced-pass");
  await page.getByLabel(t("Confirm the new password")).fill("replaced-pass");
  await page.getByRole("button", { name: t("Save the new password") }).click();
  await expect(page.getByText(t("Password updated. Their existing sessions have been signed out."))).toBeVisible({
    timeout: 15_000,
  });

  // The whole point of the change: the password the administrator typed is the
  // one the account signs in with. The old reset link answered 200 and changed
  // nothing.
  await login(page, email, "replaced-pass");
  await expect(page.getByText(t("nav.apiKeys")).first()).toBeVisible({ timeout: 20_000 });
  await guard.assertOk();
});
}
/** 用途：注册独立场景「playground shows the fake upstream assistant text」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC010() {


test("playground shows the fake upstream assistant text", async ({ page }) => {
  const guard = watchGateway(page);
  await loginAdmin(page);
  await page.goto(uiPath("/playground"));
  await page.getByPlaceholder(t("Select a Model"), { exact: true }).click();
  await page.getByRole("option", { name: "gpt-4o-mini", exact: true }).click();
  await page.getByTestId("chat-composer-input").fill("Hello from the playground.");
  await page.getByTestId("chat-send-button").click();
  await expect(page.getByText("e2e-ok").first()).toBeVisible({ timeout: 20_000 });
  console.log("chat content=e2e-ok");
  recordChain("chat content=e2e-ok");
  recordPage("/playground", "pass");
  await guard.assertOk();
});
}
/** 用途：注册独立场景「ui create is visible from the live gateway and chat returns e2e-ok」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC011() {


test("ui create is visible from the live gateway and chat returns e2e-ok", async ({ page }) => {
  const guard = watchGateway(page);
  await loginAdmin(page);
  const name = "e2e-roundtrip-key";
  await page.getByTestId("create-key-button").click();
  await chooseKeyTeam(page);
  await page.getByLabel(t("Key Name")).fill(name);
  await page.getByRole("button", { name: t("pages.apiKeys.createSubmit"), exact: true }).click();
  await expect(page.getByText(t("pages.apiKeys.saveKey"))).toBeVisible({ timeout: 15_000 });
  await page.keyboard.press("Escape");
  await expect(page.getByText(name).first()).toBeVisible();
  const bearer = await sessionBearer(page);
  const listed = await page.request.get(`${GATEWAY}/key/list`, {
    headers: { Authorization: `Bearer ${bearer}` },
  });
  expect(listed.ok()).toBeTruthy();
  expect(await listed.text()).toContain(name);
  const minted = await page.request.post(`${GATEWAY}/key/generate`, {
    headers: { Authorization: `Bearer ${bearer}`, "Content-Type": "application/json" },
    data: {
      key_alias: "e2e-chat-key",
      key_type: "llm_api",
      team_id: (
        await (
          await page.request.get(GATEWAY + "/v2/team/list?page=1&page_size=500", {
            headers: { Authorization: "Bearer " + bearer },
          })
        ).json()
      ).teams.find((x: { team_alias: string }) => x.team_alias === "e2e-fixture-team").team_id,
      user_id: JSON.parse(
        Buffer.from(
          (await page.context().cookies()).find((x) => x.name === "token")!.value.split(".")[1],
          "base64url",
        ).toString(),
      ).user_id,
    },
  });
  expect(minted.ok()).toBeTruthy();
  const secret = (await minted.json()).key as string;
  const chat = await page.request.post(`${GATEWAY}/v1/chat/completions`, {
    headers: { Authorization: `Bearer ${secret}`, "Content-Type": "application/json" },
    data: { model: "gpt-4o-mini", messages: [{ role: "user", content: "ping" }] },
  });
  const chatBody = await chat.text();
  expect(chat.ok(), chatBody).toBeTruthy();
  expect(chatBody).toContain("e2e-ok");
  console.log(`created resource name=${name}`);
  console.log("chat content=e2e-ok");
  recordChain(`created resource name=${name}`);
  recordChain("chat content=e2e-ok");
  await guard.assertOk();
});
}
/** 用途：注册独立场景「live gateway catalog sweep」；无参数、无返回值。前置真实浏览器、私有数据库和本地上游；验证页面及原始契约，保留原套件前后置清理，运行器最终回收专属 schema。异常由 Playwright 单独记录。 */
export function registerSC012() {


test("live gateway catalog sweep", async () => {
  test.setTimeout(300_000);
  const bin = path.join(process.env.E2E_RUN_DIR || path.resolve(__dirname, "../../.e2e"), "livesweep");
  const catalog = path.resolve(__dirname, "../../docs/testdata/catalog.json");
  let out = "";
  try {
    out = execFileSync(bin, {
      encoding: "utf8",
      timeout: 240_000,
      env: {
        ...process.env,
        E2E_GATEWAY: GATEWAY,
        E2E_MASTER_KEY: MASTER,
        CATALOG_JSON: catalog,
        E2E_ROUTE_LINES: path.join(reportDir, "routes.txt"),
      },
    });
  } catch (err) {
    const failed = err as { stdout?: string; stderr?: string; message?: string };
    out = `${failed.stdout || ""}\n${failed.stderr || ""}\n${failed.message || ""}`;
    console.log(out);
    throw new Error(out);
  }
  console.log(out.trim());
  const match = out.match(/checked=(\d+) misaligned=0 unique_http_routes=(\d+)/);
  expect(match, out).not.toBeNull();
  expect(match![1]).toBe(match![2]);
});
}
