import { expect, it } from "vitest";
import { canonicalAttachment, requestHistoryWithAttachments } from "./attachment_history";

/** 覆盖文件在页面存活时续聊保留、刷新后缺失时拒绝以及非附件消息不变；纯内存数据无需清理。 */
it("keeps attached PDF for follow-up without storing its bytes in history", () => {
  const part = { type: "input_file", filename: "sample.pdf", file_data: "data:application/pdf;base64,JVBERi0=" };
  const payload = canonicalAttachment([{ type: "input_text", text: "原文" }, part]);
  const history = [{ role: "user", content: "原文 [PDF attached]", attachmentToken: "local-1" }, { role: "assistant", content: "OK" }];
  expect(requestHistoryWithAttachments(history, new Map([["local-1", payload]]))).toEqual([
    { role: "user", content: [{ type: "text", text: "原文" }, { type: "file", file: { filename: "sample.pdf", file_data: part.file_data } }] },
    { role: "assistant", content: "OK" },
  ]);
  expect(JSON.stringify(history)).not.toContain("JVBERi0=");
  expect(requestHistoryWithAttachments(history, new Map())).toBeNull();
});
