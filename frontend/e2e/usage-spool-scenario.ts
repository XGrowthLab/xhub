import { selectMonitorOption } from "./monitor-select-helper";
import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { translate } from "../src/i18n/translate";
import { GATEWAY, UPSTREAM, loginAdmin, sessionBearer, stableGoto } from "./helpers";

/** 注册中英文磁盘账单恢复闭环；前置无 Redis 的真实网关、私有数据库和本地供应商，从 Playground 发起唯一推理后关闭整个IAM连接池。
 * 验证页面成功、磁盘账单恢复及正文存储失败提示，再并发发送 16 个普通与流式对话并从监控核对费用与用量。
 * finally 关闭个人记录、移除故障并删除模型，运行器回收账单/schema。 */
export function registerUsageSpoolScenario(locale: "en" | "zh-CN") {
  test("durable disk billing recovers after database disconnect " + locale, async ({ page }) => {
    test.setTimeout(120000);
    await loginAdmin(page);
    await page
      .getByRole("button", { name: locale === "en" ? "English" : translate("en", "language.zhCN"), exact: true })
      .click();
    const t = (key: string) => translate(locale, key);
    const bearer = await sessionBearer(page);
    const headers = { Authorization: "Bearer " + bearer };
    const control = JSON.parse(readFileSync(join(process.env.E2E_RUN_DIR!, "enterprise-fixture.json"), "utf8")).control;
    const model = "spool-browser-recovery-" + Date.now();
    let modelID = "";
    let guardID = "";
    let injected = false;
    try {
      const capture = await page.request.put(GATEWAY + "/account/log-capture", { headers, data: { enabled: true } });
      expect(capture.status(), await capture.text()).toBe(200);
      const created = await page.request.post(GATEWAY + "/model/new", {
        headers,
        data: {
          model_name: model,
          litellm_params: {
            model,
            custom_llm_provider: "openai",
            api_base: UPSTREAM,
            api_key: "sk-fake",
            input_cost_per_token: 0.01,
            output_cost_per_token: 0.02,
          },
          model_info: { transport: "bypass_openai_chat", pricing_source: "manual", endpoint_types: ["chat"] },
        },
      });
      expect(created.status(), await created.text()).toBe(200);
      modelID = (await created.json()).model_info.id;
      const failed = await page.request.post(control + "/usage-spool/fail", { data: { model } });
      expect(failed.status(), "本场景必须使用 E2E_REDIS=0 的私有数据库夹具: " + (await failed.text())).toBe(200);
      injected = true;
      // 先触发执行链错误，再验证后续真实对话不被重复账单污染；护栏先于上游，故此时PG仍可用。
      const guard = await page.request.post(GATEWAY + "/guardrails", {
        headers,
        data: {
          guardrail: {
            guardrail_name: model + "-block",
            litellm_params: {
              guardrail: "blocked_words",
              blocked_words: ["spool-block-marker"],
              mode: "pre_call",
              default_on: false,
            },
          },
        },
      });
      expect(guard.status(), await guard.text()).toBe(200);
      guardID = (await guard.json()).guardrail_id;
      const blocked = await page.request.post(GATEWAY + "/v1/chat/completions", {
        headers,
        data: { model, guardrails: [model + "-block"], messages: [{ role: "user", content: "spool-block-marker" }] },
      });
      expect(blocked.status(), await blocked.text()).toBe(400);
      const failedID = blocked.headers()["x-litellm-call-id"];
      const failedLog = await page.request.get(GATEWAY + "/spend/logs/ui/" + failedID, { headers });
      expect(failedLog.status()).toBe(200);
      expect((await failedLog.json()).status).toBe("error");
      const empty = await page.request.post(control + "/usage-spool/state", { data: { model } });
      expect(await empty.json()).toEqual({ pending: 0, quarantined: 0 });
      expect((await page.request.delete(GATEWAY + "/guardrails/" + guardID, { headers })).status()).toBe(200);
      guardID = "";
      await stableGoto(page, "/logs");
      await page.getByRole("tab", { name: t("Error Logs"), exact: true }).click();
      const errorPanel = page.getByRole("tabpanel", { name: t("Error Logs"), exact: true });
      // 两个日志面板共用搜索提示；切换后只应显示一个输入框，并从错误日志核对真实失败记录。
      const searchLabel = t("Search logs by ID…");
      await expect(page.getByPlaceholder(searchLabel, { exact: true }).filter({ visible: true })).toHaveCount(1);
      await errorPanel.getByPlaceholder(searchLabel, { exact: true }).fill(failedID);
      await expect(errorPanel.getByRole("row").filter({ hasText: failedID })).toHaveCount(1);
      await page.evaluate(() => sessionStorage.setItem("streamingEnabled", "false"));
      await stableGoto(page, "/playground");
      await page.getByPlaceholder(t("Select a Model"), { exact: true }).click();
      await page.getByRole("option", { name: model, exact: true }).click();
      await page.getByPlaceholder(t("Select an endpoint"), { exact: true }).click();
      await page
        .getByRole("option")
        .filter({ has: page.getByText("/v1/chat/completions", { exact: true }) })
        .click();
      await page
        .getByPlaceholder(t("Type your message... (Shift+Enter for new line)"))
        .filter({ visible: true })
        .fill("Verify durable billing recovery.");
      const pending = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith("/chat/completions") && response.request().method() === "POST",
      );
      await page.getByRole("button", { name: t("Send message") }).click();
      const response = await pending;
      expect(response.status(), "账单数据库断连不能改写已完成的模型回答").toBe(200);
      await expect(page.getByText("e2e-ok", { exact: true }).last()).toBeVisible();
      const callID = response.headers()["x-litellm-call-id"];
      expect(callID).toBeTruthy();
      const state = await page.request.post(control + "/usage-spool/state", { data: { model } });
      expect(await state.json()).toEqual({ pending: 1, quarantined: 0 });
      for (let i = 0; i < 2; i++) {
        const recovered = await page.request.post(control + "/usage-spool/recover", { data: { model } });
        expect(recovered.status(), await recovered.text()).toBe(200);
        expect(await recovered.json()).toEqual({ pending: 0, quarantined: 0 });
      }
      injected = false;
      const billResponse = await page.request.get(GATEWAY + "/spend/logs/ui/" + callID, { headers });
      expect(billResponse.status()).toBe(200);
      const bill = await billResponse.json();
      expect(bill.status).toBe("success");
      expect(Number(bill.spend)).toBeCloseTo(0.12, 8);
      expect(bill.body_capture_status).toBe("storage_failed");
      expect(JSON.stringify(bill)).not.toContain("Verify durable billing recovery.");
      const monitor = await page.request.get(GATEWAY + "/models/monitor?model=" + model, { headers });
      expect(monitor.status()).toBe(200);
      const metrics = await monitor.json();
      // 入站护栏尚未选择供应商，不增加模型供应商监控请求；成功推理仍唯一。
      expect(metrics.summary.requests).toBe(1);
      expect(metrics.summary.cost).toBeCloseTo(0.12, 8);
      await stableGoto(page, "/logs");
      const logsPanel = page.getByRole("tabpanel", { name: t("Logs"), exact: true });
      await logsPanel.getByPlaceholder(searchLabel, { exact: true }).fill(callID);
      const row = logsPanel.getByRole("row").filter({ hasText: callID });
      await expect(row).toHaveCount(1);
      await row.click();
      await expect(page.getByRole("dialog")).toContainText(model);
      await expect(page.getByRole("dialog").getByRole("note")).toContainText(t("logsUX.captureStorageFailed"));
      // 恢复后的一批交错普通/SSE请求验证前台结算、并发准入和恢复者可以共享同一账单确认。
      // 每个请求都产生独立 ID，下面逐笔验证费用、Token 与监控汇总。
      const beforeBatch = await page.request.get(GATEWAY + "/user/info", { headers });
      expect(beforeBatch.status()).toBe(200);
      const startingSpend = Number((await beforeBatch.json()).user_info.spend);
      const concurrentIDs = await Promise.all(
        Array.from({ length: 16 }, async (_, index) => {
          const stream = index % 2 === 1;
          const reply = await page.request.post(GATEWAY + "/v1/chat/completions", {
            headers,
            data: {
              model,
              stream,
              ...(stream && { stream_options: { include_usage: true } }),
              messages: [{ role: "user", content: `Verify concurrent billing after recovery, request ${index}.` }],
            },
          });
          expect(reply.status(), await reply.text()).toBe(200);
          if (stream) {
            const body = await reply.text();
            expect(body).toContain("e2e-ok");
            expect(body.trimEnd()).toMatch(/data:\s*\[DONE\]$/);
          } else {
            expect((await reply.json()).choices[0].message.content).toBe("e2e-ok");
          }
          const id = reply.headers()["x-litellm-call-id"];
          expect(id).toBeTruthy();
          return id;
        }),
      );
      // 普通与 SSE 完成后，实际扣费和本机快照确认都必须收敛，合并写盘不能遗留待确认或隔离证据。
      await expect.poll(async () => {
        const snapshot = await page.request.post(control + "/usage-spool/state", { data: { model } });
        expect(snapshot.status(), await snapshot.text()).toBe(200);
        return await snapshot.json();
      }).toEqual({ pending: 0, quarantined: 0 });
      const expectedCalls = 1 + concurrentIDs.length;
      expect(new Set([callID, ...concurrentIDs]).size).toBe(expectedCalls);
      await expect
        .poll(async () => {
          const result = await page.request.get(GATEWAY + "/models/monitor?model=" + model, { headers });
          expect(result.status()).toBe(200);
          return (await result.json()).summary;
        })
        .toMatchObject({
          requests: expectedCalls,
          total_tokens: expectedCalls * 10,
          cost: expect.closeTo(expectedCalls * 0.12, 8),
        });
      // 监控到账后再读取准入使用的个人消费，核对用户可见余额与本批次的真实账单。
      const afterBatch = await page.request.get(GATEWAY + "/user/info", { headers });
      expect(afterBatch.status()).toBe(200);
      expect(Number((await afterBatch.json()).user_info.spend) - startingSpend).toBeCloseTo(concurrentIDs.length * 0.12, 8);
      for (const id of concurrentIDs) {
        const log = await page.request.get(GATEWAY + "/spend/logs/ui/" + id, { headers });
        expect(log.status()).toBe(200);
        expect(await log.json()).toMatchObject({ request_id: id, status: "success", spend: expect.closeTo(0.12, 8) });
      }
      await stableGoto(page, "/model-monitor");
      await selectMonitorOption(page, t("monitor.model"), model);
      await expect(page.getByRole("rowheader", { name: model, exact: true })).toHaveCount(1);
      await expect(page.getByRole("region", { name: t("monitor.requests"), exact: true })).toContainText(String(expectedCalls));
      // 只拒绝夹具指定的一张零费用账单。网关仍能鉴权，退避中的磁盘证据必须连续阻止新调用。
      injected = true;
      const deferred = await page.request.post(control + "/usage-spool/defer", { data: { model } });
      expect(deferred.status(), await deferred.text()).toBe(200);
      expect(await deferred.json()).toEqual({ pending: 1, quarantined: 0 });
      for (let attempt = 0; attempt < 2; attempt++) {
        const refused = await page.request.post(GATEWAY + "/v1/chat/completions", {
          headers,
          data: { model, messages: [{ role: "user", content: "Check admission while settlement is pending." }] },
        });
        expect(refused.status(), await refused.text()).toBe(503);
        expect((await refused.json()).error.type).toBe("billing_storage_pending");
      }
      const cleared = await page.request.post(control + "/usage-spool/recover", { data: { model } });
      expect(cleared.status(), await cleared.text()).toBe(200);
      expect(await cleared.json()).toEqual({ pending: 0, quarantined: 0 });
      injected = false;
      const resumed = await page.request.post(GATEWAY + "/v1/chat/completions", {
        headers,
        data: { model, messages: [{ role: "user", content: "Verify admission after settlement." }] },
      });
      expect(resumed.status(), await resumed.text()).toBe(200);
      expect((await resumed.json()).choices[0].message.content).toBe("e2e-ok");
    } finally {
      if (injected)
        expect((await page.request.post(control + "/usage-spool/recover", { data: { model } })).status()).toBe(200);
      expect((await page.request.put(GATEWAY + "/account/log-capture", { headers, data: { enabled: false } })).status()).toBe(200);
      if (guardID)
        expect((await page.request.delete(GATEWAY + "/guardrails/" + guardID, { headers })).status()).toBe(200);
      if (modelID)
        expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id: modelID } })).status()).toBe(
          200,
        );
    }
  });
}
