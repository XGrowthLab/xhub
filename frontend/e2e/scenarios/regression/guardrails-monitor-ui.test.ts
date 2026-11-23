import { expect, test } from "@playwright/test";
import { GATEWAY, loginAdmin, sessionBearer, stableGoto } from "../../helpers";
import { translate } from "../../../src/i18n/translate";

/** 用真实网关写入放行与拦截记录，再从桌面与窄窗核对双语统计、键盘详情和日志。
 * 前置隔离 PostgreSQL 与本地上游；finally 删除测试护栏，运行器回收其余日志与 schema。 */
test("guardrail monitor shows real outcomes on desktop and mobile", async ({ page }) => {
  await loginAdmin(page);
  const headers = { Authorization: "Bearer " + await sessionBearer(page) };
  const name = "护栏监控-" + Date.now();
  const rejectedWord = "monitor-e2e-block-this-sample";
  const created = await page.request.post(GATEWAY + "/guardrails", {
    headers,
    data: {
      guardrail_name: name,
      litellm_params: { guardrail: "blocked_words", blocked_words: [rejectedWord], mode: "pre_call", default_on: false },
    },
  });
  expect(created.status(), await created.text()).toBe(200);
  const guardrailID = (await created.json()).guardrail_id;
  try {
    for (const [prompt, status] of [
      ["Check a harmless example.", 200],
      [rejectedWord, 400],
    ] as const) {
      const result = await page.request.post(GATEWAY + "/v1/chat/completions", {
        headers,
        data: { model: "gpt-4o-mini", guardrails: [name], messages: [{ role: "user", content: prompt }] },
      });
      expect(result.status(), await result.text()).toBe(status);
    }
    await expect.poll(async () => {
      const result = await page.request.get(GATEWAY + "/guardrails/usage/overview", { headers });
      expect(result.status(), await result.text()).toBe(200);
      return (await result.json()).rows.find((row: { name: string }) => row.name === name);
    }).toMatchObject({ requestsEvaluated: 2, failRate: 50 });

    await page.setViewportSize({ width: 1440, height: 900 });
    await stableGoto(page, "/guardrails-monitor");
    const zh = (key: string) => translate("zh-CN", key);
    const en = (key: string) => translate("en", key);
    await expect(page.getByRole("region", { name: zh("pages.guardrailsMonitor.metrics") })).toBeVisible();
    const zhPerformance = page.getByRole("region", { name: zh("Guardrail Performance") });
    await expect(zhPerformance.getByRole("row").filter({ hasText: name })).toContainText("50%");
    await expect(page.getByRole("heading", { name: zh("Request Outcomes Over Time"), level: 2 })).toBeVisible();
    await page.getByRole("button", { name: "English", exact: true }).click();
    await expect(page.getByRole("region", { name: en("pages.guardrailsMonitor.metrics") })).toBeVisible();
    await expect(page.getByRole("region", { name: zh("pages.guardrailsMonitor.metrics") })).toHaveCount(0);
    await page.setViewportSize({ width: 420, height: 840 });
    const main = page.getByRole("main");
    await expect.poll(() => main.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    await expect(main.getByRole("button", { name: en("pages.guardrailsMonitor.dateRange"), exact: true })).toBeInViewport();
    const chart = page.getByRole("region", { name: en("Request Outcomes Over Time") });
    await chart.getByText(en("pages.guardrailsMonitor.dailyTotals"), { exact: true }).press("Enter");
    const daily = chart.getByRole("table");
    await expect(daily.getByRole("columnheader", { name: en("pages.guardrailsMonitor.passed") })).toBeVisible();
    await expect(daily.getByRole("columnheader", { name: en("pages.guardrailsMonitor.blocked") })).toBeVisible();
    await expect(daily.getByRole("rowheader").last()).toBeVisible();
    const performance = page.getByRole("region", { name: en("Guardrail Performance") });
    await expect(performance.getByRole("button", { name: en("Evaluation settings"), exact: true })).toHaveCount(0);
    await performance.getByRole("button", { name, exact: true }).press("Enter");
    await expect(page.getByRole("heading", { name, level: 1 })).toBeVisible();
    await expect(main.getByRole("button", { name: en("Evaluation settings"), exact: true })).toHaveCount(0);
    await page.getByRole("tab", { name: en("Logs"), exact: true }).click();
    const logs = page.getByRole("tabpanel", { name: en("Logs"), exact: true });
    const blockedFilter = logs.getByRole("button", { name: en("Blocked"), exact: true });
    await blockedFilter.click();
    await expect(blockedFilter).toHaveAttribute("aria-pressed", "true");
    await expect(logs.getByText(translate("en", "Showing {value0} of {total} entries", { value0: 1, total: 2 }))).toBeVisible();
    await page.getByRole("button", { name: en("Back to Overview"), exact: true }).click();
    await expect(performance.getByRole("button", { name, exact: true })).toBeVisible();
  } finally {
    const deleted = await page.request.delete(GATEWAY + "/guardrails/" + encodeURIComponent(guardrailID), { headers });
    expect(deleted.ok(), await deleted.text()).toBeTruthy();
  }
});
