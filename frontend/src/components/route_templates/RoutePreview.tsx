"use client";

import React, { useEffect, useRef, useState } from "react";
import { useT } from "@/i18n";
import { apiClient } from "../networking";
import { Button } from "@/components/ui/button";
import { strategyLabels } from "./allocationPolicy";

const sourceLabels: Record<string, string> = {
  draft: "pages.routeTemplates.preview.fromDraft",
  selected: "pages.routeTemplates.preview.fromSelected",
  builtin: "pages.routeTemplates.binding.platform",
  key: "pages.routeTemplates.scopeKey",
  team: "pages.routeTemplates.scopeTeam",
  organization: "pages.routeTemplates.scopeOrganization",
};
const ruleLabels: Record<string, string> = {
  "model-default": "pages.routeTemplates.preview.defaultRule",
  "routing-group": "pages.routeTemplates.editor.groups",
  "template-model": "pages.routeTemplates.editor.modelRules",
};
const exclusionLabels: Record<string, string> = {
  "deployment disabled": "pages.routeTemplates.preview.deploymentDisabled",
  "deployment is cooling down": "pages.routeTemplates.preview.deploymentCoolingDown",
  "deployment has no allocated traffic": "pages.routeTemplates.preview.noWeight",
};
const fallbackLabels = {
  fallbacks: "pages.routeTemplates.editor.general",
  context_window_fallbacks: "pages.routeTemplates.editor.context",
  content_policy_fallbacks: "pages.routeTemplates.editor.contentPolicy",
} as const;

/** 预览安全投影只接收部署和选路事实，不接收密钥或上游地址。 */
export type RoutePreviewResult = {
  source: string;
  rule_source: string;
  routing_strategy: string;
  fallback_policy?: Partial<Record<keyof typeof fallbackLabels, string[]>>;
  data: {
    deployment_id: string;
    connection: string;
    supplier: string;
    upstream_model: string;
    protocol: string;
    excluded_reason?: string;
    weight?: number;
    target_share?: number;
  }[];
};

/**
 * RoutePreview 将当前草稿交给后端真实规则解析和候选筛选。
 * 参数为令牌、公开模型、草稿路由组及模板身份；返回可用候选和可见的回退顺序。
 * 模板编辑页调用；不保存草稿、不调用供应商；草稿或入口变化后清除过期结果。
 */
export default function RoutePreview({
  accessToken,
  models,
  groups,
  body,
  templateId,
  organizationId,
  teamId,
}: {
  accessToken: string | null;
  models: string[];
  groups?: string[];
  body: Record<string, unknown> | null;
  templateId?: string;
  organizationId?: string;
  teamId?: string;
}) {
  const t = useT();
  const activeRequest = useRef<AbortController | null>(null);
  const [entries, setEntries] = useState<{ id: string; label: string }[]>([]);
  const [model, setModel] = useState("");
  const [endpoint, setEndpoint] = useState("chat");
  const [catalogFailed, setCatalogFailed] = useState(false);
  const [request, setRequest] = useState<{
    key: string;
    signal: AbortSignal;
    status: "pending" | "success" | "failed";
    result?: RoutePreviewResult;
  }>();
  const canPreviewModel = models.includes(model) || Boolean(groups?.includes(model));
  const canPreview = accessToken && body && canPreviewModel && entries.some((entry) => entry.id === endpoint);
  const requestKey = JSON.stringify([
    accessToken,
    body,
    model,
    endpoint,
    templateId,
    organizationId,
    teamId,
    canPreviewModel,
  ]);
  const current = request?.key === requestKey && !request.signal.aborted ? request : undefined;
  const result = current?.result;
  const busy = current?.status === "pending";
  let error = "";
  if (catalogFailed) error = "endpointsError";
  if (current?.status === "failed") error = "requestError";
  useEffect(() => {
    if (!accessToken) return;
    let canceled = false;
    apiClient
      .get<{ endpoint_types?: { id: string; label: string }[] }>("/public/endpoints")
      .then((data) => {
        if (canceled) return;
        setEntries(data.endpoint_types ?? []);
        setCatalogFailed(false);
      })
      .catch(() => {
        if (!canceled) setCatalogFailed(true);
      });
    return () => {
      canceled = true;
    };
  }, [accessToken]);
  useEffect(() => () => activeRequest.current?.abort(), [requestKey]);

  /** 发送一次当前输入的只读预览；无参数和返回值。新请求或输入变化取消旧请求，失败留在当前结果区域。 */
  const runPreview = () => {
    if (!canPreview) return;
    activeRequest.current?.abort();
    const controller = new AbortController();
    activeRequest.current = controller;
    const pending = { key: requestKey, signal: controller.signal };
    setRequest({ ...pending, status: "pending" });
    apiClient
      .post<RoutePreviewResult>("/route_template/preview", {
        accessToken: accessToken!,
        signal: pending.signal,
        body: {
          model_name: model,
          endpoint_id: endpoint,
          body,
          template_id: templateId,
          organization_id: organizationId,
          team_id: teamId,
        },
      })
      .then((data) => {
        if (!pending.signal.aborted) setRequest({ ...pending, status: "success", result: data });
      })
      .catch(() => {
        if (!pending.signal.aborted) setRequest({ ...pending, status: "failed" });
      });
  };
  return (
    <section aria-label={t("pages.routeTemplates.preview.region")} className="space-y-3 rounded-md border p-3">
      <p className="text-sm font-medium">{t("pages.routeTemplates.preview.title")}</p>
      <p className="text-xs text-muted-foreground">{t("pages.routeTemplates.preview.hint")}</p>
      <div className="grid min-w-0 gap-2">
        <label className="grid min-w-0 gap-1">
          {t("pages.routeTemplates.preview.model")}
          <select
            className="min-w-0 w-full rounded-md border bg-background p-2"
            aria-label={t("pages.routeTemplates.preview.model")}
            value={model}
            onChange={(e) => setModel(e.target.value)}
          >
            <option value="">{t("pages.routeTemplates.preview.selectModel")}</option>
            {models.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
            {groups
              ?.filter((name) => !models.includes(name))
              .map((name) => (
                <option key={name} value={name}>
                  {t("pages.routeTemplates.preview.previewGroup", { name })}
                </option>
              ))}
          </select>
        </label>
        <label className="grid min-w-0 gap-1">
          {t("pages.routeTemplates.preview.endpoint")}
          <select
            className="min-w-0 w-full rounded-md border bg-background p-2"
            aria-label={t("pages.routeTemplates.preview.endpoint")}
            value={endpoint}
            onChange={(e) => setEndpoint(e.target.value)}
          >
            {entries.map((entry) => (
              <option key={entry.id} value={entry.id}>
                {entry.label}
              </option>
            ))}
          </select>
        </label>
        <Button type="button" variant="outline" disabled={busy || !canPreview} onClick={runPreview}>
          {t(busy ? "pages.routeTemplates.preview.busy" : "pages.routeTemplates.preview.run")}
        </Button>
      </div>
      {error && <p role="alert">{t(`pages.routeTemplates.preview.${error}`)}</p>}
      {result && (
        <div className="space-y-3 wrap-anywhere">
          <p>
            {t("pages.routeTemplates.preview.summary", {
              source: t(sourceLabels[result.source] ?? result.source),
              rule: t(ruleLabels[result.rule_source] ?? result.rule_source),
              strategy: t(strategyLabels[result.routing_strategy] ?? result.routing_strategy),
            })}
          </p>
          {result.data.length === 0 && <p>{t("pages.routeTemplates.preview.empty")}</p>}
          <ul className="divide-y rounded-lg border bg-card">
            {result.data.map((row) => (
              <li key={row.deployment_id} className="space-y-1 p-3 text-sm">
                <p className="font-medium">
                  {[row.connection || row.supplier, row.upstream_model].filter(Boolean).join(" · ")}
                </p>
                {row.excluded_reason ? (
                  <p className="text-muted-foreground">
                    {t("pages.routeTemplates.preview.excluded", {
                      reason: t(exclusionLabels[row.excluded_reason] ?? row.excluded_reason),
                    })}
                  </p>
                ) : (
                  <p>
                    {t("pages.routeTemplates.preview.candidate")}
                    {row.weight !== undefined && (
                      <span> · {t("pages.routeTemplates.preview.weight", { value: row.weight })}</span>
                    )}
                    {row.target_share !== undefined && (
                      <span>
                        {" "}
                        · {t("pages.routeTemplates.preview.share", { value: (row.target_share * 100).toFixed(1) })}
                      </span>
                    )}
                  </p>
                )}
                <p className="text-xs text-muted-foreground">
                  {[row.protocol, row.deployment_id].filter(Boolean).join(" · ")}
                </p>
              </li>
            ))}
          </ul>
          {result.fallback_policy && (
            <section className="space-y-2 border-t pt-3" aria-label={t("pages.routeTemplates.preview.fallbackTitle")}>
              <h4 className="font-medium">{t("pages.routeTemplates.preview.fallbackTitle")}</h4>
              <p className="text-xs text-muted-foreground">
                {t(
                  groups?.includes(model)
                    ? "pages.routeTemplates.preview.groupFallbackHint"
                    : "pages.routeTemplates.preview.fallbackHint",
                )}
              </p>
              {Object.entries(fallbackLabels).map(([key, label]) => {
                const targets = result.fallback_policy?.[key as keyof typeof fallbackLabels];
                return targets?.length ? (
                  <div key={key} className="text-sm">
                    <span className="text-muted-foreground">{t(label)} · </span>
                    <span>{targets.join(" → ")}</span>
                  </div>
                ) : null;
              })}
              {!Object.values(result.fallback_policy).some((targets) => targets?.length) && (
                <p className="text-sm text-muted-foreground">{t("pages.routeTemplates.preview.fallbackNone")}</p>
              )}
            </section>
          )}
        </div>
      )}
    </section>
  );
}
