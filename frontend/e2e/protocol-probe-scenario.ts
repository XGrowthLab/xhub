import { test, expect } from "@playwright/test";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { translate } from "../src/i18n/translate";
import { GATEWAY, loginAdmin, sessionBearer, stableGoto } from "./helpers";
import { protocolProbeFixture } from "./protocol-probe-fixture";

/** 双语完整验收：编辑保存开关→自动探测→手动探测及失败提示→发现接口→执行页面 curl→调试台实际 Chat 透传。
 * 前置真实浏览器、网关、隔离 PostgreSQL 与严格三协议 HTTP 夹具；不 mock API，不使用付费凭据，finally 删除部署/凭据并停止服务。 */
export function registerProtocolProbe(locale: "en" | "zh-CN") {
  test(`upstream protocol detection ${locale}`, async ({ page }, testInfo) => {
    const tr = (key: string) => translate(locale, key);
    const fixture = await protocolProbeFixture();
    const name = `protocol-${locale}-${Date.now()}`;
    let id = "";
    let key = "";
    let headers: Record<string, string> = {};
    try {
      await loginAdmin(page);
      await page.getByRole("button", { name: locale === "en" ? "English" : "中文", exact: true }).click();
      const bearer = await sessionBearer(page);
      headers = { Authorization: "Bearer " + bearer };
      const teams = await (await page.request.get(GATEWAY + "/v2/team/list?page=1&page_size=500", { headers })).json();
      const team = teams.teams.find((item: any) => item.team_alias === "e2e-fixture-team");
      const jwt = (await page.context().cookies()).find((c) => c.name === "token")!.value;
      const uid = JSON.parse(Buffer.from(jwt.split(".")[1], "base64url").toString()).user_id;
      const minted = await page.request.post(GATEWAY + "/key/generate", {
        headers,
        data: { key_alias: name, key_type: "llm_api", team_id: team.team_id, user_id: uid, models: [name] },
      });
      expect(minted.status(), await minted.text()).toBe(200);
      key = (await minted.json()).key;
      const cred = await page.request.post(GATEWAY + "/credentials", {
        headers,
        data: {
          credential_name: name,
          credential_info: { custom_llm_provider: "openai" },
          credential_values: { api_base: fixture.url, api_key: "fixture-protocol-key" },
        },
      });
      expect(cred.status(), await cred.text()).toBe(200);
      const created = await page.request.post(GATEWAY + "/model/new", {
        headers,
        data: {
          model_name: name,
          litellm_params: {
            model: "test",
            custom_llm_provider: "openai",
            litellm_credential_name: name,
            input_cost_per_token: 0.000001,
            output_cost_per_token: 0.000002,
          },
          model_info: { transport: "bypass_openai_responses", endpoint_types: ["responses"], pricing_source: "manual" },
        },
      });
      expect(created.status(), await created.text()).toBe(200);
      id = (await created.json()).model_info.id;
      await stableGoto(page, "/models-and-endpoints?model=" + id);
      await page.getByRole("button", { name: tr("providerGuideUI.editModel"), exact: true }).click();
      await page.getByRole("switch", { name: tr("protocolProbe.automatic") }).check();
      const saved = page.waitForResponse(
        (r) => r.request().method() === "PATCH" && new URL(r.url()).pathname.endsWith("/update"),
      );
      await page.getByRole("button", { name: tr("providerGuideUI.saveChanges"), exact: true }).click();
      const savedResponse = await saved;
      expect(savedResponse.status(), await savedResponse.text()).toBe(200);
      expect((await savedResponse.json()).model_info.auto_probe, "保存响应必须保留自动探测开关").toBe(true);
      await expect.poll(() => fixture.requests.length, { message: "保存后必须真实执行三协议四步探测" }).toBe(12);
      await page.getByRole("button", { name: tr("providerGuideUI.editModel"), exact: true }).click();
      const panel = page.getByRole("region", { name: tr("protocolProbe.title") });
      await expect(panel.getByText(tr("protocolProbe.verified"), { exact: true })).toHaveCount(3);
      const before = fixture.requests.length;
      await panel.getByRole("button", { name: tr("protocolProbe.manual"), exact: true }).click();
      await expect.poll(() => fixture.requests.length).toBe(before + 12);
      await expect(panel.getByRole("button", { name: tr("protocolProbe.manual"), exact: true })).toBeEnabled();
      fixture.failed = true;
      await panel.getByRole("button", { name: tr("protocolProbe.manual"), exact: true }).click();
      await expect(panel.getByText(tr("protocolProbe.temporarily_failed"), { exact: true })).toHaveCount(3);
      await expect(panel.getByText(tr("protocolProbe.retained"), { exact: true })).toHaveCount(3);
      fixture.failed = false;
      await stableGoto(page, "/mine-models");
      await page.getByRole("textbox", { name: tr("Search"), exact: true }).fill(name);
      const card = page.getByRole("article").filter({ has: page.getByRole("heading", { name, exact: true }) });
      await card.getByRole("button", { name: tr("myModels.api"), exact: true }).click();
      const dialog = page.getByRole("dialog", { name, exact: true });
      await dialog.screenshot({ path: testInfo.outputPath("protocol-selector-desktop.png") });
      // 页面生成的每个对话入口都实际执行；英文示例和公开名必须保持，未经验证媒体不出现在此列表。
      const available = await (await page.request.get(GATEWAY + "/model/available", { headers })).json();
      const endpoints = available.data.find((m: any) => m.id === name).endpoints;
      expect(endpoints).toHaveLength(10);
      for (const endpoint of endpoints) {
        await dialog.getByRole("combobox", { name: tr("myModels.selectEndpoint") }).click();
        await page
          .getByRole("option")
          .filter({ hasText: endpoint.path })
          .filter({ has: page.getByText(endpoint.path, { exact: true }) })
          .click();
        const command = await dialog
          .getByLabel(tr("myModels.copyExample"), { exact: true })
          .filter({ hasText: "curl" })
          .textContent();
        expect(command).toContain("Hello");
        expect(command).toContain(name);
        const { stdout: output } = await promisify(execFile)("bash", ["-c", command!], {
          env: { ...process.env, XHUB_API_KEY: key },
          timeout: 20000,
          encoding: "utf8",
        });
        expect(output).toContain("PROBE_OK");
      }
      await expect(page.getByRole("listbox")).toBeHidden();
      await page.setViewportSize({ width: 390, height: 844 });
      expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), "窄屏模型详情不应横向溢出").toBe(
        true,
      );
      await page.screenshot({ path: testInfo.outputPath("protocol-selector-mobile.png") });
      await page.setViewportSize({ width: 1440, height: 1000 });
      await stableGoto(page, "/playground");
      await page.getByPlaceholder(tr("Select a Model"), { exact: true }).fill(name);
      await page.getByRole("option").filter({ hasText: name }).click();
      await page.getByPlaceholder(tr("Select an endpoint"), { exact: true }).click();
      await page.getByRole("option").filter({ hasText: "/bypass/openai/v1/chat/completions" }).click();
      await page
        .getByPlaceholder(tr("Type your message... (Shift+Enter for new line)"))
        .fill("Reply briefly. 用户原文");
      await page.getByRole("button", { name: tr("Send message"), exact: true }).click();
      await expect(page.getByText("PROBE_OK", { exact: true })).toBeVisible();
      expect(JSON.stringify(fixture.requests.at(-1)?.body)).toContain("用户原文");
    } finally {
      try {
        if (key)
          expect((await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [key] } })).status()).toBe(
            200,
          );
        if (id)
          expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id } })).status()).toBe(200);
        if (headers.Authorization)
          expect((await page.request.delete(GATEWAY + "/credentials/" + name, { headers })).status()).toBe(200);
      } finally {
        await fixture.close();
      }
    }
  });
}
