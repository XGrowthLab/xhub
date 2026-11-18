"use client";
import { useEffect, useState } from "react";
import { ArrowUpRight, Building2 } from "lucide-react";
import { useT } from "@/i18n";
import { enterpriseLoginOptions } from "@/lib/http/enterprise";
import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/cva.config";

/** 加载当前启用的企业身份源并返回可聚焦的登录链接；无参数。接口失败或入口为空时保留本地表单。 */
export default function EnterpriseLogin() {
  const t = useT();
  const [options, setOptions] = useState<{ id: string; name: string; url: string }[]>([]);
  useEffect(() => {
    let alive = true;
    enterpriseLoginOptions().then((rows) => { if (alive) setOptions(rows); }).catch(() => {});
    return () => { alive = false; };
  }, []);
  if (!options.length) return null;
  return (
    <div className="mt-5 grid gap-2 border-t border-border pt-5">
      {options.map((option) => (
        <a key={option.id} href={option.url} className={cn(buttonVariants({ variant: "outline" }), "h-auto min-h-11 w-full gap-3 px-3.5 py-2.5")}>
          <Building2 aria-hidden="true" className="size-4 text-primary" />
          <span className="min-w-0 flex-1 text-start whitespace-normal break-words">{t("enterprise.login", { name: option.name })}</span>
          <ArrowUpRight aria-hidden="true" className="size-4 text-muted-foreground" />
        </a>
      ))}
    </div>
  );
}
