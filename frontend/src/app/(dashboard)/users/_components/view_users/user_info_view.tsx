import { DateCell } from "@/components/shared/table_cells/date_cell";
import React, { useState } from "react";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  userGetInfoV2,
  UserInfoV2Response,
  userDeleteCall,
  teamInfoCall,
  teamListCall,
  teamMemberAddCall,
  teamMemberDeleteCall,
  teamMemberUpdateCall,
  organizationListCall,
  Member,
} from "@/components/networking";
import { SimpleTooltip } from "@/components/ui/tooltip";
import { accountRoleKind, isTeamAdminMembership } from "@/utils/iamRoles";
import { useT, t } from "@/i18n";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import {
  Combobox,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
} from "@/components/ui/combobox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { teamDetailHref } from "@/utils/entityLinks";
import { BadgeLink } from "@/components/shared/BadgeLink";
import { formatNumberWithCommas } from "@/utils/dataUtils";
import { ArrowLeft, KeyRound, Pencil, Plus, Trash2 } from "lucide-react";
import UserAccountEditDialog, { unwrapUserInfo } from "./UserAccountEditDialog";
import { DirectoryDetailHeader, DirectoryFacts, DirectoryPanel } from "@/components/shared/DirectoryDetail";
import CopyButton from "@/components/shared/CopyButton";
import { HelpDisclosure } from "@/components/console/ConsolePrimitives";
import SetPasswordModal from "@/components/SetPasswordModal";
import { toast } from "@/lib/toast";

import DeleteResourceModal from "@/components/common_components/DeleteResourceModal";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";

interface UserInfoViewProps {
  userId: string;
  onClose: () => void;
  accessToken: string | null;
  userRole: string | null;
  onDelete?: () => void;
  possibleUIRoles: Record<string, Record<string, string>> | null;
  initialTab?: number; // 0 for Overview, 1 for Details
  startInEditMode?: boolean;
}

/** Team info used for display in user detail view */
interface TeamDisplayInfo {
  team_id: string;
  team_alias: string | null;
  user_role?: string | null;
  organization_alias?: string | null;
}

const accountRoleLabel = (role: string | null | undefined): string => {
  const kind = accountRoleKind(role);
  if (kind === "admin") return t("Platform administrator");
  if (kind === "user") return t("Regular user");
  if (!role) return t("pages.users.notSet");
  return role;
};

const membershipRoleLabel = (role: string | null | undefined): string => {
  if (!role) return "—";
  return isTeamAdminMembership(role) ? t("Team admin") : t("Team member");
};

const asTeamRows = (raw: unknown): Array<Record<string, any>> => {
  if (Array.isArray(raw)) return raw;
  if (raw && typeof raw === "object" && Array.isArray((raw as { teams?: unknown }).teams)) {
    return (raw as { teams: Array<Record<string, any>> }).teams;
  }
  return [];
};

const organizationNames = async (accessToken: string): Promise<Map<string, string>> => {
  const names = new Map<string, string>();
  try {
    const raw = await organizationListCall(accessToken);
    const rows = Array.isArray(raw) ? raw : ((raw as { organizations?: unknown[] })?.organizations ?? []);
    for (const row of rows as Array<Record<string, string>>) {
      if (row.organization_id) {
        names.set(row.organization_id, row.organization_alias || row.organization_name || row.organization_id);
      }
    }
  } catch {
    // The organization name is a label. The team still shows without it.
  }
  return names;
};

const resolveInfoTeams = async (accessToken: string, infoTeams: unknown): Promise<TeamDisplayInfo[]> => {
  if (!Array.isArray(infoTeams) || infoTeams.length === 0) return [];
  return Promise.all(
    infoTeams.map(async (item) => {
      if (typeof item === "string") {
        try {
          const teamData = await teamInfoCall(accessToken, item);
          return {
            team_id: item,
            team_alias: teamData?.team_info?.team_alias || teamData?.team_alias || null,
            user_role: null,
            organization_alias: null,
          };
        } catch {
          return { team_id: item, team_alias: null, user_role: null, organization_alias: null };
        }
      }
      const team = item as Record<string, any>;
      return {
        team_id: String(team.team_id ?? ""),
        team_alias: team.team_alias ?? null,
        user_role: team.user_role ?? null,
        organization_alias: team.organization_alias ?? null,
      };
    }),
  );
};

// The user-info payload often carries an empty teams list. The team list for
// this account is the membership, including which organization owns the team.
const loadUserTeams = async (accessToken: string, userId: string, infoTeams: unknown): Promise<TeamDisplayInfo[]> => {
  try {
    const rows = asTeamRows(await teamListCall(accessToken, null, userId));
    if (rows.length > 0) {
      const orgs = await organizationNames(accessToken);
      return rows.map((team) => ({
        team_id: team.team_id,
        team_alias: team.team_alias || team.team_id,
        user_role: team.user_role ?? null,
        organization_alias: orgs.get(team.organization_id) ?? team.organization_alias ?? null,
      }));
    }
  } catch {
    // Fall through to whatever the user payload named.
  }
  return resolveInfoTeams(accessToken, infoTeams);
};

const ADD_TEAM_FIELD_ID = "add-team-team";
const ADD_TEAM_ROLE_FIELD_ID = "add-team-role";

interface TeamOption {
  team_id: string;
  team_alias: string;
}

const memberRoleOptions = () => [
  { value: "user", label: t("Team member"), hint: t("Can view team info, but not manage it") },
  { value: "admin", label: t("Team admin"), hint: t("Can create team keys, add members, and manage settings") },
];

/** 用户详情与管理员编辑入口；参数包含目标账户、当前会话、关闭回调和初始编辑模式。返回详情及团队操作。
 * 保存后重新读取后台状态；普通用户仅查看，管理员可编辑账户、增删团队及调整团队角色，失败保留操作上下文。
 * 预算为空时有团队共享上级额度，无团队不限额；固定个人预算限制累计消费。
 */
/** 用户详情展示与编辑入口；参数指定用户、管理会话及关闭回调，返回账户、团队和个人周期信息。
 * 日期按北京时间显示；读取失败保留既有错误反馈，编辑通过真实账户接口保存并重新加载。 */
export default function UserInfoView({
  userId,
  onClose,
  accessToken,
  userRole,
  onDelete,
  initialTab = 0,
  startInEditMode = false,
}: UserInfoViewProps) {
  const t = useT();
  const [isEditing, setIsEditing] = useState(startInEditMode);
  const [updatingTeam, setUpdatingTeam] = useState<string | null>(null);
  const [userData, setUserData] = useState<UserInfoV2Response | null>(null);
  const [teamDetails, setTeamDetails] = useState<TeamDisplayInfo[]>([]);
  const [isDeleteModalOpen, setIsDeleteModalOpen] = useState(false);
  const [isSetPasswordOpen, setIsSetPasswordOpen] = useState(false);
  const [isDeletingUser, setIsDeletingUser] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [activeTab, setActiveTab] = useState<string>(initialTab === 1 ? "details" : "overview");
  const [isTeamsExpanded, setIsTeamsExpanded] = useState(false);
  const [isAddTeamModalOpen, setIsAddTeamModalOpen] = useState(false);
  const [isRemoveTeamModalOpen, setIsRemoveTeamModalOpen] = useState(false);
  const [teamToRemove, setTeamToRemove] = useState<TeamDisplayInfo | null>(null);
  const [isAddingTeam, setIsAddingTeam] = useState(false);
  const [isRemovingTeam, setIsRemovingTeam] = useState(false);
  const [allTeams, setAllTeams] = useState<Array<{ team_id: string; team_alias: string }>>([]);
  const [selectedTeamId, setSelectedTeamId] = useState<string>("");
  const [selectedRole, setSelectedRole] = useState<string>("user");
  const [isLoadingTeams, setIsLoadingTeams] = useState(false);


  React.useEffect(() => {
    const fetchData = async () => {
      try {
        if (!accessToken) return;
        const data = unwrapUserInfo(await userGetInfoV2(accessToken, userId));
        setUserData(data);
        if (data) setTeamDetails(await loadUserTeams(accessToken, userId, data.teams));
      } catch (error) {
        console.error("Error fetching user data:", error);
        toast.fromError(t("Failed to fetch user data"));
      } finally {
        setIsLoading(false);
      }
    };

    fetchData();
  }, [accessToken, userId, userRole]);

  const isProxyAdmin = userRole === "proxy_admin" || userRole === "Admin";

  /** 重新加载账户及团队；无需参数，返回完成状态，供账户/团队保存调用，失败向调用方传播。 */
  const refreshUser = async () => {
    if (!accessToken) return;
    const data = unwrapUserInfo(await userGetInfoV2(accessToken, userId));
    const teams = await loadUserTeams(accessToken, userId, data?.teams);
    setUserData(data);
    setTeamDetails(teams);
  };

  /** 接收目标团队和新角色，更新真实成员关系并刷新；返回完成状态，失败提示且保留旧角色，期间禁止重复提交。 */
  const updateTeamRole = async (team: TeamDisplayInfo, role: string) => {
    if (!accessToken || updatingTeam) return;
    setUpdatingTeam(team.team_id);
    try {
      await teamMemberUpdateCall(accessToken, team.team_id, { user_id: userId, role });
      await refreshUser();
      toast.success(t("User updated successfully"));
    } catch (error) {
      toast.fromError(error);
    } finally {
      setUpdatingTeam(null);
    }
  };

  const fetchAllTeams = async () => {
    if (!accessToken) return;
    setIsLoadingTeams(true);
    try {
      const teams = await teamListCall(accessToken, null);
      setAllTeams(
        (teams || []).map((t: any) => ({
          team_id: t.team_id,
          team_alias: t.team_alias || t.team_id,
        })),
      );
    } catch (error) {
      console.error("Error fetching teams:", error);
    } finally {
      setIsLoadingTeams(false);
    }
  };

  const handleOpenAddTeamModal = () => {
    setSelectedTeamId("");
    setSelectedRole("user");
    setIsAddTeamModalOpen(true);
    fetchAllTeams();
  };

  const handleAddTeamSubmit = async () => {
    if (!accessToken || !selectedTeamId) return;
    setIsAddingTeam(true);
    try {
      const member: Member = {
        role: selectedRole,
        user_id: userId,
        user_email: userData?.user_email ?? null,
      };
      await teamMemberAddCall(accessToken, selectedTeamId, member);
      toast.success(t("User added to team successfully"));
      setIsAddTeamModalOpen(false);
      const data = unwrapUserInfo(await userGetInfoV2(accessToken, userId));
      setUserData(data);
      setTeamDetails(await loadUserTeams(accessToken, userId, data?.teams));
    } catch (error: any) {
      console.error("Error adding user to team:", error);
      toast.fromError(error?.message || t("Failed to add user to team"));
    } finally {
      setIsAddingTeam(false);
    }
  };

  const handleOpenRemoveTeamModal = (team: TeamDisplayInfo) => {
    setTeamToRemove(team);
    setIsRemoveTeamModalOpen(true);
  };

  const handleRemoveTeamConfirm = async () => {
    if (!accessToken || !teamToRemove) return;
    setIsRemovingTeam(true);
    try {
      const member: Member = {
        role: "user",
        user_id: userId,
      };
      await teamMemberDeleteCall(accessToken, teamToRemove.team_id, member);
      toast.success(t("User removed from team successfully"));
      setIsRemoveTeamModalOpen(false);
      setTeamToRemove(null);
      // Re-fetch user data to refresh teams
      const data = unwrapUserInfo(await userGetInfoV2(accessToken, userId));
      setUserData(data);
      setTeamDetails(await loadUserTeams(accessToken, userId, data?.teams));
    } catch (error: any) {
      console.error("Error removing user from team:", error);
      toast.fromError(error?.message || t("Failed to remove user from team"));
    } finally {
      setIsRemovingTeam(false);
    }
  };

  const handleRemoveTeamCancel = () => {
    setIsRemoveTeamModalOpen(false);
    setTeamToRemove(null);
  };

  const availableTeamsForAdd = allTeams.filter((t) => !teamDetails.some((td) => td.team_id === t.team_id));

  const selectedTeamOption = availableTeamsForAdd.find((team) => team.team_id === selectedTeamId) ?? null;

  /** 确认删除当前用户；无参数，使用当前会话及单个ID，成功更新页面，失败向用户显示错误。 */
  const handleDelete = async () => {
    try {
      if (!accessToken) return;
      setIsDeletingUser(true);
      await userDeleteCall(accessToken, userId);
      toast.success(t("User deleted successfully"));
      if (onDelete) {
        onDelete();
      }
      onClose();
    } catch (error) {
      console.error("Error deleting user:", error);
      toast.fromError(t("Failed to delete user"));
    } finally {
      setIsDeleteModalOpen(false);
      setIsDeletingUser(false);
    }
  };

  const cancelDelete = () => {
    setIsDeleteModalOpen(false);
  };

  if (isLoading) {
    return (
      <div className="p-4">
        <Button variant="ghost" onClick={onClose} className="mb-4">
          <ArrowLeft />
          {t("Back to Users")}
        </Button>
        <p className="text-sm">{t("pages.users.loadingUser")}</p>
      </div>
    );
  }

  if (!userData) {
    return (
      <div className="p-4">
        <Button variant="ghost" onClick={onClose} className="mb-4">
          <ArrowLeft />
          {t("Back to Users")}
        </Button>
        <p className="text-sm">{t("pages.users.userNotFound")}</p>
      </div>
    );
  }

  // 空预算不构成调用授权门槛：有团队共享父级，无团队没有个人消费上限。
  const budgetLabel = userData.max_budget == null
    ? t(teamDetails.length === 0 ? "pages.users.unlimited" : "quotaGuide.blank")
    : `$${formatNumberWithCommas(userData.max_budget, 2)}`;

  const personalModels = userData.models?.length ? (
    <div className="flex flex-wrap gap-2">
      {userData.models.map((model) => (
        <BadgeLink key={model} className="max-w-full whitespace-normal [overflow-wrap:anywhere]">{model}</BadgeLink>
      ))}
    </div>
  ) : <p className="text-sm text-muted-foreground">{t("Models are decided by the teams below. This account has no model list of its own.")}</p>;
  const copyIdLabel = t("pages.users.copyUserId");
  const budgetReset = userData.budget_reset_day
    ? t("userBudgetReset.day", { day: userData.budget_reset_day })
    : t("userBudgetReset.none");

  return (
    <div className="mx-auto w-full min-w-0 max-w-[1600px] space-y-4">
      <DirectoryDetailHeader
        title={userData.user_email || t("User")}
        id={userData.user_id}
        copyLabel={copyIdLabel}
        backLabel={t("Back to Users")}
        onBack={onClose}
        actions={<>
          {isProxyAdmin && <Button onClick={() => setIsEditing(true)} size="sm">
            <Pencil />{t("pages.users.editUser")}
          </Button>}
          <Button variant="outline" size="sm" onClick={() => setIsSetPasswordOpen(true)}>
            <KeyRound />{t("pages.users.resetPassword")}
          </Button>
          {isProxyAdmin && (
            <Button variant="outline" size="sm" onClick={() => setIsDeleteModalOpen(true)}
              className="border-destructive/30 text-destructive hover:bg-destructive/10">
              <Trash2 />{t("pages.users.deleteUser")}
            </Button>
          )}
        </>}
      />
      {isEditing && isProxyAdmin && accessToken && (
        <UserAccountEditDialog userId={userId} user={userData} accessToken={accessToken}
          onClose={() => setIsEditing(false)} onSaved={refreshUser} />
      )}

      <DeleteResourceModal
        isOpen={isDeleteModalOpen}
        title={t("pages.users.deleteTitle")}
        message={t("pages.users.deleteMessage")}
        resourceInformationTitle={t("pages.users.infoTitle")}
        resourceInformation={[
          { label: t("pages.users.email"), value: userData.user_email },
          { label: t("pages.users.userId"), value: userData.user_id, code: true },
          {
            label: t("pages.users.role"),
            value: accountRoleLabel(userData.user_role),
          },
          {
            label: t("pages.users.spend"),
            value: userData.spend !== null && userData.spend !== undefined ? userData.spend.toFixed(2) : undefined,
          },
        ]}
        onCancel={cancelDelete}
        onOk={handleDelete}
        confirmLoading={isDeletingUser}
      />

      <Tabs value={activeTab} onValueChange={(value: unknown) => setActiveTab(String(value))} className="min-w-0 gap-3">
        <TabsList className="h-auto max-w-full flex-wrap justify-start">
          <TabsTrigger value="overview" className="flex-none px-3">{t("common.overview")}</TabsTrigger>
          <TabsTrigger value="details" className="flex-none px-3">{t("common.details")}</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" keepMounted>
          <div className="grid min-w-0 items-start gap-4 lg:grid-cols-2">
            <DirectoryPanel title={t("pages.users.spendUsd")}>
              <p className="text-2xl font-semibold tabular-nums [overflow-wrap:anywhere]">{"$" + formatNumberWithCommas(userData.spend || 0, 2)}</p>
              <p className="mt-1 text-sm text-muted-foreground">
                {userData.max_budget == null ? budgetLabel : t("pages.users.spendOf", { limit: budgetLabel })}
              </p>
            </DirectoryPanel>
            <DirectoryPanel title={t("pages.users.personalModels")}>{personalModels}</DirectoryPanel>

            <div className="min-w-0 lg:col-span-2">
              <DirectoryPanel title={t("common.teams")} actions={isProxyAdmin && (
                <Button variant="outline" size="sm" onClick={handleOpenAddTeamModal}>
                  <Plus />{t("pages.users.addTeam")}
                </Button>
              )}>
                {teamDetails.length ? (
                  <ul className="divide-y overflow-hidden rounded-lg border">
                    {teamDetails.slice(0, isTeamsExpanded ? teamDetails.length : 20).map((team) => (
                      <li key={team.team_id} className="grid min-w-0 items-center gap-2 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:gap-4">
                        <div className="min-w-0">
                          <BadgeLink href={teamDetailHref(team.team_id)} className="max-w-full whitespace-normal [overflow-wrap:anywhere]">
                            {team.team_alias || team.team_id}
                          </BadgeLink>
                          {team.organization_alias && <p className="mt-1 text-xs text-muted-foreground [overflow-wrap:anywhere]">{team.organization_alias}</p>}
                        </div>
                        {isProxyAdmin ? (
                          <div className="flex min-w-0 items-center gap-2">
                            <Select items={memberRoleOptions()} value={isTeamAdminMembership(team.user_role) ? "admin" : "user"}
                              disabled={updatingTeam !== null} onValueChange={(value) => value && void updateTeamRole(team, value)}>
                              <SelectTrigger className="min-w-0" aria-label={t("pages.users.memberRoleForTeam", { team: team.team_alias || team.team_id })}><SelectValue /></SelectTrigger>
                              <SelectContent>{memberRoleOptions().map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}</SelectContent>
                            </Select>
                            <Button variant="ghost" size="icon-sm" className="shrink-0 text-destructive"
                              aria-label={t("Remove from {value0}", { value0: team.team_alias || team.team_id })}
                              onClick={() => handleOpenRemoveTeamModal(team)}><Trash2 /></Button>
                          </div>
                        ) : <span className="text-sm text-muted-foreground">{membershipRoleLabel(team.user_role)}</span>}
                      </li>
                    ))}
                  </ul>
                ) : <p className="text-sm text-muted-foreground">{t("pages.users.noTeams")}</p>}
                {teamDetails.length > 20 && (
                  <Button variant="ghost" size="sm" className="mt-2" onClick={() => setIsTeamsExpanded(!isTeamsExpanded)}>
                    {isTeamsExpanded ? t("Show Less") : t("pages.users.moreCount", { n: teamDetails.length - 20 })}
                  </Button>
                )}
              </DirectoryPanel>
            </div>
          </div>
        </TabsContent>

        <TabsContent value="details" keepMounted>
          <div className="grid min-w-0 items-start gap-4 xl:grid-cols-2">
            <DirectoryPanel title={t("pages.users.userSettings")}>
              <DirectoryFacts fields={[
                { label: t("pages.users.userId"), value: <span className="inline-flex max-w-full items-center gap-1"><code className="min-w-0 [overflow-wrap:anywhere]">{userData.user_id}</code><CopyButton value={userData.user_id} label={copyIdLabel} className="shrink-0" /></span> },
                { label: t("pages.users.email"), value: userData.user_email || t("pages.users.notSet") },
                { label: t("pages.users.alias"), value: userData.user_alias || t("pages.users.notSet") },
                { label: t("pages.users.role"), value: accountRoleLabel(userData.user_role) },
                { label: t("common.created"), value: <DateCell value={userData.created_at} fallback="common.unknown" /> },
                { label: t("pages.users.lastUpdated"), value: <DateCell value={userData.updated_at} fallback="common.unknown" /> },
              ]} />
            </DirectoryPanel>

            <DirectoryPanel title={t("directoryForm.limits")}>
              <DirectoryFacts fields={[
                { label: t("pages.users.maxBudget"), value: userData.max_budget == null ? budgetLabel : "$" + formatNumberWithCommas(userData.max_budget, 4) },
                { label: t("userBudgetReset.label"), value: budgetReset },
                { label: t("Requests per minute Limit (RPM)"), value: userData.rpm_limit ?? t("quotaGuide.rateBlank") },
                { label: t("Tokens per minute Limit (TPM)"), value: userData.tpm_limit ?? t("quotaGuide.rateBlank") },
              ]} />
              {userData.budget_reset_at && (
                <p className="mt-3 text-xs text-muted-foreground">{t("userBudgetReset.next", { date: new Intl.DateTimeFormat("sv-SE", { timeZone: "Asia/Shanghai", dateStyle: "short", timeStyle: "short" }).format(new Date(userData.budget_reset_at)) })}</p>
              )}
            </DirectoryPanel>

            <DirectoryPanel title={t("pages.users.personalModels")}>{personalModels}</DirectoryPanel>
            {userData.metadata && Object.keys(userData.metadata).length > 0 && (
              <HelpDisclosure title={t("pages.users.metadata")}>
                <pre className="max-h-64 overflow-auto text-xs">{JSON.stringify(userData.metadata, null, 2)}</pre>
              </HelpDisclosure>
            )}
          </div>
        </TabsContent>
      </Tabs>
      <SetPasswordModal
        open={isSetPasswordOpen}
        onOpenChange={setIsSetPasswordOpen}
        accessToken={accessToken}
        user={userData ? { user_id: userId, user_email: userData.user_email, user_alias: userData.user_alias } : null}
      />

      {/* Delete Team Member Modal */}
      <DeleteResourceModal
        isOpen={isRemoveTeamModalOpen}
        title={t("pages.users.removeFromTeam")}
        alertMessage={t("pages.users.removeFromTeamAlert")}
        message={t("pages.users.removeFromTeamMessage")}
        resourceInformationTitle={t("pages.users.teamMembership")}
        resourceInformation={[
          { label: t("pages.users.team"), value: teamToRemove?.team_alias || teamToRemove?.team_id },
          { label: t("pages.users.userId"), value: userData?.user_id, code: true },
          { label: t("pages.users.email"), value: userData?.user_email },
        ]}
        onCancel={handleRemoveTeamCancel}
        onOk={handleRemoveTeamConfirm}
        confirmLoading={isRemovingTeam}
      />

      {/* Add to Team Modal */}
      <Dialog
        open={isAddTeamModalOpen}
        onOpenChange={(open) => !open && setIsAddTeamModalOpen(false)}
        disablePointerDismissal={isAddingTeam}
      >
        <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-[500px]">
          <DialogHeader>
            <DialogTitle>{t("pages.users.addUserToTeam")}</DialogTitle>
          </DialogHeader>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              handleAddTeamSubmit();
            }}
          >
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor={ADD_TEAM_FIELD_ID}>{t("pages.users.team")}</FieldLabel>
                <Combobox
                  items={availableTeamsForAdd}
                  value={selectedTeamOption}
                  onValueChange={(team: TeamOption | null) => setSelectedTeamId(team?.team_id ?? "")}
                  itemToStringLabel={(team: TeamOption) => team.team_alias}
                  isItemEqualToValue={(team: TeamOption, value: TeamOption) => team.team_id === value.team_id}
                >
                  <ComboboxInput id={ADD_TEAM_FIELD_ID} placeholder={t("pages.users.selectTeam")} className="w-full" />
                  <ComboboxContent>
                    <ComboboxEmpty>{t("pages.users.noTeamsFound")}</ComboboxEmpty>
                    <ComboboxList>
                      {(team: TeamOption) => (
                        <ComboboxItem key={team.team_id} value={team} title={team.team_alias}>
                          {team.team_alias}
                        </ComboboxItem>
                      )}
                    </ComboboxList>
                  </ComboboxContent>
                </Combobox>
              </Field>

              <Field>
                <FieldLabel htmlFor={ADD_TEAM_ROLE_FIELD_ID}>{t("pages.users.memberRole")}</FieldLabel>
                <Select
                  items={memberRoleOptions()}
                  value={selectedRole}
                  onValueChange={(value) => value !== null && setSelectedRole(value)}
                >
                  <SelectTrigger id={ADD_TEAM_ROLE_FIELD_ID} className="w-full">
                    <SelectValue>
                      {(value) => memberRoleOptions().find((option) => option.value === value)?.label ?? value}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {memberRoleOptions().map((option) => (
                      <SelectItem key={option.value} value={option.value} title={option.label}>
                        <SimpleTooltip content={option.hint}>
                          <span className="font-medium">{option.label}</span>
                          <span className="ml-2 text-muted-foreground text-sm">- {option.hint}</span>
                        </SimpleTooltip>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </FieldGroup>

            <div className="text-right mt-4">
              <Button type="submit" disabled={isAddingTeam || !selectedTeamId} aria-busy={isAddingTeam}>
                {isAddingTeam ? t("pages.users.adding") : t("pages.users.addToTeam")}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
