import { getActiveLocale, t } from "@/i18n";
import type { StatusTone } from "@/components/shared/table_cells";
import type { AuditLogEntry } from "./AuditLogsTableColumns";

export const AUDIT_RESOURCE_TYPES: Record<string, string> = {
  directory_connection: "directory_connection",
  directory_group: "directory_group",
  directory_mapping: "directory_mapping",
  enterprise_oidc: "enterprise_oidc",
  budget_reservation: "budget_reservation",
  archive: "archive",
  key: "key",
  api_keys: "key",
  LiteLLM_VerificationToken: "key",
  team: "team",
  teams: "team",
  LiteLLM_TeamTable: "team",
  user: "user",
  LiteLLM_UserTable: "user",
  organization: "organization",
  organizations: "organization",
  LiteLLM_OrganizationTable: "organization",
  model: "model",
  LiteLLM_ProxyModelTable: "model",
  project: "project",
  team_member: "team_member",
  route_template: "route_template",
  request_log: "request_log",
  log: "request_log",
};
const SPECIAL_ACTIONS: Record<string, string> = {
  "directory.user.sync": "syncUser",
  "directory.group.sync": "syncGroup",
  "directory.configure": "configureDirectory",
  "directory.mapping.set": "mapTeam",
  "directory.team.assign": "assignTeam",
  "directory.assignment.override": "overrideTeam",
  "enterprise.oidc.login": "ssoLogin",
  "enterprise.oidc.configure": "configureSSO",
  "enterprise.saml.refresh.success": "samlRefreshed",
  "enterprise.saml.refresh.failed": "samlRefreshFailed",
  "enterprise.saml.refresh.configure": "samlRefresh",
  "archive.configure": "configureArchive",
  "archive.retry": "retryArchive",
  "org.budget_reset": "resetBudget",
  "user.budget_reset": "resetUserBudget",
  "user.bulk_budget": "bulkBudget",
  "budget.reconcile": "reconcile",
  "log.read": "readLog",
  "log.read_content": "readLogContent",
  "route_template.bind": "bindTemplate",
  "key.status": "keyStatus",
  "key.revoke_team": "revokeTeamKeys",
  "key.reset_spend": "resetSpend",
  "team.move": "moveTeam",
  "user.password": "changePassword",
  "member.add": "addMember",
  "member.role": "changeMemberRole",
  "member.remove": "removeMember",
  "org.admin_add": "addAdmin",
  "org.admin_remove": "removeAdmin",
  "org.member_set": "setMembers",
  bootstrap: "bootstrap",
  "admin.seed": "seedAdmin",
};
/** 根据事件的对象类型查找中英文标签；未知类型沿用通用对象标签，技术详情保留原值。 */
export function auditResourceType(log: Pick<AuditLogEntry, "table_name">): string {
  return t("audit.resources." + (AUDIT_RESOURCE_TYPES[log.table_name] ?? "unknown"));
}
/** 审计列表与详情用同一动作标签：优先具体动作，再按 CRUD 后缀和对象类型生成本地化说明。 */
export function auditAction(log: AuditLogEntry): string {
  const special = SPECIAL_ACTIONS[log.action];
  if (special) return t("audit.actions." + special);
  const suffix = log.action.split(".").at(-1) ?? "";
  const verb = (
    {
      create: "created",
      update: "updated",
      profile: "updated",
      admin_update: "updated",
      delete: "deleted",
      rotate: "rotated",
      created: "created",
      updated: "updated",
      deleted: "deleted",
      rotated: "rotated",
    } as Record<string, string>
  )[suffix];
  return verb ? t("audit.actions." + verb, { resource: auditResourceType(log) }) : t("audit.actions.unknown");
}
/** 根据审计动作返回统一状态色；失败和删除用危险色，创建用成功色，未知事件保留信息色。 */
export function auditTone(log: AuditLogEntry): StatusTone {
  const suffix = log.action.split(".").at(-1) ?? "";
  if (["create", "created", "add", "admin_add"].includes(suffix)) return "success";
  if (["delete", "deleted", "failed", "remove", "admin_remove", "revoke_team"].includes(suffix)) return "error";
  if (["rotate", "rotated", "password", "status"].includes(suffix)) return "warning";
  return "info";
}
export function shortAuditID(value: string): string {
  return value.length > 12 ? value.slice(0, 8) + "…" + value.slice(-4) : value;
}
/** 从接口或事件快照读取对象名称；未留名的历史记录展示对象类型，完整标识仍可在详情查看。 */
export function auditResourceName(log: AuditLogEntry): string {
  if (log.object_name) return log.object_name;
  if (typeof log.detail?.object_name === "string" && log.detail.object_name) return log.detail.object_name;
  return auditResourceType(log);
}
export function auditOperator(log: AuditLogEntry): string {
  if (log.actor_kind === "master") return t("audit.master");
  if (log.actor_kind === "system") return t("audit.system");
  if (log.actor_name || log.actor_email) return log.actor_name || log.actor_email || "";
  if (log.changed_by === "default_user_id") return t("audit.defaultAdmin");
  if (log.actor_kind === "key") return t("audit.keyOperator");
  return log.changed_by ? t("audit.unidentifiedUser") : t("audit.unidentifiedOperator");
}
export function auditDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(getActiveLocale(), {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}
