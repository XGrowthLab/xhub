import { organizationKeys, useOrganizations } from "@/app/(dashboard)/hooks/organizations/useOrganizations";
import { useUserModels } from "@/app/(dashboard)/hooks/models/useModels";
import OrganizationFilters, { FilterState } from "@/app/(dashboard)/organizations/OrganizationFilters";
import { useQueryClient } from "@tanstack/react-query";
import { parseAsString, useQueryState } from "nuqs";
import React, { useState } from "react";
import DeleteResourceModal from "@/components/common_components/DeleteResourceModal";
import { toast } from "@/lib/toast";
import { organizationDeleteCall } from "@/components/networking";
import { OrgCreateDialog } from "@/components/organization/org-create/OrgCreateDialog";
import OrganizationInfoView from "@/components/organization/organization_view";
import { Button } from "@/components/ui/button";
import { QuotaGuide } from "@/components/shared/QuotaGuide";
import { useT } from "@/i18n";

import { Building2, Plus } from "lucide-react";
import { PageHeader } from "@/components/shared/PageHeader";

import OrganizationsTable from "./OrganizationsTable";

interface OrganizationsPanelProps {
  userRole: string;
  accessToken: string | null;
  premiumUser: boolean;
}

/** 按现有角色展示组织列表或查询参数指定的详情；操作沿用后台接口，切换详情保留筛选条件。 */
const OrganizationsPanel: React.FC<OrganizationsPanelProps> = ({ userRole, accessToken, premiumUser }) => {
  const t = useT();
  const [selectedOrgId, setSelectedOrgId] = useQueryState("org", parseAsString.withOptions({ history: "push" }));
  const [editingOrgId, setEditingOrgId] = useState<string | null>(null);
  const [isDeleteModalOpen, setIsDeleteModalOpen] = useState(false);
  const [orgToDelete, setOrgToDelete] = useState<string | null>(null);
  const [isDeleting, setIsDeleting] = useState(false);
  const [isOrgModalVisible, setIsOrgModalVisible] = useState(false);
  const [showFilters, setShowFilters] = useState(false);
  const [filters, setFilters] = useState<FilterState>({ org_id: "", org_alias: "" });

  const queryClient = useQueryClient();
  const { data: organizations = [], isLoading } = useOrganizations({
    org_id: filters.org_id,
    org_alias: filters.org_alias,
  });
  const { data: userModels = [] } = useUserModels();

  const searchActive = Boolean(filters.org_id || filters.org_alias);

  const refetchOrganizations = () => queryClient.invalidateQueries({ queryKey: organizationKeys.lists() });

  const handleFilterChange = (key: keyof FilterState, value: string) => {
    setFilters((previousFilters) => ({ ...previousFilters, [key]: value }));
  };

  const handleFilterReset = () => {
    setFilters({ org_id: "", org_alias: "" });
  };

  const handleDelete = (orgId: string | null) => {
    if (!orgId) return;

    setOrgToDelete(orgId);
    setIsDeleteModalOpen(true);
  };

  const confirmDelete = async () => {
    if (!orgToDelete || !accessToken) return;

    try {
      setIsDeleting(true);
      const result = await organizationDeleteCall(accessToken, orgToDelete);
      const removed = result && typeof result === "object" && "deleted" in result ? Number(result.deleted) : 1;
      if (removed < 1) {
        throw new Error(t("Organization could not be deleted"));
      }
      toast.success(t("Organization deleted successfully"));

      setIsDeleteModalOpen(false);
      setOrgToDelete(null);
      await refetchOrganizations();
    } catch (error) {
      toast.fromError(error);
    } finally {
      setIsDeleting(false);
    }
  };

  const cancelDelete = () => {
    setIsDeleteModalOpen(false);
    setOrgToDelete(null);
  };

  if (!premiumUser) {
    return (
      <div className="mx-4 mt-4">
        <p className="text-sm text-muted-foreground">
          {t("pages.organizations.enterpriseHint")}{" "}
          
            {t("pages.organizations.here")}
          
          .
        </p>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      {selectedOrgId ? (
        <OrganizationInfoView
          organizationId={selectedOrgId}
          onClose={() => void setSelectedOrgId(null)}
          accessToken={accessToken}
          is_org_admin={false}
          is_proxy_admin={userRole === "Admin"}
          userModels={userModels}
          editOrg={false}
        />
      ) : (
        <>
          <PageHeader
            title={t("nav.organizations")}
            subtitle={t("pages.organizations.subtitle")}
            icon={<Building2 />}
            primaryAction={(userRole === "Admin" || userRole === "Org Admin") ? (
              <Button onClick={() => setIsOrgModalVisible(true)}><Plus />{t("pages.organizations.create")}</Button>
            ) : undefined}
          />
          <QuotaGuide scope="organization" />
          <OrganizationFilters
            filters={filters}
            showFilters={showFilters}
            onToggleFilters={setShowFilters}
            onChange={handleFilterChange}
            onReset={handleFilterReset}
          />
          <OrganizationsTable
            organizations={organizations}
            isLoading={isLoading}
            userRole={userRole}
            searchActive={searchActive}
            onOrganizationClick={(organizationId) => void setSelectedOrgId(organizationId)}
            onEditClick={setEditingOrgId}
            onDeleteClick={handleDelete}
          />
        </>
      )}

      {editingOrgId && accessToken && (
        <OrganizationInfoView key={editingOrgId} organizationId={editingOrgId} accessToken={accessToken}
          onClose={() => setEditingOrgId(null)} is_org_admin={false} is_proxy_admin={userRole === "Admin"}
          userModels={userModels} editOrg editorOnly />
      )}

      <OrgCreateDialog open={isOrgModalVisible} onOpenChange={setIsOrgModalVisible} accessToken={accessToken || ""} />

      <DeleteResourceModal
        isOpen={isDeleteModalOpen}
        title={t("pages.organizations.deleteTitle")}
        message={t("pages.organizations.deleteMessage")}
        resourceInformationTitle={t("pages.organizations.infoTitle")}
        resourceInformation={[{ label: t("pages.organizations.orgId"), value: orgToDelete, code: true }]}
        onCancel={cancelDelete}
        onOk={confirmDelete}
        confirmLoading={isDeleting}
      />
    </div>
  );
};

export default OrganizationsPanel;
