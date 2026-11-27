"use client";

import React, { useMemo, useState } from "react";
import { ArrowLeft, Loader2, Plus, Waypoints } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "@/lib/toast";
import { t, useT } from "@/i18n";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ConsolePageHeader } from "@/components/console/ConsolePrimitives";
import RouteTemplateGuide from "@/components/route_templates/RouteTemplateGuide";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import RouteTemplateLibrary from "./RouteTemplateLibrary";
import RouteTemplateBindingDialog from "./RouteTemplateBindingDialog";
import useAuthorized from "@/app/(dashboard)/hooks/useAuthorized";
import { modelInfoCall, type RouteTemplateUsage } from "@/components/networking";
import {
  deleteRefusalText,
  loadRouteTemplates,
  loadTemplateUsage,
  removeRouteTemplate,
  saveRouteTemplate,
  type RouteTemplateRow,
} from "@/components/route_templates/routeTemplatePayload";
import TemplateEditor from "@/components/route_templates/TemplateEditor";
import {
  bodyFromForm,
  deploymentsFromInfo,
  emptyForm,
  formFromBody,
  nextCopyName,
  prettyDocument,
  type TemplateFormState,
} from "@/components/route_templates/templateForm";
import { bindingScopeLabel } from "@/components/route_templates/routeTemplateBinding";

type Draft = {
  id: string;
  name: string;
  form: TemplateFormState;
};
type UsageView = { name: string; rows: RouteTemplateUsage[]; refused: boolean };
type JsonView = { name: string; body: Record<string, unknown> };

const draftFrom = (id: string, name: string, body: Record<string, unknown>): Draft => {
  const form = formFromBody(body);
  return {
    id,
    name,
    form,
  };
};

const editorTitle = (draft: Draft | null) => {
  if (draft?.id) return t("pages.routeTemplates.edit");
  return t("pages.routeTemplates.create");
};

const editorDescription = (draft: Draft | null) => {
  if (draft?.id) return t("pages.routeTemplates.editHint");
  return t("pages.routeTemplates.createHint");
};

const documentText = (row: JsonView | null) => (row ? prettyDocument(row.body) : "");

type BindingTemplate = { id: string; name: string };

/**
 * The route template library.
 *
 * 参数为令牌，返回模板库和独立编辑草稿；创建、复制和更新时保存整份文档。
 * 非法 JSON 禁止保存，绑定中的模板拒绝删除；未绑定模板不影响数据面。
 */
const RouteTemplatesPanel: React.FC<{ accessToken: string | null }> = ({ accessToken }) => {
  const t = useT();
  const { userId, userRole } = useAuthorized();
  const [jsonValid, setJsonValid] = useState(true);
  const [jsonView, setJsonView] = useState<JsonView | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const [search, setSearch] = useState("");
  const [usage, setUsage] = useState<UsageView | null>(null);
  const [pendingDelete, setPendingDelete] = useState<RouteTemplateRow | null>(null);
  const [freshTemplate, setFreshTemplate] = useState<BindingTemplate | null>(null);
  const [bindingTemplate, setBindingTemplate] = useState<BindingTemplate | null>(null);

  const library = useQuery({
    queryKey: ["route-template-library", accessToken],
    enabled: !!accessToken,
    queryFn: async () => {
      return loadRouteTemplates(accessToken!);
    },
  });
  const rows = library.data ?? [];
  const loading = library.isPending;
  const refresh = () => library.refetch();
  const showLibrary = !draft && !library.isError && !loading;
  const editedRow = rows.find((row) => row.id === draft?.id);

  const catalog = useQuery({
    queryKey: ["route-template-deployments", accessToken, userId, userRole],
    enabled: Boolean(draft && accessToken && userId && userRole),
    retry: false,
    staleTime: 30_000,
    queryFn: async () => {
      const deployments: unknown[] = [];
      for (let page = 1; ; page++) {
        // 按部署分页，取齐同名模型的所有部署才允许在完整目录中选权重。
        const data = await modelInfoCall(accessToken!, userId!, userRole!, page, 200);
        deployments.push(...(data?.data ?? []));
        if (page >= (data?.total_pages ?? 1)) return deploymentsFromInfo(deployments);
      }
    },
  });

  const summaryLabels = useMemo(
    () => ({
      retries: (value: number) => t("pages.routeTemplates.attemptsSummary", { count: value }),
      timeout: (seconds: number) => t("pages.routeTemplates.timeoutSummary", { seconds }),
      fallbacks: (value: number) => t("pages.routeTemplates.binding.fallbackCount", { count: value }),
      groups: (value: number) => t("pages.routeTemplates.binding.groupCount", { count: value }),
      models: (value: number) => t("pages.routeTemplates.modelsSummary", { count: value }),
      none: t("pages.routeTemplates.platformDefault"),
    }),
    [t],
  );

  /** startCreate 创建可编辑草稿；无参数，无返回值，只重置本地状态，保存前不影响请求。 */
  const startCreate = () => {
    setJsonValid(true);
    setDraft({ id: "", name: "", form: emptyForm() });
  };

  /** save 提交当前合法草稿；无参数，返回完成 Promise。保存成功关闭编辑器，新模板提供独立应用入口，失败保留输入。 */
  const save = async () => {
    if (!accessToken || !draft) return;
    if (!jsonValid || saving || !draft.name.trim()) return;
    const written = bodyFromForm(draft.form);
    if (!written.ok) {
      toast.fromError(t("pages.routeTemplates.invalidField", { field: written.field }));
      return;
    }
    setSaving(true);
    try {
      const name = draft.name.trim();
      const id = await saveRouteTemplate(accessToken, {
        id: draft.id || undefined,
        name,
        body: written.body,
      });
      toast.success(t("pages.routeTemplates.saved"));
      setFreshTemplate(!draft.id && id ? { id, name } : null);
      setDraft(null);
      await refresh();
    } catch {
      toast.fromError(t("pages.routeTemplates.saveError"));
    } finally {
      setSaving(false);
    }
  };

  const copyDocument = (name: string, body: Record<string, unknown>) => {
    const nextName = nextCopyName(
      name,
      rows.map((item) => item.name),
      (source) => t("pages.routeTemplates.copyOf", { name: source }),
    );
    setJsonValid(true);
    setDraft(draftFrom("", nextName, body));
  };

  const showUsage = async (row: RouteTemplateRow, refused: boolean) => {
    if (!accessToken) return;
    try {
      const listed = await loadTemplateUsage(accessToken, row.id);
      setUsage({ name: row.name, rows: listed, refused });
      setPendingDelete(null);
    } catch (error) {
      toast.fromError(error);
    }
  };

  const remove = async (row: RouteTemplateRow) => {
    if (!accessToken) return;
    if (row.usedBy > 0) {
      await showUsage(row, true);
      return;
    }
    try {
      await removeRouteTemplate(accessToken, row.id);
      if (freshTemplate?.id === row.id) setFreshTemplate(null);
      toast.success(t("pages.routeTemplates.deleted"));
      setPendingDelete(null);
      setUsage(null);
      await refresh();
    } catch (error) {
      const refusal = deleteRefusalText(error, {
        inUse: (count) => t("pages.routeTemplates.inUseBy", { count }),
        failed: t("pages.routeTemplates.deleteFailed"),
      });
      toast.fromError(refusal.message);
      setPendingDelete(null);
      if (refusal.usage.length > 0) {
        setUsage({ name: row.name, rows: refusal.usage, refused: true });
      }
    }
  };

  const openNamed = (row: RouteTemplateRow) => {
    setJsonValid(true);
    setDraft(draftFrom(row.id, row.name, row.body));
  };

  if (!accessToken) return null;

  return (
    <div className="mx-auto w-full min-w-0 max-w-[1600px] space-y-5">
      {draft && (
        <Button variant="ghost" size="sm" disabled={saving} onClick={() => setDraft(null)}>
          <ArrowLeft />
          {t("pages.routeTemplates.backToLibrary")}
        </Button>
      )}
      <ConsolePageHeader
        title={draft ? editorTitle(draft) : t("pages.routeTemplates.title")}
        description={draft ? editorDescription(draft) : t("pages.routeTemplates.pageIntro")}
        actions={<>
          <RouteTemplateGuide />
          {!draft && (
            <Button onClick={() => startCreate()} disabled={loading || library.isError}>
              <Plus />
              {t("pages.routeTemplates.create")}
            </Button>
          )}
        </>}
      />

      {draft && catalog.isError && (
        <div role="alert" className="flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3 text-sm text-destructive">
          <span>{t("pages.routeTemplates.editor.catalogFailed")}</span>
          <Button size="sm" variant="outline" onClick={() => void catalog.refetch()}>{t("pages.routeTemplates.reload")}</Button>
        </div>
      )}
      {draft && catalog.isLoading && (
        <p role="status" className="text-sm text-muted-foreground">{t("pages.routeTemplates.editor.catalogLoading")}</p>
      )}

      {library.isError && (
        <Card>
          <CardContent className="flex flex-wrap items-center justify-between gap-3 text-sm">
            <p className="text-destructive">{t("Failed to load the templates")}</p>
            <Button variant="outline" size="sm" onClick={() => void refresh()}>
              {t("pages.routeTemplates.reload")}
            </Button>
          </CardContent>
        </Card>
      )}
      {!library.isError && loading && (
        <div
          className="flex items-center justify-center gap-2 rounded-xl border border-dashed py-12 text-sm text-muted-foreground"
          role="status"
        >
          <Loader2 className="size-4 animate-spin" />
          {t("pages.routeTemplates.loadingTemplates")}
        </div>
      )}
      {showLibrary && freshTemplate && (
        <Card>
          <CardContent className="flex flex-wrap items-center justify-between gap-3 text-sm">
            <p role="status" className="min-w-0 wrap-anywhere">
              {t("pages.routeTemplates.binding.savedUnbound", { name: freshTemplate.name })}
            </p>
            <Button size="sm" onClick={() => setBindingTemplate(freshTemplate)}>
              <Waypoints />
              {t("pages.routeTemplates.binding.open")}
            </Button>
          </CardContent>
        </Card>
      )}
      {showLibrary && (
        <RouteTemplateLibrary
          rows={rows}
          search={search}
          onSearch={setSearch}
          summaryLabels={summaryLabels}
          onCreate={() => startCreate()}
          onViewJson={setJsonView}
          onCopy={(row) => copyDocument(row.name, row.body)}
          onDelete={setPendingDelete}
          onEdit={openNamed}
          onApply={setBindingTemplate}
          onUsage={(row) => void showUsage(row, false)}
        />
      )}

      {draft && (
        <Card role="region" aria-label={editorTitle(draft)} className="gap-0 py-0">
          <div className="sticky top-0 z-sticky flex flex-wrap items-center justify-between gap-3 rounded-t-xl border-b bg-card px-5 py-3">
            <div className="min-w-0 text-sm text-muted-foreground">
              <p>{t(draft.id ? "pages.routeTemplates.binding.editIntro" : "pages.routeTemplates.binding.createIntro")}</p>
              {editedRow && editedRow.usedBy > 0 && (
                <Button size="sm" variant="link" className="h-auto px-0 py-1" onClick={() => void showUsage(editedRow, false)}>
                  {t("pages.routeTemplates.binding.boundCount", { count: editedRow.usedBy })}
                </Button>
              )}
            </div>
            <div className="flex items-center gap-2">
              <Button variant="ghost" disabled={saving} onClick={() => setDraft(null)}>
                {t("Cancel")}
              </Button>
              <Button onClick={() => void save()} disabled={saving || !jsonValid || !draft.name.trim()}>
                {saving && <Loader2 className="animate-spin" />}
                {t("pages.routeTemplates.save")}
              </Button>
            </div>
          </div>
          <CardContent className="py-6">
            <fieldset disabled={saving} className="min-w-0">
              <TemplateEditor
                key={draft.id || "new"}
                name={draft.name}
                form={draft.form}
                deployments={catalog.data ?? []}
                accessToken={accessToken}
                templateId={draft.id || undefined}
                onName={(name) => setDraft({ ...draft, name })}
                onChange={(form) => setDraft({ ...draft, form })}
                onJsonValid={setJsonValid}
              />
            </fieldset>
          </CardContent>
        </Card>
      )}

      {bindingTemplate && (
        <RouteTemplateBindingDialog
          accessToken={accessToken}
          template={bindingTemplate}
          onClose={() => setBindingTemplate(null)}
          onApplied={() => {
            if (freshTemplate?.id === bindingTemplate.id) setFreshTemplate(null);
            void refresh();
          }}
        />
      )}

      <Dialog open={jsonView !== null} onOpenChange={(open) => !open && setJsonView(null)}>
        <DialogContent className="min-w-0 max-h-[calc(100dvh-2rem)] sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{jsonView?.name}</DialogTitle>
            <DialogDescription>{t("pages.routeTemplates.jsonHint")}</DialogDescription>
          </DialogHeader>
          <pre className="max-h-[60vh] overflow-auto rounded bg-muted p-3 font-mono text-xs">
            {documentText(jsonView)}
          </pre>
        </DialogContent>
      </Dialog>

      <Dialog open={usage !== null} onOpenChange={(open) => !open && setUsage(null)}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] min-w-0 overflow-y-auto">
          <DialogHeader>
            <DialogTitle className="wrap-anywhere">{t("pages.routeTemplates.usageTitle", { name: usage?.name ?? "" })}</DialogTitle>
            <DialogDescription>
              {usage?.refused
                ? t("pages.routeTemplates.inUseBy", { count: usage.rows.length })
                : t("pages.routeTemplates.binding.directBindings")}
            </DialogDescription>
          </DialogHeader>
          {usage && usage.rows.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("pages.routeTemplates.unused")}</p>
          ) : (
            <ul className="space-y-1 text-sm">
              {usage?.rows.map((entry) => (
                <li className="wrap-anywhere rounded-md bg-muted/35 px-3 py-2" key={`${entry.scope_type}-${entry.scope_id}`}>
                  {bindingScopeLabel(entry.scope_type)}
                  {" · "}
                  {entry.name}
                </li>
              ))}
            </ul>
          )}
        </DialogContent>
      </Dialog>

      <Dialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("pages.routeTemplates.confirmDelete", { name: pendingDelete?.name ?? "" })}</DialogTitle>
            <DialogDescription>{t("pages.routeTemplates.confirmDeleteBody")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setPendingDelete(null)}>
              {t("Cancel")}
            </Button>
            <Button variant="destructive" onClick={() => pendingDelete && void remove(pendingDelete)}>
              {t("pages.routeTemplates.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
};

export default RouteTemplatesPanel;
