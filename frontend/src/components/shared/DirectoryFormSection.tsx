import { Gauge, Layers3, Users } from "lucide-react";
import type { ReactNode } from "react";
import { useT } from "@/i18n";

const sections = {
  identity: { label: "directoryForm.identity", icon: Users, tone: "bg-info/10 text-info" },
  access: {
    label: "directoryForm.modelAccess",
    icon: Layers3,
    tone: "bg-violet-500/10 text-violet-700 dark:text-violet-300",
  },
  limits: { label: "directoryForm.limits", icon: Gauge, tone: "bg-amber-500/10 text-amber-800 dark:text-amber-300" },
} as const;

/** 按 section 将资源表单的 children 分为身份、模型访问或额度字段组；供团队创建、团队编辑和组织设置共用，不改变字段名称与提交。 */
export function DirectoryFormSection({ section, children }: { section: keyof typeof sections; children: ReactNode }) {
  const t = useT();
  const { icon: Icon, tone, label } = sections[section];
  return (
    <fieldset className="min-w-0 space-y-4 rounded-xl border bg-card px-4 pb-4 sm:px-5">
      <legend className="flex items-center gap-2 px-1 text-sm font-semibold">
        <span className={`inline-flex size-7 items-center justify-center rounded-lg ${tone}`}>
          <Icon aria-hidden="true" className="size-4" />
        </span>
        {t(label)}
      </legend>
      {children}
    </fieldset>
  );
}
