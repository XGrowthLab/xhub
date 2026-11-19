"use client";

import { ColumnDef } from "@tanstack/react-table";
import { LayersIcon } from "lucide-react";

import { ProjectResponse } from "@/app/(dashboard)/hooks/projects/useProjects";
import { DataTableSortHeader } from "@/components/shared/DataTable";
import { CellTooltip, DateCell, IdentityCell, StatusBadge } from "@/components/shared/table_cells";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { useT, t } from "@/i18n";

function ProjectTeamCell({
  project,
  teamAliasMap,
  isTeamsLoading,
}: {
  project: ProjectResponse;
  teamAliasMap: Map<string, string>;
  isTeamsLoading: boolean;
}) {
  const t = useT();
  if (!project.team_id) return <span className="text-sm">—</span>;
  const alias = teamAliasMap.get(project.team_id);
  if (alias) {
    return (
      <span className="block max-w-60 truncate text-sm" title={alias}>
        {alias}
      </span>
    );
  }
  if (isTeamsLoading) return <Skeleton className="h-3.5 w-24" />;
  return (
    <span className="block max-w-60 truncate font-mono text-xs" title={project.team_id}>
      {project.team_id}
    </span>
  );
}

function ProjectModelsCell({ project }: { project: ProjectResponse }) {
  const t = useT();
  const models = project.models ?? [];
  return (
    <CellTooltip
      content={models.length > 0 ? models.join(", ") : t("No models")}
      trigger={
        <Badge variant="outline" className="cursor-default gap-1.5 font-normal">
          <LayersIcon className="size-3.5" />
          {models.length}
        </Badge>
      }
    />
  );
}

interface ProjectsTableColumnsDeps {
  onProjectClick: (projectId: string) => void;
  teamAliasMap: Map<string, string>;
  isTeamsLoading: boolean;
}

/** 为项目列表生成列，用项目名称和可选 ID 打开详情；只有点击名称时才执行调用方传入的导航回调。 */
export const getProjectsTableColumns = ({
  onProjectClick,
  teamAliasMap,
  isTeamsLoading,
}: ProjectsTableColumnsDeps): ColumnDef<ProjectResponse>[] => [
  {
    id: "project_alias",
    accessorFn: (row) => row.project_alias || row.project_id,
    meta: { title: t("Name") },
    header: ({ column }) => <DataTableSortHeader column={column} title={t("Name")} />,
    size: 216,
    cell: ({ row }) => (
      <IdentityCell
        title={row.original.project_alias || row.original.project_id}
        ariaLabel={row.original.project_alias || row.original.project_id}
        subtitle={row.original.project_alias && row.original.project_alias !== row.original.project_id ? (
          <span className="block max-w-[16ch] truncate" title={row.original.project_id}>{row.original.project_id}</span>
        ) : undefined}
        className="max-w-56"
        onClick={() => onProjectClick(row.original.project_id)}
      />
    ),
  },
  {
    id: "team",
    accessorFn: (row) => teamAliasMap.get(row.team_id ?? "") ?? "",
    meta: { title: t("Team") },
    header: ({ column }) => <DataTableSortHeader column={column} title={t("Team")} />,
    size: 180,
    enableSorting: true,
    cell: ({ row }) => (
      <ProjectTeamCell project={row.original} teamAliasMap={teamAliasMap} isTeamsLoading={isTeamsLoading} />
    ),
  },
  {
    id: "models",
    meta: { title: t("Models"), skeleton: "badge" },
    header: t("Models"),
    size: 110,
    enableSorting: false,
    cell: ({ row }) => <ProjectModelsCell project={row.original} />,
  },
  {
    id: "status",
    accessorKey: "blocked",
    meta: { title: t("Status"), skeleton: "badge" },
    header: t("Status"),
    size: 110,
    enableSorting: false,
    cell: ({ row }) => (
      <StatusBadge
        tone={row.original.blocked ? "error" : "success"}
        label={row.original.blocked ? t("Blocked") : t("Active")}
      />
    ),
  },
  {
    id: "created_at",
    accessorKey: "created_at",
    sortingFn: "datetime",
    meta: { title: t("Created") },
    header: ({ column }) => <DataTableSortHeader column={column} title={t("Created")} />,
    size: 140,
    enableSorting: true,
    cell: ({ row }) => <DateCell value={row.original.created_at} precision="date" />,
  },
  {
    id: "updated_at",
    accessorKey: "updated_at",
    meta: { title: t("Updated") },
    header: t("Updated"),
    size: 140,
    enableSorting: false,
    cell: ({ row }) => <DateCell value={row.original.updated_at} precision="date" />,
  },
];
