import { describe, expect, it } from "vitest";
import { existsSync } from "node:fs";
import { join } from "node:path";

import { dashboardAppPath } from "@/middleware";

const dashboardDir = __dirname;

describe("dashboard pages", () => {
  /** 页面被下线或由兼容跳转取代；前置当前源码树，不重新暴露旧表单或重复实现，无数据清理。 */
  it.each([
    "agents",
    "memory",
    "workflows",
    "tool-policies",
    "policies",
    "vector-stores",
    "budgets",
    "transform-request",
    "ui-theme",
    "api-reference",
    "model-hub-table",
    "access-groups",
    "mcp-servers",
    "skills",
    "cost-tracking",
    "logging-and-alerts",
    "old-usage",
    "retired",
    "tag-management",
  ])("does not ship the former %s page", (name) => {
    expect(existsSync(join(dashboardDir, name, "page.tsx")), name).toBe(false);
  });

  /** 退役工作区连同独立页面文件下架；前置当前路由源码树，仅检查文件，无业务数据。 */
  it.each([
    "chat",
    "chat/api-keys",
    "chat/credentials",
    "chat/integrations",
    "chat/logs",
    "chat/usage",
    "connect",
    "model_hub",
    "model_hub_table",
  ])("does not ship the former standalone %s page", (name) => {
    const filename = join(dashboardDir, "..", name, "page.tsx");
    expect(existsSync(filename), filename).toBe(false);
  });

  /** 保留仍可用的页面和说明；真实路径分流不因菜单隐藏而丢失，无数据清理。 */
  it.each(["/logs", "/models-and-endpoints", "/admin-panel", "/search-tools", "/prompts", "/caching"])(
    "still renders %s and its /ui alias",
    (path) => {
      expect(dashboardAppPath(path)).toBe(path);
      expect(dashboardAppPath(`/ui${path}`)).toBe(path);
    },
  );
});
