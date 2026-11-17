"use client";
import { usePathname } from "next/navigation";
import { useEffect, useRef, type ReactNode } from "react";

/** 在路由变化时淡入工作区；children 为页面，保持原组件树和表单状态。
 * 不延迟导航、不改变固定层定位；减少动画偏好或浏览器无动画 API 时直接展示，卸载取消动画。 */
export function DashboardPageTransition({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    const animation = ref.current?.animate?.([{ opacity: 0.65 }, { opacity: 1 }], {
      duration: 160,
      easing: "ease-out",
    });
    return () => animation?.cancel();
  }, [pathname]);
  return (
    <div ref={ref} className="h-full min-h-0 min-w-0">
      {children}
    </div>
  );
}
