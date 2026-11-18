/** @vitest-environment jsdom */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ConsoleSelect } from "./ConsoleSelect";

describe("ConsoleSelect", () => {
  /** 验证共用筛选器键盘选择及双语更新；纯 DOM 前置，无网络数据，卸载后自动清理。 */
  it("selects with the keyboard and updates translated option names", async () => {
    const onValueChange = vi.fn();
    const user = userEvent.setup();
    const view = render(<ConsoleSelect label="Sort models" value="name" onValueChange={onValueChange} options={[
      { value: "name", label: "Model name" }, { value: "price", label: "Input price" },
    ]} />);
    expect(screen.getByRole("combobox", { name: "Sort models" })).toHaveTextContent(/^Model name$/);
    screen.getByRole("combobox", { name: "Sort models" }).focus();
    await user.keyboard("{ArrowDown}{End}{Enter}");
    expect(onValueChange).toHaveBeenCalledWith("price");
    view.rerender(<ConsoleSelect label="模型排序" value="price" onValueChange={onValueChange} options={[
      { value: "name", label: "模型名称" }, { value: "price", label: "输入价格" },
    ]} />);
    expect(screen.getByRole("combobox", { name: "模型排序" })).toHaveTextContent("输入价格");
    expect(screen.queryByText("Input price")).not.toBeInTheDocument();
  });

  /** 验证空数据和停用选项不产生错误的筛选更新；无需外部依赖，由测试框架清理 DOM。 */
  it("disables empty lists and prevents selecting disabled options", async () => {
    const onValueChange = vi.fn();
    const user = userEvent.setup();
    const view = render(<ConsoleSelect label="Provider" value="" options={[]} onValueChange={onValueChange} />);
    expect(screen.getByRole("combobox", { name: "Provider" })).toBeDisabled();
    view.rerender(<ConsoleSelect label="Provider" value="ok" options={[
      { value: "ok", label: "Available" }, { value: "bad", label: "Unavailable", disabled: true },
    ]} onValueChange={onValueChange} />);
    screen.getByRole("combobox", { name: "Provider" }).focus();
    await user.keyboard("{ArrowDown}");
    const disabled = screen.getByRole("option", { name: "Unavailable" });
    expect(disabled).toHaveAttribute("aria-disabled", "true");
    await user.click(disabled);
    expect(onValueChange).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });
});
