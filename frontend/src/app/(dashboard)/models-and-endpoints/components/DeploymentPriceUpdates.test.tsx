import { vi, it, expect, beforeEach } from "vitest";
import { render, screen, cleanup, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider, LOCALE_COOKIE } from "@/i18n";
import { apiClient } from "@/components/networking";
import { DeploymentPriceUpdates, displayRate, type DeploymentPrice } from "./DeploymentPriceUpdates";
vi.mock("@/components/networking", () => ({ apiClient: { get: vi.fn(), post: vi.fn() } }));
const row: DeploymentPrice = {
  id: "id",
  name: "example",
  base_model: "example",
  manual: true,
  current: [{ measure: "token", side: "input", window: "all", usd: 0.000001, source_key: "input" }],
  proposed: [{ measure: "token", side: "input", window: "all", usd: 0.000002, source_key: "input" }],
  changed: true,
  reason: "",
  version: "v1",
};
/** 每例移除本地语言偏好与模拟调用；无需外部服务，组件卸载和查询缓存由测试销毁。 */
beforeEach(() => {
  cleanup();
  vi.resetAllMocks();
  localStorage.removeItem(LOCALE_COOKIE);
  document.cookie = `${LOCALE_COOKIE}=; path=/; max-age=0`;
});
/** 渲染真实翻译和独立查询客户端；参数为语言，返回渲染结果；不共享查询缓存。 */
function setup(locale: "en" | "zh-CN") {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <I18nProvider initialLocale={locale}>
        <DeploymentPriceUpdates accessToken="test" />
      </I18nProvider>
    </QueryClientProvider>,
  );
}
/** 验证人民币或旧价不会混入美元展示，零价保持真实零且小额不丢失；纯函数无清理。 */
it("formats per-million USD precisely", () => {
  expect(displayRate(row.current![0])).toBe("$1");
  expect(displayRate({ ...row.current![0], usd: 0 })).toBe("$0");
  expect(displayRate({ ...row.current![0], usd: 0.000000001 })).toBe("$0.001");
});
/** 前置可更新手工价格，验证中文默认未选择、确认可取消、请求仅传版本；模拟写操作失败后提示而非假成功。 */
it("requires confirmation and retains failure feedback in Chinese", async () => {
  vi.mocked(apiClient.get).mockResolvedValue({ deployments: [row] });
  vi.mocked(apiClient.post).mockRejectedValue(new Error("conflict"));
  setup("zh-CN");
  await userEvent.click(await screen.findByRole("button", { name: "核对并更新" }));
  const dialog = screen.getByRole("dialog");
  expect(dialog).toHaveTextContent("此操作会覆盖该部署的手工价格。");
  expect(screen.queryByText("高峰")).not.toBeInTheDocument();
  expect(apiClient.post).not.toHaveBeenCalled();
  await userEvent.click(within(dialog).getByRole("button", { name: "取消" }));
  expect(apiClient.post).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("checkbox", { name: "选择全部可更新部署" }));
  await userEvent.click(screen.getByRole("button", { name: "核对已选价格（1）" }));
  await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "确认更新价格" }));
  expect(await within(screen.getByRole("dialog")).findByRole("alert")).toHaveTextContent("更新未确认");
  expect(apiClient.post).toHaveBeenCalledWith("/price/deployments/apply", {
    accessToken: "test",
    body: { items: [{ id: "id", version: "v1" }] },
  });
});
/** 英文正常成功、来源下架禁用及缺价提示；验证动态计数与未知项不误报为免费，自动卸载。 */
it("shows English supported and delisted states", async () => {
  vi.mocked(apiClient.get).mockResolvedValue({
    deployments: [row, { ...row, id: "down", name: "old", changed: false, reason: "delisted", current: null }],
  });
  vi.mocked(apiClient.post).mockResolvedValue({ updated: 1 });
  setup("en");
  expect(await screen.findByText("Delisted or private at source; current prices retained")).toBeVisible();
  expect(screen.getByRole("checkbox", { name: "Select old" })).toBeDisabled();
  expect(screen.getByText("Price unavailable")).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "Review and update" }));
  await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Confirm price update" }));
  expect(await screen.findByRole("status")).toHaveTextContent("Updated prices for 1 deployments.");
});
/** 后台读取失败时不能选择或更新；仅展示本地化可恢复错误，不将失败当作空清单。 */
it("does not offer updates after a failed read", async () => {
  vi.mocked(apiClient.get).mockRejectedValue(new Error("offline"));
  setup("en");
  expect(await screen.findByRole("alert")).toHaveTextContent("Could not load price comparisons");
  expect(screen.getByRole("button", { name: "Review selected prices (0)" })).toBeDisabled();
});

/** 自定义历史计价维度未收录翻译时保留标识，标准维度必须中文化；无后台写入，组件清理复用 beforeEach。 */
it("localizes known dimensions and preserves custom identifiers", async () => {
  vi.mocked(apiClient.get).mockResolvedValue({
    deployments: [
      {
        ...row,
        current: [
          { ...row.current![0], variant: "uncached" },
          { ...row.current![0], variant: "resolution_4k" },
        ],
      },
    ],
  });
  setup("zh-CN");
  expect(await screen.findByText("输入 · 非缓存")).toBeVisible();
  expect(screen.getByText("输入 · resolution_4k")).toBeVisible();
  expect(screen.getAllByText(/百万 Token/).length).toBeGreaterThan(0);
  expect(document.body).not.toHaveTextContent("marketSync.");
});
