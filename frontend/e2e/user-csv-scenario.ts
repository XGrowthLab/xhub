import { test, expect } from "@playwright/test";
import fs from "node:fs/promises";
import { GATEWAY, loginAdmin, sessionBearer, stableGoto } from "./helpers";
import { translate } from "../src/i18n/translate";

// 双语完整导入流程：私有数据库、真实下载/解析/创建/回读；验证桌面和窄窗预览、跨页错误及重复导入。
// 所有用户在finally删除，整个私有schema由运行器回收；无需供应商凭据或真实员工信息。
export function registerUserCsvScenario(locale: "en" | "zh-CN") {
  test(`CSV user template and real import ${locale}`, async ({ page }) => {
    await loginAdmin(page);
    await page
      .getByRole("button", { name: locale === "en" ? "English" : translate("en", "language.zhCN"), exact: true })
      .click();
    const tr = (key: string, vars?: Record<string, number>) => translate(locale, `userCsv.${key}`, vars);
    const headers = { Authorization: "Bearer " + (await sessionBearer(page)) };
    const email = `csv-${locale}-${Date.now()}@example.invalid`;
    const alias = "测试名字";
    const teamAlias = "e2e-fixture-team";
    const teams = await page.request.get(GATEWAY + "/team/list", { headers });
    expect(teams.status(), await teams.text()).toBe(200);
    const team = (await teams.json()).find((item: { team_alias: string }) => item.team_alias === teamAlias);
    expect(team?.team_id).toBeTruthy();
    let id = "";
    try {
      await page.setViewportSize({ width: 1280, height: 720 });
      await stableGoto(page, "/users");
      await page.getByRole("button", { name: tr("title"), exact: true }).click();
      const dialog = page.getByRole("dialog");
      const downloadPromise = page.waitForEvent("download");
      await dialog.getByRole("button", { name: tr("template"), exact: true }).click();
      const downloaded = await downloadPromise;
      expect(downloaded.suggestedFilename()).toBe("users-template.csv");
      const template = await fs.readFile((await downloaded.path())!, "utf8");
      expect(template).toContain("user_email,user_role,password,user_alias,team_id,max_budget");
      expect(template).not.toMatch(/[\u4e00-\u9fff]/);
      const csv = template.replace("new.user@example.com", email).replace("New User,,", alias + "," + team.team_id + ",");
      const [csvHeader, csvExample] = csv.split(/\r?\n/);
      const testRows = Array.from({ length: 12 }, (_, i) =>
        csvExample.replace(email, i + "-" + email).replace(alias, ("测试名字" + "LongName".repeat(12)) + i),
      );
      testRows[11] = testRows[11].replace(/,10,1,,$/, ",invalid,0,,");
      const csvFile = { name: "users.csv", mimeType: "text/csv", buffer: Buffer.from([csvHeader, ...testRows].join("\n")) };
      const upload = dialog.getByRole("button", { name: tr("upload"), exact: true });
      await expect(upload).toBeEnabled();
      const choosing = page.waitForEvent("filechooser");
      await upload.press("Enter");
      await (await choosing).setFiles(csvFile);
      const preview = dialog.getByRole("region", { name: tr("preview") });
      await expect(preview.getByRole("columnheader", { name: tr("alias") })).toBeVisible();
      expect((await dialog.boundingBox())?.width).toBeGreaterThan(900);
      await expect(preview.getByRole("row", { name: new RegExp("0-" + email) }).getByText(teamAlias)).toBeVisible();
      await expect(dialog.getByRole("button", { name: tr("create", { count: 12 }) })).toBeDisabled();
      await page.setViewportSize({ width: 420, height: 840 });
      expect((await dialog.boundingBox())?.width).toBeLessThan(420);
      await expect(preview).toBeInViewport();
      await preview.focus();
      expect(await preview.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(true);
      await preview.press("ArrowRight");
      await expect.poll(() => preview.evaluate((element) => element.scrollLeft)).toBeGreaterThan(0);
      expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
      await expect(dialog.getByRole("button", { name: tr("next") })).toBeInViewport();
      await dialog.getByRole("button", { name: tr("next") }).click();
      await expect(preview.getByText(tr("budgetError"))).toBeVisible();
      await expect(preview.getByText(translate(locale, "userBudgetReset.none"))).toBeVisible();
      await expect(dialog.getByRole("button", { name: tr("next") })).toBeDisabled();
      await expect(dialog.getByRole("button", { name: tr("close") })).toBeInViewport();
      await dialog
        .getByLabel(tr("upload"))
        .setInputFiles({ name: "users.csv", mimeType: "text/csv", buffer: Buffer.from(csv.replace(/,10,1,,$/, ",10abc,1,,")) });
      await expect(dialog.getByText(tr("budgetError"))).toBeVisible();
      await expect(dialog.getByRole("button", { name: tr("create", { count: 1 }) })).toBeDisabled();
      await dialog.getByLabel(tr("upload")).setInputFiles({ name: "users.csv", mimeType: "text/csv", buffer: Buffer.from(csv.replace(/,1,,$/, ",32,,")) });
      await expect(dialog.getByText(tr("resetDayError"))).toBeVisible();
      await expect(dialog.getByRole("button", { name: tr("create", { count: 1 }) })).toBeDisabled();
      await dialog
        .getByLabel(tr("upload"))
        .setInputFiles({ name: "users.csv", mimeType: "text/csv", buffer: Buffer.from(csv) });
      await expect(dialog.getByRole("button", { name: tr("create", { count: 1 }) })).toBeEnabled();
      const createdPromise = page.waitForResponse(
        (r) => r.url().endsWith("/user/new") && r.request().method() === "POST",
      );
      await dialog.getByRole("button", { name: tr("create", { count: 1 }) }).click();
      const created = await createdPromise;
      expect(created.status()).toBe(200);
      const snapshot = await created.json();
      id = snapshot.user_id;
      expect(id).toBeTruthy();
      // 创建回执省略团队成员详情，核对真实提交后再从用户详情验证持久化。
      expect(created.request().postDataJSON()).toMatchObject({ team_id: team.team_id });
      expect(snapshot).toMatchObject({ user_email: email.toLowerCase(), user_role: "internal_user", max_budget: 10, spend: 0 });
      expect(snapshot).not.toHaveProperty("password");
      expect(snapshot).not.toHaveProperty("key");
      await expect(dialog.getByRole("status")).toHaveText(tr("summary", { success: 1, failed: 0 }));
      const read = await page.request.get(GATEWAY + "/user/info?user_id=" + id, { headers });
      expect(read.status()).toBe(200);
      const user = (await read.json()).user_info;
      expect(user.max_budget).toBe(10);
      expect(user.budget_reset_day).toBe(1);
      expect(user.budget_reset_at).toBeTruthy();
      expect(user.user_alias).toBe(alias);
      expect(user.teams).toEqual([expect.objectContaining({ team_id: team.team_id, team_alias: teamAlias })]);
      const detailV2 = await page.request.get(GATEWAY + "/v2/user/info?user_id=" + id, { headers });
      expect(detailV2.status()).toBe(200);
      expect((await detailV2.json()).user_info).toEqual(user);
      const listing = await page.request.get(GATEWAY + "/user/list?user_email=" + encodeURIComponent(email), { headers });
      expect(listing.status()).toBe(200);
      const listed = await listing.json();
      expect(listed.total).toBe(1);
      expect(listed.total_count).toBe(listed.total);
      expect(listed.current_page).toBe(listed.page);
      expect(listed.users[0].user_id).toBe(id);
      const signedIn = await page.request.post(GATEWAY + "/v2/login", {
        data: { username: email, password: "Replace-before-import-123!" },
      });
      expect(signedIn.status()).toBe(200);
      const resultPromise = page.waitForEvent("download");
      await dialog.getByRole("button", { name: tr("results") }).click();
      const results = await fs.readFile((await (await resultPromise).path())!, "utf8");
      expect(results).toContain(id);
      expect(results).not.toContain("Replace-before-import");
      await dialog.getByRole("button", { name: tr("reset") }).click();
      await expect(dialog.getByText(email)).toHaveCount(0);
      await dialog
        .getByLabel(tr("upload"))
        .setInputFiles({ name: "users.csv", mimeType: "text/csv", buffer: Buffer.from(csv) });
      const duplicateResponse = page.waitForResponse(r => r.url().endsWith("/user/new") && r.request().method() === "POST");
      await dialog.getByRole("button", { name: tr("create", { count: 1 }) }).click();
      expect((await duplicateResponse).status()).toBe(409);
      await expect(dialog.getByRole("status")).toHaveText(tr("summary", { success: 0, failed: 1 }));
      await expect(dialog.getByText(tr("requestError"))).toBeVisible();
    } finally {
      if (id) {
        const deleted = await page.request.post(GATEWAY + "/user/delete", { headers, data: { user_id: id } });
        expect(deleted.status(), await deleted.text()).toBe(200);
        expect(await deleted.json()).toEqual({ deleted: true });
        expect((await page.request.get(GATEWAY + "/user/info?user_id=" + id, { headers })).status()).toBe(404);
      }
    }
  });
}
