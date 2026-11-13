"use client";

import { useMemo } from "react";
import { useAuth } from "@/contexts/AuthContext";
import { checkTokenValidity, decodeToken } from "@/utils/jwtUtils";

/** 从同一个有效登录令牌派生消息会话；无有效用户或凭据时返回空。
 * 每次换会话创建新的内存标识，用于隔离在途响应，不把令牌放进缓存或 URL。 */
export function useNotificationSession() {
  const { token } = useAuth();
  return useMemo(() => {
    if (!token || !checkTokenValidity(token)) return null;
    const decoded = decodeToken(token);
    if (typeof decoded?.user_id !== "string" || !decoded.user_id || typeof decoded?.key !== "string" || !decoded.key)
      return null;
    return { scope: Symbol("notification-session"), accessToken: decoded.key, userId: decoded.user_id };
  }, [token]);
}
