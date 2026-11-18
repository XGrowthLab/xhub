import { expect, test } from "@playwright/test";
import { GATEWAY, UPSTREAM, loginAdmin, sessionBearer, stableGoto } from "../../helpers";
import { translate } from "../../../src/i18n/translate";

const PDF = "JVBERi0xLjQKc3ludGhldGljCiUlRU9G";

for (const locale of ["zh-CN", "en"] as const) {
  /** 浏览器上传 PDF，经真实网关转换到校验字节的隔离上游，并核对界面结果与账单。
   * 前置独立数据库和临时模型、密钥；覆盖中英文、Chat 与 Responses，finally 删除密钥和模型，运行器回收 schema。 */
  test("PDF upload follows protocol " + locale, async ({ page }) => {
    test.setTimeout(120000);
    await loginAdmin(page);
    const t = (key: string) => translate(locale, key);
    if (locale === "en")
      await page.getByRole("button", { name: translate("zh-CN", "language.en"), exact: true }).click();
    const headers = { Authorization: "Bearer " + (await sessionBearer(page)) };
    const name = "pdf-browser-" + locale;
    const model = await page.request.post(GATEWAY + "/model/new", {
      headers,
      data: {
        model_name: name,
        litellm_params: {
          model: "e2e-pdf-chat",
          custom_llm_provider: "openai",
          api_base: UPSTREAM + "/v1",
          api_key: "sk-fake",
          input_cost_per_token: 0.000001,
          output_cost_per_token: 0.000002,
        },
        model_info: {
          transport: "bypass_openai_chat",
          endpoint_types: ["chat", "responses", "messages"],
          pricing_source: "manual",
          max_input_tokens: 1024,
          max_output_tokens: 8192,
          supported_parameters: ["max_tokens"],
        },
      },
    });
    expect(model.status(), await model.text()).toBe(200);
    const id = (await model.json()).model_info.id;
    let keyID = "";
    try {
      await page.getByTestId("create-key-button").click();
      await page.getByLabel(t("Key Name")).fill("pdf-key-" + locale);
      await page.getByLabel(t("Max Budget (USD)"), { exact: true }).fill("1");
      const minted = page.waitForResponse((r) => r.url().includes("/key/generate") && r.request().method() === "POST");
      await page.getByRole("button", { name: t("pages.apiKeys.createSubmit"), exact: true }).click();
      const keyResponse = await minted;
      expect(keyResponse.status()).toBe(200);
      const key = await keyResponse.json();
      keyID = key.token_id;
      await page.keyboard.press("Escape");
      await page.evaluate(() => sessionStorage.setItem("streamingEnabled", JSON.stringify(false)));
      await stableGoto(page, "/playground");
      await page.getByText(t("myModels.connectionSettings"), { exact: true }).click();
      await page.getByRole("combobox", { name: t("Virtual Key Source"), exact: true }).click();
      await page.getByRole("option", { name: t("Virtual Key"), exact: true }).click();
      await page.getByPlaceholder(t("Enter custom Virtual Key"), { exact: true }).fill(key.key);
      for (const path of ["/v1/chat/completions", "/v1/responses", "/v1/messages"]) {
        const clear = page.getByRole("button", { name: t("Clear Chat"), exact: true });
        if (await clear.isVisible()) await clear.click();
        await page.getByPlaceholder(t("Select a Model"), { exact: true }).click();
        await page.getByRole("option", { name, exact: true }).click();
        await page.getByPlaceholder(t("Select an endpoint"), { exact: true }).click();
        await page
          .getByRole("option")
          .filter({ has: page.getByText(path, { exact: true }) })
          .click();
        if (path.endsWith("responses")) {
          const session = page.getByRole("switch", { name: t("Use API session management") });
          if (await session.isChecked()) await session.click();
        }
        const chooser = page.waitForEvent("filechooser");
        await page.getByRole("button", { name: t("Attach image or PDF"), exact: true }).click();
        await (
          await chooser
        ).setFiles({ name: "fixture.pdf", mimeType: "application/pdf", buffer: Buffer.from(PDF, "base64") });
        await page
          .getByPlaceholder(t("Type your message... (Shift+Enter for new line)"), { exact: true })
          .fill("Read the attached document.");
        // 调用代码直接使用已上传的 PDF；验证真实协议字段与原始字节，而非旧 SDK 中的静态注释示例。
        await page.getByRole("button", { name: t("consoleDetails.viewCode"), exact: true }).click();
        const guide = page.getByRole("dialog", { name: t("consoleDetails.codeTitle") });
        const example = guide.locator("pre").filter({ hasText: "curl --request POST" });
        await expect(example).toContainText("Read the attached document.");
        await expect(example).toContainText(PDF);
        await expect(example).toContainText(
          path.endsWith("responses")
            ? '"type": "input_file"'
            : path.endsWith("messages")
              ? '"type": "document"'
              : '"type": "file"',
        );
        await expect(example).not.toContainText(key.key);
        await page.keyboard.press("Escape");
        const pending = page.waitForResponse(
          (r) => new URL(r.url()).pathname.endsWith(path.replace(/^\/v1/, "")) && r.request().method() === "POST",
        );
        await page.getByRole("button", { name: t("Send message"), exact: true }).click();
        const response = await pending;
        expect(response.status(), path).toBe(200);
        await expect(page.getByText("pdf-verified", { exact: true }).last()).toBeVisible();
        const callID = response.headers()["x-litellm-call-id"];
        await expect
          .poll(async () => {
            const detail = await page.request.get(GATEWAY + "/spend/logs/ui/" + callID, { headers });
            if (!detail.ok()) return null;
            const bill = await detail.json();
            return { status: bill.status, spend: Number(bill.spend) };
          })
          .toEqual({ status: "success", spend: 0.000012 });
        // 续聊必须把同一份文件再次送到无状态上游，浏览器会话存储则不能保存文件字节。
        await expect(page.getByRole("button", { name: t("Send message"), exact: true })).toBeVisible();
        await page
          .getByPlaceholder(t("Type your message... (Shift+Enter for new line)"), { exact: true })
          .fill("Continue reading the same document.");
        // 续聊代码必须还原内存中的同一附件，而不能把历史显示占位复制成请求。
        await page.getByRole("button", { name: t("consoleDetails.viewCode"), exact: true }).click();
        await expect(
          page
            .getByRole("dialog", { name: t("consoleDetails.codeTitle") })
            .locator("pre")
            .filter({ hasText: "curl --request POST" }),
        ).toContainText(PDF);
        await page.keyboard.press("Escape");
        const followup = page.waitForResponse(
          (r) => new URL(r.url()).pathname.endsWith(path.replace(/^\/v1/, "")) && r.request().method() === "POST",
        );
        await page.getByRole("button", { name: t("Send message"), exact: true }).click();
        expect((await followup).status(), "PDF 续聊必须保留附件").toBe(200);
        await expect(page.getByText("pdf-verified", { exact: true })).toHaveCount(2);
        await expect.poll(() => page.evaluate(() => sessionStorage.getItem("chatHistory") ?? "")).not.toContain(PDF);
        if (path === "/v1/messages") {
          await expect
            .poll(() => page.evaluate(() => sessionStorage.getItem("chatHistory") ?? ""))
            .toContain("attachmentToken");
          await page.reload();
          await page
            .getByPlaceholder(t("Type your message... (Shift+Enter for new line)"), { exact: true })
            .fill("Continue after refresh.");
          await page.getByRole("button", { name: t("Send message"), exact: true }).click();
          await expect(page.getByText(t("playgroundAttachment.expired"), { exact: true })).toBeVisible();
        }
      }
    } finally {
      if (keyID) await page.request.post(GATEWAY + "/key/delete", { headers, data: { keys: [keyID] } });
      await page.request.post(GATEWAY + "/model/delete", { headers, data: { id } });
    }
  });
}
