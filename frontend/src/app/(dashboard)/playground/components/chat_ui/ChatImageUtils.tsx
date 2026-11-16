import { MessageType } from "@/components/chat_ui/types";

export interface ChatMultimodalContent {
  type: "text" | "image_url" | "file";
  text?: string;
  image_url?: {
    url: string;
    detail?: string;
  };
  file?: { filename: string; file_data: string };
}

export const convertImageToBase64 = (file: File): Promise<string> => {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = reader.result as string;
      // For chat completions, we keep the full data URI format
      resolve(result);
    };
    reader.onerror = reject;
    reader.readAsDataURL(file);
  });
};

/** 创建 Chat 图文或 PDF 消息；参数为用户文字和本地附件，返回可发送的协议内容。
 * 调试台与 curl 预览调用；PDF 使用文件块，图片使用图片块，保留用户文字和文件名，不发起网络请求。 */
export const createChatMultimodalMessage = async (
  inputMessage: string,
  file: File,
): Promise<{ role: string; content: ChatMultimodalContent[] }> => {
  const base64DataUri = await convertImageToBase64(file);

  const isPDF = file.type === "application/pdf" || file.name.toLowerCase().endsWith(".pdf");
  // MIME 为空的 PDF 仍需使用明确媒体类型；不能把 PDF 当作图片发送。
  const fileData = isPDF ? base64DataUri.replace(/^data:[^,]*,/, "data:application/pdf;base64,") : base64DataUri;
  return {
    role: "user",
    content: [
      { type: "text", text: inputMessage },
      isPDF
        ? { type: "file", file: { filename: file.name, file_data: fileData } }
        : {
            type: "image_url",
            image_url: {
              url: base64DataUri,
            },
          },
    ],
  };
};

export const createChatDisplayMessage = (
  inputMessage: string,
  hasFile: boolean,
  filePreviewUrl?: string,
  fileName?: string,
): MessageType => {
  let attachmentText = "";
  if (hasFile && fileName) {
    attachmentText = fileName.toLowerCase().endsWith(".pdf") ? "[PDF attached]" : "[Image attached]";
  }

  const displayMessage: MessageType = {
    role: "user",
    content: hasFile ? `${inputMessage} ${attachmentText}` : inputMessage,
  };

  if (hasFile && filePreviewUrl) {
    displayMessage.imagePreviewUrl = filePreviewUrl;
  }

  return displayMessage;
};

export const shouldShowChatAttachedImage = (message: MessageType): boolean => {
  return (
    message.role === "user" &&
    typeof message.content === "string" &&
    (message.content.includes("[Image attached]") || message.content.includes("[PDF attached]")) &&
    !!message.imagePreviewUrl
  );
};
