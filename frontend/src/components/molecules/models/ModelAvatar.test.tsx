import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { ModelAvatar } from "./ModelAvatar";

/** 使用外部市场地址模拟逐级加载失败，验证本地品牌和中性占位均可见；只触发 DOM 错误事件，测试后自动卸载。 */
it("市场图失败时依次回退到模型品牌和中性图标", () => {
  render(<ModelAvatar model="x-ai/grok-4.5" avatar="https://static.qiniu.com/assets/grok.png" />);
  const avatar = screen.getByTestId("model-avatar");
  expect(avatar).toHaveAttribute("data-avatar-kind", "market");
  const image = avatar.querySelector("img")!;
  expect(image).toHaveAttribute("referrerpolicy", "no-referrer");
  fireEvent.error(image);
  expect(avatar).toHaveAttribute("data-avatar-kind", "provider");
  expect(avatar.querySelector("img")?.getAttribute("src")).toMatch(/xai/);
  fireEvent.error(avatar.querySelector("img")!);
  expect(avatar).toHaveAttribute("data-avatar-kind", "generic");
  expect(avatar.querySelector("img")).toBeNull();
});

/** 使用三个不同的目录条目，覆盖位图内联、坏地址和未知模型。测试不请求图片，由 DOM 自动清理。 */
it("接受市场位图并忽略不安全或不合法的地址", () => {
  const view = render(<ModelAvatar model="independent/model" avatar="data:image/png;base64,AA==" />);
  const avatar = screen.getByTestId("model-avatar");
  expect(avatar).toHaveAttribute("data-avatar-kind", "market");
  view.rerender(<ModelAvatar model="private/custom" avatar="javascript:alert(1)" />);
  expect(avatar).toHaveAttribute("data-avatar-kind", "generic");
  view.rerender(<ModelAvatar model="gateway/claude-opus-4-7" avatar="https://user:password@example.com/image.png" />);
  expect(avatar).toHaveAttribute("data-avatar-kind", "provider");
  expect(avatar.querySelector("img")?.getAttribute("src")).toMatch(/anthropic/);
});
