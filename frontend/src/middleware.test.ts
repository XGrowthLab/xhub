import { describe, expect, it } from "vitest";
import { NextRequest } from "next/server";

import { brandIconReadPath, dashboardAppPath, documentationAssetRequest, middleware } from "./middleware";

/** 前置为真实 Next 请求；品牌 SVG 的 GET/HEAD 精确路径交给 Next，/ui 别名保留查询参数，无网络或持久化数据。 */
it.each(["GET", "HEAD"])("serves exact brand icon %s requests through Next", (method) => {
  expect(brandIconReadPath(method, "/icon.svg")).toBe("/icon.svg");
  expect(brandIconReadPath(method, "/ui/icon.svg")).toBe("/icon.svg");
  const direct = middleware(new NextRequest("https://console.example/icon.svg?revision=2", { method }));
  expect(direct.headers.get("x-middleware-next")).toBe("1");
  expect(direct.headers.get("x-middleware-rewrite")).toBeNull();
  const alias = middleware(new NextRequest("https://console.example/ui/icon.svg?revision=2", { method }));
  expect(alias.headers.get("x-middleware-rewrite")).toBe("https://console.example/icon.svg?revision=2");
});

/** 前置为写请求或近似路径；不扩大静态资源白名单，仍按原规则代理网关且保留查询参数，无数据清理。 */
it.each([
  ["POST", "/icon.svg"],
  ["PUT", "/ui/icon.svg"],
  ["DELETE", "/icon.svg"],
  ["GET", "/icon.svg/metadata"],
  ["GET", "/ui/icon.svg/metadata"],
  ["GET", "/icon.svg-api"],
  ["HEAD", "/ui/icon.svg-api"],
])("keeps %s %s on the gateway", (method, pathname) => {
  expect(brandIconReadPath(method, pathname)).toBeNull();
  const response = middleware(new NextRequest(`https://console.example${pathname}?revision=2`, { method }));
  const apiPath = pathname.startsWith("/ui/") ? pathname.slice(3) : pathname;
  const expected = new URL(apiPath + "?revision=2", process.env.XHUB_GATEWAY_ORIGIN || "http://127.0.0.1:4000").href;
  expect(response.headers.get("x-middleware-rewrite")).toBe(expected);
});

/** 目的：帮助配图读取不误代理到网关；前置实际分流函数，正常读取通过，写入及相似路径拒绝，无业务数据清理。 */
it("routes only documentation screenshot reads to public assets", () => {
  expect(documentationAssetRequest("GET", "/assets/provider-guide/provider-form-en.jpg")).toBe(true);
  expect(documentationAssetRequest("HEAD", "/assets/provider-guide/deployment-form-zh.jpg")).toBe(true);
  expect(documentationAssetRequest("POST", "/assets/provider-guide/provider-form-en.jpg")).toBe(false);
  expect(documentationAssetRequest("GET", "/assets/provider-guide-api")).toBe(false);
  expect(documentationAssetRequest("GET", "/v1/models")).toBe(false);
});

const retired = [
  "/agents",
  "/workflows",
  "/memory",
  "/tool-policies",
  "/budgets",
  "/policies",
  "/vector-stores",
  "/transform-request",
  "/ui-theme",
];

describe("dashboardAppPath", () => {
  /** 消息页独立于消息 API；/ui 别名进入页面，原始 API 仍由网关处理。 */
  it("routes the notifications UI without shadowing the notifications API", () => {
    expect(dashboardAppPath("/ui/message-center")).toBe("/message-center");
    expect(dashboardAppPath("/ui/message-center/")).toBe("/message-center");
    expect(dashboardAppPath("/notifications")).toBeNull();
    expect(dashboardAppPath("/notifications/unread")).toBeNull();
  });
  /** 目的：退役 MCP/Skills 管理入口不能作为 Next 页面；前置为真实路由解析器，根路径和 /ui 别名均交给网关，无持久化数据清理。 */
  it("leaves retired MCP and Skills surfaces to the gateway", () => {
    for (const path of ["/mcp-servers", "/skills"]) {
      expect(dashboardAppPath(path), path).toBeNull();
      expect(dashboardAppPath(`/ui${path}`), `/ui${path}`).toBeNull();
    }
  });

  /** 下线页面与别名已移出 App Router；前置实际路径解析器，保留正常页面，无数据清理。 */
  it("lists only the real pages, without retired routes or aliases", () => {
    for (const path of [...retired, "/model_hub_table", "/model-hub-table", "/api-reference", "/chat/integrations"]) {
      expect(dashboardAppPath(path), path).toBeNull();
      expect(dashboardAppPath(`/ui${path}`), path).toBeNull();
    }
    for (const path of ["/search-tools", "/caching", "/prompts", "/model_hub", "/tag-management", "/old-usage"]) {
      expect(dashboardAppPath(path), path).toBe(path);
      expect(dashboardAppPath(`/ui${path}`), path).toBe(path);
    }
  });

  it("still renders /logs and /models-and-endpoints", () => {
    expect(dashboardAppPath("/logs")).toBe("/logs");
    expect(dashboardAppPath("/ui/logs")).toBe("/logs");
    expect(dashboardAppPath("/models-and-endpoints")).toBe("/models-and-endpoints");
    expect(dashboardAppPath("/ui/models-and-endpoints")).toBe("/models-and-endpoints");
    expect(dashboardAppPath("/mine-models")).toBe("/mine-models");
    expect(dashboardAppPath("/ui/mine-models")).toBe("/mine-models");
    expect(dashboardAppPath("/price-data")).toBe("/price-data");
    expect(dashboardAppPath("/ui/price-data")).toBe("/price-data");
  });

  it("renders the oauth callback in Next", () => {
    expect(dashboardAppPath("/mcp/oauth/callback")).toBe("/mcp/oauth/callback");
  });

  it("renders the audit page and leaves audit API requests for the gateway", () => {
    expect(dashboardAppPath("/audit-logs")).toBe("/audit-logs");
    expect(dashboardAppPath("/ui/audit-logs")).toBe("/audit-logs");
    expect(dashboardAppPath("/audit/logs")).toBeNull();
  });
});

/** 目的：文档首页、子目录和未知文章均由 Next 处理；前置路由分流函数，验证 /ui 别名及相近 API 不误匹配，无持久化数据。 */
it("routes the documentation namespace without swallowing similar API paths", () => {
  for (const path of ["/docs", "/docs/api/models", "/docs/missing"]) {
    expect(dashboardAppPath(path)).toBe(path);
    expect(dashboardAppPath("/ui" + path + "/")).toBe(path);
  }
  expect(dashboardAppPath("/docs-api")).toBeNull();
  expect(dashboardAppPath("/v1/models")).toBeNull();
});

/** 前置模型监控页面已注册，验证直达和 /ui 别名均交给页面、统计 API 仍交后台；纯路径检查无需清理。 */
it("routes model monitor pages separately from monitor API", () => {
  expect(dashboardAppPath("/model-monitor")).toBe("/model-monitor");
  expect(dashboardAppPath("/ui/model-monitor/")).toBe("/model-monitor");
  expect(dashboardAppPath("/models/monitor")).toBeNull();
});

/** 明确的 /ui 旧书签读请求进入下线页；前置真实 Next 请求，保留查询参数，裸路径继续代理网关，无数据写入。 */
it.each(["GET", "HEAD"])("rewrites only explicit console %s reads to the retired page", (method) => {
  const gateway = process.env.XHUB_GATEWAY_ORIGIN || "http://127.0.0.1:4000";
  for (const path of retired) {
    const ui = middleware(new NextRequest(`https://console.example/ui${path}?filter=keep`, { method }));
    expect(ui.headers.get("x-middleware-rewrite")).toBe("https://console.example/retired?filter=keep");
    const plain = middleware(new NextRequest(`https://console.example${path}?filter=keep`, { method }));
    expect(plain.headers.get("x-middleware-rewrite")).toBe(new URL(path + "?filter=keep", gateway).href);
  }
});

/** 兼容地址统一 307 到正式页面；前置 GET/HEAD，保留多值、空值和编码参数，无数据清理。 */
it.each(["GET", "HEAD"])("redirects valid %s aliases without dropping query parameters", (method) => {
  const query = "?key=fixture%2Bkey&label=a&label=b&flag=";
  for (const [alias, target] of [
    ["/model_hub_table", "/model_hub"],
    ["/ui/model_hub_table", "/model_hub"],
    ["/model-hub-table", "/model_hub"],
    ["/ui/model-hub-table", "/model_hub"],
    ["/ui/model_hub", "/model_hub"],
    ["/api-reference", "/docs/api/models"],
    ["/ui/api-reference", "/docs/api/models"],
    ["/chat/integrations", "/ui/connect"],
    ["/ui/chat/integrations", "/ui/connect"],
  ]) {
    const response = middleware(new NextRequest(`https://console.example${alias}${query}`, { method }));
    expect(response.status, alias).toBe(307);
    expect(response.headers.get("location"), alias).toBe(`https://console.example${target}${query}`);
  }
});

/** 验证同名 API 的方法分流；实际 Next 请求测试写入仍交网关，且 /gw 读取和规则页面正常，无数据清理。 */
it.each([
  ["POST", "/policies", "/policies"],
  ["POST", "/ui/policies", "/policies"],
  ["PUT", "/ui/policies/id", "/policies/id"],
  ["POST", "/guardrails", "/guardrails"],
  ["POST", "/ui/guardrails", "/guardrails"],
  ["POST", "/ui/api-reference", "/api-reference"],
  ["GET", "/gw/budgets", "/budgets"],
  ["GET", "/gw/model_hub", "/model_hub"],
  ["GET", "/ui/budgets/detail", "/budgets/detail"],
  ["GET", "/v1/memory", "/v1/memory"],
])("keeps %s %s on the gateway", (method, pathname, path) => {
  const response = middleware(new NextRequest(`https://console.example${pathname}?page=2`, { method }));
  expect(response.headers.get("location")).toBeNull();
  expect(response.headers.get("x-middleware-rewrite")).toBe(
    new URL(path + "?page=2", process.env.XHUB_GATEWAY_ORIGIN || "http://127.0.0.1:4000").href,
  );
});
