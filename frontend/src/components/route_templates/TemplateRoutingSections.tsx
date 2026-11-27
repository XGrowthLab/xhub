"use client";
import { useT } from "@/i18n";
import { useMemo, useState } from "react";
import DeploymentIdentity from "./DeploymentIdentity";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import CopyButton from "@/components/shared/CopyButton";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import {
  groupFormError,
  groupHints,
  groupStrategies,
  reconcileGroup,
  type GroupConfig,
  type CatalogModel,
} from "@/components/routing_groups/groupForm";
import { fallbackFields, type FallbackField, validateSections } from "./templateSections";
import type { TemplateFormState, SplitDeployment } from "./templateForm";
const categories: Record<FallbackField, string> = {
  fallbacks: "pages.routeTemplates.editor.general",
  context_window_fallbacks: "pages.routeTemplates.editor.context",
  content_policy_fallbacks: "pages.routeTemplates.editor.contentPolicy",
};
type Props = { form: TemplateFormState; deployments: SplitDeployment[]; onChange: (form: TemplateFormState) => void };
/** TemplateGroups 编辑模板内组数组；参数为草稿、目录和更新回调，返回列表与完整组弹窗。
 * 弹窗确认仅更新本地草稿；成员占用范围限于当前模板，权重引用真实部署，无独立接口写入。 */
export function TemplateGroups({ form, deployments, onChange }: Props) {
  const t = useT();
  const groups = form.routing_groups ?? [];
  const [draft, setDraft] = useState<GroupConfig | null>(null),
    [editing, setEditing] = useState<number | null>(null);
  const [search, setSearch] = useState(""),
    [memberSearch, setMemberSearch] = useState(""),
    [expanded, setExpanded] = useState("");
  const catalog = useMemo<CatalogModel[]>(
    () =>
      [...new Set(deployments.map((d) => d.model_name))].map((name) => ({
        model_name: name,
        deployments: deployments
          .filter((d) => d.model_name === name)
          .map((d) => ({ id: d.deployment_id!, litellm_params: { model: d.model } })),
      })),
    [deployments],
  );
  const error = draft
    ? groupFormError(
        draft,
        groups.filter((_, i) => i !== editing),
        catalog,
      )
    : "";
  /** open 创建或复制组弹窗草稿；参数为索引，返回空，仅设置本地状态。 */
  const open = (index: number | null) => {
    setEditing(index);
    setMemberSearch("");
    setDraft(
      index === null
        ? { group_name: "", models: [], routing_strategy: "simple-shuffle" }
        : structuredClone(groups[index]),
    );
  };
  /** confirm 将合法弹窗值替换到模板数组；无参数和返回，不调用后台，保存模板时统一提交。 */
  const confirm = () => {
    if (!draft || error) return;
    onChange({
      ...form,
      routing_groups: editing === null ? [...groups, draft] : groups.map((g, i) => (i === editing ? draft : g)),
    });
    setDraft(null);
  };
  return (
    <section className="space-y-5" aria-label={t("pages.routeTemplates.editor.templateGroups")}>
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h3 className="text-lg font-semibold">{t("pages.routeTemplates.editor.groups")}</h3>
          <p className="mt-1 text-sm text-muted-foreground">{t("pages.routeTemplates.editor.groupHint")}</p>
        </div>
        <Button onClick={() => open(null)}>{t("pages.routeTemplates.editor.createGroup")}</Button>
      </div>
      <Input
        aria-label={t("pages.routeTemplates.editor.searchGroups")}
        placeholder={t("pages.routeTemplates.editor.searchGroupPlaceholder")}
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="max-w-md"
      />
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full text-left text-sm">
          <thead className="bg-muted/30 text-muted-foreground">
            <tr>
              <th className="p-4">{t("pages.routeTemplates.editor.groupName")}</th>
              <th className="p-4">{t("pages.routeTemplates.editor.members")}</th>
              <th className="p-4">{t("pages.routeTemplates.editor.strategy")}</th>
              <th className="p-4 text-right">{t("pages.routeTemplates.editor.actions")}</th>
            </tr>
          </thead>
          <tbody>
            {groups
              .map((g, index) => ({ g, index }))
              .filter(({ g }) => (g.group_name + g.models.join(" ")).toLowerCase().includes(search.toLowerCase()))
              .map(({ g, index }) => (
                <GroupRow
                  key={g.group_name}
                  group={g}
                  expanded={expanded === g.group_name}
                  onExpand={() => setExpanded(expanded === g.group_name ? "" : g.group_name)}
                  onEdit={() => open(index)}
                  deleteBlocked={fallbackFields.some((field) =>
                    (form[field] ?? []).some((row) =>
                      Object.entries(row).some(
                        ([source, targets]) => source === g.group_name || targets.includes(g.group_name),
                      ),
                    ),
                  )}
                  onDelete={() => onChange({ ...form, routing_groups: groups.filter((_, i) => i !== index) })}
                />
              ))}
            {!groups.length && (
              <tr>
                <td colSpan={4} className="p-12 text-center text-muted-foreground">
                  {t("pages.routeTemplates.editor.emptyGroups")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <Dialog open={!!draft} onOpenChange={(value) => !value && setDraft(null)}>
        <DialogContent className="flex max-h-[90dvh] flex-col overflow-hidden sm:max-w-2xl">
          <DialogHeader className="shrink-0">
            <DialogTitle>
              {editing === null
                ? t("pages.routeTemplates.editor.createGroup")
                : t("pages.routeTemplates.editor.editGroup")}
            </DialogTitle>
            <DialogDescription>{t("pages.routeTemplates.editor.draftHint")}</DialogDescription>
          </DialogHeader>
          {draft && (
            <div className="min-h-0 space-y-5 overflow-y-auto pr-1">
              <label className="block space-y-2">
                <span className="font-medium">{t("pages.routeTemplates.editor.requiredGroupName")}</span>
                <Input
                  aria-label={t("pages.routeTemplates.editor.groupNameLabel")}
                  disabled={editing !== null}
                  value={draft.group_name}
                  onChange={(e) => setDraft({ ...draft, group_name: e.target.value })}
                />
                <p className="text-xs text-muted-foreground">{t("pages.routeTemplates.editor.groupNameHint")}</p>
              </label>
              <section className="space-y-2">
                <h4 className="font-medium">
                  {t("pages.routeTemplates.editor.members")} *{" "}
                  <span className="text-xs text-muted-foreground">
                    {t("pages.routeTemplates.editor.selectedCount", { count: draft.models.length })}
                  </span>
                </h4>
                <Input
                  aria-label={t("pages.routeTemplates.editor.searchMembers")}
                  placeholder={t("pages.routeTemplates.editor.searchPublicModel")}
                  value={memberSearch}
                  onChange={(e) => setMemberSearch(e.target.value)}
                />
                <div className="max-h-48 overflow-y-auto rounded-md border p-2">
                  {catalog
                    .filter(
                      (m) =>
                        !m.model_name.includes("*") && m.model_name.toLowerCase().includes(memberSearch.toLowerCase()),
                    )
                    .map((m) => {
                      const owner = groups.find((g, i) => i !== editing && g.models.includes(m.model_name));
                      return (
                        <label
                          key={m.model_name}
                          className="flex items-center justify-between gap-3 rounded p-2 hover:bg-muted/40"
                        >
                          <span className="flex items-center gap-2">
                            <input
                              type="checkbox"
                              aria-label={t("pages.routeTemplates.editor.memberFor", { model: m.model_name })}
                              checked={draft.models.includes(m.model_name)}
                              disabled={!!owner}
                              onChange={(e) =>
                                setDraft(
                                  reconcileGroup(
                                    {
                                      ...draft,
                                      models: e.target.checked
                                        ? [...draft.models, m.model_name]
                                        : draft.models.filter((n) => n !== m.model_name),
                                    },
                                    catalog,
                                  ),
                                )
                              }
                            />
                            <span className="break-all">{m.model_name}</span>
                          </span>
                          <span className="text-xs text-muted-foreground">
                            {owner
                              ? t("pages.routeTemplates.editor.memberOwner", { name: owner.group_name })
                              : t("pages.routeTemplates.editor.deploymentCount", { count: m.deployments.length })}
                          </span>
                        </label>
                      );
                    })}
                </div>
                <p className="text-xs text-muted-foreground">{t("pages.routeTemplates.editor.oneGroup")}</p>
              </section>
              <label className="block space-y-2">
                <span className="font-medium">{t("pages.routeTemplates.editor.requiredStrategy")}</span>
                <select
                  aria-label={t("pages.routeTemplates.editor.groupStrategy")}
                  className="w-full rounded-md border bg-background p-3"
                  value={draft.routing_strategy}
                  onChange={(e) => setDraft(reconcileGroup({ ...draft, routing_strategy: e.target.value }, catalog))}
                >
                  {Object.entries(groupStrategies).map(([key, label]) => (
                    <option key={key} value={key}>
                      {t(label)}
                    </option>
                  ))}
                </select>
                <p className="text-xs text-muted-foreground">{t(groupHints[draft.routing_strategy])}</p>
              </label>
              {draft.routing_strategy === "traffic-split" && (
                <section className="space-y-3 rounded-lg border p-4">
                  <h4 className="font-medium">{t("pages.routeTemplates.editor.relativeWeights")}</h4>
                  <p className="text-xs text-muted-foreground">{t("pages.routeTemplates.editor.groupWeightHint")}</p>
                  {draft.routing_strategy_args?.allocations.map((row, i) => (
                    <label key={row.deployment_id} className="flex items-center justify-between gap-3">
                      <DeploymentIdentity
                        id={row.deployment_id}
                        deployment={deployments.find((d) => d.deployment_id === row.deployment_id)}
                      />
                      <Input
                        className="w-28 shrink-0"
                        type="number"
                        min="0"
                        step="any"
                        aria-label={t("pages.routeTemplates.editor.groupWeight", { id: row.deployment_id })}
                        value={Number.isNaN(row.weight) ? "" : row.weight}
                        onChange={(e) =>
                          setDraft({
                            ...draft,
                            routing_strategy_args: {
                              allocations: draft.routing_strategy_args!.allocations.map((r, j) =>
                                j === i ? { ...r, weight: e.target.value === "" ? NaN : Number(e.target.value) } : r,
                              ),
                            },
                          })
                        }
                      />
                    </label>
                  ))}
                </section>
              )}
              {error && draft.group_name && (
                <p role="alert" className="text-sm text-destructive">
                  {error}
                </p>
              )}
            </div>
          )}
          <DialogFooter className="shrink-0 border-t pt-4">
            <Button variant="outline" onClick={() => setDraft(null)}>
              {t("pages.routeTemplates.editor.cancel")}
            </Button>
            <Button disabled={!!error} onClick={confirm}>
              {t("pages.routeTemplates.editor.saveDraft")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
/** GroupRow 呈现模板组及三种可复制调用示例；参数为配置和草稿回调，无网络请求。 */
function GroupRow({
  group: g,
  expanded,
  onExpand,
  onEdit,
  onDelete,
  deleteBlocked,
}: {
  group: GroupConfig;
  expanded: boolean;
  onExpand: () => void;
  onEdit: () => void;
  onDelete: () => void;
  deleteBlocked: boolean;
}) {
  const t = useT();
  const [language, setLanguage] = useState("cURL");
  const request = { model: g.group_name, messages: [{ role: "user", content: "Hello" }] };
  let code =
    'import OpenAI from "openai";\n\nconst client = new OpenAI({ baseURL: process.env.XHUB_BASE_URL + "/v1", apiKey: process.env.XHUB_API_KEY });\nconst response = await client.chat.completions.create(' +
    JSON.stringify(request, null, 2) +
    ");";
  if (language === "cURL") {
    code = [
      'curl -X POST "$XHUB_BASE_URL/v1/chat/completions" \\',
      '  -H "Authorization: Bearer $XHUB_API_KEY" \\',
      "  -H 'Content-Type: application/json' \\",
      "  -d '" + JSON.stringify(request, null, 2).replaceAll("'", "'\\''") + "'",
    ].join("\n");
  } else if (language === "Python") {
    code =
      'import os\nfrom openai import OpenAI\n\nclient = OpenAI(base_url=os.environ["XHUB_BASE_URL"] + "/v1", api_key=os.environ["XHUB_API_KEY"])\nresponse = client.chat.completions.create(\n    model=' +
      JSON.stringify(g.group_name) +
      ',\n    messages=[{"role": "user", "content": "Hello"}]\n)';
  }
  return (
    <>
      <tr className="border-t">
        <td className="p-4">
          <Button
            variant="ghost"
            className="whitespace-normal text-left text-primary"
            aria-label={t("pages.routeTemplates.editor.expandGroup", { name: g.group_name })}
            aria-expanded={expanded}
            onClick={onExpand}
          >
            {expanded ? "−" : "+"} {g.group_name}
          </Button>
        </td>
        <td className="p-4">
          <div className="flex flex-wrap gap-2">
            {g.models.map((n) => (
              <span key={n} className="rounded border bg-muted/20 px-2 py-1 text-xs">
                {n}
              </span>
            ))}
          </div>
        </td>
        <td className="p-4">{t(groupStrategies[g.routing_strategy])}</td>
        <td className="p-4">
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              aria-label={t("pages.routeTemplates.editor.editGroupFor", { name: g.group_name })}
              onClick={onEdit}
            >
              {t("pages.routeTemplates.editor.edit")}
            </Button>
            <Button
              variant="ghost"
              aria-label={t("pages.routeTemplates.editor.deleteGroupFor", { name: g.group_name })}
              onClick={onDelete}
              disabled={deleteBlocked}
              title={deleteBlocked ? t("pages.routeTemplates.editor.removeGroupRefs") : undefined}
            >
              {t("pages.routeTemplates.editor.delete")}
            </Button>
          </div>
        </td>
      </tr>
      {expanded && (
        <tr>
          <td colSpan={4} className="border-t bg-muted/20 p-5">
            <p className="font-medium">{t("pages.routeTemplates.editor.callGroup")}</p>
            <p className="mt-2 text-sm text-muted-foreground">
              {t("pages.routeTemplates.editor.callHint", { hint: t(groupHints[g.routing_strategy]) })}
            </p>
            <div className="my-3 flex flex-wrap gap-2">
              {["cURL", "Python", "JavaScript"].map((n) => (
                <Button
                  key={n}
                  size="sm"
                  variant={language === n ? "secondary" : "ghost"}
                  onClick={() => setLanguage(n)}
                >
                  {n}
                </Button>
              ))}
              <CopyButton value={code} label={t("pages.routeTemplates.editor.copyGroupRequest")} className="ml-auto" />
            </div>
            <pre className="overflow-auto rounded-lg bg-slate-950 p-4 text-xs text-slate-100">{code}</pre>
          </td>
        </tr>
      )}
    </>
  );
}
type FallbackMode = "inherit" | "custom" | "disabled";

/** TemplateFallbacks 用表单维护三类有序回退映射，分别表达继承、定制和禁用，随模板整体保存。
 * 参数为草稿和实时目录；允许模型或本模板组名，禁止重复、自引用及跨类型循环，无独立API写入。 */
export function TemplateFallbacks({ form, deployments, onChange }: Props) {
  const t = useT();
  const [draft, setDraft] = useState<{
    source: string;
    field: FallbackField;
    targets: string[];
    mode: FallbackMode;
  } | null>(null);
  const [editing, setEditing] = useState<{ source: string; field: FallbackField } | null>(null);
  const names = [
    ...new Set([...deployments.map((d) => d.model_name), ...(form.routing_groups ?? []).map((g) => g.group_name)]),
  ].filter((n) => !n.includes("*"));
  /** candidate 生成单条变更后的完整草稿，参数为空，返回副本，保留其他类型和主模型配置。 */
  const candidate = () => {
    if (!draft) return form;
    let next = { ...form };
    if (editing)
      next = {
        ...next,
        [editing.field]: (next[editing.field] ?? []).filter((row) => !Object.hasOwn(row, editing.source)),
      };
    if (draft.mode === "inherit") return next;
    return {
      ...next,
      [draft.field]: [...(next[draft.field] ?? []), { [draft.source]: draft.mode === "disabled" ? [] : draft.targets }],
    };
  };
  /** 校验当前回退草稿，返回最先需要修正的本地化信息，无网络调用。继承操作允许清除已下架模型的覆盖。 */
  const validateDraft = () => {
    if (!draft || (draft.mode === "inherit" && editing)) return "";
    if (!names.includes(draft.source)) return t("pages.routeTemplates.editor.choosePrimaryError");
    const invalidTarget = !draft.targets.length || draft.targets.some((target) => !names.includes(target));
    if (draft.mode === "custom" && invalidTarget) return t("pages.routeTemplates.editor.chooseTargetError");
    const sameRule = editing?.field === draft.field && editing.source === draft.source;
    if (!sameRule && (form[draft.field] ?? []).some((row) => Object.hasOwn(row, draft.source))) {
      return t("pages.routeTemplates.editor.duplicateChainError");
    }
    return validateSections(candidate()) ? t("pages.routeTemplates.editor.cycleError") : "";
  };
  const invalid = validateDraft();
  /** move 按索引交换相邻目标；只更新弹窗草稿，边界按钮被禁用，无返回值。 */
  const move = (i: number, offset: number) => {
    if (!draft) return;
    const targets = [...draft.targets];
    [targets[i], targets[i + offset]] = [targets[i + offset], targets[i]];
    setDraft({ ...draft, targets });
  };
  return (
    <section className="space-y-5" aria-label={t("pages.routeTemplates.editor.templateFallbacks")}>
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h3 className="text-lg font-semibold">{t("pages.routeTemplates.editor.fallbacks")}</h3>
          <p className="mt-1 text-sm text-muted-foreground">{t("pages.routeTemplates.editor.fallbackHint")}</p>
        </div>
        <Button
          onClick={() => {
            setEditing(null);
            setDraft({ source: "", field: "fallbacks", targets: [], mode: "custom" });
          }}
        >
          {t("pages.routeTemplates.editor.addFallback")}
        </Button>
      </div>
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full text-left text-sm">
          <thead className="bg-muted/30 text-muted-foreground">
            <tr>
              <th className="p-4">{t("pages.routeTemplates.editor.primary")}</th>
              <th className="p-4">{t("pages.routeTemplates.editor.trigger")}</th>
              <th className="p-4">{t("pages.routeTemplates.editor.orderedChain")}</th>
              <th className="p-4 text-right">{t("pages.routeTemplates.editor.actions")}</th>
            </tr>
          </thead>
          <tbody>
            {fallbackFields.flatMap((field) =>
              (form[field] ?? []).flatMap((row) =>
                Object.entries(row).map(([source, targets]) => (
                  <tr key={field + source} className="border-t">
                    <td className="p-4 font-medium">{source}</td>
                    <td className="p-4">{t(categories[field])}</td>
                    <td className="p-4">
                      <div className="flex flex-wrap items-center gap-2">
                        {targets.length ? (
                          targets.map((name, i) => (
                            <span key={name} className="rounded border bg-muted/20 px-2 py-1 text-xs">
                              {i + 1}. {name}
                            </span>
                          ))
                        ) : (
                          <span className="text-muted-foreground">{t("pages.routeTemplates.editor.noCrossModel")}</span>
                        )}
                      </div>
                    </td>
                    <td className="p-4">
                      <div className="flex justify-end gap-2">
                        <Button
                          variant="outline"
                          aria-label={t("pages.routeTemplates.editor.editFallbackFor", {
                            source,
                            category: t(categories[field]),
                          })}
                          onClick={() => {
                            setEditing({ source, field });
                            setDraft({
                              source,
                              field,
                              targets: [...targets],
                              mode: targets.length ? "custom" : "disabled",
                            });
                          }}
                        >
                          {t("pages.routeTemplates.editor.edit")}
                        </Button>
                        <Button
                          variant="ghost"
                          aria-label={t("pages.routeTemplates.editor.inheritFallbackFor", {
                            source,
                            category: t(categories[field]),
                          })}
                          onClick={() =>
                            onChange({ ...form, [field]: (form[field] ?? []).filter((r) => !Object.hasOwn(r, source)) })
                          }
                        >
                          {t("pages.routeTemplates.editor.inheritFallback")}
                        </Button>
                      </div>
                    </td>
                  </tr>
                )),
              ),
            )}
            {!fallbackFields.some((field) => form[field]?.length) && (
              <tr>
                <td colSpan={4} className="p-12 text-center text-muted-foreground">
                  {t("pages.routeTemplates.editor.emptyFallbacks")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <div className="rounded-lg bg-muted/30 p-4 text-sm text-muted-foreground">
        {t("pages.routeTemplates.editor.fallbackRules")}
      </div>
      <Dialog open={!!draft} onOpenChange={(value) => !value && setDraft(null)}>
        <DialogContent className="flex max-h-[90dvh] flex-col overflow-hidden sm:max-w-2xl">
          <DialogHeader className="shrink-0">
            <DialogTitle>{t("pages.routeTemplates.editor.configureFallback")}</DialogTitle>
            <DialogDescription>{t("pages.routeTemplates.editor.targetLimit")}</DialogDescription>
          </DialogHeader>
          {draft && (
            <div className="min-h-0 space-y-5 overflow-y-auto pr-1">
              <label className="block space-y-2">
                <span className="font-medium">{t("pages.routeTemplates.editor.requiredPrimary")}</span>
                <select
                  aria-label={t("pages.routeTemplates.editor.primaryLabel")}
                  className="w-full rounded-md border bg-background p-3"
                  value={draft.source}
                  disabled={draft.mode === "inherit"}
                  onChange={(e) => setDraft({ ...draft, source: e.target.value })}
                >
                  <option value="">{t("pages.routeTemplates.editor.choosePrimary")}</option>
                  {names.map((n) => (
                    <option key={n}>{n}</option>
                  ))}
                </select>
              </label>
              <label className="block space-y-2">
                <span className="font-medium">{t("pages.routeTemplates.editor.trigger")}</span>
                <select
                  aria-label={t("pages.routeTemplates.editor.triggerLabel")}
                  className="w-full rounded-md border bg-background p-3"
                  value={draft.field}
                  disabled={draft.mode === "inherit"}
                  onChange={(e) => setDraft({ ...draft, field: e.target.value as FallbackField })}
                >
                  {fallbackFields.map((field) => (
                    <option key={field} value={field}>
                      {t(categories[field])}
                    </option>
                  ))}
                </select>
              </label>
              <label className="block space-y-2">
                <span className="font-medium">{t("pages.routeTemplates.editor.fallbackMode")}</span>
                <select
                  className="w-full rounded-md border bg-background p-3"
                  value={draft.mode}
                  onChange={(event) => {
                    const mode = event.target.value as FallbackMode;
                    setDraft({ ...draft, ...(mode === "inherit" && editing ? editing : {}), mode });
                  }}
                >
                  {editing && <option value="inherit">{t("pages.routeTemplates.editor.inheritFallback")}</option>}
                  <option value="custom">{t("pages.routeTemplates.editor.customFallback")}</option>
                  <option value="disabled">{t("pages.routeTemplates.editor.disableFallback")}</option>
                </select>
              </label>
              {draft.mode === "custom" && (
                <div className="rounded-lg border p-4">
                  <p className="mb-3 text-center text-xs font-medium text-primary">
                    {t("pages.routeTemplates.editor.tryNext")}
                  </p>
                  <div className="space-y-3">
                    {draft.targets.map((target, i) => (
                      <div key={i} className="flex flex-wrap items-center gap-2">
                        <span className="text-sm text-muted-foreground">{i + 1}</span>
                        <select
                          aria-label={t("pages.routeTemplates.editor.targetFor", { index: i + 1 })}
                          className="min-w-0 flex-1 rounded-md border bg-background p-2"
                          value={target}
                          onChange={(e) =>
                            setDraft({ ...draft, targets: draft.targets.map((t, j) => (i === j ? e.target.value : t)) })
                          }
                        >
                          <option value="">{t("pages.routeTemplates.editor.chooseTarget")}</option>
                          {names
                            .filter((n) => n !== draft.source)
                            .map((n) => (
                              <option key={n} disabled={draft.targets.includes(n) && n !== target}>
                                {n}
                              </option>
                            ))}
                        </select>
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={t("pages.routeTemplates.editor.moveUp", { index: i + 1 })}
                          disabled={i === 0}
                          onClick={() => move(i, -1)}
                        >
                          ↑
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={t("pages.routeTemplates.editor.moveDown", { index: i + 1 })}
                          disabled={i === draft.targets.length - 1}
                          onClick={() => move(i, 1)}
                        >
                          ↓
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={t("pages.routeTemplates.editor.removeTarget", { index: i + 1 })}
                          onClick={() => setDraft({ ...draft, targets: draft.targets.filter((_, j) => i !== j) })}
                        >
                          {t("pages.routeTemplates.editor.remove")}
                        </Button>
                      </div>
                    ))}
                  </div>
                  <Button
                    className="mt-4"
                    variant="outline"
                    disabled={!draft.source || draft.targets.length >= 32}
                    onClick={() => setDraft({ ...draft, targets: [...draft.targets, ""] })}
                  >
                    {t("pages.routeTemplates.editor.addTarget")}
                  </Button>
                </div>
              )}
              {invalid && draft.source && (
                <p role="alert" className="text-sm text-destructive">
                  {invalid}
                </p>
              )}
            </div>
          )}
          <DialogFooter className="shrink-0 border-t pt-4">
            <Button variant="outline" onClick={() => setDraft(null)}>
              {t("pages.routeTemplates.editor.cancel")}
            </Button>
            <Button
              disabled={!!invalid || !draft}
              onClick={() => {
                onChange(candidate());
                setDraft(null);
              }}
            >
              {t("pages.routeTemplates.editor.saveDraft")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
