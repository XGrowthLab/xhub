"use client";

import useAuthorized from "@/app/(dashboard)/hooks/useAuthorized";
import { useT } from "@/i18n";

/** 保留旧标签管理书签的权限说明；无参数，返回本地化说明，供已登录用户查看，无读写副作用。 */
export default function TagManagementPage() {
  const { isAuthorized } = useAuthorized();
  const t = useT();
  if (!isAuthorized) return null;

  return (
    <section className="p-6">
      <h1 className="text-xl font-semibold">{t("legacyRoutes.tagsTitle")}</h1>
      <p className="mt-2 text-sm text-muted-foreground">{t("legacyRoutes.tagsBody")}</p>
    </section>
  );
}
