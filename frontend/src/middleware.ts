import { NextResponse, type NextRequest } from "next/server";

const GATEWAY = process.env.XHUB_GATEWAY_ORIGIN || "http://127.0.0.1:4000";

/** Next App Router pages (LiteLLM dashboard). Everything else is the proxy API. */
const APP_PAGES = new Set([
  "/",
  "/login",
  "/connect",
  "/api-keys",
  "/playground",
  "/models-and-endpoints",
  "/model-providers",
  "/models-and-endpoints/catalog",
  "/models-and-endpoints/mine",
  "/mine-models",
  "/price-data",
  "/guardrails",
  "/usage",
  "/message-center",
  "/model-monitor",
  "/logs",
  "/audit-logs",
  "/guardrails-monitor",
  "/teams",
  "/projects",
  "/users",
  "/organizations",
  "/route-templates",
  "/router-settings",
  "/admin-panel",
  "/chat",
  "/chat/api-keys",
  "/chat/credentials",
  "/chat/integrations",
  "/chat/logs",
  "/chat/usage",
  "/model_hub",
  "/model_hub_table",
  "/model-hub-table",
  "/agents",
  "/workflows",
  "/memory",
  "/policies",
  "/search-tools",
  "/vector-stores",
  "/tool-policies",
  "/budgets",
  "/caching",
  "/prompts",
  "/transform-request",
  "/tag-management",
  "/ui-theme",
  "/old-usage",
  "/api-reference",
  "/mcp/oauth/callback",
]);

function stripSlash(path: string): string {
  if (path.length > 1 && path.endsWith("/")) {
    return path.slice(0, -1);
  }
  return path || "/";
}

// 页面路径上的写请求是网关 API。GET /guardrails 渲染控制台，POST /guardrails 创建护栏。
export function appPageWritePath(method: string, pathname: string): string | null {
  if (method === "GET" || method === "HEAD") return null;
  const path = stripSlash(pathname) || "/";
  return APP_PAGES.has(path) ? path : null;
}

/** 解析 Next 页面路径；参数为请求路径，返回页面路径或 null。供中间件分流调用，支持 /ui 别名及文档子目录，未知非文档路径交网关，无副作用。 */
export function dashboardAppPath(pathname: string): string | null {
  let path = pathname;
  if (path === "/ui" || path === "/ui/" || path.startsWith("/ui/")) {
    const rest = path === "/ui" || path === "/ui/" ? "/" : path.slice(3);
    path = stripSlash(rest) || "/";
  } else {
    path = stripSlash(path) || "/";
  }
  // /docs 专用目录交给 Next，未知文章由页面返回 404，不能误转发到数据面。
  return path === "/docs" || path.startsWith("/docs/") || APP_PAGES.has(path) ? path : null;
}

/** 判断帮助配图静态请求；参数为方法与路径，返回是否交给 Next public，供中间件使用。仅放行专属目录读取，写请求及相似 API 仍交网关，无副作用。 */
export function documentationAssetRequest(method: string, pathname: string): boolean {
  return (method === "GET" || method === "HEAD") && pathname.startsWith("/assets/provider-guide/");
}

/** 解析品牌图标读取；参数为方法和完整路径，命中精确 GET/HEAD 路径时返回 Next 图标路由，否则返回 null。
 * 供中间件识别 /ui 别名，避免图标被代理到网关；不接管写请求、子路径或同名前缀 API。 */
export function brandIconReadPath(method: string, pathname: string): string | null {
  if (method !== "GET" && method !== "HEAD") return null;
  return pathname === "/icon.svg" || pathname === "/ui/icon.svg" ? "/icon.svg" : null;
}

/** 按请求方法和路径分发控制台与网关；req 为 Next 请求，返回放行或保留查询参数的重写响应。
 * 品牌图标读取使用精确路由，其余请求沿用既有页面和 API 分流，不更改鉴权或正文。 */
export function middleware(req: NextRequest) {
  const { pathname, search } = req.nextUrl;

  const iconPath = brandIconReadPath(req.method, pathname);
  if (iconPath) {
    if (pathname === iconPath) return NextResponse.next();
    const url = req.nextUrl.clone();
    url.pathname = iconPath;
    return NextResponse.rewrite(url);
  }

  // 帮助截图属于控制台 public，否则图片请求会被代理到数据面并返回 404。
  if (documentationAssetRequest(req.method, pathname)) return NextResponse.next();

  if (pathname.startsWith("/_next") || pathname.startsWith("/favicon") || pathname === "/robots.txt") {
    return NextResponse.next();
  }

  if (pathname === "/gw" || pathname.startsWith("/gw/")) {
    const rest = pathname === "/gw" || pathname === "/gw/" ? "/" : pathname.slice(3);
    return NextResponse.rewrite(new URL((stripSlash(rest) || "/") + search, GATEWAY));
  }

  // LiteLLM proxy mounts the static dashboard at /ui (see uiHref in production).
  if (pathname === "/ui" || pathname === "/ui/" || pathname.startsWith("/ui/")) {
    const appPath = dashboardAppPath(pathname);
    if (!appPath) {
      const rest = pathname === "/ui" || pathname === "/ui/" ? "/" : pathname.slice(3);
      return NextResponse.rewrite(new URL((stripSlash(rest) || "/") + search, GATEWAY));
    }
    const url = req.nextUrl.clone();
    url.pathname = appPath;
    return NextResponse.rewrite(url);
  }

  const writePath = appPageWritePath(req.method, pathname);
  if (writePath) {
    return NextResponse.rewrite(new URL(writePath + search, GATEWAY));
  }

  if (dashboardAppPath(pathname)) {
    return NextResponse.next();
  }

  const apiPath = stripSlash(pathname) || "/";
  return NextResponse.rewrite(new URL(apiPath + search, GATEWAY));
}

export const config = {
  matcher: ["/((?!_next/static|_next/image).*)"],
};
