import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@/i18n";
import { ChatWelcome } from "./ChatWelcome";

/** 双语验证时段边界和动态名称。测试在内存 DOM 运行，自动卸载，无外部资源。 */
describe("ChatWelcome", () => {
  it.each([
    ["zh-CN", 4, "", "晚上好"],
    ["zh-CN", 5, "QA 用户", "早上好，QA 用户"],
    ["en", 11, "", "Good morning"],
    ["en", 12, "User 原文", "Good afternoon, User 原文"],
    ["en", 17, "", "Good evening"],
  ] as const)("显示语言 %s 和小时 %s 对应的问候", (locale, hour, name, expected) => {
    render(
      <I18nProvider initialLocale={locale}>
        <ChatWelcome name={name} hour={hour} onSuggestion={vi.fn()}>
          {null}
        </ChatWelcome>
      </I18nProvider>,
    );
    expect(screen.getByRole("heading", { name: expected })).toBeVisible();
  });

  /** 在中文界面点击建议仍写入英文示例，保留已有输入的原文。
   * 夹具只记录回调，无持久化操作，测试框架清理组件。
   */
  it("使用本地化按钮展示英文章节建议", async () => {
    const onSuggestion = vi.fn();
    const user = userEvent.setup();
    render(
      <I18nProvider initialLocale="zh-CN">
        <ChatWelcome name="" hour={12} onSuggestion={onSuggestion}>
          <input defaultValue="用户原文" />
        </ChatWelcome>
      </I18nProvider>,
    );
    expect(screen.getByRole("textbox")).toHaveValue("用户原文");
    await user.click(screen.getByRole("button", { name: "编程" }));
    expect(onSuggestion).toHaveBeenCalledWith("Code: ");
  });
});
