"use client";

import { usePathname } from "next/navigation";
import { useLayoutEffect, useRef, type ReactNode } from "react";

/** 在已提交的路由切换上呈现淡入与轻微上移。children 保留原挂载策略与输入状态；
 * 相对定位的短距离位移保留页面内固定弹层的视口参照。减少动效、快速切页和不支持动画的浏览器均可直接操作页面。 */
export function DashboardPageTransition({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const preference = window.matchMedia("(prefers-reduced-motion: reduce)");
    const element = ref.current;
    if (!element || preference.matches || !element.animate) return;
    const animation = element.animate(
      [{ opacity: 0.24, top: "10px" }, { opacity: 1, top: "0px" }],
      { id: "xhub-page-enter", duration: 280, easing: "cubic-bezier(.16, 1, .3, 1)" },
    );
    const cancel = () => animation.cancel();
    preference.addEventListener("change", cancel);
    return () => {
      preference.removeEventListener("change", cancel);
      cancel();
    };
  }, [pathname]);
  return (
    <div ref={ref} className="relative h-full min-h-0 min-w-0">
      {children}
    </div>
  );
}
