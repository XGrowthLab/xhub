import Link from "next/link";
import { uiHref } from "@/utils/uiHref";
import DirectorySettings from "@/components/Settings/AdminSettings/DirectorySettings";
import { useT } from "@/i18n";
import { ConsolePageHeader } from "@/components/console/ConsolePrimitives";
import { buttonVariants } from "@/components/ui/button";
import { Users } from "lucide-react";
import React from "react";

/** 平台管理员设置页面；无参数，返回与模型管理一致的主题化页头及企业配置入口。
 * 仅呈现已接入后台的日志、目录及登录设置，不改变权限或持久化行为。
 */
const AdminPanel: React.FC = () => {
  const t = useT();
  return (
    <section data-testid="admin-settings-canvas" className="mx-auto w-full min-w-0 max-w-[1600px] space-y-5">
      <ConsolePageHeader
        title={t("enterprise.settingsTabs")}
        actions={
          <Link href={uiHref("users")} className={buttonVariants({ variant: "outline", size: "sm" })}>
            <Users aria-hidden="true" />
            {t("pages.adminPanel.openUsers")}
          </Link>
        }
      />
      <DirectorySettings />
    </section>
  );
};

export default AdminPanel;
