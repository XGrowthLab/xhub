"use client";

import { Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import React from "react";

import { useT } from "@/i18n";
import { Button } from "@/components/ui/button";

/** 顶栏主题切换；使用现有主题持久化，按钮与无障碍名称随界面语言切换，不改变用户内容。 */
const ThemeToggle: React.FC = () => {
  const t = useT();
  const { setTheme, resolvedTheme } = useTheme();
  const isDark = resolvedTheme === "dark";
  const label = t(isDark ? "consoleUi.lightMode" : "consoleUi.darkMode");

  return (
    <Button
      variant="ghost"
      size="icon-sm"
      aria-label={label}
      title={label}
      className="text-muted-foreground"
      onClick={() => setTheme(isDark ? "light" : "dark")}
    >
      {isDark ? <Moon /> : <Sun />}
    </Button>
  );
};

export default ThemeToggle;
