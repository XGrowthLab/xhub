import { expect, test } from "@playwright/test";
import { GATEWAY, loginAdmin, sessionBearer } from "../../helpers";

/** 管理员通过保留接口编辑公开模型目录链接；隔离网关回读原文并维护管理权限，最后恢复原配置。 */
test("model catalog configured links preserve names through gateway aliases", async ({ page, request }) => {
  await loginAdmin(page);
  const headers = { Authorization: "Bearer " + (await sessionBearer(page)) };
  const info = await page.request.get(GATEWAY + "/public/model_hub/info");
  expect(info.status()).toBe(200);
  const old = await info.json();
  expect(typeof old.litellm_version).toBe("string");
  expect(old.litellm_version).not.toBe("");

  const name = "LiteLLM 模型 anthropic/claude-opus-5.5";
  const usefulLinks = { [name]: "https://example.com/guide" };
  const data = { useful_links: usefulLinks };
  const denied = await request.post("/model_hub/update_useful_links", { data });
  expect(denied.status()).toBe(401);
  try {
    const updated = await page.request.post("/ui/model_hub/update_useful_links", { headers, data });
    expect(updated.status(), await updated.text()).toBe(200);
    for (const path of ["/public/model_hub/info", "/gw/public/model_hub/info"]) {
      const read = await page.request.get(path);
      expect(read.status(), await read.text()).toBe(200);
      expect((await read.json()).useful_links).toEqual(usefulLinks);
    }
  } finally {
    const restore = await page.request.post(GATEWAY + "/model_hub/update_useful_links", {
      headers,
      data: { useful_links: old.useful_links },
    });
    expect(restore.status(), await restore.text()).toBe(200);
  }
});
