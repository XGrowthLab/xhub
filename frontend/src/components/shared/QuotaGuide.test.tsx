import userEvent from "@testing-library/user-event";
import { render, screen, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { translate } from "@/i18n/translate";
import { QuotaGuide } from "./QuotaGuide";

let locale: "en" | "zh-CN" = "en";
vi.mock("@/i18n", async () => {
  const original = await vi.importActual<typeof import("@/i18n")>("@/i18n");
  return { ...original, useT: () => (key: string) => translate(locale, key) };
});
afterEach(cleanup);

/** 验证各额度层帮助在中英文完整渲染；前置真实字典，断言层级、共享和唯一团队，结束清理 DOM。 */
describe("QuotaGuide", () => {
  for (const language of ["en", "zh-CN"] as const) {
    for (const scope of ["organization", "team", "person", "key"] as const) {
      /** 默认折叠、点击展开并可收起；真实字典，无网络，结束由 cleanup 清理 DOM。 */
      it(language + " " + scope, async () => {
        locale = language;
        render(<QuotaGuide scope={scope} />);
        expect(screen.getByRole("complementary", { name: translate(locale, "quotaGuide.title") })).toBeVisible();
        const summary = screen.getByText(translate(locale, "quotaGuide.title"));
        expect(screen.getByText(translate(locale, "quotaGuide.chain"))).not.toBeVisible();
        await userEvent.click(summary);
        for (const key of ["chain", scope, "shared", "rateMeaning", "rateSharing", "rateAccounting"]) {
          expect(screen.getByText(translate(locale, "quotaGuide." + key))).toBeVisible();
        }
        const more = screen.getByText(translate(locale, "quotaGuide.moreHeading"));
        expect(screen.getByText(translate(locale, "quotaGuide.singleTeam"))).not.toBeVisible();
        await userEvent.click(more);
        expect(screen.getByText(translate(locale, "quotaGuide.singleTeam"))).toBeVisible();
        expect(screen.getByText(translate(locale, "quotaGuide.business"))).toBeVisible();
        await userEvent.click(more);
        await userEvent.click(summary);
        expect(screen.getByText(translate(locale, "quotaGuide.chain"))).not.toBeVisible();
      });
    }
  }
});

/** 打开说明后切换语言；前置已展开，不重置展开状态，文本完整变化；DOM 自动清理。 */
it("changes language while keeping the guide open", async () => {
  locale = "en";
  const view = render(<QuotaGuide scope="key" />);
  await userEvent.click(screen.getByText(translate(locale, "quotaGuide.title")));
  locale = "zh-CN";
  view.rerender(<QuotaGuide scope="key" />);
  expect(screen.getByText(translate(locale, "quotaGuide.title"))).toBeVisible();
  expect(screen.getByText(translate(locale, "quotaGuide.key"))).toBeVisible();
  expect(screen.queryByText(translate("en", "quotaGuide.key"))).not.toBeInTheDocument();
});

/** 验证双语说明与真实限流和成员移除规则一致；仅检查文案，无网络或数据需要清理。 */
it.each([
  ["en", /remaining capacity/i, /do not reserve/i, /keys are retained/i, /keeps their personal keys/i],
  ["zh-CN", /剩余容量/, /不预留容量/, /个人密钥保留/, /个人密钥仍保留/],
] as const)("%s explains shared rate capacity and retained personal keys", (language, remaining, notReserved, retained, warning) => {
  expect(translate(language, "quotaGuide.rateSharing")).toMatch(remaining);
  expect(translate(language, "quotaGuide.rateSharing")).toMatch(notReserved);
  expect(translate(language, "quotaGuide.singleTeam")).toMatch(retained);
  expect(translate(language, "quotaGuide.leaveTeamWarning")).toMatch(warning);
});
