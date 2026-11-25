"use client";

import { useEffect, useState } from "react";
import { userGetInfoV2, type UserInfoV2Response } from "@/components/networking";
import { DirectoryEditDialog } from "@/components/shared/DirectoryDetail";
import { useT } from "@/i18n";
import UserAccountEditor from "./UserAccountEditor";

/** 读取单个用户的账户快照；参数可能来自列表编辑或已加载的详情，兼容历史 user_info 包装。 */
export function unwrapUserInfo(data: unknown): UserInfoV2Response | null {
  if (!data || typeof data !== "object") return null;
  const record = data as Partial<UserInfoV2Response> & { user_info?: UserInfoV2Response };
  const candidate = record.user_id ? record : record.user_info;
  return typeof candidate?.user_id === "string" && candidate.user_id.length > 0 ? candidate as UserInfoV2Response : null;
}

/** 用独立弹窗编辑用户；user 已知时直接使用详情快照，否则按 userId 读取后台。取消与保存只关闭弹窗，不导航。 */
export default function UserAccountEditDialog({ userId, user, accessToken, onClose, onSaved }: {
  userId: string;
  user?: UserInfoV2Response;
  accessToken: string;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const t = useT();
  const [fetched, setFetched] = useState<UserInfoV2Response | null>();
  useEffect(() => {
    if (user) return;
    let active = true;
    void userGetInfoV2(accessToken, userId)
      .then((data) => { if (active) setFetched(unwrapUserInfo(data)); })
      .catch(() => { if (active) setFetched(null); });
    return () => { active = false; };
  }, [accessToken, user, userId]);
  const record = user ?? fetched;
  return (
    <DirectoryEditDialog open title={t("pages.users.editUser")} onClose={onClose}>
      {record ? (
        <UserAccountEditor key={userId} user={record} accessToken={accessToken} onCancel={onClose}
          onSaved={async () => { await onSaved(); onClose(); }} />
      ) : (
        <p role={fetched === undefined ? "status" : "alert"} className="text-sm text-muted-foreground">
          {t(fetched === undefined ? "pages.users.loadingUser" : "pages.users.userNotFound")}
        </p>
      )}
    </DirectoryEditDialog>
  );
}
