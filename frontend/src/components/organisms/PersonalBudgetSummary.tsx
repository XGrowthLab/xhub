import { useCurrentUser } from "@/app/(dashboard)/hooks/users/useCurrentUser";
import { useT } from "@/i18n";
import { BudgetOverview } from "@/components/console/ConsolePrimitives";

/** 创建密钥时显示当前个人预算；加载和异常保持明确状态，只展示账面快照，不改变预占与祖先限制。 */
export default function PersonalBudgetSummary() {
  const t = useT();
  const { data, isLoading, isError } = useCurrentUser();
  return (
    <div className="mb-4">
      {isLoading || isError || !data ? (
        <section aria-label={t("quotaGuide.personalTitle")} className="rounded-xl border p-4 text-sm">
          <p role="status">{t(isLoading ? "quotaGuide.personalLoading" : "quotaGuide.personalUnavailable")}</p>
        </section>
      ) : (
        <BudgetOverview
          title={t("quotaGuide.personalTitle")}
          spent={data.spend}
          limit={data.max_budget}
          remaining={data.max_budget === null ? undefined : data.max_budget - data.spend}
          note={t("quotaGuide.personalNote")}
        />
      )}
    </div>
  );
}
