import { screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import GuardrailsMonitor from "./page";
import { renderWithProviders, testQueryClient } from "../../../../tests/test-utils";
import { I18nProvider } from "@/i18n";

// 这是授权状态夹具，不是 React Hook；命名避免将普通模拟函数误判为 Hook 调用。
const { authorizedStateMock } = vi.hoisted(() => ({ authorizedStateMock: vi.fn() }));

/** 按服务器能力验证访问门禁；角色夹具只用于组织场景，能力不由页面自行推导。 */
vi.mock("@/app/(dashboard)/hooks/useCan", () => ({
  default: (capability: string) => capability === "viewGuardrailUsage" &&
    authorizedStateMock().capabilities?.includes("platform_admin"),
}));

vi.mock("@/app/(dashboard)/hooks/useAuthorized", () => ({
  default: authorizedStateMock,
}));

const fetchMock = vi.fn();

const requestUrl = (input: RequestInfo | URL) => (input instanceof Request ? input.url : String(input));

const requestedUrls = () => fetchMock.mock.calls.map(([input]) => requestUrl(input));

const emptyOverview = {
  rows: [],
  chart: [],
  totalRequests: 0,
  totalBlocked: 0,
  passRate: 100,
  totalUsageUnits: {},
  totalCost: null,
  totalUntrackedUsageUnits: {},
};

const jsonResponse = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });

/** 按给定角色与真实英文语言上下文渲染页面，返回测试句柄；保留能力夹具，DOM 与查询缓存由测试清理。 */
const renderAs = (userRole: string) => {
  authorizedStateMock.mockReturnValue({
    accessToken: "sk-test", userId: "u1", userRole,
    capabilities: userRole === "Admin" ? ["platform_admin"] : [],
  });
  return renderWithProviders(<I18nProvider initialLocale="en"><GuardrailsMonitor /></I18nProvider>);
};

// `/guardrails/usage/*` aggregates across tenants and is listed in
// admin_viewer_routes, so it is proxy-admin-only. Nothing on this page works
// for a non-admin, hence the whole page is gated rather than a section of it.
describe("Guardrails Monitor page access by role", () => {
  beforeEach(() => {
    testQueryClient.clear();
    vi.clearAllMocks();
    fetchMock.mockImplementation(async (input: RequestInfo | URL) =>
      jsonResponse(requestUrl(input).includes("/guardrails/usage/overview") ? emptyOverview : []),
    );
    vi.stubGlobal("fetch", fetchMock);
  });

  it("fetches the guardrails usage overview for an admin", async () => {
    renderAs("Admin");

    await waitFor(() => expect(requestedUrls().some((url) => url.includes("/guardrails/usage/overview"))).toBe(true));
  });

  it.each(["Internal User", "Internal Viewer", "Org Admin", "Unknown Role"])(
    "renders the admin-only notice and fires no usage request for %s",
    async (userRole) => {
      renderAs(userRole);

      expect(await screen.findByText("Guardrails Monitor is only available to admin users.")).toBeInTheDocument();
      await waitFor(() => expect(requestedUrls().filter((url) => url.includes("/guardrails/usage"))).toEqual([]));
    },
  );
});
