import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { XHubMark } from "./XHubMark";

describe("XHubMark", () => {
  /** 默认标记是可缩放装饰 SVG，无外链或额外读屏名称；前置为无自定义品牌，结束自动卸载。 */
  it("renders a self-contained decorative mark", () => {
    render(<XHubMark className="size-7" data-testid="brand-mark" />);
    const mark = screen.getByTestId("brand-mark");
    expect(mark).toHaveAttribute("viewBox", "0 0 40 40");
    expect(mark).toHaveAttribute("aria-hidden", "true");
    expect(mark).toHaveAttribute("focusable", "false");
    expect(mark).not.toHaveAccessibleName();
  });

  /** 调用方可指定紧凑尺寸和颜色外观，未传属性也能渲染；仅操作内存 DOM，无清理副作用。 */
  it("accepts compact dimensions without external assets", () => {
    const { rerender } = render(<XHubMark width={24} height={24} data-testid="brand-mark" />);
    expect(screen.getByTestId("brand-mark")).toHaveAttribute("width", "24");
    rerender(<XHubMark data-testid="brand-mark" />);
    expect(screen.getByTestId("brand-mark")).toHaveAttribute("viewBox", "0 0 40 40");
  });
});
