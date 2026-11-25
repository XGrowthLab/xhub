import { expect, test, type Page } from "@playwright/test";
import { GATEWAY, loginAdmin, t, uiPath, watchGateway } from "./helpers";
import { recordPage } from "./report";
import { discoverPages } from "./routes";
import { requirePageSuccess } from "./assertions";
import { translate } from "../src/i18n/translate";

/** 用途：从已登录控制台 cookie 提取会话 bearer；参数为当前页面；返回会话 key，缺少 cookie 或非法 JWT 时抛错；供真实后台读接口使用，无写副作用。 */
async function sessionBearer(page: Page): Promise<string> {
  const cookies = await page.context().cookies();
  const token = cookies.find((c) => c.name === "token")?.value;
  const payload = JSON.parse(Buffer.from(token!.split(".")[1], "base64url").toString());
  return payload.key as string;
}

/** 用途：确认页面没有进入统一错误边界；参数为页面与路由标签；返回无；供全路由巡检调用，只读 DOM。 */
async function noDashboardError(page: Page, route: string) {
  await expect(page.getByText("This page couldn’t load"), route).toHaveCount(0);
  await expect(page.getByText(/Dashboard error:/), route).toHaveCount(0);
}

/** 用途：注册独立业务路径巡检；参数为页面与实际路径，无返回。
 * 每个路径独立登录并验证 HTTP/DOM/API，异常独立报告；上下文和隔离 schema 由运行器清理。 */
export function registerPageScenario(item: ReturnType<typeof discoverPages>[number], businessPath: string) {
 test("页面可用 " + businessPath, async ({ page }) => {
  test.setTimeout(300_000);
  const guard = watchGateway(page);
  const errors: string[] = [];
  const failedReads: string[] = [];
  let usageActivitySettled = false;
  const observed: Record<string, any> = {};
  page.on("pageerror", (err) => errors.push(`${page.url()}: ${err.message}`));
  page.on("response", async (res) => {
    const url = res.url();
    if (url.includes("/user/daily/activity") && res.status() < 400) usageActivitySettled = true;
    // 空态必须服从页面实际读到的 API；共享管理员已有用量时改验真实表格、图表或统计卡。
    if (res.status() < 400) {
      const path = new URL(url).pathname;
      if (path === "/user/daily/activity") observed.usage = await res.json().catch(() => undefined);
      if (path === "/spend/logs/ui") observed.logs = await res.json().catch(() => undefined);
      if (path === "/guardrails/usage/overview") observed.guardrails = await res.json().catch(() => undefined);
    }
    if (!/\/user\/daily\/activity|\/guardrails\/usage\/overview|\/key\/list/.test(url)) return;
    if (res.status() >= 400) failedReads.push(`${res.status()} ${res.request().method()} ${url}`);
  });
  await loginAdmin(page);
  const authHeaders = { Authorization: "Bearer " + (await sessionBearer(page)) };

    if (item.route === "/usage") usageActivitySettled = false;
    const response = await page.goto(businessPath.startsWith("/docs") ? businessPath : uiPath(businessPath));
    requirePageSuccess(response?.status(), businessPath, page.url());
    await noDashboardError(page, item.route);
    await expect(page.getByRole("heading", {name:/^(404|page not found|页面不存在)$/i})).toHaveCount(0);
    await expect(
      page
        .getByRole("heading")
        .or(page.getByRole("table"))
        .or(page.getByRole("tab"))
        .or(page.getByText(t("pages.costTracking.calculator")))
        .first(),
      item.route,
    ).toBeVisible({ timeout: 15_000 });
    if (!item.removed && item.route !== "/login") {
      await expect(page, item.route).not.toHaveURL(/\/login/);
    }
    if (item.route === "/usage") {
      const usageResponse = await page.request.get(GATEWAY + "/user/daily/activity", { headers: authHeaders });
      expect(usageResponse.status(), await usageResponse.text()).toBe(200);
      observed.usage = await usageResponse.json();
      await expect.poll(() => usageActivitySettled, { timeout: 15_000 }).toBe(true);
      const settled = page.getByTestId("daily-spend-settled");
      await expect(settled).toBeVisible({ timeout: 15_000 });
      await expect(page.getByText(t("Loading chart data..."))).toHaveCount(0);
      const requests = Number(observed.usage?.metadata?.total_api_requests ?? 0);
      if (requests === 0) {
        await expect(settled.getByText(t("No data"))).toBeVisible();
        await expect(settled.locator("svg")).toHaveCount(0);
      } else {
        await expect(settled.locator("svg").first()).toBeVisible();
        await expect(settled.getByText(t("No data"))).toHaveCount(0);
      }
    }
    if (item.route === "/logs") {
      const logsResponse = await page.request.get(GATEWAY + "/spend/logs/ui?page=1&page_size=1", { headers: authHeaders });
      expect(logsResponse.status(), await logsResponse.text()).toBe(200);
      observed.logs = await logsResponse.json();
      await expect.poll(() => observed.logs !== undefined, { timeout: 15_000 }).toBe(true);
      const rows = observed.logs?.data ?? observed.logs ?? [];
      if (rows.length === 0) await expect(page.getByText(t("logsUX.empty")).first()).toBeVisible({ timeout: 15_000 });
      else await expect(page.getByRole("table")).toBeVisible({ timeout: 15_000 });
    }
    // 前置：用已登录管理员读取隔离网关的规则清单。在窄屏调试未保存规则，验证按钮可见以及取消后清单无变化。
    if (item.route === "/guardrails") {
      const listResponse = await page.request.get(GATEWAY + "/guardrails/list", { headers: authHeaders });
      expect(listResponse.ok(), await listResponse.text()).toBe(true);
      const originalIds = (await listResponse.json()).guardrails.map((row: { guardrail_id: string }) => row.guardrail_id);
      await page.setViewportSize({ width: 520, height: 720 });
      const tabs = page.getByRole("tablist");
      await tabs.getByRole("tab", { name: t("pages.guardrails.garden"), exact: true }).click();
      await expect(tabs.getByRole("tab", { name: t("pages.guardrails.playground"), exact: true })).toBeInViewport();
      await expect.poll(() => tabs.evaluate((el) => el.scrollHeight - el.clientHeight)).toBeLessThanOrEqual(1);
      await page.getByRole("button", { name: /关键词 \/ 正则护栏/ }).click();
      const localDialog = page.getByRole("dialog", { name: "创建关键词 / 正则护栏" });
      await expect(localDialog).toBeVisible();
      await expect(localDialog.getByRole("button", { name: t("Cancel"), exact: true })).toBeInViewport();
      await expect(localDialog.getByRole("button", { name: t("保存护栏"), exact: true })).toBeInViewport();
      await expect.poll(() => localDialog.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1);
      await localDialog.getByLabel("护栏名称", { exact: true }).fill("ui-audit-draft");
      await localDialog.getByLabel("关键词（每行一个）", { exact: true }).fill("synthetic private marker");
      await localDialog.getByLabel("测试文本", { exact: true }).fill("Contains a synthetic private marker.");
      const trial = page.waitForResponse((r) => new URL(r.url()).pathname === "/guardrails/apply_guardrail");
      await localDialog.getByRole("button", { name: "测试当前规则", exact: true }).click();
      expect(await (await trial).json()).toMatchObject({ action: "block" });
      await expect(localDialog.getByLabel("测试结果", { exact: true })).toContainText('"action": "block"');
      await expect(localDialog.getByRole("button", { name: t("Cancel"), exact: true })).toBeInViewport();
      await localDialog.getByRole("button", { name: t("Cancel"), exact: true }).click();
      await expect(localDialog).toHaveCount(0);
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.getByRole("button", { name: "English", exact: true }).click();
      await page.getByRole("button", { name: /Custom XGo guardrail/ }).click();
      const xgoDialog = page.getByRole("dialog", { name: "Create XGo guardrail" });
      await expect(xgoDialog.getByLabel("XGo code", { exact: true })).toBeVisible();
      await expect.poll(() => xgoDialog.evaluate((el) => el.offsetWidth)).toBeLessThan(1250);
      await expect(xgoDialog.getByRole("button", { name: "Cancel", exact: true })).toBeInViewport();
      await xgoDialog.getByRole("button", { name: "Cancel", exact: true }).click();
      await expect(xgoDialog).toHaveCount(0);
      const afterResponse = await page.request.get(GATEWAY + "/guardrails/list", { headers: authHeaders });
      expect(afterResponse.ok(), await afterResponse.text()).toBe(true);
      expect((await afterResponse.json()).guardrails.map((row: { guardrail_id: string }) => row.guardrail_id)).toEqual(originalIds);
    }
    if (item.route === "/guardrails-monitor") {
      const guardrailsResponse = await page.request.get(GATEWAY + "/guardrails/usage/overview", { headers: authHeaders });
      expect(guardrailsResponse.status(), await guardrailsResponse.text()).toBe(200);
      observed.guardrails = await guardrailsResponse.json();
      await expect.poll(() => observed.guardrails !== undefined, { timeout: 15_000 }).toBe(true);
      const rows = observed.guardrails?.rows ?? [];
      if (rows.length === 0) await expect(page.getByText(t("pages.guardrailsMonitor.emptyPerformance")).first()).toBeVisible({ timeout: 15_000 });
      else await expect(page.getByRole("table")).toBeVisible({ timeout: 15_000 });
      await expect(page.getByText(t("Failed to load data. Try again."))).toHaveCount(0);
      await expect(page.getByText("评估次数", { exact: true })).toBeVisible();
      await expect(page.getByText("通过率", { exact: true })).toBeVisible();
      await expect(page.getByText("请求处理结果趋势", { exact: true })).toBeVisible();
      await page.getByRole("button", { name: "English", exact: true }).click();
      await expect(page.getByText("Total Evaluations", { exact: true })).toBeVisible();
      await expect(page.getByText("Request Outcomes Over Time", { exact: true })).toBeVisible();
    }
    if (item.route === "/route-templates") {
      await page.setViewportSize({ width: 760, height: 780 });
      // 在实际模板库切换两种语言并打开指南，校验字段、说明、示例与弹窗宽度。
      for (const locale of ["zh-CN", "en"] as const) {
        if (locale === "en") await page.getByRole("button", { name: "English", exact: true }).click();
        const tr = (key: string) => translate(locale, "pages.routeTemplates.jsonGuide." + key);
        await page.getByRole("button", { name: tr("openButton") }).click();
        const dialog = page.getByRole("dialog", { name: tr("openButton") });
        await expect(dialog).toContainText("retry_policy");
        await dialog.getByRole("tab", { name: tr("tabExecution") }).click();
        await expect(dialog.getByRole("list")).toContainText("max_attempts");
        await dialog.getByRole("tab", { name: tr("tabExamples") }).click();
        await expect(dialog.getByRole("region", { name: tr("exampleTitle") })).toContainText("model_routes");
        const box = await dialog.boundingBox();
        expect(box && box.x >= 0 && box.x + box.width <= 760).toBe(true);
        await page.keyboard.press("Escape");
        await expect(dialog).toBeHidden();
      }
    }
    if (item.route === "/admin-panel") {
      // SSO 和 SCIM 实际读取隔离网关配置。表单展开并关闭时只验证控件，不提交写操作。
      await page.setViewportSize({ width: 760, height: 800 });
      await page.getByRole("button", { name: t("enterprise.configureSSO"), exact: true }).click();
      const sso = page.getByRole("dialog", { name: t("enterprise.configureSSO") });
      await expect(sso.getByLabel(t("enterprise.publicURL"))).toBeVisible();
      const ssoBox = await sso.boundingBox();
      expect(ssoBox && ssoBox.x >= 0 && ssoBox.x + ssoBox.width <= 760).toBe(true);
      await page.keyboard.press("Escape");
      await expect(sso).toBeHidden();
      await page.getByRole("tab", { name: "SCIM" }).click();
      // 已经有连接时入口名称为「新建目录连接」，两种状态应打开同一创建表单。
      await page.getByRole("button", { name: t("enterprise.configureSCIM"), exact: true })
        .or(page.getByRole("button", { name: t("enterprise.newConnection"), exact: true })).click({ timeout: 15_000 });
      const scim = page.getByRole("dialog", { name: t("enterprise.configureSCIM") });
      await expect(scim.getByLabel(t("directory.name"))).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(scim).toBeHidden();

      await expect(page.getByRole("group", { name: t("enterprise.identitySection") }).getByRole("tab", { name: "SCIM" })).toBeVisible();
      const logs = page.getByRole("group", { name: t("enterprise.storageSection") }).getByRole("tab", { name: t("enterprise.tabs.logs") });
      await logs.click();
      await expect(logs).toHaveAttribute("aria-selected", "true");
      await page.getByRole("link", { name: t("pages.adminPanel.openUsers") }).click();
      await expect(page).toHaveURL(/\/ui\/users\/?$/);
    }
    requirePageSuccess(response?.status(), businessPath, page.url());
  expect(errors, errors.join("\n")).toEqual([]);
  expect(failedReads, failedReads.join("\n")).toEqual([]);
  await guard.assertOk();
  recordPage(item.route, "pass", businessPath);
 });
}

