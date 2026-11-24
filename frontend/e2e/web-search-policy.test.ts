import { expect, test } from "@playwright/test";
import { translate } from "../src/i18n/translate";
import { GATEWAY, UPSTREAM, loginAdmin, sessionBearer, stableGoto } from "./helpers";

for (const locale of ["zh-CN", "en"] as const) {
  /** 前置真实浏览器、私有数据库与本地 Chat 上游；验证默认拒绝、双语编辑保存、JSON/SSE 降级、强制搜索拒绝及日志提示。
   * 提示词保持英文示例和用户原始输入；finally 删除模型/凭据，运行器回收账单和专属 schema，不使用外部供应商。 */
  test("web search policy editor and data plane " + locale, async ({ page }) => {
    test.setTimeout(120000);
    await loginAdmin(page);
    if (locale === "en") await page.getByRole("button", { name: "English", exact: true }).click();
    const tr = (key: string) => translate(locale, key);
    const bearer = await sessionBearer(page);
    const headers = { Authorization: "Bearer " + bearer };
    const name = "e2e-search-policy-" + locale;
    const credential = name + "-connection";
    let id = "";
    const extraIDs: string[] = [];
    expect((await page.request.post(GATEWAY + "/credentials", { headers, data: { credential_name: credential, credential_info: { custom_llm_provider: "openai" }, credential_values: { api_base: UPSTREAM + "/v1", api_key: "sk-fake" } } })).status()).toBe(200);
    /** 从真实浏览器经同源代理调用 Responses，返回状态、正文及降级/账单头；参数 stream/choice 指定协议模式与工具选择，无其他副作用。 */
    const call = async (stream: boolean, choice: unknown = "auto") => page.evaluate(async ({ bearer, name, stream, choice }) => {
      const response = await fetch("/gw/v1/responses", { method: "POST", headers: { Authorization: "Bearer " + bearer, "Content-Type": "application/json" }, body: JSON.stringify({ model: name, input: "Keep my original input. 保留我的输入。", tools: [{ type: "web_search" }], tool_choice: choice, stream }) });
      return { status: response.status, raw: await response.text(), dropped: response.headers.get("x-xhub-dropped-capabilities"), callID: response.headers.get("x-litellm-call-id") };
    }, { bearer, name, stream, choice });
    try {
      const created = await page.request.post(GATEWAY + "/model/new", { headers, data: { model_name: name, litellm_params: { model: "fake-model", custom_llm_provider: "openai", litellm_credential_name: credential, input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 }, model_info: { transport: "bypass_openai_chat", endpoint_types: ["responses"], pricing_source: "manual", base_model: "deepseek/deepseek-v4.1-flash", supports_vision: false } } });
      expect(created.status(), await created.text()).toBe(200);
      id = (await created.json()).model_info.id;
      await stableGoto(page, "/models-and-endpoints?model=" + id);
      const rejected = await call(false);
      expect(rejected.status, rejected.raw).toBe(400);
      expect(rejected.raw).toContain("web_search");
      await page.getByRole("button", { name: tr("providerGuideUI.editModel"), exact: true }).click();
      await page.getByText(tr("providerGuideUI.advancedProtocol"), { exact: true }).click();
      await expect(page.getByLabel(tr("providerGuideUI.searchSupport"))).toHaveValue("inherit");
      await expect(page.getByLabel(tr("providerGuideUI.searchFallback"))).toHaveValue("reject");
      await page.getByLabel(tr("providerGuideUI.searchSupport")).selectOption("unsupported");
      await page.getByLabel(tr("providerGuideUI.searchFallback")).selectOption("drop_optional");
      await expect(page.getByText(tr("providerGuideUI.searchDropHelp"))).toBeVisible();
      // 真正切换界面语言，确认表单值不丢失、新区域无遗留旧语言文案。
      const other = locale === "en" ? "zh-CN" : "en";
      await page.getByRole("button", { name: translate(locale, other === "en" ? "language.en" : "language.zhCN"), exact: true }).click();
      await expect(page.getByLabel(translate(other, "providerGuideUI.searchSupport"))).toHaveValue("unsupported");
      await expect(page.getByText(tr("providerGuideUI.searchDropHelp"))).toHaveCount(0);
      await page.getByRole("button", { name: translate(other, locale === "en" ? "language.en" : "language.zhCN"), exact: true }).click();
      const saving = page.waitForResponse(r => r.request().method() === "PATCH" && new URL(r.url()).pathname.endsWith("/update"));
      await page.getByRole("button", { name: tr("providerGuideUI.saveChanges"), exact: true }).click();
      expect((await saving).status()).toBe(200);
      await page.reload();
      await page.getByRole("button", { name: tr("providerGuideUI.editModel"), exact: true }).click();
      await page.getByText(tr("providerGuideUI.advancedProtocol"), { exact: true }).click();
      await expect(page.getByLabel(tr("providerGuideUI.searchSupport"))).toHaveValue("unsupported");
      await expect(page.getByLabel(tr("providerGuideUI.searchFallback"))).toHaveValue("drop_optional");
      await page.getByRole("button", { name: tr("providerGuideUI.cancel"), exact: true }).click();
      // 透传不能绕过模型的搜索设置，也不能把普通同名函数误判为托管搜索。
      const native = await page.evaluate(async ({ bearer, name }) => {
        const post = async (extra: object) => {
          const response = await fetch("/gw/bypass/openai/v1/chat/completions", {
            method: "POST", headers: { Authorization: "Bearer " + bearer, "Content-Type": "application/json" },
            body: JSON.stringify({ model: name, messages: [{ role: "user", content: "Hello" }], ...extra }),
          });
          return { status: response.status, raw: await response.text() };
        };
        return {
          blocked: await post({ web_search_options: {} }),
          ordinary: await post({ tools: [{ type: "function", function: { name: "web_search", parameters: { type: "object", properties: {} } } }] }),
        };
      }, { bearer, name });
      expect(native.blocked.status, native.blocked.raw).toBe(400);
      expect(native.blocked.raw).toContain("unsupported_capability");
      expect(native.ordinary.status, native.ordinary.raw).toBe(200);
      // 市场目录支持视觉不代表此连接已测通；公开列表与模型卡片均应尊重部署显式关闭。
      const available = await page.request.get(GATEWAY + "/model/available", { headers });
      expect(available.ok()).toBeTruthy();
      const visible = (await available.json()).data.find((model: { id: string }) => model.id === name);
      expect(visible?.capabilities).not.toContain("vision");
      await stableGoto(page, "/mine-models");
      const card = page.getByTestId("my-model-" + name);
      await expect(card).toBeVisible();
      await expect(card.getByText(tr("priceCatalog.vision"), { exact: true })).toHaveCount(0);
      // 同名模型若会路由到未验证图片能力的连接，模型卡不能按首个连接作出整体支持承诺。
      const mixed = name + "-mixed";
      for (const supportsVision of [true, false]) {
        const createdMixed = await page.request.post(GATEWAY + "/model/new", { headers, data: {
          model_name: mixed,
          litellm_params: { model: "fake-model", custom_llm_provider: "openai", litellm_credential_name: credential, input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 },
          model_info: { transport: "bypass_openai_chat", endpoint_types: ["chat"], pricing_source: "manual", base_model: "deepseek/deepseek-v4.1-flash", supports_vision: supportsVision },
        } });
        expect(createdMixed.status(), await createdMixed.text()).toBe(200);
        extraIDs.push((await createdMixed.json()).model_info.id);
      }
      const mixedList = await page.request.get(GATEWAY + "/model/available", { headers });
      const mixedCard = (await mixedList.json()).data.find((model: { id: string }) => model.id === mixed);
      expect(mixedCard?.capabilities).not.toContain("vision");
      await page.reload();
      await expect(page.getByTestId("my-model-" + mixed)).toBeVisible();
      await expect(page.getByTestId("my-model-" + mixed).getByText(tr("priceCatalog.vision"), { exact: true })).toHaveCount(0);
      const mixedCall = await page.request.post(GATEWAY + "/v1/chat/completions", { headers, data: { model: mixed, messages: [{ role: "user", content: "Hello" }] } });
      expect(mixedCall.status(), await mixedCall.text()).toBe(200);
      let lastCall = "";
      for (const stream of [false, true]) {
        const result = await call(stream);
        expect(result.status, result.raw).toBe(200);
        expect(result.dropped).toBe("web_search");
        expect(result.raw).toContain("e2e-ok");
        if (stream) expect(result.raw).toContain("response.completed");
        lastCall = result.callID!;
        await expect.poll(async () => {
          const response = await page.request.get(GATEWAY + "/spend/logs/ui/" + result.callID, { headers });
          return response.ok() ? (await response.json()).metadata?.upstream_response?.dropped_capabilities : null;
        }).toEqual(["web_search"]);
      }
      for (const choice of ["required", { type: "web_search" }]) {
        const result = await call(false, choice);
        expect(result.status, result.raw).toBe(400);
        expect(result.raw).toContain("unsupported_capability");
        expect(result.raw).toContain("web_search");
      }
      await stableGoto(page, "/logs");
      await page.getByPlaceholder(/按 ID 搜索日志|Search logs by ID/).fill(lastCall);
      await page.getByRole("row").filter({ hasText: lastCall }).click();
      await expect(page.getByRole("dialog").getByRole("status")).toHaveText(tr("providerGuideUI.searchDropped").replace("{capabilities}", "web_search"));
    } finally {
      for (const extraID of extraIDs) expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id: extraID } })).status()).toBe(200);
      if (id) expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id } })).status()).toBe(200);
      expect((await page.request.delete(GATEWAY + "/credentials/" + credential, { headers })).status()).toBe(200);
    }
  });
}
