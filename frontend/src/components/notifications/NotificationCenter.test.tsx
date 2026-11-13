/* @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { I18nProvider, LOCALE_COOKIE } from "@/i18n";
import { NotificationCenter, canLoadNextNotificationsPage } from "./NotificationCenter";
import { NotificationBell } from "./NotificationBell";
import { listNotifications, unreadNotifications } from "./api";

const { auth, get, post } = vi.hoisted(() => ({ auth: { token: "a" as string | null }, get: vi.fn(), post: vi.fn() }));
vi.mock("@/contexts/AuthContext", () => ({ useAuth: () => auth }));
vi.mock("@/utils/jwtUtils", () => ({
  checkTokenValidity: (token: string | null) => !!token,
  decodeToken: (token: string) => ({ user_id: token, key: `session-${token}` }),
}));
vi.mock("@/components/networking", () => ({ apiClient: { get, post } }));
const budget = {
  id: "n1",
  type: "budget_threshold",
  created_at: "2026-11-13T10:00:00Z",
  read: false,
  data: { threshold: 80, spent: 400, limit: 500, remaining: 100 },
};
/** 用指定语言渲染独立组件；重置本地语言避免其他测试影响，DOM 由框架清理。 */
function view(locale: "en" | "zh-CN", element: React.ReactNode) {
  localStorage.removeItem(LOCALE_COOKIE);
  document.cookie = `${LOCALE_COOKIE}=;path=/;max-age=0`;
  return render(<I18nProvider initialLocale={locale}>{element}</I18nProvider>);
}
beforeEach(() => {
  auth.token = "a";
  get.mockReset();
  post.mockReset();
});
afterEach(() => {
  vi.restoreAllMocks();
});

describe("personal notifications", () => {
  /** 两种语言均清晰展示已知模型变化和固定广场链接；未知项与外部地址不能注入链接。 */
  it.each([
    ["zh-CN", "模型广场更新", "价格变化: example-model", "查看模型广场"],
    ["en", "Model marketplace update", "Price changed: example-model", "View model marketplace"],
  ] as const)("renders market changes safely in %s", async (locale, title, change, link) => {
    get.mockResolvedValue({ items: [{ ...budget, type: "market_update", data: { changes: [
      { kind: "price_changed", model: "example-model", href: "https://invalid.example" },
      { kind: "unknown", model: "DO_NOT_SHOW" },
    ] } }], total: 1 });
    view(locale, <NotificationCenter />);
    expect(await screen.findByRole("heading", { name: title })).toBeVisible();
    expect(screen.getByText(change)).toBeVisible();
    expect(screen.queryByText("DO_NOT_SHOW")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: link })).toHaveAttribute("href", "/ui/price-data");
    expect(document.querySelector('a[href="https://invalid.example"]')).toBeNull();
    expect(get.mock.calls.some(([path]) => path === "/notifications/market-detail")).toBe(false);
  });
  /** 完整报价仅在点击后分段获取；分页、受限摘要、失败重试均有可见状态，不拼接保留所有页。 */
  it("loads market details only on demand, one page at a time and retries failures", async () => {
    let fail = true;
    get.mockImplementation((path: string, options: { query: { offset: number } }) => {
      if (path !== "/notifications/market-detail") return Promise.resolve({ items: [{ ...budget, type: "market_update", data: { changes: [{ kind: "price_changed", model: "example" }], total_changes: 3, summary_truncated: true } }], total: 1 });
      if (fail) { fail = false; return Promise.reject(new Error("unauthorized")); }
      return Promise.resolve(options.query.offset === 0 ? { text: '"previous_price": 1', next_offset: 19 } : { text: '"current_price": 2 <img src=x onerror=alert(1)>', next_offset: null });
    });
    view("en", <NotificationCenter />);
    expect(await screen.findByText("This sync found 3 changes:")).toBeVisible();
    expect(screen.getByText("Only some changes are shown here. View all details page by page.")).toBeVisible();
    expect(get.mock.calls.some(([path]) => path === "/notifications/market-detail")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "View full changes" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load details");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText('"previous_price": 1')).toBeVisible();
    const details = screen.getByRole("region", { name: "View full changes" });
    fireEvent.click(within(details).getByRole("button", { name: "Next" }));
    expect(await screen.findByText(/"current_price": 2/)).toBeVisible();
    expect(details.querySelector("img")).toBeNull();
    expect(screen.queryByText('"previous_price": 1')).not.toBeInTheDocument();
    fireEvent.click(within(details).getByRole("button", { name: "Previous" }));
    expect(await screen.findByText('"previous_price": 1')).toBeVisible();
  });
  /** 切换账户时旧详情响应被中止且不得再次展示，避免跨账号读取内存中的原始报价。 */
  it("discards market detail responses after account change", async () => {
    let finish!: (value: unknown) => void;
    const delayed = new Promise((resolve) => { finish = resolve; });
    get.mockImplementation((path: string, options: { accessToken: string }) => path === "/notifications/market-detail" ? delayed : Promise.resolve(options.accessToken === "session-a" ? { items: [{ ...budget, type: "market_update", data: { changes: [{ kind: "added", model: "m" }] } }], total: 1 } : { items: [], total: 0 }));
    const rendered = view("en", <NotificationCenter />);
    fireEvent.click(await screen.findByRole("button", { name: "View full changes" }));
    auth.token = "b";
    rendered.rerender(<I18nProvider initialLocale="en"><NotificationCenter /></I18nProvider>);
    expect(await screen.findByText("You have no notifications.")).toBeVisible();
    finish({ text: "OLD_PRIVATE_PRICE", next_offset: null });
    await waitFor(() => expect(screen.queryByText("OLD_PRIVATE_PRICE")).not.toBeInTheDocument());
  });
  /** 过长的变化列表显示有限条目并提供剩余数量，空数据不得伪造已完成更新。 */
  it("limits market change previews and rejects malformed changes", async () => {
    get.mockResolvedValue({ items: [
      { ...budget, type: "market_update", data: { changes: Array.from({ length: 10 }, (_, i) => ({ kind: "added", model: `model-${i}` })) } },
      { ...budget, id: "n2", type: "market_update", data: { changes: [{ kind: "added", model: "" }] } },
    ], total: 2 });
    view("zh-CN", <NotificationCenter />);
    fireEvent.click(await screen.findByText("展开其余 2 项变化"));
    expect(screen.getByText("新上架: model-8")).toBeVisible();
    expect(screen.getByText("此类消息暂不支持展示。")).toBeVisible();
  });
  /** 有限预算快照显示中文账面数，标已读只提交自己的消息 ID，顶栏随后刷新。 */
  it("shows the budget snapshot and marks one message read", async () => {
    let isRead = false;
    get.mockImplementation((path: string) =>
      path.endsWith("/unread")
        ? Promise.resolve({ count: isRead ? 0 : 1 })
        : Promise.resolve({ items: [{ ...budget, read: isRead }], total: 1 }),
    );
    post.mockImplementation(async () => {
      isRead = true;
      return {};
    });
    view(
      "zh-CN",
      <>
        <NotificationBell />
        <NotificationCenter />
      </>,
    );
    expect(await screen.findByText("你的个人预算已记账消费达到 80%。")).toBeVisible();
    expect(screen.getByText(/账面剩余/)).toHaveTextContent("$100");
    expect(screen.getByText(/待结算费用/)).toBeVisible();
    expect(screen.getByText(/系统约每分钟检查一次/)).toHaveTextContent("账面数可能有延迟");
    expect(screen.getByRole("link", { name: "查看用量" })).toHaveAttribute("href", "/ui/usage");
    expect(await screen.findByRole("link", { name: "消息中心，1 条未读" })).toHaveAttribute(
      "href",
      "/ui/message-center",
    );
    fireEvent.click(screen.getByRole("button", { name: "标为已读" }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/notifications/read", { accessToken: "session-a", body: { id: "n1" } }),
    );
    await waitFor(() =>
      expect(get.mock.calls.filter(([path]) => path === "/notifications/unread").length).toBeGreaterThan(1),
    );
    expect(await screen.findByText("已读")).toBeVisible();
    expect(screen.queryByRole("button", { name: "标为已读" })).not.toBeInTheDocument();
  });
  /** 缺少剩余值按账面差计算；英文界面不回显未知类型的数据和不受信任的外部链接。 */
  it("uses English and safely handles unknown types and missing balances", async () => {
    get.mockResolvedValue({
      items: [
        { ...budget, data: { threshold: 95, spent: 475, limit: 500 } },
        { ...budget, id: "n2", type: "unknown", data: { secret: "DO_NOT_SHOW", href: "https://invalid.example" } },
      ],
      total: 2,
    });
    view("en", <NotificationCenter />);
    expect(await screen.findByText("Your recorded spending reached 95% of your personal budget.")).toBeVisible();
    expect(screen.getByText(/estimated balance/)).toHaveTextContent("$25");
    expect(screen.getByText(/checks approximately once a minute/)).toHaveTextContent("Recorded amounts may lag");
    expect(screen.getByText("This notification type is not supported yet.")).toBeVisible();
    expect(screen.queryByText("DO_NOT_SHOW")).not.toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "View usage" })).toHaveLength(1);
  });
  /** 零额度是有效阻断状态：展示额度为零，不能误称已消费百分之百。 */
  it("explains a zero budget without implying spending", async () => {
    get.mockResolvedValue({
      items: [{ ...budget, data: { threshold: 100, spent: 0, limit: 0, remaining: 0 } }],
      total: 1,
    });
    view("zh-CN", <NotificationCenter />);
    expect(await screen.findByText("此消息产生时，你的个人预算上限为 $0。")).toBeVisible();
    expect(screen.queryByText(/消费达到 100%/)).not.toBeInTheDocument();
  });
  /** 后台读写失败时提示错误，重试可恢复并且不能误显示已读或空列表。 */
  it("reports load and mark errors", async () => {
    get.mockRejectedValueOnce(new Error("failed")).mockResolvedValue({ items: [budget], total: 1 });
    post.mockRejectedValue(new Error("failed"));
    view("en", <NotificationCenter />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load notifications.");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    fireEvent.click(await screen.findByRole("button", { name: "Mark as read" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not mark this notification as read.");
    expect(screen.getByText("Unread")).toBeVisible();
  });
  /** 换账号时旧响应晚到也不能覆盖当前用户；注销后不再展示旧消息。 */
  it("isolates pending data across account switches and logout", async () => {
    let resolveOld!: (value: unknown) => void;
    const old = new Promise((resolve) => {
      resolveOld = resolve;
    });
    get.mockImplementation((_path: string, options: { accessToken: string }) =>
      options.accessToken === "session-a" ? old : Promise.resolve({ items: [], total: 0 }),
    );
    const rendered = view("en", <NotificationCenter />);
    auth.token = "b";
    rendered.rerender(
      <I18nProvider initialLocale="en">
        <NotificationCenter />
      </I18nProvider>,
    );
    expect(await screen.findByText("You have no notifications.")).toBeVisible();
    resolveOld({ items: [budget], total: 1 });
    await waitFor(() => expect(screen.queryByText(/reached 80%/)).not.toBeInTheDocument());
    auth.token = null;
    rendered.rerender(
      <I18nProvider initialLocale="en">
        <NotificationCenter />
      </I18nProvider>,
    );
    expect(screen.queryByText("You have no notifications.")).not.toBeInTheDocument();
  });
  /** 畸形页与未读数应报错而非显示零；不涉及真实凭据。 */
  it("rejects malformed API results", async () => {
    get.mockResolvedValueOnce({ items: null, total: 0 }).mockResolvedValueOnce({ count: -1 });
    await expect(listNotifications("test", 0, 20)).rejects.toThrow();
    await expect(unreadNotifications("test")).rejects.toThrow();
  });
  /** 未读数接口失败时显示未知状态，不能误提示没有未读消息；测试结束后由框架清理。 */
  it("reports unavailable unread count instead of zero", async () => {
    get.mockRejectedValue(new Error("offline"));
    view("en", <NotificationBell />);
    expect(await screen.findByRole("link", { name: "Notifications, unread count unavailable" })).toBeVisible();
  });
  /** 预算数据损坏时不展示未经验证的数值或用量链接，测试结束后由框架清理。 */
  it("does not render invalid budget values", async () => {
    get.mockResolvedValue({ items: [{ ...budget, data: { threshold: 80, spent: -10, limit: 500 } }], total: 1 });
    view("en", <NotificationCenter />);
    expect(await screen.findByText("This notification type is not supported yet.")).toBeVisible();
    expect(screen.queryByRole("link", { name: "View usage" })).not.toBeInTheDocument();
  });
  /** 服务器只接受偏移不超过 10000；验证边界、正常分页和总数结束，不创建外部数据。 */
  it("honors the API offset cap when deciding whether to load the next page", () => {
    expect(canLoadNextNotificationsPage(0, 21)).toBe(true);
    expect(canLoadNextNotificationsPage(0, 20)).toBe(false);
    expect(canLoadNextNotificationsPage(9980, 10021)).toBe(true);
    expect(canLoadNextNotificationsPage(10000, 10021)).toBe(false);
    expect(canLoadNextNotificationsPage(10000, 10020)).toBe(false);
  });
  /** 分页按钮应使用实际服务端的分页结果，并在最后一页禁用下一页；由测试框架清理。 */
  it("loads the next page and stops at the end", async () => {
    get.mockImplementation((_path: string, options: { query: { offset: number } }) =>
      Promise.resolve({ items: [{ ...budget, id: `n${options.query.offset}` }], total: 21 }),
    );
    view("en", <NotificationCenter />);
    await screen.findByText("Page 1");
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("Page 2")).toBeVisible();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
    expect(get).toHaveBeenCalledWith("/notifications", expect.objectContaining({ query: { offset: 20, limit: 20 } }));
  });
});
