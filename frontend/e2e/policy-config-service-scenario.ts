import { expect, test as base, type Page } from "@playwright/test";
import { GATEWAY, loginAdmin, sessionBearer } from "./helpers";
import type { Locale } from "../src/i18n/translate";

type Resources = { headers: Record<string, string>; name: string; policyID?: string };

/** 策略场景资源夹具；独立请求上下文在页面关闭或测试超时后仍可清理。
 * 参数由 Playwright 注入；返回独占名称与鉴权头，清理优先使用已获知的 ID，创建响应缺失时按名称回查。
 * 清理失败继续尝试其他记录并汇总报错；不扩大业务超时，私有 schema 由运行脚本二次清理。
 */
export const test = base.extend<{ policyResources: Resources }>({
  policyResources: [async ({ playwright }, runTest) => {
    const api = await playwright.request.newContext({ timeout: 10_000 });
    const state: Resources = { headers: {}, name: `policy-service-${Date.now()}-${Math.random().toString(36).slice(2)}` };
    try { await runTest(state); }
    finally {
      try {
        if (state.headers.Authorization) {
          const response = await api.get(GATEWAY + "/policies/list", { headers: state.headers });
          expect(response.status(), "策略清理前列表必须可读").toBe(200);
          const rows = (await response.json()).policies as { id: string; name: string }[];
          const results = await Promise.allSettled(rows.filter(row => row.id === state.policyID || row.name === state.name).map(async row => {
            const deleted = await api.delete(GATEWAY + "/policies/" + encodeURIComponent(row.id), { headers: state.headers });
            expect(deleted.status(), "策略清理必须成功").toBe(200);
          }));
          const failures = results.filter(result => result.status === "rejected");
          expect(failures, "所有策略清理均须成功").toEqual([]);
        }
      } finally { await api.dispose(); }
    }
  }, { timeout: 30_000 }],
});

/** 通过真实网关为策略边界场景创建持久化记录。参数为已登录的页面和独立夹具，返回策略 ID 与读取地址。
 * 旧策略界面下线后由调用方直接使用兼容 API 建数。创建失败令场景失败，已落库记录由夹具负责清理。
 */
export async function createBoundaryPolicy(page: Page, resources: Resources) {
  const { headers, name } = resources;
  const response = await page.request.post(GATEWAY + "/policies", { headers, data: { policy_name: name, name } });
  expect(response.status(), await response.text()).toBe(200);
  const policy = await response.json();
  resources.policyID = policy.policy_id;
  expect(policy).toMatchObject({ policy_id: expect.stringMatching(/^policie_/), status: "active", version: 1 });
  return { policyID: policy.policy_id as string, readPath: GATEWAY + "/policies/" + encodeURIComponent(policy.policy_id) };
}

/** 注册真实策略配置流程；locale 指定控制台语言，无返回值。
 * 前置独立浏览器、PostgreSQL及离线模型；验证旧页面退役、HTTP创建、编辑、拒绝、恢复、对话和删除，清理由独立夹具保证。
 * 配置存储不等于策略执行；对话只验证管理重构未破坏现有模型通路，计费和权限由真实后台回归验证。
 */
export function registerPolicyConfigService(locale: Locale) {
  test("policy configuration persistence and isolation " + locale, async ({ page, policyResources: resources }) => {
    await loginAdmin(page);
    await page.getByRole("button", { name: locale === "en" ? "English" : "中文", exact: true }).click();
    const headers = resources.headers = { Authorization: "Bearer " + await sessionBearer(page) };
    expect((await page.goto("/ui/policies"))?.status()).toBe(410);
    const { policyID, readPath } = await createBoundaryPolicy(page, resources);
    const created = await page.request.get(readPath, { headers });
    expect(created.status()).toBe(200);
    expect(await created.json()).toMatchObject({ policy_id: policyID, policy_name: resources.name });
    const updated = await page.request.put(readPath, { headers, data: { policy_name: "Original 用户 input", status: "inactive", version: 0 } });
    expect(updated.status(), await updated.text()).toBe(200);
    const invalid = await page.request.put(readPath, { headers: { ...headers, "Content-Type": "application/json" }, data: '{"policy_name":' });
    expect(invalid.status()).toBe(400);
    const read = await page.request.get(readPath, { headers });
    expect(read.status()).toBe(200);
    expect(await read.json()).toMatchObject({ policy_name: "Original 用户 input", status: "inactive", version: 0 });
    const recovered = await page.request.put(readPath, { headers, data: { status: "active", version: null } });
    expect(recovered.status()).toBe(200);
    expect(await recovered.json()).toMatchObject({ status: "active", version: 1 });
    const chat = await page.request.post(GATEWAY + "/v1/chat/completions", { headers, data: {
      model: "gpt-4o-mini", messages: [{ role: "user", content: "Reply with a short greeting." }],
    } });
    expect(chat.status(), await chat.text()).toBe(200);
    expect((await chat.json()).choices[0].message.content).toBe("e2e-ok");
    const deleted = await page.request.delete(readPath, { headers });
    expect(deleted.status()).toBe(200);
    expect((await page.request.get(readPath, { headers })).status()).toBe(404);
  });
}
