import { renderWithProviders, screen } from "@/../tests/test-utils";
import { describe, expect, it } from "vitest";
import { I18nProvider, translate } from "@/i18n";
import EndpointUsageTable from "./EndpointUsageTable";

describe("EndpointUsageTable", () => {
  it("should render", () => {
    const mockEndpointData = {
      "endpoint-1": {
        metrics: {
          spend: 100.5,
          prompt_tokens: 5000,
          completion_tokens: 3000,
          total_tokens: 8000,
          api_requests: 100,
          successful_requests: 95,
          failed_requests: 5,
          cache_read_input_tokens: 0,
          cache_creation_input_tokens: 0,
        },
        metadata: {},
        api_key_breakdown: {},
      },
    };

    renderWithProviders(<EndpointUsageTable endpointData={mockEndpointData} />);

    expect(screen.getAllByRole("columnheader").map((header) => header.textContent)).toEqual([
      "Endpoint",
      "Successful",
      "Failed",
      "Total Request",
      "Success Rate",
      "Total Tokens",
      "Spend",
    ]);
    expect(screen.getByText("endpoint-1")).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "95", exact: true })).toBeVisible();
    expect(screen.getByRole("cell", { name: "5", exact: true })).toBeVisible();
    expect(screen.getByText("95.00%")).toBeInTheDocument();
    expect(screen.getByText("$100.50")).toBeInTheDocument();
  });
});

/** 前置空统计；按实际控制台语言检查独立数值列和空状态，组件由测试框架清理。 */
it.each(["zh-CN", "en"] as const)("%s localizes endpoint columns and the empty message", locale => {
  renderWithProviders(<I18nProvider initialLocale={locale}><EndpointUsageTable endpointData={{}} /></I18nProvider>);
  expect(screen.getByRole("columnheader", { name: translate(locale, "Successful") })).toBeVisible();
  expect(screen.getByRole("columnheader", { name: translate(locale, "Failed") })).toBeVisible();
  expect(screen.getByText(translate(locale, "usagePolish.noEndpointData"))).toBeVisible();
});
