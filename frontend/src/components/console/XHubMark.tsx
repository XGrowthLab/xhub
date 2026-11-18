import type { SVGProps } from "react";

/** 渲染原创的双路交汇标记；接收标准 SVG 外观属性，供品牌导航使用。
 * 标记仅作装饰，由相邻链接提供本地化名称；不含外部资源、动画或第三方商标。 */
export function XHubMark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 40 40" fill="none" aria-hidden="true" focusable="false" {...props}>
      <rect width="40" height="40" rx="12" fill="#0F172A" />
      <path d="M11 11L29 29" stroke="#F8FAFC" strokeWidth="4" strokeLinecap="round" />
      <path d="M11 29L17 23M23 17L29 11" stroke="#38BDF8" strokeWidth="4" strokeLinecap="round" />
      <circle cx="20" cy="20" r="3" fill="#38BDF8" stroke="#0F172A" strokeWidth="2" />
    </svg>
  );
}
