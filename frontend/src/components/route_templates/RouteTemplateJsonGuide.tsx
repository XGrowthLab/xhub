"use client";

import { useState } from "react";
import { Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useT } from "@/i18n";
import routeTemplateExample from "../../../../docs/testdata/route-template-example.json";

/** 可导入示例覆盖当前严格结构；公开模型名是占位值，不包含凭据，也不会发起请求。 */
export const ROUTE_TEMPLATE_EXAMPLE = routeTemplateExample;

const fieldRows = [
  ["model_routes", "fieldModelRoutes"],
  ["retry_policy", "fieldRetryPolicyRequired"],
  ["routing_groups", "fieldRoutingGroups"],
  ["fallbacks", "fieldFallbackShape"],
  ["context_window_fallbacks", "fieldContextFallbacks"],
  ["content_policy_fallbacks", "fieldContentFallbacks"],
] as const;

const strategies = [
  "random",
  "simple-shuffle",
  "traffic-split",
  "least-busy",
  "cost-based-routing",
  "latency-based-routing",
  "usage-based-routing",
] as const;

/** JsonExample 显示并复制严格结构示例；失败时给出本地化反馈，不更新模板草稿或后台数据。 */
function JsonExample() {
  const t = useT();
  const [status, setStatus] = useState("");
  const json = JSON.stringify(ROUTE_TEMPLATE_EXAMPLE, null, 2);

  /** copy 写入系统剪贴板并返回可见结果；权限失败只更新状态，无外部数据副作用。 */
  async function copy() {
    try {
      await navigator.clipboard.writeText(json);
      setStatus(t("pages.routeTemplates.jsonGuide.copied"));
    } catch {
      setStatus(t("pages.routeTemplates.jsonGuide.copyFailed"));
    }
  }

  return (
    <section aria-label={t("pages.routeTemplates.jsonGuide.exampleTitle")} className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="font-medium">{t("pages.routeTemplates.jsonGuide.exampleTitle")}</h4>
        <Button variant="outline" size="sm" onClick={() => void copy()}>
          <Copy aria-hidden="true" />
          {t("pages.routeTemplates.jsonGuide.copy")}
        </Button>
      </div>
      <pre className="max-h-96 overflow-auto rounded-lg border bg-muted/35 p-4 font-mono text-xs leading-5">{json}</pre>
      <p role="status" className="text-xs text-muted-foreground">
        {status}
      </p>
    </section>
  );
}

/** RouteTemplateJsonGuide 展示随语言切换的严格模板契约、示例和执行边界；仅复制示例，不执行配置。 */
export default function RouteTemplateJsonGuide() {
  const t = useT();
  return (
    <div className="min-w-0 text-sm">
      <Tabs defaultValue="fields" className="gap-3">
        <TabsList className="max-w-full flex-wrap justify-start group-data-horizontal/tabs:h-auto [&>[data-slot=tabs-trigger]]:min-h-8 [&>[data-slot=tabs-trigger]]:flex-none" aria-label={t("pages.routeTemplates.jsonGuide.tabsLabel")}>
          <TabsTrigger value="fields">{t("pages.routeTemplates.jsonGuide.tabFields")}</TabsTrigger>
          <TabsTrigger value="strategies">{t("pages.routeTemplates.jsonGuide.tabStrategies")}</TabsTrigger>
          <TabsTrigger value="examples">{t("pages.routeTemplates.jsonGuide.tabExamples")}</TabsTrigger>
          <TabsTrigger value="execution">{t("pages.routeTemplates.jsonGuide.tabExecution")}</TabsTrigger>
          <TabsTrigger value="validation">{t("pages.routeTemplates.jsonGuide.tabValidation")}</TabsTrigger>
        </TabsList>

        <TabsContent value="fields" className="space-y-3">
          <p className="leading-6 text-muted-foreground">{t("pages.routeTemplates.jsonGuide.currentStatusBody")}</p>
          <dl aria-label={t("pages.routeTemplates.jsonGuide.referenceTitle")} className="divide-y rounded-lg border bg-card">
            {fieldRows.map(([field, key]) => (
              <div key={field} className="grid gap-1 px-3 py-2.5 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)] sm:gap-4">
                <dt className="[overflow-wrap:anywhere] font-mono text-xs leading-6 text-foreground">{field}</dt>
                <dd className="text-sm leading-6 text-muted-foreground">{t("pages.routeTemplates.jsonGuide." + key)}</dd>
              </div>
            ))}
          </dl>
        </TabsContent>

        <TabsContent value="strategies" className="space-y-3 leading-6">
          <p>{t("pages.routeTemplates.jsonGuide.strategiesHint")}</p>
          <ul className="grid gap-2 sm:grid-cols-2">
            {strategies.map((strategy) => (
              <li key={strategy} className="rounded-md border bg-muted/30 px-3 py-1.5">
                <code className="break-words text-xs">{strategy}</code>
              </li>
            ))}
          </ul>
          <p>{t("pages.routeTemplates.jsonGuide.fieldRoutingStrategyArgs")}</p>
        </TabsContent>

        <TabsContent value="examples" className="space-y-3 leading-6">
          <p>{t("pages.routeTemplates.jsonGuide.examplesHint")}</p>
          <JsonExample />
        </TabsContent>

        <TabsContent value="execution" className="space-y-3 leading-6">
          <h4 className="font-medium">{t("pages.routeTemplates.jsonGuide.flowTitle")}</h4>
          <ol className="list-decimal space-y-3 rounded-lg border bg-card py-3 ps-8 pe-4 marker:font-semibold marker:text-primary">
            {[1, 2, 3, 4].map((step) => (
              <li key={step}>
                <h5 className="font-medium">{t("pages.routeTemplates.jsonGuide.flow" + step + "Title")}</h5>
                <p className="text-muted-foreground">{t("pages.routeTemplates.jsonGuide.flow" + step + "Body")}</p>
              </li>
            ))}
          </ol>
          <p className="text-muted-foreground">{t("pages.routeTemplates.jsonGuide.executionBoundary")}</p>
        </TabsContent>

        <TabsContent value="validation" className="space-y-3 leading-6">
          <h4 className="font-medium">{t("pages.routeTemplates.jsonGuide.tabValidation")}</h4>
          <p>{t("pages.routeTemplates.jsonGuide.validationBody")}</p>
          <p className="text-muted-foreground">{t("pages.routeTemplates.jsonGuide.ruleBody")}</p>
        </TabsContent>
      </Tabs>
    </div>
  );
}
