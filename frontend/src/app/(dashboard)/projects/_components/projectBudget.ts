import type { ProjectResponse } from "@/app/(dashboard)/hooks/projects/useProjects";

/** 项目详情与编辑共用的预算读值；参数可为空，优先采用新接口（包括零和 null），旧记录缺少该字段时才读取预算表。 */
export function projectBudgetLimit(project: ProjectResponse | undefined | null): number | null {
  if (project?.max_budget !== undefined) return project.max_budget;
  return project?.litellm_budget_table?.max_budget ?? null;
}
