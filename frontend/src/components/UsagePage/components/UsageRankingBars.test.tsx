import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider, translate } from "@/i18n";
import { UsageRankingBars } from "./UsageRankingBars";

describe("UsageRankingBars", () => {
  /** 前置包含长历史名称、零值、未关联和可点击记录；检查金额与键盘激活，不发起请求，组件自动清理。 */
  it("keeps aliases and amounts visible and exposes only linked rows as actions", async () => {
    const alias = "Historical-long-key-alias-长名称-without-truncating-the-value-in-data";
    const select = vi.fn();
    render(
      <UsageRankingBars
        items={[
          { id: "linked", label: alias, value: 8240000000, details: ["Recorded request data"] },
          { id: "unlinked", label: "Unlinked", value: 0, interactive: false },
        ]}
        formatValue={value => Intl.NumberFormat("en", { currency: "USD", style: "currency" }).format(value)}
        onSelect={select}
      />,
    );
    const linked = screen.getByRole("button", { name: new RegExp(alias) });
    expect(within(linked).getByText("$8,240,000,000.00")).toBeVisible();
    expect(within(linked).getByText(alias)).toHaveAttribute("title", alias + "\nRecorded request data");
    expect(screen.getByText("Unlinked")).toBeVisible();
    expect(screen.queryByRole("button", { name: /Unlinked/ })).not.toBeInTheDocument();
    linked.focus();
    await userEvent.keyboard("{Enter}");
    expect(select).toHaveBeenCalledWith("linked");
  });

  /** 前置无排行数据；双语均显示空状态而非空白图表，组件自动清理。 */
  it.each(["en", "zh-CN"] as const)("%s renders the localized empty state", locale => {
    render(<I18nProvider initialLocale={locale}><UsageRankingBars items={[]} formatValue={String} /></I18nProvider>);
    expect(screen.getByText(translate(locale, "usagePolish.noRankingData"))).toBeVisible();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });
});
