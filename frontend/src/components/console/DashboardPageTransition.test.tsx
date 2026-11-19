/* @vitest-environment jsdom */
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useState } from "react";
import { DashboardPageTransition } from "./DashboardPageTransition";

const route = vi.hoisted(() => ({ path: "/usage" }));
vi.mock("next/navigation", () => ({ usePathname: () => route.path }));
afterEach(() => {
  vi.restoreAllMocks();
  route.path = "/usage";
});

/** 交互夹具保持局部状态，用于发现过渡层重挂载造成的输入丢失；无接口写入。 */
function InputFixture() {
  const [value, setValue] = useState("");
  return <input aria-label="Draft" value={value} onChange={(event) => setValue(event.target.value)} />;
}

/** 路由变化只触发短淡入，不重挂载子组件；结束取消动画，DOM 和模拟 API 均由框架恢复。 */
it("animates routes without losing input and cancels stale animations", () => {
  vi.spyOn(window, "matchMedia").mockReturnValue(Object.assign(new EventTarget(), { matches: false }) as MediaQueryList);
  const cancel = vi.fn();
  const animate = vi.fn().mockReturnValue({ cancel });
  vi.stubGlobal("Animation", class {});
  const old = HTMLElement.prototype.animate;
  HTMLElement.prototype.animate = animate;
  try {
    const view = render(
      <DashboardPageTransition>
        <InputFixture />
      </DashboardPageTransition>,
    );
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "保留我的输入" } });
    route.path = "/logs";
    view.rerender(
      <DashboardPageTransition>
        <InputFixture />
      </DashboardPageTransition>,
    );
    expect(screen.getByRole("textbox")).toHaveValue("保留我的输入");
    expect(animate).toHaveBeenCalledTimes(2);
    expect(animate).toHaveBeenLastCalledWith(
      [{ opacity: 0.24, top: "10px" }, { opacity: 1, top: "0px" }],
      expect.objectContaining({ id: "xhub-page-enter" }),
    );
    expect(cancel).toHaveBeenCalledOnce();
    view.unmount();
    expect(cancel).toHaveBeenCalledTimes(2);
  } finally {
    HTMLElement.prototype.animate = old;
    vi.unstubAllGlobals();
  }
});

/** 减少动画偏好和缺少浏览器动画 API 时直接显示，不阻断导航或输入；无外部数据。 */
it.each([true, false])("renders safely with reduced motion %s and no animation API", (reduced) => {
  vi.spyOn(window, "matchMedia").mockReturnValue({ matches: reduced } as MediaQueryList);
  render(
    <DashboardPageTransition>
      <InputFixture />
    </DashboardPageTransition>,
  );
  expect(screen.getByRole("textbox")).toBeVisible();
});

/** 系统在切页时打开减少动效应立即结束正在播放的过渡；模拟已挂载路由，卸载并还原浏览器原型。 */
it("stops the active transition when the motion preference changes", () => {
  const query = Object.assign(new EventTarget(), { matches: false });
  vi.spyOn(window, "matchMedia").mockReturnValue(query as MediaQueryList);
  const old = HTMLElement.prototype.animate;
  const cancel = vi.fn();
  HTMLElement.prototype.animate = vi.fn().mockReturnValue({ cancel });
  try {
    const view = render(<DashboardPageTransition><InputFixture /></DashboardPageTransition>);
    query.matches = true;
    query.dispatchEvent(new Event("change"));
    expect(cancel).toHaveBeenCalledOnce();
    view.unmount();
    query.dispatchEvent(new Event("change"));
    expect(cancel).toHaveBeenCalledTimes(2);
  } finally {
    HTMLElement.prototype.animate = old;
  }
});
