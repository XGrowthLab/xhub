/** 将聊天内容块转换为 Responses 的对应块；part 为已有消息块，role 决定文字方向。
 * 实际请求与 curl 预览共用；保留文件名和字节，不拉取文件，未知块留给目标协议校验。 */
export function responsesContentPart(part: any, role: string): any {
  if (part.type === "text") return { type: role === "assistant" ? "output_text" : "input_text", text: part.text };
  if (part.type === "image_url") return { type: "input_image", image_url: part.image_url.url };
  if (part.type === "file") return { type: "input_file", filename: part.file.filename, file_data: part.file.file_data };
  return part;
}

/** 将聊天内容块转换为 Messages 的对应块；part 为已有消息块，返回原生图片或内联 PDF 块。
 * 实际请求与 curl 预览共用；不读取远端地址，其他未知块保留供上游判断。 */
export function messagesContentPart(part: any): any {
  if (part.type === "file" || part.type === "input_file") {
    const file = part.type === "file" ? part.file : part;
    const match = /^data:application\/pdf;base64,(.+)$/.exec(file.file_data ?? "");
    if (!match) return part;
    return {
      type: "document",
      title: file.filename,
      source: { type: "base64", media_type: "application/pdf", data: match[1] },
    };
  }
  if (part.type !== "image_url") return part;
  const url = part.image_url.url as string;
  const match = /^data:([^;]+);base64,(.+)$/.exec(url);
  return {
    type: "image",
    source: match ? { type: "base64", media_type: match[1], data: match[2] } : { type: "url", url },
  };
}
