import type { UserInfoV2Response } from "@/components/networking";
import { BudgetOverview } from "@/components/console/ConsolePrimitives";
import { formatConsoleMoney } from "@/components/console/money";
import { useI18n } from "@/i18n";

/** 用量页的个人预算快照；data 是当前用户信息，userID 用于阻止身份切换时误展示旧账面数。
 * loading/error 保持明确状态；展示刷新策略和超额信息，不受统计区间影响，不修改预算或准入。 */
export default function UsageBudgetSummary({
  data,
  userID,
  loading,
  error,
}: {
  data?: UserInfoV2Response;
  userID: string | null;
  loading: boolean;
  error: boolean;
}) {
  const { t, locale } = useI18n();
  if (loading || error || !data || data.user_id !== userID) {
    return (
      <section aria-label={t("usageBudget.title")} className="rounded-xl border bg-card p-5">
        <p role="status">{t(loading ? "quotaGuide.personalLoading" : "quotaGuide.personalUnavailable")}</p>
      </section>
    );
  }
  let reset = t("usageBudget.resetUnknown");
  if (data.budget_reset_day === 0) reset = t("userBudgetReset.none");
  else if (data.budget_reset_day) reset = t("userBudgetReset.day", { day: data.budget_reset_day });
  const nextReset = data.budget_reset_at ? new Date(data.budget_reset_at) : null;
  return (
    <BudgetOverview
      title={t("usageBudget.title")}
      spent={data.spend}
      limit={data.max_budget}
      status={
        <>
          {reset}
          {data.max_budget !== null && data.spend > data.max_budget && (
            <p>{t("usageBudget.exceeded", { amount: formatConsoleMoney(data.spend - data.max_budget, locale) })}</p>
          )}
          {Boolean(data.budget_reset_day) && nextReset && Number.isFinite(nextReset.getTime()) && (
            <p>
              {t("userBudgetReset.next", {
                date: new Intl.DateTimeFormat(locale, {
                  timeZone: "Asia/Shanghai",
                  dateStyle: "medium",
                  timeStyle: "short",
                  hour12: false,
                }).format(nextReset),
              })}
            </p>
          )}
        </>
      }
      note={t("usageBudget.note")}
    />
  );
}
