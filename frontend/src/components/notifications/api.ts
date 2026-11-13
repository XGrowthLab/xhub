import { t } from "@/i18n/runtime";
import { apiClient } from "@/components/networking";

export interface NotificationEntry {
  id: string;
  type: string;
  created_at: string;
  read: boolean;
  data: Record<string, unknown>;
}
export interface NotificationPage {
  items: NotificationEntry[];
  total: number;
}

/** 获取当前会话的一页消息；token 为会话凭据，offset/limit 为分页参数，异常或畸形响应会抛错。 */
export async function listNotifications(
  token: string,
  offset: number,
  limit: number,
  signal?: AbortSignal,
): Promise<NotificationPage> {
  const response = await apiClient.get<NotificationPage>("/notifications", {
    accessToken: token,
    query: { offset, limit },
    signal,
  });
  if (
    !response ||
    !Array.isArray(response.items) ||
    !Number.isSafeInteger(response.total) ||
    response.total < 0 ||
    response.items.some(
      (item) =>
        !item ||
        typeof item.id !== "string" ||
        !item.id ||
        typeof item.type !== "string" ||
        typeof item.read !== "boolean" ||
        typeof item.created_at !== "string" ||
        !item.data ||
        typeof item.data !== "object" ||
        Array.isArray(item.data),
    )
  ) {
    throw new Error(t("notifications.invalidData"));
  }
  return response;
}

/** 读取当前用户未读数；凭据和中止信号由调用方提供，非法响应抛错，不能显示为零。 */
export async function unreadNotifications(token: string, signal?: AbortSignal): Promise<number> {
  const response = await apiClient.get<{ count: number }>("/notifications/unread", { accessToken: token, signal });
  if (!response || !Number.isSafeInteger(response.count) || response.count < 0)
    throw new Error(t("notifications.invalidData"));
  return response.count;
}

/** 将当前用户指定消息标为已读；凭据、标识由调用方提供；服务端拒绝或故障会抛错。 */
export async function markNotificationRead(token: string, id: string): Promise<void> {
  await apiClient.post("/notifications/read", { accessToken: token, body: { id } });
}
