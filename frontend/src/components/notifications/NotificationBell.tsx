"use client";

import { useEffect, useState } from "react";
import { Bell } from "lucide-react";
import Link from "next/link";
import { useT } from "@/i18n";
import { uiHref } from "@/utils/uiHref";
import { unreadNotifications } from "./api";
import { useNotificationSession } from "./session";

/** 顶栏显示当前用户未读数并提供消息入口；仅活动页每分钟刷新，换账号立即隐藏旧数值。
 * 不推断管理员权限；接口失败明确标注，组件卸载时中止请求并清理定时器。 */
export function NotificationBell() {
  const t = useT();
  const session = useNotificationSession();
  const [state, setState] = useState<{ scope: symbol; count: number | null; failed: boolean } | null>(null);
  useEffect(() => {
    if (!session) return;
    let active = true;
    let pending: AbortController | undefined;
    const refresh = () => {
      if (document.visibilityState === "hidden") return;
      pending?.abort();
      const request = new AbortController();
      pending = request;
      void unreadNotifications(session.accessToken, request.signal)
        .then((count) => {
          if (active && !request.signal.aborted) setState({ scope: session.scope, count, failed: false });
        })
        .catch(() => {
          if (active && !request.signal.aborted) setState({ scope: session.scope, count: null, failed: true });
        });
    };
    refresh();
    const timer = window.setInterval(refresh, 60000);
    window.addEventListener("xhub-notifications-changed", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      active = false;
      pending?.abort();
      window.clearInterval(timer);
      window.removeEventListener("xhub-notifications-changed", refresh);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [session]);
  if (!session) return null;
  const current = state?.scope === session.scope ? state : null;
  let label = t("notifications.open");
  if (current?.failed) label = t("notifications.unreadUnavailable");
  else if (current?.count) label = t("notifications.unreadCount", { count: current.count });
  return (
    <Link
      href={uiHref("message-center")}
      aria-label={label}
      title={label}
      className="relative flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
    >
      <Bell className="size-5" aria-hidden />
      {current?.failed && (
        <span className="absolute right-0 top-0 text-xs text-destructive" aria-hidden>
          !
        </span>
      )}
      {!!current?.count && (
        <span
          className="absolute right-0 top-0 min-w-4 rounded-full bg-primary px-1 text-center text-[10px] text-primary-foreground"
          aria-hidden
        >
          {current.count > 99 ? "99+" : current.count}
        </span>
      )}
    </Link>
  );
}
