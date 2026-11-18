"use client";

import { ViewUserDashboard } from "./_components";
import useAuthorized from "@/app/(dashboard)/hooks/useAuthorized";
import { useTeams } from "@/app/(dashboard)/hooks/teams/useTeams";

/** 用户目录按当前会话加载权限和团队；无外部参数，返回列表或深链接详情。 */
export default function UsersPage() {
  const { accessToken, token, userRole, userId } = useAuthorized();
  const { data: teams } = useTeams();
  return (
    <ViewUserDashboard
      userID={userId}
      userRole={userRole}
      token={token}
      teams={teams ?? null}
      accessToken={accessToken}
    />
  );
}
