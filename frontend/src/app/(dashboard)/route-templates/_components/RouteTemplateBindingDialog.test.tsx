import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, expect, it, vi } from "vitest";
import {
  getRouteTemplateBindingCall,
  keyListCall,
  organizationListCall,
  setRouteTemplateBindingCall,
  teamListCall,
} from "@/components/networking";
import { ApiError } from "@/lib/http/client";
import { setActiveLocale } from "@/i18n";
import RouteTemplateBindingDialog from "./RouteTemplateBindingDialog";

vi.mock("@/components/networking", () => ({
  getRouteTemplateBindingCall: vi.fn(),
  keyListCall: vi.fn(),
  organizationListCall: vi.fn(),
  setRouteTemplateBindingCall: vi.fn(),
  teamListCall: vi.fn(),
}));

const builtin = { scope_type: "builtin" as const };
const teamDefault = { template_id: "parent", name: "Parent", scope_type: "team" as const };
const saved = { route_template_id: "", effective: teamDefault, inherited: teamDefault };

/** 安装只返回隔离测试数据的接口，重置语言与 Mock，供各用例独立建立绑定会话。 */
beforeEach(() => {
  setActiveLocale("en");
  vi.resetAllMocks();
  vi.mocked(keyListCall).mockResolvedValue({
    keys: [{ token_id: "key-id", key_alias: "Test key", key_prefix: "sk-demo" }],
    total_pages: 1,
  });
  vi.mocked(organizationListCall).mockResolvedValue([{ organization_id: "org-id", organization_alias: "Acme" }]);
  vi.mocked(teamListCall).mockResolvedValue([{ team_id: "team-id", team_alias: "Research" }]);
  vi.mocked(getRouteTemplateBindingCall).mockResolvedValue(saved);
});

/** 用无重试查询缓存渲染模板应用弹窗，返回用户操作器和刷新监听，组件卸载时框架清理。 */
function setup() {
  const onApplied = vi.fn();
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouteTemplateBindingDialog
        accessToken="session"
        template={{ id: "target", name: "Example" }}
        onClose={vi.fn()}
        onApplied={onApplied}
      />
    </QueryClientProvider>,
  );
  return { user: userEvent.setup(), onApplied };
}

/** 通过可访问选择器选目标。接收操作器和选项名，成功后返回，无网络副作用之外的写入。 */
async function choose(user: ReturnType<typeof userEvent.setup>, name: string) {
  const target = screen.getByLabelText("Select a target");
  await waitFor(() => expect(target).toBeEnabled());
  await user.click(target);
  await user.click(await screen.findByRole("option", { name }));
}

/** 前置继承团队规则和带遮罩的密钥。验证应用使用真实对象 ID 和预期绑定，状态来自响应，卸载清理 UI。 */
it("applies to one key and reports the resolved result", async () => {
  const direct = { template_id: "target", name: "Example", scope_type: "key" as const };
  vi.mocked(getRouteTemplateBindingCall).mockResolvedValueOnce(saved).mockResolvedValue({
    route_template_id: "target",
    effective: direct,
    inherited: teamDefault,
  });
  vi.mocked(setRouteTemplateBindingCall).mockResolvedValue({ effective: direct });
  const { user, onApplied } = setup();
  await choose(user, "Test key · sk-demo");
  expect(await screen.findByText("Parent (from Team)")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Confirm application" }));
  expect(await screen.findByText("Binding updated. Now in effect: Example (from Key)")).toBeVisible();
  expect(setRouteTemplateBindingCall).toHaveBeenCalledExactlyOnceWith("session", "key", "key-id", { id: "target", expected: "" });
  expect(onApplied).toHaveBeenCalledOnce();
});

/** 前置其他操作者先修改绑定。验证 409 后重新读取、保留目标并显示新模板，无实际资源写入。 */
it("keeps the target and refreshes after a conflict", async () => {
  vi.mocked(getRouteTemplateBindingCall)
    .mockResolvedValueOnce(saved)
    .mockResolvedValue({
      route_template_id: "other",
      effective: { template_id: "other", name: "Changed", scope_type: "key" },
      inherited: teamDefault,
    });
  vi.mocked(setRouteTemplateBindingCall).mockRejectedValue(new ApiError("conflict", 409, {}));
  const { user, onApplied } = setup();
  await choose(user, "Test key · sk-demo");
  await user.click(await screen.findByRole("button", { name: "Confirm application" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Another change updated this binding");
  expect(await screen.findByText("Changed (from Key)")).toBeVisible();
  expect(screen.getByLabelText("Select a target")).toHaveTextContent("Test key");
  expect(onApplied).not.toHaveBeenCalled();
});

/** 前置已直接绑定的团队，验证中文恢复继承、实际生效默认值和提交参数，卸载清理 UI。 */
it("restores inheritance on a team using the current locale", async () => {
  setActiveLocale("zh-CN");
  vi.mocked(getRouteTemplateBindingCall)
    .mockResolvedValueOnce({
      route_template_id: "target",
      effective: { template_id: "target", name: "Example", scope_type: "team" },
      inherited: builtin,
    })
    .mockResolvedValue({ route_template_id: "", effective: builtin, inherited: builtin });
  vi.mocked(setRouteTemplateBindingCall).mockResolvedValue({ effective: builtin });
  const { user } = setup();
  const dialog = screen.getByRole("dialog", { name: "应用「Example」" });
  await user.click(within(dialog).getByLabelText("范围"));
  await user.click(await screen.findByRole("option", { name: "团队" }));
  const target = within(dialog).getByLabelText("选择应用对象");
  await waitFor(() => expect(target).toBeEnabled());
  await user.click(target);
  await user.click(await screen.findByRole("option", { name: "Research" }));
  await user.click(await screen.findByRole("button", { name: "恢复继承" }));
  expect(await screen.findByText("绑定已更新。当前生效：内置模型默认配置")).toBeVisible();
  expect(setRouteTemplateBindingCall).toHaveBeenCalledExactlyOnceWith("session", "team", "team-id", { id: "", expected: "target" });
});

/** 前置组织读取失败。验证表单明确反馈，确认按钮不可用，网络 mock 不产生写入。 */
it("blocks writes when the current organization binding is unavailable", async () => {
  vi.mocked(getRouteTemplateBindingCall).mockRejectedValue(new ApiError("not found", 404, {}));
  const { user } = setup();
  await user.click(screen.getByLabelText("Scope"));
  await user.click(await screen.findByRole("option", { name: "Organization" }));
  await choose(user, "Acme");
  expect(await screen.findByRole("alert")).toHaveTextContent("Could not read the current binding");
  expect(screen.getByRole("button", { name: "Confirm application" })).toBeDisabled();
  expect(setRouteTemplateBindingCall).not.toHaveBeenCalled();
});
