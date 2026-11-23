"use client";

import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

const suggestions = {
  write: "Write: ",
  learn: "Learn: ",
  code: "Code: ",
  brainstorm: "Brainstorm: ",
} as const;

/** 对话首页没有消息时显示本地时段问候、输入区和主题建议。name 保留用户原始名称，
 * children 是已有输入框，onSuggestion 仅填入英文示例草稿。hour 默认为本地时间，可用于验证时段边界。
 */
export function ChatWelcome({
  name,
  children,
  onSuggestion,
  hour = new Date().getHours(),
}: {
  name: string;
  children: ReactNode;
  onSuggestion: (prompt: string) => void;
  hour?: number;
}) {
  const t = useT();
  const period = hour < 5 || hour >= 17 ? "evening" : hour < 12 ? "morning" : "afternoon";
  const greeting = t(`pages.chat.welcome.${period}${name ? "Named" : ""}`, { name });

  return (
    <div className="flex flex-1 flex-col items-center justify-center px-6 pb-20">
      <h1 className="m-0 mb-8 text-center text-[28px] font-semibold tracking-tight text-foreground">{greeting}</h1>
      <p className="-mt-4 mb-7 max-w-[520px] text-center text-sm leading-relaxed text-muted-foreground">
        {t("pages.chat.welcome.intro")}
      </p>
      <div className="w-full max-w-[680px]">{children}</div>
      <div className="mt-3.5 flex flex-wrap justify-center gap-2">
        {(Object.keys(suggestions) as Array<keyof typeof suggestions>).map((key) => (
          <Button
            key={key}
            variant="outline"
            size="sm"
            onClick={() => onSuggestion(suggestions[key])}
            className="rounded-full px-4 text-muted-foreground"
          >
            {t(`pages.chat.welcome.${key}`)}
          </Button>
        ))}
      </div>
    </div>
  );
}
