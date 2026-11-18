"use client";
import { useEffect, useState } from "react";
import { CurlRequestGuide } from "./CurlRequestGuide";
import { playgroundCurlBody, type CurlBodyOptions } from "./playgroundCurlBody";
import { createChatMultimodalMessage } from "./ChatImageUtils";
import { createMultimodalMessage } from "./ResponsesImageUtils";

/** 为聊天、图片及音频表单展示实时 curl；参数为正文选项、标签和当前附件，返回统一教程。
 * presentation 控制独立弹窗直接显示或内嵌折叠；调用方 ChatUI；多模态附件异步编码，切换草稿时忽略旧读取；多张编辑图片按界面逐张发送。
 * 不读取密钥、不发送网络请求，文件上传示例只包含本地文件名，编码失败显示无效正文提示。 */
export function ChatCurlGuide({
  options,
  tags,
  image,
  files,
  presentation = "disclosure",
}: {
  options: CurlBodyOptions & { model: string };
  tags: string[];
  image?: File | null;
  files?: File[];
  presentation?: "disclosure" | "panel";
}) {
  const [attachment, setAttachment] = useState<{
    file: File;
    input: string;
    protocol: string;
    message: { role: string; content: unknown };
  } | null>(null);
  const [failure, setFailure] = useState<{ file: File; input: string; protocol: string } | null>(null);
  useEffect(() => {
    let active = true;
    if (image) {
      const encode =
        options.endpoint.protocol === "openai-responses" ? createMultimodalMessage : createChatMultimodalMessage;
      void encode(options.input, image)
        .then((message) => {
          if (active) {
            setAttachment({ file: image, input: options.input, protocol: options.endpoint.protocol, message });
            setFailure(null);
          }
        })
        .catch(() => {
          if (active) setFailure({ file: image, input: options.input, protocol: options.endpoint.protocol });
        });
    }
    return () => {
      active = false;
    };
  }, [image, options.input, options.endpoint.protocol]);
  // 编码结果只属于当前文件、草稿和协议；失败及成功记录均按同一身份匹配，切换输入无需在 effect 内同步重置状态。
  /** 校验异步结果归属；value 是先前编码或失败记录，返回是否仍对应当前草稿，不修改数据。 */
  const matchesCurrentInput = (value: { file: File; input: string; protocol: string } | null) =>
    value?.file === image && value?.input === options.input && value?.protocol === options.endpoint.protocol;
  const attachmentReady = attachment !== null && matchesCurrentInput(attachment);
  const failed = matchesCurrentInput(failure);
  const messages = [...options.messages];
  if (options.input.trim() || image)
    messages.push(attachmentReady ? attachment.message : { role: "user", content: options.input });
  const body = playgroundCurlBody({ ...options, messages });
  const multipart =
    options.endpoint.path.endsWith("/images/edits") ||
    ["openai-audio-transcription", "openai-audio-translation"].includes(options.endpoint.protocol);
  let names = [""];
  const fileField = options.endpoint.path.endsWith("/images/edits") ? "image" : "file";
  if (multipart) {
    const fallbackName = fileField === "image" ? "input.png" : "audio.mp3";
    names = files?.length ? files.map((file) => file.name) : [fallbackName];
  }
  const serializedBody = failed || (image && !attachmentReady) ? "" : JSON.stringify(body);
  return (
    <>
      {names.map((name, index) => (
        <CurlRequestGuide
          key={index}
          presentation={presentation}
          endpoint={options.endpoint}
          model={options.model}
          base={options.base}
          body={serializedBody}
          multipart={multipart}
          files={multipart ? { [fileField]: name } : undefined}
          headers={tags.length ? { "x-litellm-tags": tags.join(",") } : undefined}
        />
      ))}
    </>
  );
}
