import useAuthorized from "@/app/(dashboard)/hooks/useAuthorized";
import { useTeams } from "@/app/(dashboard)/hooks/teams/useTeams";
import { organizationKeys, useOrganization } from "@/app/(dashboard)/hooks/organizations/useOrganizations";
import { useQueryClient } from "@tanstack/react-query";
import { useVisitedTabs } from "@/hooks/useVisitedTabs";
import { MoneyCell } from "@/components/shared/table_cells";
import { Button } from "@/components/ui/button";
import LanguageSwitcher from "@/components/LanguageSwitcher";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatNumberWithCommas } from "@/utils/dataUtils";
import { teamDetailHref } from "@/utils/entityLinks";
import { createTeamAliasMap } from "@/utils/teamUtils";
import { BadgeLink } from "@/components/shared/BadgeLink";
import { DateCell } from "@/components/shared/table_cells";
import { Pencil } from "lucide-react";
import { DirectoryDetailHeader, DirectoryEditDialog, DirectoryFacts, DirectoryPanel } from "@/components/shared/DirectoryDetail";
import React, { useMemo, useState } from "react";
import MemberTable, { type MemberTableColumn } from "../common_components/MemberTable";
import UserSearchModal from "../common_components/user_search_modal";
import { toast } from "@/lib/toast";
import {
  Member,
  organizationMemberAddCall,
  organizationMemberDeleteCall,
} from "../networking";
import { OrgSettingsForm, organizationBudgetLabel } from "./org-settings/OrgSettingsForm";
import { useT } from "@/i18n";

interface OrganizationInfoProps {
  organizationId: string;
  onClose: () => void;
  accessToken: string | null;
  is_org_admin: boolean;
  is_proxy_admin: boolean;
  userModels: string[];
  editOrg: boolean;
  editorOnly?: boolean;
}

/** 组织详情视图：接收组织 ID、身份权限与关闭回调，返回团队、管理员和设置页。
 * 模型、预算周期和速率上限仅平台管理员可编辑；组织关系只允许添加/撤销，已授权字段按表单增量保存；加载失败保留页面错误，无自动写入。 */
const OrganizationInfoView: React.FC<OrganizationInfoProps> = ({
  organizationId,
  onClose,
  accessToken,
  is_org_admin,
  is_proxy_admin,
  userModels,
  editOrg,
  editorOnly = false,
}) => {
  const t = useT();
  const queryClient = useQueryClient();
  const { data: orgData, isLoading: loading } = useOrganization(organizationId);
  const [isEditing, setIsEditing] = useState(editOrg);
  const [isAddMemberModalVisible, setIsAddMemberModalVisible] = useState(false);
  const budgetTable = (orgData?.litellm_budget_table ?? {}) as {
    max_budget?: number | null;
    budget_duration?: string | null;
    tpm_limit?: number | null;
    rpm_limit?: number | null;
    max_parallel_requests?: number | null;
  };
  const maxBudget =
    typeof orgData?.max_budget === "number" || orgData?.max_budget === null
      ? orgData.max_budget
      : (budgetTable.max_budget ?? null);
  const budgetLabel = maxBudget === null || maxBudget === undefined ? t("Unlimited") : `$${formatNumberWithCommas(maxBudget, 4)}`;
  const models = orgData?.models ?? [];
  const showRates = typeof budgetTable.tpm_limit === "number" || typeof budgetTable.rpm_limit === "number";
  const { userId } = useAuthorized();
  const selfAdmin = (orgData?.members ?? []).some(
    (member) => member?.user_id === userId && (member.user_role === "org_admin" || member.role === "org_admin"),
  );
  const canEditOrg = is_proxy_admin || is_org_admin || selfAdmin;
  const { data: teams } = useTeams();
  const { onTabChange, hasVisited } = useVisitedTabs("overview");

  const teamAliasMap = useMemo(() => createTeamAliasMap(teams), [teams]);

  const handleMemberAdd = async (values: any) => {
    try {
      if (accessToken == null) {
        return;
      }

      const member: Member = {
        user_email: values.user_email,
        user_id: values.user_id,
        role: values.role,
      };
      await organizationMemberAddCall(accessToken, organizationId, member);

      toast.success(t("Organization member added successfully"));
      setIsAddMemberModalVisible(false);
      queryClient.invalidateQueries({ queryKey: organizationKeys.all });
    } catch (error) {
      toast.fromError(t("Failed to add organization member"));
      console.error("Error adding organization member:", error);
    }
  };

  const handleMemberDelete = async (values: any) => {
    try {
      if (!accessToken) return;

      await organizationMemberDeleteCall(accessToken, organizationId, values.user_id);
      toast.success(t("Organization member deleted successfully"));
      queryClient.invalidateQueries({ queryKey: organizationKeys.all });
    } catch (error) {
      toast.fromError(t("Failed to delete organization member"));
      console.error("Error deleting organization member:", error);
    }
  };

  /** 关闭组织编辑；列表入口关闭其独立弹窗，详情入口保留当前页签。 */
  const closeEditor = () => {
    if (editorOnly) onClose();
    else setIsEditing(false);
  };
  const editDialog = (
    <DirectoryEditDialog
      open={editorOnly || isEditing}
      title={t("Organization Settings")}
      headerActions={<LanguageSwitcher />}
      onClose={closeEditor}
    >
      {loading || !orgData ? (
        <p role={loading ? "status" : "alert"} className="text-sm text-muted-foreground">
          {t(loading ? "Loading..." : "Organization not found")}
        </p>
      ) : canEditOrg && accessToken ? (
        <OrgSettingsForm canEditModels={is_proxy_admin} organizationId={organizationId} org={orgData}
          accessToken={accessToken} onCancel={closeEditor} onSaved={closeEditor} />
      ) : <p role="alert" className="text-sm text-muted-foreground">{t("directoryForm.editDenied")}</p>}
    </DirectoryEditDialog>
  );

  if (editorOnly) return editDialog;
  if (loading || !orgData) {
    return <p role={loading ? "status" : "alert"} className="text-sm text-muted-foreground">
      {t(loading ? "Loading..." : "Organization not found")}
    </p>;
  }

  const orgMemberById = new Map((orgData.members || []).map((m) => [m.user_id, m]));
  const orgMemberFor = (record: Member) => (record.user_id != null ? orgMemberById.get(record.user_id) : undefined);

  const orgExtraColumns: MemberTableColumn[] = [
    {
      title: t("Spend (USD)"),
      key: "spend",
      sortValue: (record: Member) => orgMemberFor(record)?.spend ?? null,
      render: (record: Member) => <MoneyCell value={orgMemberFor(record)?.spend} decimals={4} />,
    },
    {
      title: t("Created At"),
      key: "created_at",
      sortValue: (record: Member) => orgMemberFor(record)?.created_at ?? null,
      render: (record: Member) => {
        const createdAt = orgMemberFor(record)?.created_at;
        return <DateCell value={createdAt} />;
      },
    },
  ];

  const modelsList = models.length > 0 ? (
    <div className="flex min-w-0 flex-wrap gap-2">
      {models.map((model) => <BadgeLink key={model} className="max-w-full whitespace-normal [overflow-wrap:anywhere]">{model}</BadgeLink>)}
    </div>
  ) : <p className="text-sm text-muted-foreground">{t("organizationConfig.modelScopeInherited")}</p>;

  return (
    <div className="mx-auto w-full min-w-0 max-w-[1600px] space-y-4">
      <DirectoryDetailHeader title={orgData.organization_alias} id={orgData.organization_id}
        backLabel={t("Back to Organizations")} copyLabel={t("Copy organization ID")} onBack={onClose}
        actions={canEditOrg && <Button size="sm" onClick={() => setIsEditing(true)}><Pencil />{t("Edit Settings")}</Button>} />
      {isEditing && editDialog}

      <Tabs defaultValue="overview" onValueChange={onTabChange} className="min-w-0 gap-3">
        <TabsList className="h-auto max-w-full flex-wrap justify-start">
          <TabsTrigger value="overview" className="flex-none px-3">{t("Overview")}</TabsTrigger>
          <TabsTrigger value="members" className="flex-none px-3">{t("Organization administrators")}</TabsTrigger>
          <TabsTrigger value="settings" className="flex-none px-3">{t("Settings")}</TabsTrigger>
        </TabsList>

        <TabsContent keepMounted={hasVisited("overview")} value="overview">
          <div className="grid min-w-0 items-start gap-4 lg:grid-cols-2">
            <DirectoryPanel title={t("Budget Status")}>
              <p className="text-2xl font-semibold tabular-nums [overflow-wrap:anywhere]">{"$" + formatNumberWithCommas(orgData.spend || 0, 4)}</p>
              <p className="mt-1 text-sm text-muted-foreground">{t("Budget cap: {value0}", { value0: budgetLabel })}</p>
              {budgetTable.budget_duration && <p className="mt-1 text-xs text-muted-foreground">
                {t("Reset: {value0}", { value0: organizationBudgetLabel(budgetTable.budget_duration) })}
              </p>}
            </DirectoryPanel>
            <DirectoryPanel title={t("Organization Details")}>
              <DirectoryFacts fields={[
                { label: t("common.created"), value: <DateCell value={orgData.created_at} precision="date" fallback="common.unknown" /> },
                { label: t("common.updated"), value: <DateCell value={orgData.updated_at} precision="date" fallback="common.unknown" /> },
              ]} />
              {orgData.created_by && <p className="mt-3 text-xs text-muted-foreground [overflow-wrap:anywhere]">
                {t("Created By: {value0}", { value0: orgData.created_by })}
              </p>}
            </DirectoryPanel>
            <DirectoryPanel title={t("Models")}>{modelsList}</DirectoryPanel>
            <DirectoryPanel title={t("Teams")}>
              {orgData.teams?.length ? (
                <div className="flex min-w-0 flex-wrap gap-2">
                  {orgData.teams.map((team) => (
                    <BadgeLink key={team.team_id} href={teamDetailHref(team.team_id)} className="max-w-full whitespace-normal [overflow-wrap:anywhere]">
                      {teamAliasMap[team.team_id] || team.team_id}
                    </BadgeLink>
                  ))}
                </div>
              ) : <p className="text-sm text-muted-foreground">{t("None")}</p>}
            </DirectoryPanel>
            {showRates && <DirectoryPanel title={t("Rate Limits")}>
              <DirectoryFacts fields={[
                { label: t("Tokens per minute Limit (TPM)"), value: budgetTable.tpm_limit ?? t("Unlimited") },
                { label: t("Requests per minute Limit (RPM)"), value: budgetTable.rpm_limit ?? t("Unlimited") },
              ]} />
              {budgetTable.max_parallel_requests != null && <p className="mt-2 text-xs text-muted-foreground">
                {t("Max Parallel Requests: {value0}", { value0: budgetTable.max_parallel_requests })}
              </p>}
            </DirectoryPanel>}
          </div>
        </TabsContent>

        <TabsContent keepMounted={hasVisited("members")} value="members">
          <div className="space-y-4">
            <MemberTable
              key={orgData.organization_id}
              members={(orgData.members || []).map((m) => ({
                role: m.user_role || "",
                user_id: m.user_id,
                user_email: m.user_email,
                user_alias: m.user?.user_alias ?? null,
              }))}
              canEdit={canEditOrg}
              onDelete={(member) => handleMemberDelete(member)}
              onAddMember={() => setIsAddMemberModalVisible(true)}
              addLabel={t("Add organization administrator")}
              roleColumnTitle={t("Organization administrators")}
              extraColumns={orgExtraColumns}
              emptyText={t("No organization administrators")}
            />
          </div>
        </TabsContent>

        <TabsContent keepMounted={hasVisited("settings")} value="settings">
          <div className="grid min-w-0 items-start gap-4 lg:grid-cols-2">
            <DirectoryPanel title={t("Organization Settings")}>
              <DirectoryFacts fields={[
                { label: t("Organization Name"), value: orgData.organization_alias },
                { label: t("Organization ID"), value: <code>{orgData.organization_id}</code> },
                { label: t("Created At"), value: <DateCell value={orgData.created_at} /> },
                { label: t("Budget"), value: budgetLabel },
                { label: t("Reset Budget"), value: organizationBudgetLabel(budgetTable.budget_duration) },
              ]} />
            </DirectoryPanel>
            <DirectoryPanel title={t("Models")}>{modelsList}</DirectoryPanel>
            {showRates && <DirectoryPanel title={t("Rate Limits")}>
              <DirectoryFacts fields={[
                { label: t("Tokens per minute Limit (TPM)"), value: budgetTable.tpm_limit ?? t("Unlimited") },
                { label: t("Requests per minute Limit (RPM)"), value: budgetTable.rpm_limit ?? t("Unlimited") },
              ]} />
            </DirectoryPanel>}
          </div>
        </TabsContent>
      </Tabs>

      <UserSearchModal
        isVisible={isAddMemberModalVisible}
        onCancel={() => setIsAddMemberModalVisible(false)}
        onSubmit={handleMemberAdd}
        accessToken={accessToken}
        title={t("Add organization administrator")}
        roles={[
          {
            label: t("Org Admin"),
            value: "org_admin",
            description: t("An organization administrator runs this organization's teams. This is not an account role."),
          },
        ]}
        defaultRole="org_admin"
      />

    </div>
  );
};

export default OrganizationInfoView;
