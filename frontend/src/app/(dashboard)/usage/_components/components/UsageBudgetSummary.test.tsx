import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { I18nProvider, LOCALE_COOKIE } from "@/i18n";
import { translate } from "@/i18n/translate";
import type { UserInfoV2Response } from "@/components/networking";
import UsageBudgetSummary from "./UsageBudgetSummary";

afterEach(cleanup);
/** 双语预算保留刷新和超额状态；身份不符/加载/失败不能泄露旧数，非法时间不展示；纯组件自动清理。 */
it.each(["en", "zh-CN"] as const)("keeps budget states explicit in %s", (locale) => {
  localStorage.removeItem(LOCALE_COOKIE);
  document.cookie = `${LOCALE_COOKIE}=;path=/;max-age=0`;
  const data = {
    user_id: "current",
    spend: 600,
    max_budget: 500,
    budget_reset_day: 1,
    budget_reset_at: "2026-12-01T00:00:00+08:00",
  } as UserInfoV2Response;
  const view = render(
    <I18nProvider initialLocale={locale}>
      <UsageBudgetSummary data={data} userID="current" loading={false} error={false} />
    </I18nProvider>,
  );
  expect(screen.getByText(translate(locale, "userBudgetReset.day", { day: 1 }))).toBeVisible();
  expect(screen.getByText(translate(locale, "usageBudget.exceeded", { amount: "$100.00" }))).toBeVisible();
  expect(
    screen.getByText(
      translate(locale, "userBudgetReset.next", {
        date: new Intl.DateTimeFormat(locale, {
          timeZone: "Asia/Shanghai",
          dateStyle: "medium",
          timeStyle: "short",
          hour12: false,
        }).format(new Date(data.budget_reset_at!)),
      }),
    ),
  ).toBeVisible();
  for (const [userID, loading, error] of [
    ["other", false, false],
    ["current", true, false],
    ["current", false, true],
  ] as const) {
    view.rerender(
      <I18nProvider initialLocale={locale}>
        <UsageBudgetSummary data={data} userID={userID} loading={loading} error={error} />
      </I18nProvider>,
    );
    expect(screen.getByRole("status")).toBeVisible();
    expect(screen.queryByText("$600.00")).not.toBeInTheDocument();
  }
  view.rerender(
    <I18nProvider initialLocale={locale}>
      <UsageBudgetSummary
        data={{ ...data, budget_reset_at: "invalid", budget_reset_day: 0 }}
        userID="current"
        loading={false}
        error={false}
      />
    </I18nProvider>,
  );
  expect(screen.getByText(translate(locale, "userBudgetReset.none"))).toBeVisible();
  expect(screen.queryByText(/Invalid Date/)).not.toBeInTheDocument();
});
