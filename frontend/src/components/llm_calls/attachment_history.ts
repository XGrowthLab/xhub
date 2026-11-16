import type { MessageType } from "@/components/chat_ui/types";

/** 将已发送的 Responses 附件规范为 Chat 形态；parts 为内存内容块，返回可跨协议重编码的副本。
 * 调试台本地续聊使用；保留原始文字、文件名与文件字节，不联网、不修改调用者数据。 */
export function canonicalAttachment(parts: any[]): any[] {
  return parts.map((part) => {
    if (part.type === "input_text") return { type: "text", text: part.text };
    if (part.type === "input_image") return { type: "image_url", image_url: { url: part.image_url } };
    if (part.type === "input_file") return { type: "file", file: { filename: part.filename, file_data: part.file_data } };
    return part;
  });
}

/** 准备实际外发历史；history 为显示消息，attachments 是当前页面内存中的附件，返回消息或缺失标记。
 * 不读取浏览器存储或网络；刷新后文件字节已清除时返回 null，避免静默把附件标记当作实际文件发送。 */
export function requestHistoryWithAttachments(history: MessageType[], attachments: Map<string, any[]>): { role: string; content: any }[] | null {
  const result: { role: string; content: any }[] = [];
  for (const message of history) {
    if (message.isImage || message.isAudio) continue;
    if (message.attachmentToken) {
      const content = attachments.get(message.attachmentToken);
      if (!content) return null;
      result.push({ role: message.role, content });
    } else result.push({ role: message.role, content: message.content });
  }
  return result;
}
