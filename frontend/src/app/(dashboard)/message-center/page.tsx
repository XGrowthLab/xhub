"use client";

import { NotificationCenter } from "@/components/notifications/NotificationCenter";

/** 当前登录用户的消息页；数据和身份由消息组件读取，不显示其他用户消息。 */
export default function NotificationsPage() {
  return <NotificationCenter />;
}
