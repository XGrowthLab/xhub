"use client";

import React, { useEffect, useState } from "react";
import { HelpDisclosure } from "@/components/console/ConsolePrimitives";
import { useT } from "@/i18n";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
  getRouteTemplateBindingCall,
  getRouteTemplatesCall,
  type EffectiveRouteTemplate,
} from "@/components/networking";
import { effectiveBindingLabel } from "./routeTemplateBinding";

const INHERIT = "inherit";

/** 路由模板选择器；Props 提供对象范围、表单值与变更回调。
 * 组织、团队和密钥设置使用，分开显示服务端生效结果和待保存选择。读取错误显示状态不可用，组件不提交写入。 */
const RouteTemplateSelect: React.FC<{
  accessToken: string | null;
  value: string;
  onChange: (templateId: string) => void;
  scope?: "organization" | "team" | "key";
  scopeId?: string;
  disabled?: boolean;
}> = ({ accessToken, value, onChange, scope, scopeId, disabled }) => {
  const t = useT();
  const [catalog, setCatalog] = useState<{ token: string; options: Array<{ id: string; name: string }> }>();
  const [lookup, setLookup] = useState<{
    key: string;
    binding: Awaited<ReturnType<typeof getRouteTemplateBindingCall>> | null;
  }>();
  const bindingKey = JSON.stringify([accessToken, scope, scopeId, value]);
  const optionsLoaded = catalog?.token === accessToken;
  const options = optionsLoaded ? catalog.options : [];
  const bindingLoaded = lookup?.key === bindingKey;
  const binding = bindingLoaded ? lookup.binding : null;

  useEffect(() => {
    if (!accessToken) return;
    let cancelled = false;
    void getRouteTemplatesCall(accessToken)
      .then((rows) => {
        if (cancelled) return;
        setCatalog({ token: accessToken, options: rows.map((row) => ({ id: row.id, name: row.name })) });
      })
      .catch(() => {
        if (!cancelled) setCatalog({ token: accessToken, options: [] });
      });
    return () => {
      cancelled = true;
    };
  }, [accessToken]);

  useEffect(() => {
    if (!accessToken || !scope || !scopeId) return;
    let cancelled = false;
    void getRouteTemplateBindingCall(accessToken, scope, scopeId)
      .then((binding) => {
        if (cancelled) return;
        setLookup({ key: bindingKey, binding });
      })
      .catch(() => {
        if (!cancelled) setLookup({ key: bindingKey, binding: null });
      });
    return () => {
      cancelled = true;
    };
  }, [accessToken, scope, scopeId, bindingKey]);

  const selected = value || INHERIT;
  const effective = binding?.effective;
  const selectedOption = options.find((option) => option.id === value);
  const selectedLabel =
    selected === INHERIT
      ? t("pages.routeTemplates.notApplied")
      : selectedOption?.name ||
        (effective?.template_id === value ? effective.name : undefined) ||
        t(optionsLoaded ? "pages.routeTemplates.unknownTemplate" : "pages.routeTemplates.loadingTemplates");
  const preview: EffectiveRouteTemplate | undefined =
    value && scope ? { template_id: value, name: selectedOption?.name, scope_type: scope } : binding?.inherited;

  return (
    <div className="space-y-1">
      <Select
        value={selected}
        disabled={disabled}
        onValueChange={(next) => {
          if (!next) return;
          onChange(next === INHERIT ? "" : next);
        }}
      >
        <SelectTrigger className="w-full" aria-label={t("pages.routeTemplates.title")}>
          <SelectValue>{selectedLabel}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={INHERIT}>{t("pages.routeTemplates.binding.restore")}</SelectItem>
          {options.map((option) => (
            <SelectItem key={option.id} value={option.id}>
              {option.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {scope && scopeId ? (
        <div className="text-sm text-muted-foreground">
          <p>
            {t("pages.routeTemplates.binding.current")} ·{" "}
            {bindingLoaded ? effectiveBindingLabel(effective) : t("pages.routeTemplates.binding.loading")}
          </p>
          {binding && binding.route_template_id !== value && (
            <p>
              {t("pages.routeTemplates.binding.afterApply")} · {effectiveBindingLabel(preview)}
            </p>
          )}
        </div>
      ) : (
        <HelpDisclosure title={t("consoleUi.routeHelp")}>{t("pages.routeTemplates.inheritHint")}</HelpDisclosure>
      )}
    </div>
  );
};

export default RouteTemplateSelect;
