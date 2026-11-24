"use client";
import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { apiClient, getProxyBaseUrl, modelInfoCall } from "@/components/networking";
import { endpointLabel, type EndpointDescriptor, type EndpointTransport } from "@/components/add_model/endpointCatalog";
import { ModelAvatar } from "@/components/molecules/models/ModelAvatar";
import { t as translate, useT } from "@/i18n";
import { uiHref } from "@/utils/uiHref";
import type { PriceCatalogRow } from "./priceCatalogRows";
import { marketBrandLabel, marketMoney, marketPrices, marketModalities, MODALITY_NAMES, marketStrings } from "./marketCatalog";
import { modelDeployments, modelCurl, marketDocUrl, effectiveMarketEndpoints, type MarketDeployment } from "./modelAccess";

/** 读取当前用户可见的完整部署与端点；参数为会话，返回部署和公开端点。
 * 供内部详情使用，分页读取避免漏掉部署；读取失败交由查询错误状态展示，不误报为未部署。 */
export async function loadMarketAccess(token: string, userId: string, role: string) {
  const endpoints = await apiClient.get<{ endpoint_types?: EndpointDescriptor[]; transports?: EndpointTransport[] }>("/public/endpoints");
  const deployments: MarketDeployment[] = [];
  for (let page = 1; ; page++) {
    const result = await modelInfoCall(token, userId, role, page, 1000);
    if (!result || !Array.isArray(result.data)) throw new Error(translate("marketSync.detail.invalidAccess"));
    deployments.push(...result.data);
    if (page >= (result.total_pages ?? 1) || !result.data.length) break;
  }
  const catalog = endpoints?.endpoint_types ?? [];
  const transports = endpoints?.transports ?? [];
  return { deployments: deployments.map((item) => ({ ...item, model_info: { ...item.model_info, endpoint_types: effectiveMarketEndpoints(item, catalog, transports) } })), endpoints: catalog };
}

/** 内部模型详情；参数为价格行、会话和关闭回调，返回全部价格档位与实际接入信息。
 * 仅登录且弹窗打开时读取部署；未部署、禁用和未知媒体协议不生成示例，不读取供应商密钥。 */
export function MarketModelDetail({
  row,
  accessToken,
  userId,
  userRole,
  onClose,
}: {
  row: PriceCatalogRow | null;
  accessToken: string | null;
  userId: string | null;
  userRole: string;
  onClose: () => void;
}) {
  const t = useT();
  const access = useQuery({
    queryKey: ["marketModelAccess", accessToken, userId],
    queryFn: () => loadMarketAccess(accessToken!, userId ?? "", userRole),
    enabled: !!row && !!accessToken,
    staleTime: 0,
  });
  if (!row) return null;
  const deployments = modelDeployments(row, access.data?.deployments ?? []);
  const prices = marketPrices(row, t);
  const origin = getProxyBaseUrl() || (typeof window !== "undefined" ? window.location.origin : "");
  const links = [
    [t("marketSync.detail.pricingDoc"), row.raw.pricing_page_url],
    [t("marketSync.detail.modelDoc"), row.raw.model_doc_url],
    [t("marketSync.detail.integrationDoc"), row.raw.integration_doc_url],
  ];
  const accessState = !accessToken ? (
    <p>{t("marketSync.detail.signIn")}</p>
  ) : access.isPending ? (
    <p role="status">{t("marketSync.detail.loadingAccess")}</p>
  ) : access.isError ? (
    <p role="alert">
      {t("marketSync.detail.accessFailed")}
      <button onClick={() => void access.refetch()} className="ml-2 underline">
        {t("marketSync.retry")}
      </button>
    </p>
  ) : !deployments.length ? (
    <p>{t("marketSync.detail.undeployed")}</p>
  ) : null;
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-4xl">
        <DialogHeader className="flex-row items-center gap-3">
          <ModelAvatar model={row.id} avatar={row.raw.avatar} issuer={row.raw.issuer} provider={row.provider} className="size-10" />
          <div className="min-w-0 space-y-1 text-left">
            <DialogTitle className="[overflow-wrap:anywhere]">{row.displayName || row.id}</DialogTitle>
            <DialogDescription className="[overflow-wrap:anywhere]">{t("marketSync.detail.marketID", { id: row.id })}</DialogDescription>
          </div>
        </DialogHeader>
        {typeof row.raw.description === "string" && row.raw.description.trim() && (
          <p className="text-sm leading-6 text-muted-foreground">{row.raw.description}</p>
        )}
        <Tabs defaultValue="pricing">
          <TabsList className="mb-4 flex h-auto w-fit max-w-full flex-wrap">
            <TabsTrigger value="pricing">{t("marketSync.detail.pricingTab")}</TabsTrigger>
            <TabsTrigger value="access">{t("marketSync.detail.accessTab")}</TabsTrigger>
            <TabsTrigger value="api">{t("marketSync.detail.apiTab")}</TabsTrigger>
            <TabsTrigger value="docs">{t("marketSync.detail.docsTab")}</TabsTrigger>
          </TabsList>
          <TabsContent value="pricing" className="space-y-4">
            {row.overridden && (
              <div className="space-y-1 rounded-lg border bg-muted/30 p-3 text-sm">
                <p className="font-medium">{t("marketSync.detail.manualPricing")}</p>
                <p>{t("marketSync.detail.effectiveInput", { price: marketMoney(row.input, "$", t), unit: t("marketSync.unit.token") })}</p>
                <p>{t("marketSync.detail.effectiveOutput", { price: marketMoney(row.output, "$", t), unit: t("marketSync.unit.token") })}</p>
                {row.extraPrices.map((price) => (
                  <p key={price.key}>
                    {t("marketSync.detail.extraPrice", { item: price.key, price: marketMoney(price.value, "$", t), unit: price.unit })}
                  </p>
                ))}
              </div>
            )}
            <p className="text-xs text-muted-foreground">
              {t("marketSync.detail.pricingHelp")}
            </p>
            <div className="overflow-x-auto rounded-lg border">
              <table className="w-full text-left text-sm tabular-nums">
                <caption className="sr-only">{t("marketSync.detail.fullPricing")}</caption>
                <thead className="bg-muted/60">
                  <tr>
                    <th scope="col" className="p-3">{t("marketSync.detail.priceColumn")}</th>
                    <th scope="col" className="p-3">{t("marketSync.detail.usd")}</th>
                    <th scope="col" className="p-3">{t("marketSync.detail.cny")}</th>
                    <th scope="col" className="p-3">{t("marketSync.detail.unit")}</th>
                  </tr>
                </thead>
                <tbody>
                  {prices.map((price) => (
                    <tr className="border-t" key={price.tier + ":" + price.key}>
                      <td className="p-3">
                        {price.label}
                        <span className="block text-xs text-muted-foreground">{price.range || t("marketSync.detail.defaultTier")}</span>
                      </td>
                      <td className="p-3">{marketMoney(price.usd, "$", t)}</td>
                      <td className="p-3">{marketMoney(price.cny, "¥", t)}</td>
                      <td className="p-3">{price.unit}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {!prices.length && <p>{t("marketSync.detail.noPrice")}</p>}
          </TabsContent>
          <TabsContent value="access" className="space-y-4">
            <dl className="grid gap-3 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-muted-foreground">{t("marketSync.provider")}</dt>
                <dd>{marketBrandLabel(row, t)}</dd>
              </div>
              {(["input", "output"] as const).map((side) => (
                <div key={side}>
                  <dt className="text-muted-foreground">{t(side === "input" ? "marketSync.inputModalities" : "marketSync.outputModalities")}</dt>
                  <dd>
                    {marketModalities(row, side)
                      .map((m) => Object.hasOwn(MODALITY_NAMES, m) ? t("marketSync.modality." + m) : m)
                      .join(" · ") || t("marketSync.notDeclared")}
                  </dd>
                </div>
              ))}
              <div>
                <dt className="text-muted-foreground">{t("marketSync.detail.protocols")}</dt>
                <dd>{marketStrings(row.raw.support_api_protocols).join(" · ") || t("marketSync.notDeclared")}</dd>
              </div>
            </dl>
            {accessState}
            {deployments.map((item, i) => (
              <div key={i} className="rounded border p-3 text-sm">
                <p>{t("marketSync.detail.publicName", { name: item.model_name })}</p>
                <p className="break-all text-muted-foreground">{t("marketSync.detail.upstream", { name: item.litellm_params?.model ?? t("marketSync.notDeclared") })}</p>
                <p>{t("marketSync.detail.endpoints", { endpoints: item.model_info?.endpoint_types?.join(" · ") || t("marketSync.notDeclared") })}</p>
              </div>
            ))}
            <Link
              className="inline-block text-cyan-700 underline"
              href={uiHref("models-and-endpoints") + "?catalog=" + encodeURIComponent(row.id)}
            >
              {t("marketSync.detail.deploy")}
            </Link>
          </TabsContent>
          <TabsContent value="api" className="space-y-4">
            <p className="text-sm">
              {t("marketSync.detail.gatewayAddress")} <code className="break-all">{origin}</code>
            </p>
            <p className="text-sm text-muted-foreground">
              {t("marketSync.detail.apiHelp")}
            </p>
            {accessState}
            {deployments.map((item, i) => (
              <div key={i} className="space-y-3">
                <h3 className="font-semibold">{item.model_name}</h3>
                {(access.data?.endpoints ?? [])
                  .filter((endpoint) => item.model_info?.endpoint_types?.includes(endpoint.id))
                  .map((endpoint) => {
                    const example = modelCurl(item, endpoint, origin);
                    return (
                      <div key={endpoint.id} className="rounded border p-3">
                        <p className="font-medium">{endpointLabel(endpoint)}</p>
                        {endpoint.paths?.map((path) => (
                          <code className="block break-all text-xs" key={path}>
                            {path}
                          </code>
                        ))}
                        {example ? (
                          <pre className="mt-3 overflow-x-auto rounded bg-muted p-4 text-xs">{example}</pre>
                        ) : (
                          <p className="mt-2 text-sm text-muted-foreground">
                            {t("marketSync.detail.endpointHelp")}
                          </p>
                        )}
                      </div>
                    );
                  })}
              </div>
            ))}
          </TabsContent>
          <TabsContent value="docs" className="space-y-4">
            <p className="text-sm">
              {t("marketSync.detail.workflow")}
            </p>
            <p className="text-sm text-muted-foreground">
              {t("marketSync.detail.errorHelp")}
            </p>
            <ul className="space-y-2">
              {links.map(([label, value]) => {
                const url = marketDocUrl(value);
                return url ? (
                  <li key={String(label)}>
                    <a href={url} target="_blank" rel="noopener noreferrer" className="text-cyan-700 underline">
                      {String(label)}
                    </a>
                  </li>
                ) : null;
              })}
            </ul>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}
