"use client";

import Teams from "@/components/Teams";
import useAuthorized from "@/app/(dashboard)/hooks/useAuthorized";

/** 团队路由读取当前会话并渲染列表或详情；无外部参数，授权状态由已有组件处理。 */
export default function TeamsPage() {
  const { accessToken, userId, userRole, premiumUser } = useAuthorized();
  return <Teams accessToken={accessToken} userID={userId} userRole={userRole} premiumUser={premiumUser ?? false} />;
}
