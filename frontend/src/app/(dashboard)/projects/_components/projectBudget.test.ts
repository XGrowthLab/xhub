import { expect, it } from "vitest";
import type { ProjectResponse } from "@/app/(dashboard)/hooks/projects/useProjects";
import { projectBudgetLimit } from "./projectBudget";

/** 验证当前及历史格式的项目预算，显式解除和零预算优先于旧值；纯函数测试无需清理。 */
it.each([
  [{ max_budget: 0, litellm_budget_table: { max_budget: 70 } }, 0],
  [{ max_budget: null, litellm_budget_table: { max_budget: 70 } }, null],
  [{ max_budget: 50 }, 50],
  [{ litellm_budget_table: { max_budget: 0 } }, 0],
  [{ litellm_budget_table: { max_budget: 70 } }, 70],
  [undefined, null],
] as const)("reads a persisted budget without treating zero as unlimited %#", (project, expected) => {
  expect(projectBudgetLimit(project as ProjectResponse | undefined)).toBe(expected);
});
