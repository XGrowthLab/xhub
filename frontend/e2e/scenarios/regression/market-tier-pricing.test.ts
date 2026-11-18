import { expect, test } from "@playwright/test";
import { GATEWAY, UPSTREAM, loginAdmin, sessionBearer, stableGoto } from "../../helpers";

/** 真实浏览器经过管理界面确认分档，并通过隔离网关调用验证实际账单和中英文展示。
 * 使用独立数据库和本地供应商；finally 删除部署，测试运行器清理专属 schema 和进程。 */
test("market tier confirmation controls actual billing", async ({ page }) => {
  await loginAdmin(page);
  const headers = { Authorization: "Bearer " + (await sessionBearer(page)) };
  expect((await page.request.post(GATEWAY + "/reload/model_cost_map", { headers })).status()).toBe(200);
  const created = await page.request.post(GATEWAY + "/model/new", {
    headers,
    data: {
      model_name: "e2e-tier-deployment",
      litellm_params: {
        model: "e2e-tier-upstream",
        custom_llm_provider: "custom",
        api_base: UPSTREAM,
        api_key: "sk-fake",
        input_cost_per_token: 0.000001,
        output_cost_per_token: 0.000001,
      },
      model_info: {
        transport: "bypass_openai_chat",
        endpoint_types: ["chat"],
        pricing_source: "manual",
        base_model: "e2e-market-tier",
      },
    },
  });
  expect(created.status(), await created.text()).toBe(200);
  const id = (await created.json()).model_info.id;
  let sequence = 0;
  const cost = async () => {
    const response = await page.request.post(GATEWAY + "/v1/chat/completions", {
      headers,
      data: {
        model: "e2e-tier-deployment",
        messages: [{ role: "user", content: `Verify tier billing ${++sequence}.` }],
      },
    });
    expect(response.status(), await response.text()).toBe(200);
    return Number(response.headers()["x-litellm-response-cost"]);
  };
  try {
    const before = await cost();
    expect(before).toBeGreaterThan(0);
    await stableGoto(page, "/price-data");
    // 管理员目录的筛选与本地档案也使用统一控件，真实数据从价格同步接口加载。
    await page.getByRole("combobox", { name: "模型排序", exact: true }).click();
    await page.getByRole("option", { name: "输入价格", exact: true }).click();
    await page.getByRole("combobox", { name: "上下文长度", exact: true }).click();
    await page.getByRole("option", { name: "不限", exact: true }).click();
    await page.getByRole("tab", { name: "本地模型列表", exact: true }).click();
    await page.getByRole("combobox", { name: "本地模型状态", exact: true }).click();
    await page.getByRole("option", { name: "已下架", exact: true }).click();
    await page.getByRole("combobox", { name: "本地模型状态", exact: true }).click();
    await page.getByRole("option", { name: "全部", exact: true }).click();
    await page.getByRole("textbox", { name: "搜索本地模型", exact: true }).fill("e2e-market-tier");
    await expect(page.getByRole("row").filter({ hasText: "e2e-market-tier" })).toHaveCount(1);
    await page.getByRole("tab", { name: "部署价格更新", exact: true }).click();
    const section = page.getByRole("region", { name: "部署价格更新", exact: true });
    const row = section.getByRole("row").filter({ hasText: "e2e-tier-deployment" });
    await expect(row).toContainText("输入 Token：0 < 数量 ≤ 5");
    await expect(row).toContainText("输入 Token：5 < 数量 ≤ 99,999,999");
    await row.getByRole("button", { name: "核对并更新" }).click();
    const dialog = page.getByRole("dialog", { name: "确认更新 1 个部署" });
    await expect(dialog).toContainText("输入 Token：5 < 数量 ≤ 99,999,999");
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    expect(await cost()).toBeCloseTo(before, 10);
    await row.getByRole("button", { name: "核对并更新" }).click();
    await dialog.getByRole("button", { name: "确认更新价格" }).click();
    await expect(section.getByRole("status")).toHaveText("已更新 1 个部署的价格。");
    expect(await cost()).toBeCloseTo(0.000022, 10);
    await page.getByRole("button", { name: "English", exact: true }).click();
    await page.getByRole("tab", { name: "Deployment price updates", exact: true }).click();
    await expect(page.getByRole("row").filter({ hasText: "e2e-tier-deployment" })).toContainText(
      "Input tokens: 0 < amount ≤ 5",
    );
    // 真实授权卡片必须显示当前已确认档位，不再将分档部署标为缺价；语言切换不影响账单。
    await stableGoto(page, "/mine-models");
    await page.getByRole("textbox", { name: "Search", exact: true }).fill("e2e-tier-deployment");
    const card = page.getByRole("article").filter({ hasText: "e2e-tier-deployment" });
    await expect(card).toContainText("$1–$2");
    await expect(card).toContainText("$2–$3");
    await expect(card).not.toContainText("Price unavailable");
    await card.getByRole("button", { name: "Model details", exact: true }).click();
    const detail = page.getByRole("dialog");
    await expect(detail.getByRole("table", { name: "Input tokens: 0 < amount ≤ 5", exact: true })).toBeVisible();
    await expect(
      detail.getByRole("table", { name: "Input tokens: 5 < amount ≤ 99,999,999", exact: true }),
    ).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "中文", exact: true }).click();
    await card.getByRole("button", { name: "模型详情", exact: true }).click();
    await expect(detail.getByRole("table", { name: "输入 Token：5 < 数量 ≤ 99,999,999", exact: true })).toBeVisible();
    expect(await cost()).toBeCloseTo(0.000022, 10);
  } finally {
    expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id } })).status()).toBe(200);
  }
});
