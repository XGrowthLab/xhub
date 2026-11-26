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
    deployments: [
      row,
      { ...row, id: "down", name: "old", changed: false, reason: "delisted", current: null, proposed: null },
    ],
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

/** 分别以中英文核对统一价与两个市场档位。同行显示完整金额与范围，缓存新增仍单独标注，组件卸载时清理。 */
it.each([
  {
    locale: "zh-CN", input: "输入", range: "输入 Token：0 < 数量 ≤ 272,000", openRange: "输入 Token：数量 > 272,000",
    review: "核对并更新", title: "确认更新 1 个部署", ttl: "12 小时", flat: "统一价", tiered: "改为分档", added: "新增",
  },
  {
    locale: "en", input: "Input", range: "Input tokens: 0 < amount ≤ 272,000", openRange: "Input tokens: amount > 272,000",
    review: "Review and update", title: "Confirm prices for 1 deployments", ttl: "12 hours", flat: "Flat rate", tiered: "Now tiered", added: "Added",
  },
] as const)("groups market tiers under the same item in $locale", async ({ locale, input, range, openRange, review, title, ttl, flat, tiered, added }) => {
  vi.mocked(apiClient.get).mockResolvedValue({
    deployments: [
      {
        ...row,
        proposed: null,
        proposed_tiers: [
          {
            input_range: [0, 272000],
            output_range: [0, -1],
            rates: [row.proposed![0], { ...row.proposed![0], side: "cache_write", variant: "12h" }],
          },
          {
            input_range: [272000, 99999999],
            output_range: [0, 99999999],
            rates: [{ ...row.proposed![0], usd: 0.000004 }],
          },
        ],
      },
    ],
  });
  setup(locale);
  expect((await screen.findAllByText(range))[0]).toBeVisible();
  expect(screen.getByText(new RegExp(ttl))).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: review }));
  const dialog = screen.getByRole("dialog", { name: title });
  const table = within(dialog).getByRole("table");
  const item = within(table).getByRole("rowheader", { name: new RegExp(`^${input}`) });
  const [current, proposed] = within(item.closest("tr")!).getAllByRole("cell");
  expect(current).toHaveTextContent("$1");
  expect(current).toHaveTextContent(flat);
  expect(within(proposed).getByText(range)).toBeVisible();
  expect(within(proposed).getByText(openRange)).toBeVisible();
  expect(within(proposed).getAllByRole("listitem")).toHaveLength(2);
  expect(proposed).toHaveTextContent("$4");
  expect(item).toHaveTextContent(tiered);
  expect(item).not.toHaveTextContent(added);
  expect(table).not.toHaveTextContent("99,999,999");
});

/** 前置搜索、真实秒计价和市场新增价；验证对齐及金额、单位和增减状态。关闭时不写，夹具由组件测试清理。 */
it.each([
  { locale: "zh-CN", review: "核对并更新", searchLabel: "联网搜索", query: "次", second: "秒", unitChange: "单位变化", priceChange: "价格变化", added: "新增", removed: "移除", offpeak: "低谷" },
  { locale: "en", review: "Review and update", searchLabel: "Web search", query: "request", second: "second", unitChange: "Unit changed", priceChange: "Price changed", added: "Added", removed: "Removed", offpeak: "Off-peak" },
] as const)(
  "compares observable units and marks price changes, new or removed items in $locale",
  async ({ locale, review, searchLabel, query, second, unitChange, priceChange, added, removed, offpeak }) => {
    const token = row.current![0];
    const webSearch = {
      ...token,
      measure: "query",
      side: "output",
      variant: "search",
      source_key: "web_search_req",
      usd: 0.01,
    };
    const duration = { ...token, measure: "second", side: "output", usd: 0.04 };
    const audioInput = { ...duration, side: "input", variant: "audio", usd: 0.03 };
    const offpeakToken = { ...token, window: "offpeak" };
    vi.mocked(apiClient.get).mockResolvedValue({
      deployments: [
        {
          ...row,
          current: [token, webSearch, duration, audioInput, offpeakToken],
          proposed: [
            { ...webSearch, usd: 0.02 },
            { ...token, side: "output", usd: 0.000007 },
            { ...token, side: "cache_read", usd: 0 },
            { ...token, usd: 0.000004 },
          ],
        },
      ],
    });
    setup(locale);
    await userEvent.click(await screen.findByRole("button", { name: review }));
    const dialog = within(screen.getByRole("dialog"));
    const search = dialog.getByText(searchLabel, { exact: true }).closest("tr")!;
    expect(
      within(search)
        .getAllByRole("cell")
        .map(({ textContent }) => textContent),
    ).toEqual([`$0.01 / ${query}`, `$0.02 / ${query}`]);
    expect(search).not.toHaveTextContent(unitChange);
    expect(search).toHaveTextContent(priceChange);
    expect(dialog.getByText(unitChange, { exact: true }).closest("tr")).toHaveTextContent(`$0.04 / ${second}`);
    expect(dialog.getByText(added, { exact: true }).closest("tr")).toHaveTextContent("$0 /");
    expect(dialog.getByRole("rowheader", { name: new RegExp(offpeak) }).closest("tr")).toHaveTextContent(removed);
    expect(dialog.getByRole("rowheader", { name: /audio/ }).closest("tr")).toHaveTextContent(removed);
    expect(apiClient.post).not.toHaveBeenCalled();
  },
);

/** 前置多部署快照，切换预览时只渲染一个表且冻结整个批次；提交后验证每个部署及版本，模拟请求不写外部数据。 */
it("reviews one deployment at a time and applies the complete frozen batch", async () => {
  const user = userEvent.setup();
  const second = { ...row, id: "second-id", name: "second", version: "v2" };
  vi.mocked(apiClient.get).mockResolvedValue({ deployments: [row, second] });
  vi.mocked(apiClient.post).mockResolvedValue({ updated: 2 });
  setup("en");
  await user.click(await screen.findByRole("checkbox", { name: "Select all eligible deployments" }));
  await user.click(screen.getByRole("button", { name: "Review selected prices (2)" }));
  const dialog = within(screen.getByRole("dialog"));
  expect(dialog.getByRole("status")).toHaveTextContent("1 of 2");
  expect(dialog.getByRole("button", { name: "Previous deployment" })).toBeDisabled();
  expect(dialog.getByRole("table", { name: "Compare prices for example" })).toBeVisible();
  await user.click(dialog.getByRole("button", { name: "Next deployment" }));
  expect(dialog.getByRole("status")).toHaveTextContent("2 of 2");
  expect(dialog.getAllByRole("table")).toHaveLength(1);
  expect(dialog.getByRole("table", { name: "Compare prices for second" })).toBeVisible();
  expect(apiClient.post).not.toHaveBeenCalled();
  await user.click(dialog.getByRole("button", { name: "Confirm price update" }));
  expect(apiClient.post).toHaveBeenCalledWith("/price/deployments/apply", {
    accessToken: "test",
    body: { items: [{ id: "id", version: "v1" }, { id: "second-id", version: "v2" }] },
  });
  expect(await screen.findByRole("status")).toHaveTextContent("Updated prices for 2 deployments.");
});

/** 前置同输入档位以不同顺序返回且开放上界使用不同标记，另有同名计费维度的不同单位。验证金额不串列，卸载清理。 */
it("does not mark equivalent tier ranges or pair incompatible parallel units", async () => {
  const token = row.current![0];
  const second = { ...token, measure: "second" };
  const tiers = [
    { input_range: [0, 5] as [number, number], output_range: [0, -1] as [number, number], rates: [token, second] },
    { input_range: [5, -1] as [number, number], output_range: [0, -1] as [number, number], rates: [token] },
  ];
  vi.mocked(apiClient.get).mockResolvedValue({ deployments: [{
    ...row, current_tiers: tiers, proposed_tiers: [
      { ...tiers[1], input_range: [5, 99_999_999], output_range: [0, 99_999_999] },
      { ...tiers[0], rates: [token] },
    ],
  }] });
  setup("en");
  const table = within(await screen.findByRole("table", { name: "Compare prices for example" }));
  const tokenRow = table.getByRole("rowheader", { name: "Input 1M tokens", exact: true }).closest("tr")!;
  const [current, proposed] = within(tokenRow).getAllByRole("cell");
  for (const cell of [current, proposed]) {
    expect(cell).toHaveTextContent("Input tokens: amount > 5");
    expect(within(cell).getAllByRole("listitem")).toHaveLength(2);
  }
  const seconds = table.getByRole("rowheader", { name: /Input second/ });
  expect(seconds).toHaveTextContent("Removed");
  expect(within(seconds.closest("tr")!).getAllByRole("cell").at(-1)).toHaveTextContent("—");
});
