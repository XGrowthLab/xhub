import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type {
  GuardrailUsageOverview,
  GuardrailUsageOverviewRow,
} from "@/app/(dashboard)/hooks/guardrails/useGuardrailsUsage";
import { I18nProvider } from "@/i18n";
import { GuardrailsOverview } from "./GuardrailsOverview";

const useGuardrailsUsageOverviewMock = vi.fn();
vi.mock("@/app/(dashboard)/hooks/guardrails/useGuardrailsUsage", () => ({
  useGuardrailsUsageOverview: (...args: unknown[]) => useGuardrailsUsageOverviewMock(...args),
}));

vi.mock("./ScoreChart", () => ({
  ScoreChart: () => <div>Score chart</div>,
}));

const baseRow: GuardrailUsageOverviewRow = {
  id: "guardrail",
  name: "Guardrail",
  type: "content_filter",
  provider: "XHub",
  requestsEvaluated: 0,
  failRate: 0,
  avgScore: null,
  avgLatency: null,
  status: "healthy",
  trend: "stable",
  usageUnits: {},
  cost: null,
  untrackedUsageUnits: {},
};

const overview: GuardrailUsageOverview = {
  rows: [
    {
      ...baseRow,
      id: "guardrail-low",
      name: "Low Failure Guardrail",
      requestsEvaluated: 1200,
      failRate: 2.5,
      avgLatency: 45,
      trend: "down",
    },
    {
      ...baseRow,
      id: "guardrail-high",
      name: "High Failure Guardrail",
      provider: "Bedrock",
      requestsEvaluated: 300,
      failRate: 18,
      status: "warning",
      trend: "up",
      usageUnits: { contentPolicyUnits: 1000, sensitiveInformationPolicyUnits: 250 },
      cost: 0.15,
      untrackedUsageUnits: { sensitiveInformationPolicyUnits: 250 },
    },
    {
      ...baseRow,
      id: "guardrail-free",
      name: "Free Bedrock Guardrail",
      provider: "Bedrock",
      requestsEvaluated: 10,
      failRate: 0,
      usageUnits: { contentPolicyUnits: 40 },
      cost: 0,
    },
  ],
  chart: [],
  totalRequests: 1510,
  totalBlocked: 84,
  passRate: 94.4,
  totalUsageUnits: { contentPolicyUnits: 1040, sensitiveInformationPolicyUnits: 250 },
  totalCost: 0.15,
  totalUntrackedUsageUnits: { sensitiveInformationPolicyUnits: 250 },
};

function renderOverview(onSelectGuardrail = vi.fn()) {
  return render(
    <GuardrailsOverview
      accessToken="test-token"
      startDate="2026-08-01"
      endDate="2026-08-12"
      onSelectGuardrail={onSelectGuardrail}
    />,
  );
}

const rowNamed = (name: string) => screen.getByRole("row", { name: new RegExp(name) });

describe("GuardrailsOverview", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useGuardrailsUsageOverviewMock.mockReturnValue({ data: overview, isLoading: false, error: null });
  });

  /** 使用含已计价和异常护栏的监控响应，验证中文标题、表头、状态及费用计算说明；组件自动卸载。 */
  it("监控指标和计算说明完整使用中文", async () => {
    const user = userEvent.setup();
    render(<I18nProvider initialLocale="zh-CN"><GuardrailsOverview startDate="2026-08-01" endDate="2026-08-12" onSelectGuardrail={vi.fn()} /></I18nProvider>);
    expect(await screen.findByText("评估次数")).toBeVisible();
    expect(screen.getByText("通过率")).toBeVisible();
    expect(screen.getByRole("columnheader", { name: /失败率/ })).toBeVisible();
    expect(await screen.findByText("警告")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "如何计算？" }));
    expect(await screen.findByText("护栏费用 + 护栏费用 + … = 总费用")).toBeVisible();
    expect(screen.getByText(/有 250 个单位无已知单价，未计入费用/)).toBeVisible();
  });

  it("asks for the usage overview of the selected window", () => {
    renderOverview();

    expect(useGuardrailsUsageOverviewMock).toHaveBeenCalledWith({
      accessToken: "test-token",
      startDate: "2026-08-01",
      endDate: "2026-08-12",
    });
  });

  it("renders performance data and selects a guardrail", async () => {
    const onSelectGuardrail = vi.fn();
    const user = userEvent.setup();

    renderOverview(onSelectGuardrail);

    expect(await screen.findByRole("columnheader", { name: "Guardrail" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: /Requests/ })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: /Fail Rate/ })).toBeInTheDocument();
    expect(await screen.findByText("Low Failure Guardrail")).toBeInTheDocument();
    expect(screen.getByText("1,200")).toBeInTheDocument();
    expect(screen.getByText("18%")).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "45ms", exact: true })).toBeInTheDocument();

    const rows = screen.getAllByRole("row");
    expect(rows[1]).toHaveTextContent("High Failure Guardrail");
    expect(rows[2]).toHaveTextContent("Low Failure Guardrail");

    await user.click(screen.getByRole("button", { name: "Low Failure Guardrail" }));

    expect(onSelectGuardrail).toHaveBeenCalledWith("guardrail-low");
  });

  it("shows each guardrail's usage units and cost, marking the units cost leaves out", async () => {
    renderOverview();

    expect(await screen.findByRole("columnheader", { name: "Usage Units" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: /Cost/ })).toBeInTheDocument();

    const priced = rowNamed("High Failure Guardrail");
    expect(within(priced).getByText("1,250")).toBeInTheDocument();
    expect(within(priced).getByText("$0.1500")).toBeInTheDocument();
    expect(within(priced).getByLabelText("250 units unpriced")).toBeInTheDocument();

    const free = rowNamed("Free Bedrock Guardrail");
    expect(within(free).getByText("40")).toBeInTheDocument();
    expect(within(free).getByText("$0.0000")).toBeInTheDocument();
    expect(within(free).queryByLabelText(/unpriced/)).not.toBeInTheDocument();

    const unmetered = rowNamed("Low Failure Guardrail");
    expect(within(unmetered).getAllByText("—")).toHaveLength(2);
  });

  it("breaks the usage units down per counter on hover", async () => {
    const user = userEvent.setup();
    renderOverview();

    await user.hover(within(rowNamed("High Failure Guardrail")).getByText("1,250"));

    expect(await screen.findByText("Content Policy: 1,000")).toBeInTheDocument();
    expect(screen.getByText("Sensitive Information Policy: 250")).toBeInTheDocument();
  });

  it("sorts by cost when its header is clicked, keeping guardrails with no known cost last either way", async () => {
    const user = userEvent.setup();
    renderOverview();
    const rowNames = () =>
      screen
        .getAllByRole("row")
        .slice(1)
        .map((r) => r.textContent ?? "");

    await user.click(await screen.findByRole("button", { name: /Cost/ }));
    await waitFor(() => expect(rowNames()[0]).toContain("Free Bedrock Guardrail"));
    expect(rowNames()[1]).toContain("High Failure Guardrail");
    expect(rowNames()[2]).toContain("Low Failure Guardrail");

    await user.click(screen.getByRole("button", { name: /Cost/ }));
    await waitFor(() => expect(rowNames()[0]).toContain("High Failure Guardrail"));
    expect(rowNames()[1]).toContain("Free Bedrock Guardrail");
    expect(rowNames()[2]).toContain("Low Failure Guardrail");
  });

  /** 前置完整指标；验证页面介绍和有效控件，测试库卸载 DOM，无额外资源。 */
  it("renders the page header without an inactive export action", async () => {
    renderOverview();

    expect(await screen.findByRole("heading", { name: "Guardrails Monitor", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("Review guardrail decisions, added latency, and costs for the selected period.")).toBeInTheDocument();
    expect(document.querySelector(".lucide-heart-pulse")).not.toBeNull();
    expect(screen.queryByRole("button", { name: /Export Data/i })).not.toBeInTheDocument();
  });

  it("renders every summary metric card", async () => {
    renderOverview();

    expect(await screen.findByText("1,510")).toBeInTheDocument();
    expect(screen.getByText("Total Evaluations")).toBeInTheDocument();
    expect(screen.getByText("Blocked Requests")).toBeInTheDocument();
    expect(screen.getByText("84")).toBeInTheDocument();
    expect(screen.getByText("Pass Rate")).toBeInTheDocument();
    expect(screen.getByText("94.4%")).toBeInTheDocument();
    const summary = within(screen.getByRole("region", { name: "Guardrail summary" }));
    expect(summary.getByRole("group", { name: "Avg. latency added" })).toHaveTextContent("45ms");
    expect(screen.getByText("Active Guardrails")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
  });

  it("totals guardrail cost across the window and says how many units it leaves out", async () => {
    renderOverview();

    const card = await screen.findByRole("group", { name: "Guardrail Cost" });
    expect(card).toHaveTextContent("$0.1500");
    expect(card).toHaveTextContent("250 units unpriced");
  });

  it("lays the guardrail cost total out per guardrail", async () => {
    const user = userEvent.setup();
    renderOverview();

    const card = await screen.findByRole("group", { name: "Guardrail Cost" });
    await user.click(within(card).getByRole("button", { name: /How is this calculated/ }));

    const dialog = await screen.findByRole("dialog", { name: "How this cost is calculated" });
    const cells = within(dialog)
      .getAllByRole("row")
      .map((row) =>
        within(row)
          .getAllByRole("cell")
          .map((cell) => cell.textContent ?? ""),
      );
    expect(cells).toEqual([
      ["High Failure Guardrail", "$0.1500"],
      ["Free Bedrock Guardrail", "$0.0000"],
      ["Total", "$0.1500"],
    ]);
    expect(within(dialog).getByText(/250 units with no known price are left out of the cost/)).toBeInTheDocument();
    const issueLink = within(dialog).getByRole("link", { name: "Request pricing on GitHub" });
    const issueUrl = new URL(issueLink.getAttribute("href") ?? "");
    expect(issueUrl.searchParams.get("template")).toBe("feature_request.yml");
    expect(issueUrl.searchParams.get("the-feature")).toContain("sensitiveInformationPolicyUnits");
  });

  it("shows a dash for guardrail cost when nothing in the window was priced", async () => {
    useGuardrailsUsageOverviewMock.mockReturnValue({
      data: { ...overview, totalCost: null, totalUntrackedUsageUnits: {} },
      isLoading: false,
      error: null,
    });
    renderOverview();

    const card = await screen.findByRole("group", { name: "Guardrail Cost" });
    expect(card).toHaveTextContent("—");
    expect(card).not.toHaveTextContent("unpriced");
  });

  /** 前置性能数据，验证标题层级、表格区域和详情说明；测试库自动清理。 */
  it("renders the table toolbar heading and its description", async () => {
    renderOverview();

    const performance = within(await screen.findByRole("region", { name: "Guardrail Performance" }));
    expect(performance.getByRole("heading", { name: "Guardrail Performance", level: 2 })).toBeInTheDocument();
    expect(performance.getByText("Open a guardrail to see its usage and logs.")).toBeInTheDocument();
  });

  /** 前置后台记录 0ms、45ms 与未知值，平均值保留真正的零值并排除未知值；无外部资源。 */
  it("averages only measured latency, including an observed zero", async () => {
    useGuardrailsUsageOverviewMock.mockReturnValue({
      data: { ...overview, rows: [...overview.rows, { ...baseRow, id: "zero-latency", avgLatency: 0 }] },
      isLoading: false,
      error: null,
    });
    renderOverview();
    const summary = within(await screen.findByRole("region", { name: "Guardrail summary" }));
    expect(summary.getByRole("group", { name: "Avg. latency added" })).toHaveTextContent("23ms");
  });

  /** 模拟没有请求与耗时数据的时段，验证实际零计数与不可计算的指标可区分；无外部资源。 */
  it("distinguishes zero evaluations from an unobserved rate or latency", async () => {
    useGuardrailsUsageOverviewMock.mockReturnValue({
      data: { ...overview, rows: [], totalRequests: 0, totalBlocked: 0, passRate: 0, chart: [] },
      isLoading: false,
      error: null,
    });
    renderOverview();
    const summary = within(await screen.findByRole("region", { name: "Guardrail summary" }));
    expect(summary.getByRole("group", { name: "Total Evaluations" })).toHaveTextContent("0");
    expect(summary.getByRole("group", { name: "Pass Rate" })).toHaveTextContent("—");
    expect(summary.getByRole("group", { name: "Avg. latency added" })).toHaveTextContent("—");
    expect(screen.getByText("No data for this period")).toBeVisible();
  });

  it("marks the overview busy while the usage request is in flight", async () => {
    useGuardrailsUsageOverviewMock.mockReturnValue({ data: undefined, isLoading: true, error: null });
    renderOverview();

    await waitFor(() => expect(document.querySelector('[aria-busy="true"]')).toBeInTheDocument());
  });

  it("shows a failure message when the usage request rejects", async () => {
    useGuardrailsUsageOverviewMock.mockReturnValue({
      data: undefined,
      isLoading: false,
      error: new Error("network down"),
    });
    renderOverview();

    expect(await screen.findByText("Failed to load data. Try again.")).toBeInTheDocument();
  });
});
