import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider, setActiveLocale, type Locale } from "@/i18n";
import { uiHref } from "@/utils/uiHref";
import MCPCredentialsTab from "./MCPCredentialsTab";
import { listMCPUserCredentials } from "../networking";

vi.mock("../networking", () => ({
  listMCPUserCredentials: vi.fn(),
  deleteMCPOAuthUserCredential: vi.fn(),
}));

/** 使用独立查询缓存渲染凭据；locale 为当前语言，返回组件测试界面，不保留网关数据。 */
function renderCredentials(locale: Locale) {
  const client = new QueryClient();
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider initialLocale={locale}>
        <MCPCredentialsTab accessToken="oauth-fixture" />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("MCP credentials", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    setActiveLocale("en");
  });

  /** 前置可用但空的凭据列表；双语入口链接去真实连接路径，无远程写入。 */
  it.each([
    ["zh-CN", "还没有连接", "打开 MCP 连接"],
    ["en", "No connections yet", "Open MCP connections"],
  ] as const)("shows the %s link to the current connection page", async (locale, empty, open) => {
    vi.mocked(listMCPUserCredentials).mockResolvedValue([]);
    renderCredentials(locale);

    expect(await screen.findByText(empty)).toBeVisible();
    expect(screen.getByRole("link", { name: open })).toHaveAttribute("href", uiHref("connect"));
  });

  /** 前置不可用契约，页面明确说明功能关闭；不误报为空，测试数据由独立查询缓存释放。 */
  it("describes a gateway without app credentials", async () => {
    vi.mocked(listMCPUserCredentials).mockResolvedValue(null);
    renderCredentials("zh-CN");
    expect(await screen.findByText("当前网关未提供应用凭据功能。")).toBeVisible();
    expect(screen.queryByText("还没有连接")).not.toBeInTheDocument();
  });

  /** 首次加载异常后通过重试读回凭据；保留服务别名原文，无效及过期日期各有明确状态，测试不写库。 */
  it("retries a failed load and renders saved credentials with localized status", async () => {
    vi.mocked(listMCPUserCredentials)
      .mockRejectedValueOnce(new Error("Test gateway unavailable"))
      .mockResolvedValueOnce([
        {
          server_id: "fixture",
          alias: "文档服务",
          credential_type: "oauth",
          has_credential: true,
          expires_at: "not a date",
        },
        { server_id: "expired", credential_type: "oauth", has_credential: true, expires_at: "2000-01-01T00:00:00Z" },
      ]);
    renderCredentials("zh-CN");
    expect(await screen.findByRole("alert")).toHaveTextContent("无法加载你的应用凭据。");
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByRole("cell", { name: "文档服务" })).toBeVisible();
    expect(screen.getByText("到期时间未知")).toBeVisible();
    expect(screen.getByText("已过期")).toBeVisible();
    fireEvent.click(within(screen.getByRole("row", { name: /文档服务/ })).getByRole("button", { name: "撤销连接" }));
    expect(await screen.findByRole("alertdialog", { name: "撤销此连接？" })).toHaveTextContent("文档服务");
  });
});
