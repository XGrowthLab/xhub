import Link from "next/link";
import { uiHref } from "@/utils/uiHref";
import DirectorySettings from "@/components/Settings/AdminSettings/DirectorySettings";
import { useT } from "@/i18n";
import React from "react";

/** 平台管理员设置页面；无参数，返回与模型管理一致的主题化页头及企业配置入口。
 * 仅呈现已接入后台的日志、目录及登录设置，不改变权限或持久化行为。
 */
const AdminPanel: React.FC = () => {
  const t = useT();
  return (
    <div
      data-testid="admin-settings-canvas"
      className="-m-3 min-h-[calc(100%+1.5rem)] bg-background p-3 sm:-m-6 sm:min-h-[calc(100%+3rem)] sm:p-6"
    >
      <div className="mx-auto w-full max-w-[1600px] space-y-6">
        <header className="py-2">
          <h2 className="text-2xl font-semibold tracking-tight">{t("enterprise.settingsTabs")}</h2>
          <p className="mt-1 text-sm text-muted-foreground">{t("pages.adminPanel.manageAccess")}</p>
          <Link
            href={uiHref("users")}
            className="mt-2 inline-block text-sm text-primary underline-offset-4 hover:underline focus-visible:underline"
          >
            {t("pages.adminPanel.openUsers")}
          </Link>
        </header>
        <DirectorySettings />
      </div>
    </div>
  );
};

export default AdminPanel;
