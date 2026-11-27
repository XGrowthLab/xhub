"use client";

import { useDeferredValue, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useT } from "@/i18n";
import { ApiError } from "@/lib/http/client";
import { getRouteTemplateBindingCall, setRouteTemplateBindingCall } from "@/components/networking";
import {
  bindingScopeLabel,
  effectiveBindingLabel,
  loadBindingTargets,
} from "@/components/route_templates/routeTemplateBinding";
import { SCOPE_KINDS, type ScopeKind } from "@/components/route_templates/routeTemplatePayload";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

type Props = {
  accessToken: string;
  template: { id: string; name: string };
  onClose: () => void;
  onApplied: () => void;
};

/** 应用或撤销模板的单对象对话框。Props 提供授权会话、模板身份和关闭、刷新回调。
 * 读取后端生效绑定后才允许提交，写入附带确认时的直接绑定。失败时保留选择并重新查询，成功后显示后台生效结果。 */
export default function RouteTemplateBindingDialog({ accessToken, template, onClose, onApplied }: Props) {
  const t = useT();
  const client = useQueryClient();
  const [scope, setScope] = useState<ScopeKind>("key");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [targetId, setTargetId] = useState("");
  const deferredSearch = useDeferredValue(search.trim());
  const keySearch = scope === "key" ? deferredSearch : "";
  const targets = useQuery({
    queryKey: ["route-template-targets", accessToken, scope, keySearch, page],
    queryFn: () => loadBindingTargets(accessToken, scope, keySearch, page),
    retry: false,
  });
  const bindingKey = ["route-template-binding", accessToken, scope, targetId];
  const binding = useQuery({
    queryKey: bindingKey,
    queryFn: () => getRouteTemplateBindingCall(accessToken, scope, targetId),
    enabled: Boolean(targetId),
    retry: false,
    staleTime: 0,
  });
  const write = useMutation({
    mutationFn: ({ next, expected }: { next: string; expected: string }) =>
      setRouteTemplateBindingCall(accessToken, scope, targetId, { id: next, expected }),
    onSuccess: (result, { next }) => {
      // 采用服务器确认后的生效结果，清除绑定时可能已从上级继承另一份模板。
      client.setQueryData(bindingKey, { ...binding.data, route_template_id: next, effective: result.effective });
      void client.invalidateQueries({ queryKey: bindingKey });
      onApplied();
    },
    onError: () => {
      // 409 或响应中断时重新读取实际状态，用户看过新的状态后再决定下一步。
      void binding.refetch();
    },
  });
  const rows = (targets.data?.rows ?? []).filter(
    (row) => scope === "key" || row.label.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()),
  );
  const busy = write.isPending;
  const bindingReady = Boolean(binding.data && !binding.isError && !binding.isFetching && !busy);
  const isBoundHere = binding.data?.route_template_id === template.id;
  const pageCount = targets.data?.pages ?? 0;
  const targetPending = targets.isFetching || (scope === "key" && deferredSearch !== search.trim());
  const writeStatus = write.error instanceof ApiError ? write.error.status : undefined;
  const targetUnavailable = targets.isError || rows.length === 0;
  let writeError = "pages.routeTemplates.binding.writeFailed";
  if (writeStatus === 409) writeError = "pages.routeTemplates.binding.conflict";
  if (writeStatus === 403 || writeStatus === 404) writeError = "pages.routeTemplates.binding.rejected";

  /** 切换查找条件后清除上一次的目标和操作反馈。无参数和返回值，对话框内调用。 */
  const clearTarget = () => {
    setTargetId("");
    write.reset();
  };

  /** 切换范围后重新开始搜索和分页；next 为空时保留原选择，供下拉控件调用。 */
  const changeScope = (next: ScopeKind | null) => {
    if (!next) return;
    setScope(next);
    setSearch("");
    setPage(1);
    clearTarget();
  };

  /** 更新对象搜索文本，回到第一页并清除旧目标；在输入时调用，不提交绑定。 */
  const changeSearch = (value: string) => {
    setSearch(value);
    setPage(1);
    clearTarget();
  };

  /** 按用户已读到的绑定状态提交。next 是模板 ID 或恢复继承的空值，返回前保持当前选择。 */
  const submit = (next: string) => {
    if (bindingReady && binding.data) write.mutate({ next, expected: binding.data.route_template_id });
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] min-w-0 gap-4 overflow-y-auto sm:max-w-lg"
        showCloseButton={!busy}
      >
        <DialogHeader className="pr-8">
          <DialogTitle className="wrap-anywhere">
            {t("pages.routeTemplates.binding.title", { name: template.name })}
          </DialogTitle>
          <DialogDescription>{t("pages.routeTemplates.binding.description")}</DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="grid gap-3 sm:grid-cols-[8rem_minmax(0,1fr)]">
            <Select value={scope} disabled={busy} onValueChange={changeScope}>
              <SelectTrigger className="w-full" aria-label={t("pages.routeTemplates.scope")}>
                <SelectValue>{bindingScopeLabel(scope)}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                {SCOPE_KINDS.map((item) => (
                  <SelectItem key={item} value={item}>
                    {bindingScopeLabel(item)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Input
              aria-label={t("pages.routeTemplates.binding.searchTarget")}
              placeholder={t("pages.routeTemplates.binding.searchTarget")}
              value={search}
              disabled={busy}
              onChange={(event) => changeSearch(event.target.value)}
            />
          </div>
          <Select
            value={targetId}
            disabled={busy || targetPending || targetUnavailable}
            onValueChange={(id) => {
              setTargetId(id ?? "");
              write.reset();
            }}
          >
            <SelectTrigger className="w-full" aria-label={t("pages.routeTemplates.binding.target")}>
              <SelectValue>
                {rows.find((row) => row.id === targetId)?.label ?? t("pages.routeTemplates.binding.target")}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {rows.map(({ id, label }) => (
                <SelectItem key={id} value={id}>
                  {label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {targets.isError && (
            <div role="alert" className="flex items-center justify-between gap-2 text-destructive">
              <span>{t("pages.routeTemplates.binding.targetsFailed")}</span>
              <Button size="sm" variant="outline" onClick={() => void targets.refetch()}>
                {t("pages.routeTemplates.reload")}
              </Button>
            </div>
          )}
          {!targets.isError && targetPending && (
            <p role="status" className="text-muted-foreground">
              {t("pages.routeTemplates.binding.loading")}
            </p>
          )}
          {!targets.isError && !targetPending && rows.length === 0 && (
            <p className="text-muted-foreground">{t("pages.routeTemplates.binding.noTargets")}</p>
          )}
          {pageCount > 1 && (
            <div className="flex flex-wrap items-center justify-between gap-2 text-muted-foreground">
              <span>{t("pages.routeTemplates.binding.page", { page, total: pageCount })}</span>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy || targetPending || page <= 1}
                  onClick={() => {
                    setPage(page - 1);
                    clearTarget();
                  }}
                >
                  {t("pages.routeTemplates.binding.previous")}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy || targetPending || page >= pageCount}
                  onClick={() => {
                    setPage(page + 1);
                    clearTarget();
                  }}
                >
                  {t("pages.routeTemplates.binding.next")}
                </Button>
              </div>
            </div>
          )}
        </div>

        {targetId && (
          <div className="space-y-2" aria-busy={binding.isFetching}>
            {binding.isError && (
              <div role="alert" className="flex items-center justify-between gap-2 text-destructive">
                <span>{t("pages.routeTemplates.binding.readFailed")}</span>
                <Button size="sm" variant="outline" onClick={() => void binding.refetch()}>
                  {t("pages.routeTemplates.reload")}
                </Button>
              </div>
            )}
            {!binding.isError && binding.data && (
              <dl className="grid gap-4 rounded-lg border bg-muted/25 p-3 sm:grid-cols-2">
                <div className="min-w-0">
                  <dt className="text-muted-foreground">{t("pages.routeTemplates.binding.current")}</dt>
                  <dd className="wrap-anywhere font-medium">{effectiveBindingLabel(binding.data.effective)}</dd>
                </div>
                <div className="min-w-0">
                  <dt className="text-muted-foreground">
                    {t(
                      isBoundHere
                        ? "pages.routeTemplates.binding.afterRestore"
                        : "pages.routeTemplates.binding.afterApply",
                    )}
                  </dt>
                  <dd className="wrap-anywhere font-medium">
                    {effectiveBindingLabel(
                      isBoundHere ? binding.data.inherited : { name: template.name, scope_type: scope },
                    )}
                  </dd>
                </div>
              </dl>
            )}
            {!binding.isError && !binding.data && (
              <p role="status" className="text-muted-foreground">
                {t("pages.routeTemplates.binding.loading")}
              </p>
            )}
            {write.isError && (
              <p role="alert" className="text-destructive">
                {t(writeError)}
              </p>
            )}
            {write.data && (
              <p role="status" className="wrap-anywhere font-medium text-foreground">
                {t("pages.routeTemplates.binding.confirmed", {
                  effective: effectiveBindingLabel(write.data.effective),
                })}
              </p>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            {t("Close")}
          </Button>
          {isBoundHere ? (
            <Button disabled={!bindingReady} onClick={() => submit("")}>
              {t("pages.routeTemplates.binding.restore")}
            </Button>
          ) : (
            <Button disabled={!bindingReady} onClick={() => submit(template.id)}>
              {t("pages.routeTemplates.binding.apply")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
