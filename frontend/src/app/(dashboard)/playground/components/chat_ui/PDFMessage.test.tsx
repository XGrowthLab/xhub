import { describe, expect, it } from "vitest";
import { createChatMultimodalMessage } from "./ChatImageUtils";
import { createMultimodalMessage } from "./ResponsesImageUtils";

/** 验证调试台生成真实协议 PDF 块，包含 MIME 缺失的上传文件与未修改的用户文字；不联网、不保留文件。 */
describe("PDF message encoding", () => {
  it.each(["application/pdf", ""])("uses PDF blocks for MIME %s", async (mime) => {
    const file = new File(["%PDF-1.4\nfixture"], "document.pdf", { type: mime });
    const chat = await createChatMultimodalMessage("原始输入", file);
    const responses = await createMultimodalMessage("原始输入", file);
    expect(chat.content[0]).toEqual({ type: "text", text: "原始输入" });
    expect(chat.content[1]).toMatchObject({
      type: "file",
      file: { filename: "document.pdf", file_data: expect.stringMatching(/^data:application\/pdf;base64,/) },
    });
    expect(responses.content[0]).toEqual({ type: "input_text", text: "原始输入" });
    expect(responses.content[1]).toMatchObject({
      type: "input_file",
      filename: "document.pdf",
      file_data: expect.stringMatching(/^data:application\/pdf;base64,/),
    });
  });
  it("keeps image inputs as images", async () => {
    const image = new File(["image"], "image.png", { type: "image/png" });
    expect((await createChatMultimodalMessage("hello", image)).content[1].type).toBe("image_url");
    expect((await createMultimodalMessage("hello", image)).content[1].type).toBe("input_image");
  });
});
