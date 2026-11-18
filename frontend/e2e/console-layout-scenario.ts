import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { translate } from "../src/i18n/translate";
import { formatConsoleMoney } from "../src/components/console/money";
import { expectKeyFiltersAndColumns, expectRunnablePlaygroundCode } from "./console-details-scenario";
import { GATEWAY, UPSTREAM, login, loginAdmin, sessionBearer, stableGoto } from "./helpers";

/** 保存本轮布局截图并检查页面级溢出；page 为真实浏览器，name 为测试证据名，图片只写隔离测试目录。 */
async function recordLayout(page: Page, name: string) {
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1),
    "页面不应被长名称撑出横向滚动",
  ).toBe(true);
  const directory = join(process.env.E2E_RUN_DIR!, "screenshots");
  mkdirSync(directory, { recursive: true });
  await page.screenshot({ path: join(directory, `${name}.png`), fullPage: false, animations: "disabled" });
}

/** 检查真实排版的同排卡片；page 为已加载列表，prefix 限定本测试模型。
 * 比较外框、价格和操作区的实际坐标，覆盖不同元数据长度；不依赖类名、不写数据。 */
async function expectAlignedModelCards(page: Page, prefix: string) {
  const cards = page
    .getByRole("article")
    .filter({ has: page.getByRole("heading", { name: new RegExp("^" + prefix) }) });
  await expect(cards).toHaveCount(3);
  const positions = await cards.evaluateAll((elements) =>
    elements.map((element) => {
      const bounds = element.getBoundingClientRect();
      const price = element.querySelector('[data-testid="model-price-summary"]')!.getBoundingClientRect();
      const actions = element.querySelector('[data-testid="model-actions"]')!.getBoundingClientRect();
      return { top: bounds.top, bottom: bounds.bottom, price: price.top, actions: actions.top };
    }),
  );
  for (let index = 1; index < positions.length; index++) {
    const first = positions[index - 1],
      next = positions[index];
    if (Math.abs(first.top - next.top) > 1) continue;
    for (const key of ["bottom", "price", "actions"] as const) {
      expect(Math.abs(first[key] - next[key]), `同排卡片 ${key} 必须对齐`).toBeLessThanOrEqual(1);
    }
  }
  expect(
    positions.some((entry, index) => index > 0 && Math.abs(entry.top - positions[index - 1].top) <= 1),
    "桌面必须实际验证至少一对并排卡片",
  ).toBe(true);
}

/** 检查用量工具栏无意外纵向滚动及按钮文字挤压；page 为真实用量页，locale 控制可访问名称。
 * 断言真实 DOM 尺寸，控件组可换行但标签保持完整；不改变预算或筛选规则。 */
async function expectReadableUsageControls(page: Page, locale: "en" | "zh-CN") {
  for (const tablist of await page.getByRole("tablist").all()) {
    if (!(await tablist.isVisible())) continue;
    expect(
      await tablist.evaluate((element) => element.scrollHeight - element.clientHeight),
      "切换条不能产生竖向滚动条",
    ).toBeLessThanOrEqual(1);
  }
  for (const key of ["usagePolish.table", "usagePolish.chart"]) {
    const button = page.getByRole("button", { name: translate(locale, key), exact: true }).first();
    await expect(button).toBeVisible();
    const size = await button.evaluate((element) => {
      const range = document.createRange();
      range.selectNodeContents(element);
      return {
        textHeight: range.getBoundingClientRect().height,
        lineHeight: parseFloat(getComputedStyle(element).lineHeight),
        overflow: element.scrollWidth - element.clientWidth,
      };
    });
    expect(size.textHeight, `${key} 不应挤成多行或竖排`).toBeLessThanOrEqual(size.lineHeight + 1);
    expect(size.overflow, `${key} 不应截断文字`).toBeLessThanOrEqual(1);
  }
}

/** 控制台双语真实闭环；locale 指定语言，独立用户/模型由真实管理接口创建。
 * 验证浏览器创建密钥、调用、结算、预算展示、模型详情、监控、账户复制及窄屏；finally 删除本场景资源，外层回收 schema。 */
export function registerConsoleLayoutScenario(locale: "en" | "zh-CN") {
  test("console layout and real user flows " + locale, async ({ page }) => {
    test.setTimeout(180_000);
    await page.setViewportSize({ width: 1440, height: 1000 });
    await loginAdmin(page);
    const admin = { Authorization: "Bearer " + (await sessionBearer(page)) };
    const email = `layout-${locale}-${Date.now()}@example.com`.toLowerCase();
    const model = `gpt-console-layout-${locale}`;
    const tr = (key: string) => translate(locale, key);
    const alias = "My desktop key";
    const extraModelIDs: string[] = [];
    let userID = "",
      modelID = "",
      keyID = "";
    try {
      const deployment = await page.request.post(GATEWAY + "/model/new", {
        headers: admin,
        data: {
          model_name: model,
          litellm_params: {
            model: "gpt-4o-mini",
            custom_llm_provider: "openai",
            api_base: UPSTREAM + "/v1",
            api_key: "sk-fake",
            input_cost_per_token: 0.000001,
            output_cost_per_token: 0.000002,
          },
          model_info: {
            transport: "bypass_openai_chat",
            endpoint_types: ["chat"],
            pricing_source: "manual",
            max_input_tokens: 128000,
            max_output_tokens: 16000,
          },
        },
      });
      expect(deployment.status(), await deployment.text()).toBe(200);
      modelID = (await deployment.json()).model_info.id;
      // 与稀疏模型并排放置最长能力说明，避免只测同构卡片漏掉线上错位。
      for (const variant of ["brief", "features"]) {
        const detailed = variant === "features";
        const extra = await page.request.post(GATEWAY + "/model/new", {
          headers: admin,
          data: {
            model_name: model + "-" + variant,
            litellm_params: {
              model: "e2e-layout-no-metadata",
              custom_llm_provider: "openai",
              api_base: UPSTREAM + "/v1",
              api_key: "sk-fake",
              input_cost_per_token: 0.000001,
              output_cost_per_token: 0.000003,
              ...(detailed
                ? {
                    cache_read_input_token_cost: 0.0000001,
                    cache_creation_input_token_cost: 0.000002,
                    rates: [
                      { measure: "token", side: "input", window: "all", usd: 0.000001 },
                      { measure: "token", side: "output", window: "all", usd: 0.000003 },
                      { measure: "token", side: "cache_read", window: "all", usd: 0.0000001 },
                      { measure: "token", side: "cache_write", window: "all", usd: 0.000002 },
                      { measure: "query", side: "output", variant: "search", window: "all", usd: 0.01 },
                    ],
                  }
                : {}),
            },
            model_info: {
              transport: "bypass_openai_chat",
              endpoint_types: ["chat"],
              pricing_source: "manual",
              category: detailed ? "chat" : "llm",
              supports_function_calling: detailed,
              supports_response_schema: detailed,
              supports_reasoning: detailed,
              supports_vision: detailed,
              supports_prompt_caching: detailed,
              ...(detailed ? { max_input_tokens: 1000000, max_output_tokens: 384000 } : {}),
            },
          },
        });
        expect(extra.status(), await extra.text()).toBe(200);
        extraModelIDs.push((await extra.json()).model_info.id);
      }
      const account = await page.request.post(GATEWAY + "/user/new", {
        headers: admin,
        data: { user_email: email, password: "fixture-password", max_budget: 500, budget_reset_day: 0 },
      });
      expect(account.status(), await account.text()).toBe(200);
      userID = (await account.json()).user_id;
      if (locale === "en") await page.getByRole("button", { name: "English", exact: true }).click();
      await login(page, email, "fixture-password", locale);
      await expect(page.getByText(tr("nav.apiKeys")).first()).toBeVisible();
      const auth = { Authorization: "Bearer " + (await sessionBearer(page)) };

      // 从真实表单创建；折叠说明不应遮盖主字段，也不能改变空预算与零速率的既有含义。
      await page.getByTestId("create-key-button").click();
      await page.getByLabel(tr("Key Name")).fill(alias);
      const dialog = page.getByRole("dialog");
      await expect(dialog.getByText(tr("quotaGuide.key"), { exact: true })).toBeHidden();
      await dialog.getByText(tr("consoleUi.budgetHelp"), { exact: true }).click();
      await expect(dialog.getByText(tr("quotaGuide.key"), { exact: true })).toBeVisible();
      await dialog.getByText(tr("consoleUi.budgetHelp"), { exact: true }).click();
      await expect(dialog.getByText(tr("keyEditor.expiryTitle"), { exact: true })).toHaveCount(0);
      await expect(dialog.getByRole("textbox", { name: tr("keyEditor.expire"), exact: true })).toHaveCount(1);
      await recordLayout(page, `key-form-${locale}`);
      const created = page.waitForResponse(
        (response) => response.url().includes("/key/generate") && response.request().method() === "POST",
      );
      await dialog.getByRole("button", { name: tr("pages.apiKeys.createSubmit"), exact: true }).click();
      const response = await created;
      expect(response.status(), await response.text()).toBe(200);
      const key = await response.json();
      keyID = key.token_id;
      expect(key.max_budget ?? null).toBeNull();
      await page.keyboard.press("Escape");
      const row = page.getByRole("row").filter({ hasText: alias });
      await expect(row).toHaveCount(1);
      await expect(row.getByText(alias, { exact: true })).toHaveCount(1);
      const date = row.locator("time");
      await expect(date).toHaveCount(1);
      if (locale === "zh-CN") await expect(date).toContainText("年");
      else await expect(date).toContainText(/[A-Z][a-z]{2}/);
      await expectKeyFiltersAndColumns(page, locale);
      // 使用新密钥实际调用，页面布局变更不能扩权、改变预算或破坏调用链。
      const called = await page.request.post(GATEWAY + "/v1/chat/completions", {
        headers: { Authorization: "Bearer " + key.key },
        data: { model, messages: [{ role: "user", content: "Hello" }] },
      });
      expect(called.status(), await called.text()).toBe(200);
      expect((await called.json()).choices[0].message.content).toBeTruthy();

      await page.getByRole("button", { name: translate(locale, "consoleUi.accountMenu", { identity: email }) }).click();
      await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
      await page.getByRole("button", { name: tr("consoleUi.copyEmail"), exact: true }).click();
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(email);
      await recordLayout(page, `account-${locale}`);
      await page.keyboard.press("Escape");

      await page.getByRole("link", { name: tr("nav.myModels"), exact: true }).click();
      await page.getByRole("textbox", { name: tr("Search"), exact: true }).fill(model);
      const card = page.getByRole("article").filter({ has: page.getByRole("heading", { name: model, exact: true }) });
      await expect(card).toBeVisible();
      expect((await card.boundingBox())!.height).toBeLessThan(600);
      for (const width of [1280, 1920, 1440]) {
        await page.setViewportSize({ width, height: 1000 });
        await expectAlignedModelCards(page, model);
      }
      await page.evaluate(() => (document.documentElement.style.fontSize = "20px"));
      await expectAlignedModelCards(page, model);
      await page.evaluate(() => document.documentElement.style.removeProperty("font-size"));
      const pageBounds = await card.boundingBox();
      await page.getByRole("combobox", { name: tr("myModels.sort"), exact: true }).click();
      await page.getByRole("option", { name: tr("myModels.sortInput"), exact: true }).click();
      await expect(card).toBeVisible();
      expect((await card.boundingBox())!.x).toBeCloseTo(pageBounds!.x, 0);
      await page.getByRole("button", { name: tr("myModels.filters"), exact: true }).click();
      // 历史 llm 与默认 chat 仅展示一个对话类别，筛选后保留全部三条真实部署。
      await expect(page.getByRole("button", { name: tr("Chat"), exact: true })).toHaveCount(1);
      await expect(page.getByRole("region", { name: tr("Chat"), exact: true })).toHaveCount(1);
      await page.getByRole("button", { name: tr("Chat"), exact: true }).click();
      await expect(page.getByRole("article")).toHaveCount(3);
      await page.getByRole("combobox", { name: tr("Provider"), exact: true }).click();
      await page.getByRole("option", { name: "openai", exact: true }).click();
      await expect(card).toBeVisible();
      await page.getByRole("button", { name: new RegExp("^" + tr("myModels.filters") + "(?:\\s+\\d+)?$") }).click();
      const beforeHelp = await card.boundingBox();
      await page.getByRole("button", { name: tr("consoleUi.priceHelp"), exact: true }).click();
      await expect(page.getByText(tr("myModels.units"), { exact: true })).toBeVisible();
      expect((await card.boundingBox())!.y, "价格说明使用浮层，不应撑开模型网格").toBeCloseTo(beforeHelp!.y, 0);
      await page.keyboard.press("Escape");
      expect(
        await page.evaluate(() => document.documentElement.scrollHeight - innerHeight),
        "控制台只应有一个内容滚动区",
      ).toBeLessThanOrEqual(1);
      await expect(page.getByText("vxhub-dev", { exact: true })).toHaveCount(0);
      const iconResponse = await page.request.get("/ui/icon.svg");
      expect(iconResponse.status()).toBe(200);
      expect(await iconResponse.text()).toContain("<svg");
      await recordLayout(page, `models-${locale}`);
      await page.getByRole("button", { name: tr("consoleUi.darkMode"), exact: true }).click();
      await expect(page.locator("html")).toHaveClass(/dark/);
      await recordLayout(page, `models-${locale}-dark`);
      await page.getByRole("button", { name: tr("consoleUi.lightMode"), exact: true }).click();
      await card.getByRole("button", { name: tr("consoleUi.modelDetails"), exact: true }).click();
      await expect(page.getByRole("dialog").getByRole("tab")).toHaveCount(4);
      await page.keyboard.press("Escape");
      await expect(card.getByRole("button", { name: tr("consoleUi.modelDetails"), exact: true })).toBeFocused();

      // 完整价格按实际单位标注，搜索按次价格不能被表头误说成每百万 Token。
      const featureCard = page
        .getByRole("article")
        .filter({ has: page.getByRole("heading", { name: model + "-features", exact: true }) });
      await featureCard.getByRole("button", { name: tr("consoleUi.modelDetails"), exact: true }).click();
      const priceDetail = page.getByRole("dialog");
      await expect(
        priceDetail.getByRole("columnheader", { name: tr("myModels.unitPrice"), exact: true }),
      ).toBeVisible();
      const searchRate = priceDetail.getByRole("row").filter({ hasText: tr("marketSync.variant.search") });
      await expect(searchRate).toContainText("$0.01");
      await expect(searchRate).toContainText("/ " + tr("marketSync.unit.query"));
      await expect(priceDetail).toContainText(tr("myModels.priceNote"));
      await page.keyboard.press("Escape");

      await stableGoto(page, "/playground");
      await page.getByPlaceholder(tr("Select a Model"), { exact: true }).click();
      await page.getByRole("option", { name: model, exact: true }).click();
      await page.getByRole("button", { name: "Write me a poem", exact: true }).click();
      await expect(page.getByTestId("chat-composer-input")).toHaveValue("Write me a poem");
      const original = "你好，保留这段用户输入。";
      await page.getByTestId("chat-composer-input").fill(original);
      await page.getByRole("button", { name: locale === "en" ? "中文" : "English", exact: true }).click();
      await expect(page.getByTestId("chat-composer-input")).toHaveValue(original);
      await page.getByRole("button", { name: locale === "en" ? "English" : "中文", exact: true }).click();
      await expectRunnablePlaygroundCode(page, locale, model, key.key);
      await page.getByTestId("chat-send-button").click();
      await expect(page.getByTestId("message-surface").filter({ hasText: /e2e-ok/ })).toBeVisible();
      await expect(page.getByTestId("message-surface").filter({ hasText: original })).toBeVisible();
      await expect(
        page
          .getByTestId("message-surface")
          .filter({ hasText: original })
          .getByText(tr("consoleUi.roles.user"), { exact: true }),
      ).toBeVisible();
      await expect(
        page
          .getByTestId("message-surface")
          .filter({ hasText: /e2e-ok/ })
          .getByText(tr("consoleUi.roles.assistant"), { exact: true }),
      ).toBeVisible();
      // 过渡容器不能截断全高调试区；在 1000px 高窗口中输入区应接近底部，而不是只占半屏。
      expect((await page.getByTestId("chat-composer-input").boundingBox())!.y).toBeGreaterThan(650);
      await recordLayout(page, `playground-${locale}`);

      let spent = 0;
      await expect
        .poll(async () => {
          const current = await page.request.get(GATEWAY + "/user/info?user_id=" + userID, { headers: auth });
          expect(current.status()).toBe(200);
          spent = Number((await current.json()).user_info.spend);
          return spent;
        })
        .toBeGreaterThan(0);
      // 删除已真实调用的测试密钥，验证历史别名仍保留且不会伪装成当前有效密钥。
      expect(
        (await page.request.post(GATEWAY + "/key/delete", { headers: auth, data: { keys: [keyID] } })).status(),
      ).toBe(200);
      keyID = "";
      await stableGoto(page, "/usage");
      const budget = page.getByRole("region", { name: tr("usageBudget.title"), exact: true });
      await expect(budget).toBeVisible();
      await expect(budget.getByRole("progressbar")).toHaveAttribute("aria-valuenow", String(Math.round(spent / 5)));
      await expect(budget).toContainText("$500.00");
      await expect(page.getByRole("tablist", { name: tr("pages.usage.viewTitle") })).toHaveCount(0);
      await expect(
        page.getByRole("paragraph").and(page.getByText(formatConsoleMoney(spent / 3, locale), { exact: true })),
      ).toBeVisible();
      const dailyChart = page.getByTestId("daily-spend-settled");
      await expect(dailyChart).toBeVisible();
      const barWidths = await dailyChart
        .locator(".recharts-bar-rectangle path")
        .evaluateAll((bars) => bars.map((bar) => Number(bar.getAttribute("width"))));
      expect(barWidths.length).toBeGreaterThan(0);
      expect(barWidths.every((width) => width > 0 && width <= 48)).toBe(true);
      const modelChart = page.getByTestId("top-models-spend");
      const modelLabel = modelChart.getByText(model, { exact: true });
      await expect(modelLabel).toBeVisible();
      expect(
        (await modelLabel.boundingBox())!.x - (await modelChart.boundingBox())!.x,
        "模型排行短标签左侧不能固定留出大块空白",
      ).toBeLessThan(24);
      await expectReadableUsageControls(page, locale);
      await page.evaluate(() => (document.documentElement.style.fontSize = "20px"));
      await expectReadableUsageControls(page, locale);
      await page.evaluate(() => document.documentElement.style.removeProperty("font-size"));
      await recordLayout(page, `usage-${locale}`);
      for (const tab of ["pages.usage.modelActivity", "pages.usage.keyActivity", "pages.usage.endpointActivity"]) {
        await page.getByRole("tab", { name: tr(tab), exact: true }).click();
        const panel = page.getByRole("tabpanel", { name: tr(tab), exact: true });
        await expect(
          panel.getByRole("region", { name: tr("usagePolish.singleDate"), exact: true }).first(),
        ).toBeVisible();
        if (tab === "pages.usage.keyActivity") {
          await panel.getByText(tr("usagePolish.historyTitle"), { exact: true }).click();
          await expect(panel.getByText(tr("usagePolish.historyHint"), { exact: true })).toBeVisible();
          await expect(panel.getByText(alias, { exact: true }).first()).toBeVisible();
        }
        const widths = await panel
          .locator(".recharts-bar-rectangle path")
          .evaluateAll((bars) => bars.map((bar) => Number(bar.getAttribute("width"))));
        expect(widths.length, "活动页签必须有真实数据柱").toBeGreaterThan(0);
        expect(
          widths.every((width) => width > 0 && width <= 40),
          "单日柱不能占满图表",
        ).toBe(true);
        await recordLayout(page, `${tab}-${locale}`);
      }
      await page.getByRole("tab", { name: tr("pages.usage.cost"), exact: true }).click();

      await stableGoto(page, "/model-monitor");
      await page.getByRole("combobox", { name: tr("monitor.model"), exact: true }).click();
      await page.getByRole("option", { name: "gpt-4o-mini", exact: true }).click();
      const modelCell = page.getByRole("rowheader", { name: "gpt-4o-mini", exact: true });
      await expect(modelCell).toBeVisible();
      expect(await modelCell.evaluate((element) => getComputedStyle(element).whiteSpace)).toBe("nowrap");
      await recordLayout(page, `monitor-${locale}`);

      // 窄屏和减少动画偏好：表格可以内部滚动，页面本身不能溢出，导航仍可操作。
      await page.emulateMedia({ reducedMotion: "reduce" });
      for (const width of [768, 390]) {
        await page.setViewportSize({ width, height: 844 });
        await stableGoto(page, "/mine-models");
        await expect(page.getByRole("complementary").first()).toHaveCSS("width", "64px");
        await page.getByRole("textbox", { name: tr("Search"), exact: true }).fill(model);
        await expect(page.getByRole("heading", { name: model, exact: true })).toBeVisible();
        await recordLayout(page, `models-${locale}-${width}`);
        await stableGoto(page, "/usage");
        await expect(page.getByRole("region", { name: tr("usageBudget.title"), exact: true })).toBeVisible();
        await expectReadableUsageControls(page, locale);
        await expect(page.getByRole("tabpanel", { name: tr("pages.usage.cost"), exact: true })).toHaveCSS(
          "animation-name",
          "none",
        );
        await recordLayout(page, `usage-${locale}-${width}`);
        await page.getByRole("button", { name: tr("Select Time Range"), exact: true }).click();
        await recordLayout(page, `date-picker-${locale}-${width}`);
        await page.getByRole("button", { name: tr("consoleUi.date.last30") }).click();
        const refreshed = page.waitForResponse(
          (response) =>
            response.url().includes("/user/daily/activity/aggregated") && response.request().method() === "GET",
        );
        await page.getByRole("button", { name: tr("Apply"), exact: true }).click();
        expect((await refreshed).status()).toBe(200);
        await expect(page.getByRole("region", { name: tr("usageBudget.title"), exact: true })).toContainText("$500.00");
        await stableGoto(page, "/playground");
        await expect(page.getByPlaceholder(tr("Select a Model"), { exact: true })).toBeVisible();
        await recordLayout(page, `playground-${locale}-${width}`);
      }
      const after = await page.request.get(GATEWAY + "/user/info?user_id=" + userID, { headers: admin });
      expect((await after.json()).user_info).toMatchObject({ max_budget: 500, budget_reset_day: 0 });
    } finally {
      for (const id of extraModelIDs)
        expect((await page.request.post(GATEWAY + "/model/delete", { headers: admin, data: { id } })).status()).toBe(
          200,
        );
      if (keyID)
        expect(
          (await page.request.post(GATEWAY + "/key/delete", { headers: admin, data: { keys: [keyID] } })).status(),
        ).toBe(200);
      if (userID)
        expect(
          (await page.request.post(GATEWAY + "/user/delete", { headers: admin, data: { user_id: userID } })).status(),
        ).toBe(200);
      if (modelID)
        expect(
          (await page.request.post(GATEWAY + "/model/delete", { headers: admin, data: { id: modelID } })).status(),
        ).toBe(200);
    }
  });
}
