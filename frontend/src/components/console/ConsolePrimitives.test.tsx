/* @vitest-environment jsdom */
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { I18nProvider, LOCALE_COOKIE } from "@/i18n";
import { translate } from "@/i18n/translate";
import { BudgetOverview, ConsolePageHeader, HelpDisclosure } from "./ConsolePrimitives";

afterEach(cleanup);

/** 展示中英文预算快照，动态金额、零与无上限均保持原始含义；纯展示不写数据，DOM 自动清理。 */
it.each(["en", "zh-CN"] as const)("keeps financial states distinct in %s", (locale) => {
  localStorage.removeItem(LOCALE_COOKIE);
  document.cookie = `${LOCALE_COOKIE}=;path=/;max-age=0`;
  const wrap = (props: { spent: number; limit: number | null; remaining?: number }) => (
    <I18nProvider initialLocale={locale}>
      <BudgetOverview title="Budget" {...props} />
    </I18nProvider>
  );
  const view = render(wrap({ spent: 125, limit: 500 }));
  expect(screen.getByText(translate(locale, "consoleUi.balance")).parentElement).toHaveTextContent("$375.00");
  expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "25");
  expect(screen.getByRole("progressbar")).toHaveAttribute(
    "aria-valuetext",
    translate(locale, "consoleUi.budgetProgressValue", { spent: "$125.00", limit: "$500.00" }),
  );
  view.rerender(wrap({ spent: 600, limit: 500 }));
  expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
  expect(screen.getByText(translate(locale, "consoleUi.spent")).parentElement).toHaveTextContent("$600.00");
  view.rerender(wrap({ spent: 0, limit: 0 }));
  expect(screen.getByText(translate(locale, "consoleUi.limit")).parentElement).toHaveTextContent("$0.00");
  view.rerender(wrap({ spent: 0.000001, limit: null }));
  expect(screen.getByText(translate(locale, "consoleUi.spent")).parentElement).toHaveTextContent("$0.000001");
  expect(screen.getByText(translate(locale, "consoleUi.limit")).parentElement).toHaveTextContent(
    translate(locale, "quotaGuide.personalUnlimited"),
  );
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
});

/** 非法数据必须显示未知而非零额度；覆盖负数、非有限数及非法剩余数，不产生外部副作用。 */
it.each([
  { spent: -1, limit: 500 },
  { spent: NaN, limit: 500 },
  { spent: 1, limit: Infinity },
  { spent: 1, limit: -1 },
  { spent: 1, limit: 500, remaining: NaN },
])("does not invent a balance for %j", (props) => {
  render(<BudgetOverview title="Budget" {...props} />);
  expect(screen.getByRole("status")).toBeVisible();
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
});

/** 页头保留主要操作，折叠说明按需展开而非占据首屏；纯 DOM 验证，测试结束自动卸载。 */
it("keeps actions visible and secondary help discoverable", () => {
  render(
    <>
      <ConsolePageHeader title="Usage" description="Period activity" actions={<button>Export</button>} />
      <HelpDisclosure title="Details">Budget rules</HelpDisclosure>
    </>,
  );
  expect(screen.getByRole("heading", { level: 1, name: "Usage" })).toBeVisible();
  expect(screen.getByRole("button", { name: "Export" })).toBeVisible();
  expect(screen.getByText("Budget rules")).not.toBeVisible();
  fireEvent.click(screen.getByText("Details"));
  expect(screen.getByText("Budget rules")).toBeVisible();
});
