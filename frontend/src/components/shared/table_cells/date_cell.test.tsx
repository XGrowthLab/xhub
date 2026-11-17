import { render as rawRender, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import { I18nProvider, LOCALE_COOKIE } from "@/i18n";
import { setActiveLocale } from "@/i18n/runtime";
beforeEach(() => { setActiveLocale("en"); localStorage.removeItem(LOCALE_COOKIE); document.cookie = `${LOCALE_COOKIE}=;path=/;max-age=0`; });
/** 提供真实语言上下文，避免无 Provider 回退语言影响时间；返回渲染结果供重渲染验证。 */
const render: typeof rawRender = (ui, options) => rawRender(ui, { wrapper: ({children}) => <I18nProvider initialLocale="en">{children}</I18nProvider>, ...options });

import { DateCell, formatCellDate, formatFullTimestamp } from "./date_cell";

const localIso = new Date(2026, 6, 7, 9, 50, 13).toISOString();

describe("formatCellDate", () => {
  it("formats datetime precision as 'MMM D, HH:mm:ss' without a year", () => {
    expect(formatCellDate(new Date(2026, 6, 7, 9, 50, 13), "datetime")).toBe("Jul 7, 09:50:13");
  });

  it("zero-pads hours, minutes and seconds", () => {
    expect(formatCellDate(new Date(2026, 0, 2, 1, 2, 3), "datetime")).toBe("Jan 2, 01:02:03");
  });

  it("formats date precision as 'MMM D, YYYY' with no time", () => {
    expect(formatCellDate(new Date(2026, 11, 31, 23, 59, 59), "date")).toBe("Dec 31, 2026");
  });
});

describe("formatFullTimestamp", () => {
  it("includes year, 24h time and the IANA timezone", () => {
    const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    expect(formatFullTimestamp(new Date(2026, 6, 7, 9, 50, 13))).toBe(`Jul 7, 2026, 09:50:13 (${timeZone})`);
  });
});

describe("DateCell", () => {
  it("renders the datetime format by default", () => {
    render(<DateCell value={localIso} />);
    expect(screen.getByText("Jul 7, 09:50:13")).toBeInTheDocument();
  });

  it("renders date-only when precision is 'date'", () => {
    render(<DateCell value={localIso} precision="date" />);
    expect(screen.getByText("Jul 7, 2026")).toBeInTheDocument();
  });

  it("renders '-' for null and undefined", () => {
    const { rerender } = render(<DateCell value={null} />);
    expect(screen.getByText("-")).toBeInTheDocument();
    rerender(<DateCell value={undefined} />);
    expect(screen.getByText("-")).toBeInTheDocument();
  });

  it("renders the custom fallback for empty values", () => {
    render(<DateCell value="" fallback="Never" />);
    expect(screen.getByText("Never")).toBeInTheDocument();
  });

  it("renders the fallback instead of 'Invalid Date' for unparseable input", () => {
    render(<DateCell value="not-a-date" fallback="Unknown" />);
    expect(screen.getByText("Unknown")).toBeInTheDocument();
    expect(screen.queryByText(/Invalid/)).not.toBeInTheDocument();
  });
});

/** 中英文日期与完整时间使用当前语言，非法时间不伪造有效日期；没有外部数据，DOM 自动清理。 */
it("localizes dates and preserves the original timestamp", () => {
  setActiveLocale("zh-CN");
  rawRender(<I18nProvider initialLocale="zh-CN"><DateCell value={localIso} precision="date" /></I18nProvider>);
  const date = screen.getByText("2026年7月7日");
  expect(date.closest("time")).toHaveAttribute("dateTime", localIso);
  expect(formatCellDate(new Date("invalid"), "date", "zh-CN")).toBe("—");
});
