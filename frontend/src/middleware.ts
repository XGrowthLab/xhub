import { NextResponse, type NextRequest } from "next/server";

const GATEWAY = process.env.XHUB_GATEWAY_ORIGIN || "http://127.0.0.1:4000";

/** 控制台可读取的 Next 页面。未匹配的请求由网关处理。 */
const APP_PAGES = new Set([
  "/",
  "/login",
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
  "/search-tools",
  "/caching",
  "/prompts",
  "/mcp/oauth/callback",
]);

// 仅退役旧控制台地址，裸路径的同名网关 API 仍按原契约响应。
const RETIRED_UI_PAGES = new Set([
  "/agents",
  "/workflows",
  "/memory",
  "/tool-policies",
  "/budgets",
  "/policies",
  "/vector-stores",
  "/transform-request",
  "/ui-theme",
  "/model_hub",
]);

// 从前有根地址和 /ui 别名的界面；只拦截这几个精确的读路径。
const RETIRED_STANDALONE_PAGES = new Set([
  "/chat",
  "/chat/api-keys",
  "/chat/credentials",
  "/chat/integrations",
  "/chat/logs",
  "/chat/usage",
  "/connect",
  "/model_hub_table",
  "/model-hub-table",
  "/retired",
  "/tag-management",
]);

/** 清理路径末尾斜杠；参数为 URL 路径，返回用于中间件匹配的路径，空路径归一到根目录。 */
function stripSlash(path: string): string {
  if (path.length > 1 && path.endsWith("/")) {
    return path.slice(0, -1);
  }
  return path || "/";
}

/** 去掉控制台别名前缀；参数为 URL 路径，返回后端或 App Router 识别的路径，保留完整子路径。 */
function uiAliasPath(pathname: string): string {
  return stripSlash(pathname === "/ui" || pathname.startsWith("/ui/") ? pathname.slice(3) : pathname);
}

/** 解析 Next 页面路径；参数为请求路径，返回页面路径或 null。供中间件分流调用，支持 /ui 别名及文档子目录，未知非文档路径交网关，无副作用。 */
export function dashboardAppPath(pathname: string): string | null {
  const path = uiAliasPath(pathname);
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

/** 分发 Next 请求并保留查询参数和请求正文；返回页面、兼容跳转或网关重写。
 * 退役页面读请求返回不可缓存的 410；/gw 和写入保持交给网关，避免拦截同名 API。 */
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
    return NextResponse.rewrite(new URL(stripSlash(pathname.slice(3)) + search, GATEWAY));
  }

  const path = uiAliasPath(pathname);
  const isUIAlias = pathname === "/ui" || pathname.startsWith("/ui/");
  if (req.method === "GET" || req.method === "HEAD") {
    if (RETIRED_STANDALONE_PAGES.has(path) || (isUIAlias && RETIRED_UI_PAGES.has(path))) {
      return new NextResponse(null, { status: 410, headers: { "Cache-Control": "no-store" } });
    }

    // 从历史地址直达当前用量或正式 API 文档，移除中间的说明页。
    if (path === "/api-reference" || path === "/old-usage") {
      const url = req.nextUrl.clone();
      url.pathname = path === "/api-reference" ? "/docs/api/models" : "/ui/usage";
      return NextResponse.redirect(url);
    }

    const appPath = dashboardAppPath(pathname);
    if (appPath) {
      if (!isUIAlias) return NextResponse.next();
      const url = req.nextUrl.clone();
      url.pathname = appPath;
      return NextResponse.rewrite(url);
    }
  }

  return NextResponse.rewrite(new URL(path + search, GATEWAY));
}

export const config = {
  matcher: ["/((?!_next/static|_next/image).*)"],
};
