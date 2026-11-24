import { expect, test } from "@playwright/test";
import { loginAdmin, GATEWAY, sessionBearer, stableGoto } from "./helpers";

/** 前置为隔离数据库、真实网关及管理员浏览器；创建独立组织和目录，不覆盖现有数据。
 * 验证页面创建凭据、启用、SCIM 用户创建/读取/停用和中英文切换；全部数据随隔离 schema 清理。
 */
export function registerDirectoryScenario(situation: "keyboard" | "sso-config" | "credential" | "discovery" | "user-update" | "revocation" | "assignment" | "archive-validation" | "archive-persistence" | "archive-schedule" | "locale-layout" | "delete" | "delete-cancel") {
 test("目录独立场景 " + situation, async ({ page }) => {
 test.setTimeout(120_000);
  await loginAdmin(page);
  // 真实浏览器授权剪贴板，验证写入结果，不替换或模拟 navigator.clipboard。
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"], {origin: new URL(page.url()).origin});
  const suffix = Date.now() + "-" + situation;
  const loginName = "Enterprise Login " + suffix;
  const directoryName = "Enterprise Directory " + suffix;
  const headers = { Authorization: "Bearer " + await sessionBearer(page) };
  const orgResponse = await page.request.post(GATEWAY + "/organization/new", { headers, data: { organization_alias: "directory-company-" + suffix } });
  expect(orgResponse.status(), await orgResponse.text()).toBe(200);
  const org = await orgResponse.json();
  const teamResponse = await page.request.post(GATEWAY + "/team/new", { headers, data: { organization_id: org.organization_id, team_alias: "directory-team-" + suffix } });
  expect(teamResponse.status(), await teamResponse.text()).toBe(200);
  const team = await teamResponse.json();
  await stableGoto(page, "/admin-panel");
  const section = page.getByRole("region", { name: "企业目录" });
  if (situation === "keyboard") {
  await expect(page.getByTestId("admin-settings-canvas")).toBeVisible();
  // 与实际版本的四个配置页签对照。焦点从两端往返，验证标签与面板的关联。
  const tabs = section.getByRole("tablist", {name: "管理员设置"});
  await expect(tabs.getByRole("tab")).toHaveCount(4);
  await tabs.getByRole("tab", {name: "单点登录设置"}).focus();
  await page.keyboard.press("End");
  await expect(tabs.getByRole("tab", {name: "版本更新"})).toBeFocused();
  await expect(section.getByRole("tabpanel", {name: "版本更新"})).toBeVisible();
  await page.keyboard.press("Home");
  await expect(tabs.getByRole("tab", {name: "单点登录设置"})).toBeFocused();
  }
  if (situation === "keyboard") return;
  if (situation === "sso-config" || situation === "delete" || situation === "delete-cancel") {
  await section.getByRole("tab", {name:"单点登录设置"}).click();
  // 独立创建 SSO，不预先创建 SCIM；保存仅写登录配置，随后同步复用身份锚点但保留独立名称。
  await section.getByRole("button", {name: "配置单点登录", exact:true}).click();
  const sso = page.getByRole("dialog", {name:"配置单点登录"});
  await sso.getByLabel("组织 ID").selectOption(org.organization_id);
  await sso.getByLabel("单点登录名称").fill(loginName);
  await sso.getByLabel("XHub 公开访问地址").fill("https://console.example.com");
  await sso.getByLabel("身份平台配置模板").selectOption("entra");
  await expect(sso.getByLabel("稳定身份声明")).toHaveValue("oid");
  await sso.getByLabel("身份平台配置模板").selectOption("oidc");
  await sso.getByRole("button", {name:"生成回调和返回地址"}).click();
  await expect(sso.getByLabel("回调地址")).toHaveValue("https://console.example.com/auth/enterprise/callback");
  await sso.getByLabel("OIDC 签发者").fill("https://identity.example.com");
  await sso.getByLabel("客户端 ID", {exact:true}).fill("xhub-e2e");
  await sso.getByLabel("客户端密钥").fill("e2e-sso-secret");
  await sso.getByRole("button", {name:"保存集成配置"}).click();
  await expect(sso).not.toBeVisible();
  const overview = section.getByRole("region", {name:"" + loginName + " 的单点登录"});
  await expect(overview.getByRole("switch", {name:"启用 " + loginName + " 的单点登录"})).not.toBeChecked();
  await overview.getByRole("button", {name:"配置 " + loginName + " 的单点登录"}).click();
  await expect(sso.getByLabel("客户端 ID", {exact:true})).toHaveValue("xhub-e2e");
  await expect(sso.getByLabel("客户端密钥")).toHaveValue("");
  await page.keyboard.press("Escape");
  if (situation === "sso-config") {
  const emptySCIM = await page.request.get(GATEWAY + "/admin/directory/connections", {headers});
  expect(emptySCIM.status()).toBe(200);
  expect((await emptySCIM.json()).some((item: {organization_id:string}) => item.organization_id === org.organization_id)).toBe(false);
  return;
  }
  }
  await section.getByRole("tab", { name: "SCIM", exact: true }).click();
  await expect(section.getByLabel("目录名称")).not.toBeVisible();
  await section.getByRole("button", {name:/^(配置 SCIM|新建目录连接)$/, exact:true}).click();
  const scim = page.getByRole("dialog", {name:"配置 SCIM"});
  await scim.getByLabel("组织 ID").selectOption(org.organization_id);
  await scim.getByLabel("XHub 公开访问地址").fill("https://console.example.com");
  await scim.getByLabel("目录名称").fill(directoryName);
  await scim.getByRole("button", { name: "创建目录连接" }).click();
  const token = await scim.getByLabel("SCIM 同步凭据").inputValue();
  const base = new URL(await scim.getByLabel("SCIM 基础地址").inputValue()).pathname;
  expect(token).not.toBe("");
  if (situation === "credential") {
  await scim.getByRole("button", {name:"复制 SCIM 凭据"}).click();
  await expect(scim.getByText("已复制 SCIM 凭据。")).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(token);
  }
  await scim.getByRole("button", {name:"关闭配置"}).click();
  const scimHeaders = { Authorization: "Bearer " + token, "Content-Type": "application/scim+json" };
  expect((await page.request.get(GATEWAY + base + "/Users", { headers: scimHeaders })).status()).toBe(401);
  await section.getByRole("switch", { name: "启用 " + directoryName + "" }).click();
  await expect(section.getByRole("switch", { name: "停用 " + directoryName + "" })).toBeChecked();
  if (situation === "credential") {
    const available = await page.request.get(GATEWAY + base + "/Users", {headers:scimHeaders});
    expect(available.status(), await available.text()).toBe(200);
    await section.getByRole("switch", {name:"停用 " + directoryName}).click();
    await expect(section.getByRole("switch", {name:"启用 " + directoryName})).not.toBeChecked();
    expect((await page.request.get(GATEWAY + base + "/Users", {headers:scimHeaders})).status()).toBe(401);
    return;
  }
  const userResponse = await page.request.post(GATEWAY + base + "/Users", { headers: scimHeaders, data: {
    schemas: ["urn:ietf:params:scim:schemas:core:2.0:User"], externalId: "employee-e2e-1",
    userName: "directory-" + suffix + "@example.com", displayName: "Directory User", active: true,
  } });
  expect(userResponse.status(), await userResponse.text()).toBe(201);
  const user = await userResponse.json();
  if (situation === "discovery") {
  for (const resource of ["Schemas", "ResourceTypes", "ServiceProviderConfig"]) {
    const discovery = await page.request.get(GATEWAY + base + "/" + resource, { headers: scimHeaders });
    expect(discovery.status(), await discovery.text()).toBe(200);
    if (resource === "ServiceProviderConfig") expect((await discovery.json()).patch.supported).toBe(true);
  }
  return;
  }
  if (situation === "user-update") {
  const patched = await page.request.patch(GATEWAY + base + "/Users/" + user.id, { headers: scimHeaders, data: {
    schemas: ["urn:ietf:params:scim:api:messages:2.0:PatchOp"], Operations: [{op: "replace", path: "displayName", value: "Updated Directory User"}],
  } });
  expect(patched.status(), await patched.text()).toBe(200);
  expect((await patched.json()).displayName).toBe("Updated Directory User");
  const read = await page.request.get(GATEWAY + base + "/Users/" + user.id, {headers:scimHeaders});
  expect(read.status()).toBe(200);expect((await read.json()).displayName).toBe("Updated Directory User");
  return;
  }
  const groupResponse = await page.request.post(GATEWAY + base + "/Groups", { headers: scimHeaders, data: {
    schemas: ["urn:ietf:params:scim:schemas:core:2.0:Group"], externalId: "department-e2e",
    displayName: "Engineering", members: [{ value: user.id, type: "User" }],
  } });
  expect(groupResponse.status(), await groupResponse.text()).toBe(201);
  const group = await groupResponse.json();
  await section.getByRole("button", {name:"配置 " + directoryName + " 的 SCIM"}).click();
  await expect(scim.getByRole("button", { name: "加载目录归属" })).not.toBeVisible();
  await scim.getByText("团队映射与用户归属", {exact: true}).click();
  await scim.getByRole("button", { name: "加载目录归属" }).click();
  await scim.getByLabel("企业目录组").selectOption(group.id);
  await scim.getByLabel("目标团队 ID").selectOption(team.team_id);
  await scim.getByLabel("映射优先级").fill("3");
  await scim.getByRole("button", { name: "保存组映射" }).click();
  const assignments = scim.getByRole("list", { name: "用户归属列表" });
  await expect(assignments).toContainText(team.team_id);
  const connectionID = base.split("/").pop();
  await expect(scim.getByLabel("SCIM 基础地址")).toHaveValue("https://console.example.com" + base);
  await scim.getByRole("button", {name:"关闭配置"}).click();
  const archiveOverview = section.getByRole("region", { name: "七牛 Kodo 计费日志归档" });
  const archive = page.getByRole("dialog", {name:"配置 Kodo 归档"});
  if (situation.startsWith("archive-") || situation === "locale-layout") {
  await section.getByRole("tab", { name: "日志设置" }).click();
  // 其他独立场景可已保存停用配置；当前场景覆盖自己的字段，不要求全局初始为空。
  await archiveOverview.getByRole("button", {name: "配置 Kodo 归档"}).click();
  await archive.getByRole("button", { name: "加载配置与状态" }).click();
  await expect(archive.getByLabel("归档队列")).toContainText("待处理");
  await archive.getByRole("switch", {name:"启用集成"}).uncheck();
  if (situation === "archive-validation") {
  await archive.getByLabel("Kodo 存储空间").fill("invalid bucket");
  await archive.getByRole("button", { name: "保存集成配置" }).click();
  await expect(archive.getByRole("alert")).toContainText("操作失败");
  await archive.getByRole("button", { name: "测试 Kodo 连接" }).click();
  await expect(archive.getByRole("alert")).toContainText("操作失败");
  await archive.getByRole("button", { name: "重试失败归档" }).click();
  await expect(archive.getByRole("status")).toContainText("操作已完成");
  return;
  }
  // 使用虚构凭据保存停用配置，验证真实持久化及秘密清空，不向外部服务写入。
  await archive.getByLabel("Kodo 存储空间").fill("xhub-e2e-private");
  await archive.getByLabel("Kodo 访问密钥").fill("e2e-fake-ak");
  await archive.getByLabel("Kodo 秘密密钥").fill("e2e-fake-sk");
  await archive.getByLabel("Kodo 区域").selectOption("z1");
  await archive.getByLabel("归档处理间隔").selectOption("300");
  await archive.getByLabel("每轮最多处理事件数").fill("50");
  await archive.getByRole("button", { name: "保存集成配置" }).click();
  await expect(archive).not.toBeVisible();
  await archiveOverview.getByRole("button", {name:"配置 Kodo 归档"}).click();
  await expect(archive.getByLabel("Kodo 访问密钥")).toHaveValue("");
  await expect(archive.getByLabel("Kodo 秘密密钥")).toHaveValue("");
  await archive.getByRole("button", { name: "加载配置与状态" }).click();
  await expect(archive.getByLabel("Kodo 存储空间")).toHaveValue("xhub-e2e-private");
  await expect(archive.getByLabel("Kodo 区域")).toHaveValue("z1");
  await expect(archive.getByLabel("归档处理间隔")).toHaveValue("300");
  await expect(archive.getByLabel("每轮最多处理事件数")).toHaveValue("50");
  await expect(archive.getByText(/每个事件独立保存为 JSONL.gz/)).toBeVisible();
  const storedArchive = await page.request.get(GATEWAY + "/admin/archive/kodo", { headers });
  expect(storedArchive.status()).toBe(200);
  expect(await storedArchive.text()).not.toContain("e2e-fake");
  expect((await storedArchive.json()).configuration).toMatchObject({interval_seconds: 300, batch_size: 50});
  // 直接调用真实后台验证非法调度拒绝，页面重载确认合法配置没有被错误请求覆盖。
  if (situation === "archive-schedule") {
  const invalidSchedule = await page.request.put(GATEWAY + "/admin/archive/kodo", {headers, data: {bucket: "xhub-e2e-private", region: "z1", prefix: "billing", interval_seconds: 1, batch_size: 201}});
  expect(invalidSchedule.status()).toBe(400);
  await archive.getByRole("button", {name: "加载配置与状态"}).click();
  await expect(archive.getByLabel("归档处理间隔")).toHaveValue("300");
  }
  await page.keyboard.press("Escape");
  if (situation.startsWith("archive-")) return;
  }
  await section.getByRole("tab", { name: "SCIM", exact: true }).click();
  await section.getByRole("button", {name:"配置 " + directoryName + " 的 SCIM"}).click();
  await scim.getByText("团队映射与用户归属", {exact:true}).click();
  await scim.getByRole("button", {name:"加载目录归属"}).click();
  const detailPath = GATEWAY + "/admin/directory/connections/" + connectionID;
  const detail = await (await page.request.get(detailPath, { headers })).json();
  if (situation === "assignment") {
  let currentDetail = detail;
  expect(detail.users[0]).toMatchObject({team_id: team.team_id, mode: "auto"});
  await scim.getByLabel("目录用户").selectOption(detail.users[0].user_id);
  await scim.getByRole("button", { name: "保存用户归属" }).click();
  await expect(assignments).toContainText("无团队");
  await expect(assignments).toContainText("人工固定");
  currentDetail = await (await page.request.get(detailPath, { headers })).json();
  expect(currentDetail.users[0]).toMatchObject({team_id: "", mode: "manual"});
  await scim.getByLabel("归属模式").selectOption("auto");
  await scim.getByRole("button", { name: "保存用户归属" }).click();
  await expect(assignments).toContainText(team.team_id);
  await expect(assignments).toContainText("自动分配");
  }
  if (situation === "revocation") {
  // 同步用户的实际密钥必须能访问数据面；后续 SCIM 删除验证撤权，而非只检查目录 active 字段。
  const approvedBudget = await page.request.post(GATEWAY + "/user/update", {headers, data: {user_id: detail.users[0].user_id, max_budget: 10}});
  expect(approvedBudget.status(), await approvedBudget.text()).toBe(200);
  const provisionedKey = await page.request.post(GATEWAY + "/key/generate", {headers, data: {user_id: detail.users[0].user_id, key_alias: "scim-provisioned-e2e"}});
  expect(provisionedKey.status(), await provisionedKey.text()).toBe(200);
  const employeeKey = (await provisionedKey.json()).key;
  const employeeRequest = {headers: {Authorization: "Bearer " + employeeKey}, data: {model: "gpt-4o-mini", messages: [{role: "user", content: "Verify provisioned user access"}]}};
  const allowed = await page.request.post(GATEWAY + "/v1/chat/completions", employeeRequest);
  expect(allowed.status(), await allowed.text()).toBe(200);
  const groupRead = await page.request.get(GATEWAY + base + "/Groups/" + group.id, { headers: scimHeaders });
  expect(groupRead.status()).toBe(200);
  expect((await groupRead.json()).members).toEqual([{ value: user.id, type: "User" }]);
  const read = await page.request.get(GATEWAY + base + "/Users/" + user.id, { headers: scimHeaders });
  expect(read.status()).toBe(200);
  expect(await read.json()).toMatchObject({ id: user.id, externalId: "employee-e2e-1", userName: "directory-" + suffix + "@example.com", active: true });
  expect((await page.request.delete(GATEWAY + base + "/Users/" + user.id, { headers: scimHeaders })).status()).toBe(204);
  const stopped = await page.request.get(GATEWAY + base + "/Users/" + user.id, { headers: scimHeaders });
  expect((await stopped.json()).active).toBe(false);
  const blocked = await page.request.post(GATEWAY + "/v1/chat/completions", employeeRequest);
  expect([401, 403], "SCIM deactivation must revoke data-plane access").toContain(blocked.status());
  const removedKey = await page.request.post(GATEWAY + "/key/delete", {headers, data: {keys: [employeeKey]}});
  expect(removedKey.status(), await removedKey.text()).toBe(200);
  expect((await page.request.delete(GATEWAY + base + "/Groups/" + group.id, { headers: scimHeaders })).status()).toBe(204);
  await expect(scim.getByLabel("SCIM 同步凭据")).toHaveCount(0);
  }
  await scim.getByRole("button", {name:"关闭配置"}).click();
  if (situation === "locale-layout") {
  await page.getByRole("button", { name: "English", exact: true }).click();
  const english = page.getByRole("region", { name: "Enterprise directory" });
  await english.getByRole("tab", { name: "SCIM", exact: true }).click();
  await english.getByRole("button", {name:"Configure SCIM for " + directoryName + ""}).click();
  const englishSCIM = page.getByRole("dialog", {name:"Configure SCIM"});
  // 语言切换会重新挂载页面；身份快照不持久化，需要按真实用户流程重新加载。
  await englishSCIM.getByText("Team mappings and user assignments", {exact: true}).click();
  await englishSCIM.getByRole("button", { name: "Load directory assignments" }).click();
  await expect(englishSCIM.getByLabel("Mapping priority")).toBeVisible();
  await expect(englishSCIM.getByRole("list", { name: "User assignments" })).toBeVisible();
  await expect(englishSCIM.getByLabel("SCIM credential")).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(english.getByRole("switch", { name: "Disable " + directoryName + "" })).toBeChecked();
  const englishArchiveOverview = english.getByRole("region", { name: "Qiniu Kodo billing archive" });
  const englishArchive = page.getByRole("dialog", {name:"Configure Kodo archive"});
  await english.getByRole("tab", { name: "Log settings" }).click();
  await expect(englishArchiveOverview.getByRole("switch", {name: "Enable billing archive"})).not.toBeChecked();
  await englishArchiveOverview.getByRole("button", {name: "Configure Kodo archive"}).click();
  await englishArchive.getByRole("button", { name: "Load configuration and status" }).click();
  await expect(englishArchive.getByLabel("Kodo bucket")).toHaveValue("xhub-e2e-private");
  await expect(englishArchive.getByLabel("Archive processing interval")).toHaveValue("300");
  await expect(englishArchive.getByLabel("Maximum events per cycle")).toHaveValue("50");
  await expect(englishArchive.getByText(/Each event becomes a separate JSONL.gz/)).toBeVisible();
  await expect(englishArchive.getByRole("switch", {name: "Enable integration"})).not.toBeChecked();
  // 窄屏下表单不得超出配置面板；模拟暗色主题不写入业务配置，保存自动化截图供复核。
  await page.setViewportSize({width: 390, height: 844});
  const input = englishArchive.getByLabel("Kodo bucket");
  await input.scrollIntoViewIfNeeded();
  const panelBounds = await englishArchive.boundingBox();
  const inputBounds = await input.boundingBox();
  expect(panelBounds).not.toBeNull();
  expect(inputBounds).not.toBeNull();
  expect(inputBounds!.x).toBeGreaterThanOrEqual(panelBounds!.x);
  expect(inputBounds!.x + inputBounds!.width).toBeLessThanOrEqual(panelBounds!.x + panelBounds!.width);
  if (process.env.E2E_BROWSER_REPORT_DIR) {
    await page.screenshot({path: process.env.E2E_BROWSER_REPORT_DIR + "/admin-mobile.png", fullPage: true});
  }
  await page.setViewportSize({width: 1440, height: 1000});
  await page.keyboard.press("Escape");
  await page.getByRole("button", {name: "Switch to dark mode", exact: true}).click();
  await englishArchiveOverview.getByRole("button", {name:"Configure Kodo archive"}).click();
  await expect(page.locator("html")).toHaveClass(/dark/);
  await expect(input).toHaveValue("xhub-e2e-private");
  if (process.env.E2E_BROWSER_REPORT_DIR) {
    await page.screenshot({path: process.env.E2E_BROWSER_REPORT_DIR + "/admin-dark.png", fullPage: true});
  }
  }
  if (situation === "delete" || situation === "delete-cancel") {
  await page.getByRole("button", {name:"English",exact:true}).click();
  const english = page.getByRole("region", {name:"Enterprise directory"});
  // SCIM 删除仅撤销同步；SSO 单独确认删除，本地用户与历史记录保留。隔离 schema 清理测试数据。
  await page.keyboard.press("Escape");
  await english.getByRole("tab", {name:"SCIM", exact:true}).click();
  await english.getByRole("button", {name:"Delete " + directoryName + ""}).click();
  await page.getByRole("dialog").getByRole("button", {name:"Cancel"}).click();
  expect((await page.request.get(detailPath, {headers})).status()).toBe(200);
  if (situation === "delete-cancel") return;
  await english.getByRole("button", {name:"Delete " + directoryName + ""}).click();
  await page.getByRole("dialog").getByRole("button", {name:"Delete connection"}).click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await expect(english.getByRole("button", {name:"Delete " + directoryName + ""})).toHaveCount(0);
  expect((await page.request.get(detailPath, {headers})).status()).toBe(404);
  expect((await page.request.get(GATEWAY + "/admin/enterprise/sso/" + connectionID, {headers})).status()).toBe(200);
  await english.getByRole("tab", {name:"Single sign-on"}).click();
  await english.getByRole("button", {name:"Delete SSO for " + loginName + ""}).click();
  await page.getByRole("dialog").getByRole("button", {name:"Delete connection"}).click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
  expect((await page.request.get(GATEWAY + "/admin/enterprise/sso/" + connectionID, {headers})).status()).toBe(404);
  expect((await page.request.get(GATEWAY + base + "/Users", {headers:scimHeaders})).status()).toBe(401);
  expect((await page.request.get(GATEWAY + "/user/info?user_id=" + detail.users[0].user_id, {headers})).status()).toBe(200);  }

 });
}

/** 前置隔离数据库、真实管理员页面与本地模型供应商；一天间隔避免虚构凭据产生云调用。
 * 验证页面启用归档后真实推理账务入队、停用后不再入队且积压保留；finally 停用并删除密钥，脚本清理 schema。
 */
export function registerArchiveQueueScenario() {
 test("归档调度配置驱动真实计费入队并保留停用积压", async ({page}) => {
  await loginAdmin(page);
  const headers = {Authorization: "Bearer " + await sessionBearer(page)};
  const minted = await page.request.post(GATEWAY + "/key/generate", {headers, data: {key_alias: "archive-schedule-e2e"}});
  expect(minted.status(), await minted.text()).toBe(200);
  const key = (await minted.json()).key;
  try {
    await stableGoto(page, "/admin-panel");
    await page.getByRole("tab", {name: "日志设置"}).click();
    const archiveOverview = page.getByRole("region", {name: "七牛 Kodo 计费日志归档"});
    const archive = page.getByRole("dialog", {name:"配置 Kodo 归档"});
    await archiveOverview.getByRole("button", {name: "配置 Kodo 归档"}).click();
    await archive.getByLabel("Kodo 存储空间").fill("xhub-e2e-private");
    await archive.getByLabel("Kodo 访问密钥").fill("e2e-fake-ak");
    await archive.getByLabel("Kodo 秘密密钥").fill("e2e-fake-sk");
    await archive.getByLabel("归档处理间隔").selectOption("86400");
    await archive.getByLabel("每轮最多处理事件数").fill("1");
    await archive.getByRole("switch", {name: "启用集成"}).check();
    await archive.getByRole("button", {name: "保存集成配置"}).click();
    await expect(archive).not.toBeVisible();
    // 调用真实数据面并等待账务落库，而不是手工插入队列模拟端到端。
    const beforeResponse = await page.request.get(GATEWAY + "/admin/archive/kodo", {headers});
    expect(beforeResponse.status()).toBe(200);
    const before = await beforeResponse.json();
    const completion = await page.request.post(GATEWAY + "/v1/chat/completions", {headers: {Authorization: "Bearer " + key}, data: {model: "gpt-4o-mini", messages: [{role: "user", content: "Verify billing archive queue"}]}});
    expect(completion.status(), await completion.text()).toBe(200);
    const callID = completion.headers()["x-litellm-call-id"];
    expect(callID).toBeTruthy();
    await expect.poll(async () => {
      const response = await page.request.get(GATEWAY + "/admin/archive/kodo", {headers});
      expect(response.status()).toBe(200);
      return (await response.json()).events.some((event: {request_id: string; state: string}) => event.request_id === callID && event.state === "pending");
    }).toBe(true);
    const log = await page.request.get(GATEWAY + "/spend/logs/ui/" + callID, {headers});
    expect(log.status(), await log.text()).toBe(200);
    expect((await log.json()).model).toBe("gpt-4o-mini");
    await archiveOverview.getByRole("button", {name:"配置 Kodo 归档"}).click();
    await archive.getByRole("button", {name: "加载配置与状态"}).click();
    await expect(archive.getByLabel("归档队列")).toContainText("待处理 " + ((before.counts.pending ?? 0) + 1));
    await page.keyboard.press("Escape");
    await archiveOverview.getByRole("switch", {name: "启用计费日志归档"}).click();
    await expect(archiveOverview.getByRole("switch", {name: "启用计费日志归档"})).not.toBeChecked();
    const disabledCall = await page.request.post(GATEWAY + "/v1/chat/completions", {headers: {Authorization: "Bearer " + key}, data: {model: "gpt-4o-mini", messages: [{role: "user", content: "Verify archive disabled"}]}});
    expect(disabledCall.status(), await disabledCall.text()).toBe(200);
    const disabledID = disabledCall.headers()["x-litellm-call-id"];
    await expect.poll(async () => (await page.request.get(GATEWAY + "/spend/logs/ui/" + disabledID, {headers})).status()).toBe(200);
    const afterResponse = await page.request.get(GATEWAY + "/admin/archive/kodo", {headers});
    expect(afterResponse.status()).toBe(200);
    const after = await afterResponse.json();
    expect(after.configuration).toMatchObject({enabled: false, interval_seconds: 86400, batch_size: 1});
    expect(after.counts.pending).toBe((before.counts.pending ?? 0) + 1);
    expect(after.events.some((event: {request_id: string}) => event.request_id === disabledID)).toBe(false);
    expect(JSON.stringify(after)).not.toContain("Verify billing archive queue");
    expect(JSON.stringify(after)).not.toContain("e2e-fake");
  } finally {
    const stopped = await page.request.put(GATEWAY + "/admin/archive/kodo", {headers, data: {bucket: "xhub-e2e-private", region: "z0", prefix: "billing", interval_seconds: 86400, batch_size: 1, enabled: false}});
    expect(stopped.status(), await stopped.text()).toBe(200);
    const removed = await page.request.post(GATEWAY + "/key/delete", {headers, data: {keys: [key]}});
    expect(removed.status(), await removed.text()).toBe(200);
  }
});
}
