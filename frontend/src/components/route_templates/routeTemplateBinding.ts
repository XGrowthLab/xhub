import { keyListCall, organizationListCall, teamListCall, type EffectiveRouteTemplate } from "@/components/networking";
import { t } from "@/i18n";
import type { ScopeKind } from "./routeTemplatePayload";

type BindingTarget = { id: string; label: string };
type BindingTargets = { rows: BindingTarget[]; pages: number };

/** 将现有可见资源列表转换为模板应用选择项。token、scope、search、page 对应登录会话、范围、搜索和页码。
 * 密钥按服务端分页，只用记录 ID、名称和遮罩前缀。组织和团队列表在本地过滤。读取失败交给选择器展示。 */
export async function loadBindingTargets(
  token: string,
  scope: ScopeKind,
  search: string,
  page: number,
): Promise<BindingTargets> {
  if (scope === "key") {
    const result = await keyListCall(token, null, null, search || null, null, null, page, 30);
    const keys: { token_id?: string; key_alias?: string; key_prefix?: string }[] = result.keys;
    return {
      rows: keys.flatMap(({ token_id, key_alias, key_prefix }) => {
        if (!token_id) return [];
        return [{ id: token_id, label: [key_alias, key_prefix].filter(Boolean).join(" · ") || token_id }];
      }),
      pages: result.total_pages,
    };
  }

  const rows: { organization_id?: string; organization_alias?: string; team_id?: string; team_alias?: string }[] =
    scope === "team" ? await teamListCall(token, null) : await organizationListCall(token);
  const normalized = search.toLocaleLowerCase();
  return {
    rows: rows.flatMap((row) => {
      const id = scope === "team" ? row.team_id : row.organization_id;
      const name = scope === "team" ? row.team_alias : row.organization_alias;
      if (!id || ![id, name].some((value) => value?.toLocaleLowerCase().includes(normalized))) return [];
      return [{ id, label: name || id }];
    }),
    pages: 1,
  };
}

/** 为模板绑定控件返回范围名称。scope 来自受支持的范围，调用方在语言切换时重新渲染，无副作用。 */
export function bindingScopeLabel(scope: ScopeKind): string {
  if (scope === "organization") return t("pages.routeTemplates.scopeOrganization");
  if (scope === "team") return t("pages.routeTemplates.scopeTeam");
  return t("pages.routeTemplates.scopeKey");
}

/** 为绑定确认和范围设置展示后台解析的生效模板。缺失结果显示未知，内置模板兼容两种来源标记。 */
export function effectiveBindingLabel(effective: EffectiveRouteTemplate | null | undefined): string {
  if (!effective) return t("pages.routeTemplates.binding.unknown");
  const scope = effective.scope_type;
  if (scope === "builtin" || scope === "platform") return t("pages.routeTemplates.binding.platform");
  return t("pages.routeTemplates.binding.fromScope", {
    name: effective.name || t("pages.routeTemplates.unknownTemplate"),
    scope: bindingScopeLabel(scope),
  });
}
