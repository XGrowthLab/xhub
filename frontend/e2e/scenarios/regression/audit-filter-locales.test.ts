import { expect, test } from "@playwright/test";
import { translate } from "../../../src/i18n/translate";
import { GATEWAY, loginAdmin, sessionBearer, stableGoto } from "../../helpers";

/** 前置真实后台、隔离数据库和管理员浏览器；双语应用审计筛选并核验接口与行数据，未知对象返回空状态。
 * 创建测试用户产生审计事件，finally 删除用户，运行器销毁专属 schema 与审计数据；不访问外部供应商。 */
test("audit filter actions translate and query real records in both locales", async ({ page }) => {
  await loginAdmin(page);
  const headers = { Authorization: "Bearer " + await sessionBearer(page) };
  const created = await page.request.post(GATEWAY + "/user/new", { headers, data: {
    user_email: "audit-filter-locales@example.test", user_role: "user", password: "audit-filter-password",
  } });
  expect(created.status(), await created.text()).toBe(200);
  const userID = (await created.json()).user_id as string;
  expect(userID).toBeTruthy();
  let keyID = "";
  const alias = "Audit retained key";
  try {
    const minted = await page.request.post(GATEWAY + "/key/generate", { headers, data: { key_alias: alias, owner_type: "personal", user_id: userID } });
    expect(minted.status()).toBe(200);
    keyID = (await minted.json()).token_id;
    expect(keyID).toBeTruthy();
    const revoked = await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [keyID] } });
    expect(revoked.status()).toBe(200);
    const objectID = keyID;
    keyID = "";
    await stableGoto(page, "/audit-logs");
    for (const locale of ["en", "zh-CN"] as const) {
      const tr = (key: string) => translate(locale, key);
      await page.getByRole("button", { name: locale === "en" ? "English" : translate("en", "language.zhCN"), exact: true }).click();
      await page.getByTestId("datatable-filters-trigger").click();
      await page.getByPlaceholder(tr("Enter object ID…")).fill(objectID);
      const response = page.waitForResponse(res => res.url().includes("/audit/logs?") && new URL(res.url()).searchParams.get("object_id") === objectID);
      await page.getByRole("button", { name: tr("common.applyFilters"), exact: true }).click();
      const result = await response;
      expect(result.status()).toBe(200);
      const body = await result.json();
      expect(body.total).toBe(2);
      expect(body.audit_logs.every((entry: { object_id: string }) => entry.object_id === objectID)).toBe(true);
      await expect(page.getByRole("row").filter({ hasText: alias })).toHaveCount(2);
      await page.getByTestId("datatable-filters-trigger").click();
      await expect(page.getByPlaceholder(tr("Enter object ID…"))).toHaveValue(objectID);
      await page.getByPlaceholder(tr("Enter object ID…")).fill("audit-filter-unknown-object");
      const empty = page.waitForResponse(res => res.url().includes("/audit/logs?") && new URL(res.url()).searchParams.get("object_id") === "audit-filter-unknown-object");
      await page.getByRole("button", { name: tr("common.applyFilters"), exact: true }).click();
      expect((await (await empty).json()).total).toBe(0);
      await expect(page.getByText(tr("No matching audit logs"), { exact: true })).toBeVisible();
    }
  } finally {
    if (keyID) await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [keyID] } });
    const deleted = await page.request.post(GATEWAY + "/user/delete", { headers, data: { user_id: userID } });
    expect(deleted.status(), await deleted.text()).toBe(200);
  }
});
