import useAuthorized from "@/app/(dashboard)/hooks/useAuthorized";
import { navAccountDisplayName } from "@/components/Navbar/navDisplayName";
import CopyButton from "@/components/shared/CopyButton";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import { cn } from "@/lib/cva.config";
import { ChevronsUpDown, IdCard, KeyRound, LogOut, Mail, ShieldCheck, Users, Building2 } from "lucide-react";
import Link from "next/link";
import React, { useState } from "react";
import SetPasswordModal from "@/components/SetPasswordModal";
import useIsOrgAdmin from "@/app/(dashboard)/hooks/useIsOrgAdmin";
import { useSessionIdentity } from "@/app/(dashboard)/hooks/sessionIdentity/useSessionIdentity";
import { isProxyAdminRole } from "@/utils/roles";
import { useT } from "@/i18n";

/** 根据身份生成稳定头像颜色；输入身份字符串，返回色相，不存储身份。 */
function hueFromString(seed: string): number {
  let h = 0;
  for (let i = 0; i < seed.length; i += 1) {
    h = seed.charCodeAt(i) + ((h << 5) - h);
  }
  return Math.abs(h) % 360;
}

/** 从邮箱或用户 ID 提取头像缩写；空身份使用问号，供账户入口展示。 */
function initialsFromIdentity(email: string | null, userId: string | null): string {
  const local = email?.split("@")[0]?.trim();
  if (local) {
    const parts = local
      .replace(/[^a-zA-Z0-9]+/g, " ")
      .trim()
      .split(/\s+/)
      .filter(Boolean);
    if (parts.length >= 2) {
      return `${parts[0]!.charAt(0)}${parts[1]!.charAt(0)}`.toUpperCase();
    }
    if (parts.length === 1) {
      const p = parts[0]!;
      return p.length >= 2 ? p.slice(0, 2).toUpperCase() : `${p.charAt(0)}`.toUpperCase();
    }
  }
  if (userId && userId.length >= 2) {
    return userId.slice(0, 2).toUpperCase();
  }
  if (userId && userId.length === 1) {
    return `${userId.toUpperCase()}•`;
  }
  return "?";
}

/** 账户详情采用固定标签列和弹性值列；长邮箱与 ID 不挤压标签，值由子组件提供。 */
const InfoRow: React.FC<{ icon: React.ReactNode; label: string; children: React.ReactNode }> = ({
  icon,
  label,
  children,
}) => {
  return (
    <div className="grid min-h-10 grid-cols-[72px_minmax(0,1fr)] items-center gap-3">
      <span className="flex shrink-0 items-center gap-2 whitespace-nowrap text-sm text-muted-foreground [&>svg]:shrink-0">
        {icon}
        {label}
      </span>
      {children}
    </div>
  );
};

/** 显示可截断但可完整复制的身份值；value 是原始文本，copyLabel 为本地化按钮名。 */
const MonoValue: React.FC<{ value: string | null; copyLabel: string }> = ({ value, copyLabel }) => {
  return (
    <span className="flex min-w-0 items-center justify-end gap-1">
      <span className="min-w-0 truncate text-sm text-foreground" title={value || "-"}>
        {value || "-"}
      </span>
      <CopyButton value={value} label={copyLabel} />
    </span>
  );
};

interface SidebarAccountMenuProps {
  onLogout: () => void;
  collapsed?: boolean;
}

/** 显示当前身份和已有账户操作；collapsed 控制紧凑入口，权限判断沿用原会话，不修改账户。 */
const SidebarAccountMenu: React.FC<SidebarAccountMenuProps> = ({ onLogout, collapsed = false }) => {
  const t = useT();
  const { userId, userEmail, userRoleLabel: userRole, accessToken } = useAuthorized();
  const isOrgAdmin = useIsOrgAdmin();
  const { data: identity } = useSessionIdentity();
  const isTeamAdmin = Boolean(identity?.teams?.some((team) => team.role === "team_admin"));
  const isPlatformAdmin = isProxyAdminRole(userRole);
  const roleLabels = [
    isPlatformAdmin ? t("Platform administrator") : null,
    isOrgAdmin ? t("Organization administrator") : null,
    isTeamAdmin ? t("Team management") : null,
  ].filter((label): label is string => Boolean(label));
  if (roleLabels.length === 0) roleLabels.push(t("Regular user"));
  const [passwordOpen, setPasswordOpen] = useState(false);

  const seed = userEmail || userId || "user";
  const initials = initialsFromIdentity(userEmail, userId);
  const hue = hueFromString(seed);
  const displayName = navAccountDisplayName(userEmail, userId);
  const triggerLabel = t("consoleUi.accountMenu", { identity: displayName });

  return (
    <>
      <Popover>
        <PopoverTrigger
          className={cn(
            "flex w-full items-center rounded-lg border border-transparent transition-colors hover:bg-sidebar-accent",
            collapsed ? "justify-center px-0 py-1" : "gap-2.5 px-2 py-1.5 text-left",
          )}
          aria-label={triggerLabel}
          title={collapsed ? displayName : undefined}
        >
          <Avatar className="size-[30px] shadow-inner ring-1 ring-black/5" aria-hidden>
            <AvatarFallback className="font-semibold text-white" style={{ backgroundColor: `hsl(${hue} 46% 38%)` }}>
              {initials}
            </AvatarFallback>
          </Avatar>
          {!collapsed && (
            <>
              <span className="min-w-0 flex-1 leading-tight">
                <span className="block truncate text-[13px] font-medium text-sidebar-foreground">{displayName}</span>
                <span className="block truncate text-[11px] text-muted-foreground">{roleLabels.join(" · ")}</span>
              </span>
              <ChevronsUpDown size={16} strokeWidth={1.75} className="shrink-0 text-muted-foreground" aria-hidden />
            </>
          )}
        </PopoverTrigger>

        <PopoverContent
          side="top"
          align="start"
          sideOffset={8}
          className="w-[340px] max-w-[calc(100vw-2rem)] gap-0 overflow-hidden rounded-xl p-0"
          data-testid="sidebar-account-menu-panel"
        >
          <div className="flex items-center gap-2 border-b border-border px-3 py-3">
            <span className="text-[15px] font-bold tracking-tight text-foreground">{t("site.product")}</span>
            <span className="flex-1" />
          </div>

          <div className="flex flex-col px-3 py-2">
            <InfoRow icon={<ShieldCheck className="size-[17px]" />} label={t("Role")}>
              <span className="flex flex-wrap justify-end gap-1">
                {isPlatformAdmin ? <Badge variant="secondary">{t("Platform administrator")}</Badge> : null}
                {isOrgAdmin ? (
                  <Badge variant="secondary" render={<Link href="/organizations" />}>
                    <Building2 className="size-3" />
                    {t("Organization administrator")}
                  </Badge>
                ) : null}
                {isTeamAdmin ? (
                  <Badge variant="secondary" render={<Link href="/teams" />}>
                    <Users className="size-3" />
                    {t("Team management")}
                  </Badge>
                ) : null}
                {!isPlatformAdmin && !isOrgAdmin && !isTeamAdmin ? (
                  <Badge variant="secondary">{t("Regular user")}</Badge>
                ) : null}
              </span>
            </InfoRow>
            <InfoRow icon={<Mail className="size-[17px]" />} label={t("Email")}>
              <MonoValue value={userEmail} copyLabel={t("consoleUi.copyEmail")} />
            </InfoRow>
            <InfoRow icon={<IdCard className="size-[17px]" />} label={t("User ID")}>
              <MonoValue value={userId} copyLabel={t("consoleUi.copyUserId")} />
            </InfoRow>
          </div>

          <Separator />

          <Button
            variant="ghost"
            onClick={() => setPasswordOpen(true)}
            disabled={!userId}
            className="h-[42px] w-full justify-start gap-2.5 rounded-none px-3 text-sm font-medium text-foreground"
          >
            <KeyRound className="size-[19px] text-muted-foreground" />
            {t("Change account password")}
          </Button>

          <Separator />

          <Button
            variant="ghost"
            onClick={onLogout}
            className="h-[42px] w-full justify-start gap-2.5 rounded-none px-3 text-sm font-medium text-foreground"
          >
            <LogOut className="size-[19px] text-muted-foreground" />
            {t("Logout")}
          </Button>
        </PopoverContent>
      </Popover>
      <SetPasswordModal
        open={passwordOpen}
        onOpenChange={setPasswordOpen}
        accessToken={accessToken}
        user={userId ? { user_id: userId, user_email: userEmail } : null}
      />
    </>
  );
};

export default SidebarAccountMenu;
