"use client";

import { Archive } from "lucide-react";
import Link from "next/link";
import useAuthorized from "@/app/(dashboard)/hooks/useAuthorized";
import { buttonVariants } from "@/components/ui/button";
import { useT } from "@/i18n";
import { uiHref } from "@/utils/uiHref";

/** 展示旧控制台书签的下线说明；无参数，返回已登录用户可用的迁移入口。
 * 由中间件处理明确的退役路径，保留原书签 URL，不读取或写入退役接口。 */
export default function RetiredPage() {
  const { isAuthorized } = useAuthorized();
  const t = useT();

  if (!isAuthorized) return null;

  return (
    <section className="mx-auto mt-8 max-w-xl rounded-xl border bg-card p-6 sm:p-8">
      <Archive aria-hidden className="mb-4 size-6 text-muted-foreground" />
      <h1 className="text-2xl font-semibold tracking-tight">{t("legacyRoutes.retiredTitle")}</h1>
      <p className="mt-2 text-sm leading-relaxed text-muted-foreground">{t("legacyRoutes.retiredBody")}</p>
      <div className="mt-6 flex flex-wrap gap-3">
        <Link href={uiHref("")} className={buttonVariants()}>
          {t("legacyRoutes.openConsole")}
        </Link>
        <Link href="/docs" className={buttonVariants({ variant: "outline" })}>
          {t("legacyRoutes.openDocs")}
        </Link>
      </div>
    </section>
  );
}
