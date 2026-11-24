import React from "react";
import { fireEvent, render, screen, waitFor, cleanup, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@/i18n";
import * as directory from "@/lib/http/directory";
import { versionUpdateStatus } from "@/lib/http/versionUpdates";
import DirectorySettings from "./DirectorySettings";
import * as enterprise from "@/lib/http/enterprise";
import userEvent from "@testing-library/user-event";

vi.mock("@/lib/http/directory");
vi.mock("@/lib/http/enterprise");
vi.mock("@/lib/http/versionUpdates");
vi.mock("./LoggingSettings/LoggingSettings", () => ({ default: () => null }));

describe("DirectorySettings", () => {
  beforeEach(() => { vi.resetAllMocks(); window.localStorage.clear(); document.cookie = "xhub_locale=; max-age=0; path=/"; vi.mocked(directory.listDirectoryConnections).mockResolvedValue([]); vi.mocked(directory.listDirectoryOrganizations).mockResolvedValue([{organization_id:"org-1",organization_alias:"Company"}]); vi.mocked(directory.listDirectoryTeams).mockResolvedValue([]); vi.mocked(directory.enterprisePublicOrigin).mockImplementation((raw) => raw === "https://console.example.com" ? raw : ""); });
  // 为企业设置提供未配置归档和标准 OIDC 404；读取不涉及外部服务，afterEach 清理组件。
  beforeEach(() => { vi.mocked(enterprise.listSSO).mockResolvedValue([]); vi.mocked(enterprise.readEnterprise).mockResolvedValue({configuration:null, counts:{}, events:[]}); });
  afterEach(cleanup);

  /** 两种语言均按企业身份和日志存储分组；只读取模拟管理接口，测试框架清理组件。 */
  it.each([["zh-CN", "企业身份", "日志与存储", "日志设置"], ["en", "Enterprise identity", "Logs and storage", "Log settings"]] as const)(
    "按职责分组设置页签 %s", async (locale, identity, storage, logs) => {
      const user = userEvent.setup();
      render(<I18nProvider initialLocale={locale}><DirectorySettings /></I18nProvider>);
      expect(within(screen.getByRole("group", { name: identity })).getByRole("tab", { name: "SCIM" })).toBeVisible();
      const logsTab = within(screen.getByRole("group", { name: storage })).getByRole("tab", { name: logs });
      await user.click(logsTab);
      expect(logsTab).toHaveAttribute("aria-selected", "true");
    },
  );

  // 前置为模拟管理接口和中文组件；验证原始名称、一次性凭据、动态按钮与启用，清理组件和 mock。
  it("创建连接保留用户输入并可隐藏凭据和启用", async () => {
    const connection = { id: "directory-1", organization_id: "org-1", name: "研发 Directory", enabled: false };
    vi.mocked(directory.createDirectoryConnection).mockResolvedValue({ connection, token: "secret-once" });
    vi.mocked(directory.configureDirectoryConnection).mockResolvedValue();
    render(<I18nProvider initialLocale="zh-CN"><DirectorySettings /></I18nProvider>);
    fireEvent.click(screen.getByRole("tab", { name: "SCIM" }));
    fireEvent.click(screen.getByRole("button", {name: "配置 SCIM"}));
    await screen.findByRole("option", {name:"Company"});
    fireEvent.change(screen.getByLabelText("XHub 公开访问地址"), {target:{value:"https://console.example.com"}});
    fireEvent.change(screen.getByLabelText("组织 ID"), { target: { value: "org-1" } });
    fireEvent.change(screen.getByLabelText("目录名称"), { target: { value: connection.name } });
    fireEvent.click(screen.getByRole("button", { name: "创建目录连接" }));
    await waitFor(() => expect(screen.getByLabelText("SCIM 同步凭据")).toHaveValue("secret-once"));
    expect(screen.getByLabelText("SCIM 基础地址")).toHaveValue("https://console.example.com/scim/v2/connections/directory-1");
    expect(directory.createDirectoryConnection).toHaveBeenCalledWith("org-1", connection.name);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {configurable:true, value:{writeText}});
    fireEvent.click(screen.getByRole("button", {name:"复制 SCIM 凭据"}));
    await screen.findByText("已复制 SCIM 凭据。");
    expect(writeText).toHaveBeenCalledWith("secret-once");
    writeText.mockRejectedValue(new Error("private clipboard error"));
    fireEvent.click(screen.getByRole("button", {name:"复制 SCIM 凭据"}));
    await screen.findByText("复制失败，请选择凭据手动复制。");
    expect(screen.queryByText("private clipboard error")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "隐藏凭据" }));
    expect(screen.queryByLabelText("SCIM 同步凭据")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", {name: "关闭配置"}));
    fireEvent.click(screen.getByRole("switch", { name: "启用 研发 Directory" }));
    await waitFor(() => expect(directory.configureDirectoryConnection).toHaveBeenCalledWith(connection, true));
    await waitFor(() => expect(screen.getByRole("switch", { name: "停用 研发 Directory" })).toBeChecked());
  });

  // 前置无 SCIM 的组织与可控 SSO 创建接口；验证独立弹窗、失败保留输入和成功仅创建登录配置，清理组件/mock。
  it("无需创建目录即可独立保存 SSO，失败不暴露错误或丢失输入", async () => {
    vi.mocked(enterprise.createSSO).mockRejectedValueOnce(new Error("private identity error")).mockResolvedValueOnce({connection_id:"sso-id", name:"原始 Login", enabled:false});
    vi.mocked(enterprise.readEnterprise).mockResolvedValue({name:"原始 Login", enabled:false, client_id:"client"});
    render(<I18nProvider initialLocale="zh-CN"><DirectorySettings /></I18nProvider>);
    fireEvent.click(await screen.findByRole("button", {name:"配置单点登录"}));
    await screen.findByRole("option", {name:"Company"});
    fireEvent.change(screen.getByLabelText("组织 ID"), {target:{value:"org-1"}});
    fireEvent.change(screen.getByLabelText("单点登录名称"), {target:{value:"原始 Login"}});
    fireEvent.change(screen.getByLabelText("客户端密钥"), {target:{value:"private-secret"}});
    fireEvent.click(screen.getByRole("button", {name:"保存集成配置"}));
    await screen.findByRole("alert");
    expect(screen.getByLabelText("客户端密钥")).toHaveValue("private-secret");
    expect(screen.queryByText("private identity error")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", {name:"保存集成配置"}));
    await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(enterprise.createSSO).toHaveBeenLastCalledWith(expect.objectContaining({organization_id:"org-1", name:"原始 Login", client_secret:"private-secret"}));
    expect(directory.createDirectoryConnection).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("tab", {name:"SCIM"}));
    expect(screen.getByRole("heading", {name:"尚无 SCIM 连接"})).toBeVisible();
  });

  // 前置为英文组件且接口失败；验证英文错误及能力边界，无外部写入，清理组件与 mock。
  it("英文失败状态不会出现未翻译文案", async () => {
    vi.mocked(directory.listDirectoryConnections).mockRejectedValue(new Error("database failure"));
    render(<I18nProvider initialLocale="en"><DirectorySettings /></I18nProvider>);
    expect(screen.getByRole("heading", { name: "Enterprise directory" })).toBeVisible();
    fireEvent.click(screen.getByRole("tab", { name: "SCIM" }));
    fireEvent.click(screen.getByRole("button", {name: "Configure SCIM"}));
    fireEvent.click(within(screen.getByRole("dialog")).getByText("Setup guide"));
    expect(screen.getByText(/atomic PATCH and protocol discovery/)).toBeVisible();
    await waitFor(() => expect(within(screen.getByRole("dialog")).getByRole("alert")).toHaveTextContent("Directory operation failed"));
    expect(screen.queryByText("database failure")).not.toBeInTheDocument();
  });

  // 前置为无目录连接、未启用更新的中文页面；验证配置隔离和新页签的键盘导航，卸载时清理轮询。
  it("空态引导创建目录且配置按页签隔离", async () => {
    vi.mocked(versionUpdateStatus).mockResolvedValue({ enabled: false });
    const user = userEvent.setup();
    render(<I18nProvider initialLocale="zh-CN"><DirectorySettings /></I18nProvider>);
    expect(screen.getByRole("tab", {name: "单点登录设置"})).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByRole("heading", {name: "尚未配置单点登录"})).toBeVisible();
    fireEvent.click(screen.getByRole("button", {name: "配置单点登录"}));
    expect(screen.getByRole("dialog", {name: "配置单点登录"})).toBeVisible();
    expect(screen.getByLabelText("XHub 公开访问地址")).toBeVisible();
    expect(screen.getByLabelText("单点登录名称")).toBeVisible();
    expect(screen.queryByLabelText("目录名称")).not.toBeInTheDocument();
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button",{name:"关闭"}));
    await user.click(screen.getByRole("tab", { name: "日志设置" }));
    expect(screen.getByRole("tabpanel", { name: "日志设置" })).toBeVisible();
    expect(screen.getByRole("region", {name: "七牛 Kodo 计费日志归档"})).toBeVisible();
    expect(screen.queryByLabelText("XHub 公开访问地址")).not.toBeInTheDocument();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: "版本更新" })).toHaveFocus();
    expect(screen.getByRole("tabpanel", { name: "版本更新" })).toBeVisible();
    expect(await screen.findByText("此实例尚未启用主机更新执行器。")).toBeVisible();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", {name: "单点登录设置"})).toHaveFocus();
    await user.keyboard("{End}");
    expect(screen.getByRole("tab", { name: "版本更新" })).toHaveFocus();
    await user.keyboard("{Home}");
    expect(screen.getByRole("tab", {name: "单点登录设置"})).toHaveFocus();
  });

  // 前置已存在目录及可控剪贴板；验证完整 URL 复制成功与失败降级，不暴露凭据，清理组件及 mock。
  it("复制地址成功与失败均给出可见反馈", async () => {
    vi.mocked(directory.listDirectoryConnections).mockResolvedValue([{id: "copy-directory", organization_id: "org-1", name: "Copy Directory", enabled: false}]);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {configurable: true, value: {writeText}});
    render(<I18nProvider initialLocale="zh-CN"><DirectorySettings /></I18nProvider>);
    fireEvent.click(screen.getByRole("tab", {name: "SCIM"}));
    await screen.findByRole("switch", {name: "启用 Copy Directory"});
    fireEvent.click(screen.getByRole("button",{name:"配置 Copy Directory 的 SCIM"}));
    expect(screen.getByRole("button", {name: "复制 SCIM 地址"})).toBeDisabled();
    fireEvent.change(screen.getByLabelText("XHub 公开访问地址"), {target: {value: "https://console.example.com"}});
    fireEvent.click(screen.getByRole("button", {name: "复制 SCIM 地址"}));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("已复制 SCIM 地址"));
    expect(writeText).toHaveBeenCalledWith("https://console.example.com/scim/v2/connections/copy-directory");
    writeText.mockRejectedValue(new Error("denied"));
    fireEvent.click(screen.getByRole("button", {name: "复制 SCIM 地址"}));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("复制失败"));
  });

  // 前置已有停用连接且配置接口拒绝写入；验证 SCIM 开关失败仍停用，SSO 保持独立空态；清理组件和模拟接口。
  it("SCIM 启用失败不改变状态也不伪造 SSO 配置", async () => {
    vi.mocked(directory.listDirectoryConnections).mockResolvedValue([{id:"existing", organization_id:"org-1", name:"原始 Directory", enabled:false}]);
    vi.mocked(directory.configureDirectoryConnection).mockRejectedValue(new Error("private error"));
    render(<I18nProvider initialLocale="zh-CN"><DirectorySettings /></I18nProvider>);
    expect(await screen.findByRole("heading", {name:"尚未配置单点登录"})).toBeVisible();
    fireEvent.click(screen.getByRole("tab", {name:"SCIM"}));
    const toggle = screen.getByRole("switch", {name:"启用 原始 Directory"});
    fireEvent.click(toggle);
    await screen.findByRole("alert");
    expect(toggle).not.toBeChecked();
    expect(screen.queryByText("private error")).not.toBeInTheDocument();
  });

  // 前置为已加载连接，中英文分别运行；验证取消零写入、删除失败保留、重试成功回到空态，清理 DOM 与 mock。
  it.each(["zh-CN", "en"] as const)("%s 删除确认可取消且失败不移除连接", async (locale) => {
    const name = "原始 Directory";
    vi.mocked(directory.listDirectoryConnections).mockResolvedValue([{id:"delete-id", organization_id:"org-1", name, enabled:true}]);
    vi.mocked(directory.deleteDirectoryConnection).mockRejectedValueOnce(new Error("private database error")).mockResolvedValueOnce();
    render(<I18nProvider initialLocale={locale}><DirectorySettings /></I18nProvider>);
    fireEvent.click(screen.getByRole("tab", {name:"SCIM"}));
    const label = locale === "en" ? "Delete " + name : "删除 " + name;
    fireEvent.click(await screen.findByRole("button", {name:label}));
    fireEvent.click(screen.getByRole("button", {name:locale === "en" ? "Cancel" : "取消"}));
    expect(directory.deleteDirectoryConnection).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", {name:label}));
    const confirm = screen.getByRole("button", {name:locale === "en" ? "Delete connection" : "删除连接"});
    fireEvent.click(confirm);
    expect(await screen.findByRole("alert")).toHaveTextContent(locale === "en" ? "Directory operation failed" : "目录操作失败");
    expect(screen.getByRole("dialog")).toBeVisible();
    expect(screen.queryByText("private database error")).not.toBeInTheDocument();
    fireEvent.click(confirm);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByRole("button", {name:label})).not.toBeInTheDocument();
    expect(directory.deleteDirectoryConnection).toHaveBeenLastCalledWith("delete-id");
    expect(screen.getByRole("heading", {name:locale === "en" ? "No SCIM connection found" : "尚无 SCIM 连接"})).toBeVisible();
  });
});
