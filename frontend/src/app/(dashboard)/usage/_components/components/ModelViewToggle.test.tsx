import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { setActiveLocale } from "@/i18n/runtime";
import ModelViewToggle from "./ModelViewToggle";

// 两种语言下都可用键盘切换聚合模式，当前选中状态对辅助技术可读；只测试回调，DOM 自动清理。
it.each(["en", "zh-CN"] as const)("switches the model view with readable pressed states in %s", async (locale) => {
  setActiveLocale(locale);
  const onChange = vi.fn();
  const { rerender } = render(<ModelViewToggle value="groups" onChange={onChange} />);
  const publicButton = screen.getByRole("button", { name: locale === "en" ? "Public Model Name" : "公开模型名称" });
  const actualButton = screen.getByRole("button", { name: locale === "en" ? "XHub Model Name" : "XHub 模型名称" });
  expect(publicButton).toHaveAttribute("aria-pressed", "true");
  expect(actualButton).toHaveAttribute("aria-pressed", "false");
  actualButton.focus();
  await userEvent.setup().keyboard("{Enter}");
  expect(onChange).toHaveBeenCalledWith("individual");
  rerender(<ModelViewToggle value="individual" onChange={onChange} />);
  expect(actualButton).toHaveAttribute("aria-pressed", "true");
  setActiveLocale("en");
});
