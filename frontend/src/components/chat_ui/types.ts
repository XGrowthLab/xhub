import type { TokenUsage } from "./ResponseMetrics";

export interface VectorStoreSearchResult {
  score: number;
  content: Array<{ text: string; type: string }>;
  file_id?: string;
  filename?: string;
  attributes?: Record<string, any>;
}

export interface VectorStoreSearchResponse {
  object: string;
  search_query: string;
  data: VectorStoreSearchResult[];
}

export interface A2ATaskMetadata {
  taskId?: string;
  contextId?: string;
  status?: {
    state?: string;
    timestamp?: string;
    message?: string;
  };
  metadata?: Record<string, any>;
}

export interface MessageType {
  role: string;
  content: string | MultimodalContent[];
  model?: string;
  isImage?: boolean;
  isAudio?: boolean;
  isEmbeddings?: boolean;
  reasoningContent?: string;
  timeToFirstToken?: number;
  totalLatency?: number;
  usage?: TokenUsage;
  toolName?: string;
  imagePreviewUrl?: string;
  /** 附件的临时内存标识；浏览器会话只保存标识，不持久化文件内容。 */
  attachmentToken?: string;
  image?: {
    url: string;
    detail: string;
  };
  searchResults?: VectorStoreSearchResponse[];
  a2aMetadata?: A2ATaskMetadata;
}

export interface MultimodalContent {
  type: "input_text" | "input_image" | "input_file";
  text?: string;
  image_url?: string;
  filename?: string;
  file_data?: string;
}
