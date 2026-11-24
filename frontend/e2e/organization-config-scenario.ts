import { expect, test } from "@playwright/test";
import { translate } from "../src/i18n/translate";
import { GATEWAY, UPSTREAM, loginAdmin, sessionBearer, stableGoto } from "./helpers";

/** 注册组织配置闭环：locale 决定可访问名称，返回无；真实页面和后台共用隔离数据库及本地供应商。
 * 覆盖创建、刷新回读、非法元数据提示、语言切换保留原文、修改模型后密钥拒绝、清空和删除；finally 删除自建资源。 */
export function registerOrganizationConfigScenario(locale: "en" | "zh-CN") {
  test("organization config roundtrip and model ceiling " + locale, async ({ page }) => {
    test.setTimeout(180_000);
    page.setDefaultTimeout(15_000);
    await loginAdmin(page);
    const headers = { Authorization: "Bearer " + await sessionBearer(page) };
    await page.getByRole("button", {name: locale === "en" ? "English" : translate("en", "language.zhCN"), exact: true}).click();
    const tr = (key: string) => translate(locale, key);
    const orgName = "config-" + locale;
    const metadata = {department: "研发试点", nested: {enabled: true, limit: 3}};
    let orgID = "", teamID = "", userID = "", keyID = "";
    /** 调用真实管理接口并断言成功；参数为路径和 JSON，返回解析对象；写入仅限当前测试组织，finally 回收。 */
    const create = async (path: string, data: unknown) => {
      const response = await page.request.post(GATEWAY + path, {headers, data});
      expect(response.status(), path + ": " + await response.text()).toBe(200);
      return response.json();
    };
    try {
      await stableGoto(page, "/organizations");
      await page.getByRole("button", {name: tr("pages.organizations.create"), exact: true}).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByLabel(tr("Organization Name"), {exact: true}).fill(orgName);
      await expect(dialog.getByText(tr("organizationConfig.modelScopeHint"))).toBeVisible();
      await dialog.getByRole("combobox", {name: tr("Select Models"), exact: true}).fill("gpt-4o-mini");
      await page.getByRole("option", {name: "gpt-4o-mini", exact: true}).click();
      await dialog.getByLabel(tr("Metadata"), {exact: true}).fill(JSON.stringify(metadata));
      const created = page.waitForResponse(r => r.url().endsWith("/organization/new") && r.request().method() === "POST");
      await dialog.getByRole("button", {name: tr("pages.organizations.createTitle"), exact: true}).click();
      const response = await created;
      expect(response.status(), await response.text()).toBe(200);
      const body = await response.json(); orgID = body.organization_id;
      expect(body).toMatchObject({models: ["gpt-4o-mini"], metadata});
      teamID = (await create("/team/new", {organization_id: orgID, team_alias: orgName + "-team"})).team_id;
      userID = (await create("/user/new", {user_email: orgName + "@example.test", password: "config-test-password", team_id: teamID, team_role: "admin"})).user_id;
      const key = await create("/key/generate", {user_id: userID, key_alias: "config-test-key"}); keyID = key.token_id;
      const keyHeaders = {Authorization: "Bearer " + key.key};
      const request = {model: "gpt-4o-mini", messages: [{role: "user", content: "Reply with OK."}], max_tokens: 16};
      const allowed = await page.request.post(GATEWAY + "/v1/chat/completions", {headers: keyHeaders, data: request});
      expect(allowed.status(), await allowed.text()).toBe(200);
      expect((await allowed.json()).choices.length).toBeGreaterThan(0);
      await stableGoto(page, "/organizations?org=" + orgID);
      await page.getByRole("tab", {name: tr("Settings"), exact: true}).click();
      await page.getByRole("button", {name: tr("Edit Settings"), exact: true}).click();
      const metaField = dialog.getByLabel(tr("Metadata"), {exact: true});
      expect(JSON.parse(await metaField.inputValue())).toEqual(metadata);
      await metaField.fill("invalid json");
      await dialog.getByRole("button", {name: tr("Save Changes"), exact: true}).click();
      await expect(dialog.getByRole("alert").filter({hasText: tr("Metadata must be a valid JSON object")})).toHaveText(tr("Metadata must be a valid JSON object"));
      const updatedMeta = {...metadata, note: "保留中文输入"};
      await metaField.fill(JSON.stringify(updatedMeta));
      const otherLocale = locale === "en" ? "zh-CN" : "en";
      await dialog.getByRole("button", {name: otherLocale === "en" ? "English" : translate("en", "language.zhCN"), exact: true}).click();
      await expect(dialog.getByLabel(translate(otherLocale, "Metadata"), {exact: true})).toHaveValue(JSON.stringify(updatedMeta));
      await expect(dialog.getByText(translate(otherLocale, "organizationConfig.modelScopeHint"))).toBeVisible();
      await dialog.getByRole("button", {name: locale === "en" ? "English" : translate("en", "language.zhCN"), exact: true}).click();
      // 选择禁止默认模型，替换原白名单；模型选择器的特殊项必须实际持久化并立即作用于已有密钥。
      const models = dialog.getByRole("combobox", {name: tr("Select Models"), exact: true});
      await models.click();
      await page.getByRole("option", {name: tr("No Default Models"), exact: true}).click();
      // Tab 离开模型下拉框，保留外层设置弹窗及未保存的元数据。
      await models.press("Tab");
      const saved = page.waitForResponse(r => r.url().endsWith("/organization/update") && r.request().method() === "PATCH");
      await dialog.getByRole("button", {name: tr("Save Changes"), exact: true}).click();
      const savedResponse = await saved;
      expect(savedResponse.status(), await savedResponse.text()).toBe(200);
      expect(await savedResponse.json()).toMatchObject({models: ["no-default-models"], metadata: updatedMeta});
      await expect(dialog).toBeHidden();
      const countBefore = await (await page.request.get(UPSTREAM + "/e2e/request-count")).json();
      const denied = await page.request.post(GATEWAY + "/v1/chat/completions", {headers: keyHeaders, data: request});
      expect(denied.status(), await denied.text()).toBe(401);
      expect(await (await page.request.get(UPSTREAM + "/e2e/request-count")).json()).toEqual(countBefore);
      const listed = await page.request.get(GATEWAY + "/v1/models", {headers: keyHeaders});
      expect((await listed.json()).data).toEqual([]);
      await page.reload();
      await page.getByRole("tab", {name: tr("Settings"), exact: true}).click();
      await page.getByRole("button", {name: tr("Edit Settings"), exact: true}).click();
      expect(JSON.parse(await dialog.getByLabel(tr("Metadata"), {exact: true}).inputValue())).toEqual(updatedMeta);
      // 空白元数据明确清空；键盘删除特殊项恢复继承，避免依赖控件内部 CSS。
      await dialog.getByLabel(tr("Metadata"), {exact: true}).fill("");
      await models.focus();
      await models.press("Backspace");
      await models.press("Backspace");
      await models.press("Tab");
      const cleared = page.waitForResponse(r => r.url().endsWith("/organization/update") && r.request().method() === "PATCH");
      await dialog.getByRole("button", {name: tr("Save Changes"), exact: true}).click();
      const clearedResponse = await cleared;
      expect(clearedResponse.status(), await clearedResponse.text()).toBe(200);
      expect(await clearedResponse.json()).toMatchObject({models: [], metadata: {}});
      const restored = await page.request.post(GATEWAY + "/v1/chat/completions", {headers: keyHeaders, data: request});
      expect(restored.status(), await restored.text()).toBe(200);
    } finally {
      test.setTimeout(test.info().timeout + 30_000);
      if (keyID) expect((await page.request.post(GATEWAY + "/key/delete", {headers, data: {keys: [keyID]}})).status()).toBe(200);
      if (teamID) expect((await page.request.post(GATEWAY + "/team/delete", {headers, data: {team_id: teamID}})).status()).toBe(200);
      if (userID) expect((await page.request.post(GATEWAY + "/user/delete", {headers, data: {user_id: userID}})).status()).toBe(200);
      if (orgID) expect((await page.request.delete(GATEWAY + "/organization/delete", {headers, data: {organization_id: orgID}})).status()).toBe(200);
    }
  });
}
