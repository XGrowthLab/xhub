import { describe, expect, it } from "vitest";
import { messagesContentPart, responsesContentPart } from "./attachment_parts";

/** 验证附件映射保留文件名与字节，非法文件引用不会被伪装成合法 PDF；纯内存数据无需清理。 */
describe("protocol attachment parts", () => {
  it("maps Chat PDF to Responses and Messages without changing bytes", () => {
    const original = {
      type: "file",
      file: { filename: "sample.pdf", file_data: "data:application/pdf;base64,JVBERi0=" },
    };
    expect(responsesContentPart(original, "user")).toEqual({
      type: "input_file",
      filename: "sample.pdf",
      file_data: original.file.file_data,
    });
    expect(messagesContentPart(original)).toEqual({
      type: "document",
      title: "sample.pdf",
      source: { type: "base64", media_type: "application/pdf", data: "JVBERi0=" },
    });
    expect(original.file.file_data).toBe("data:application/pdf;base64,JVBERi0=");
  });
  it("preserves unsupported file references for protocol validation", () => {
    const unknown = { type: "input_file", file_id: "file-1" };
    expect(messagesContentPart(unknown)).toEqual(unknown);
    expect(responsesContentPart({ type: "text", text: "原文" }, "user")).toEqual({ type: "input_text", text: "原文" });
  });
});
