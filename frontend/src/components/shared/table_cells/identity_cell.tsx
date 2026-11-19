"use client";

import { ChevronRight } from "lucide-react";
import * as React from "react";

import { useEntityLinkClick } from "@/components/shared/EntityLink";
import { cn } from "@/lib/cva.config";

interface IdentityCellProps {
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  badge?: React.ReactNode;
  onClick?: () => void;
  href?: string;
  className?: string;
  titleClassName?: string;
  ariaLabel?: string;
}

const INTERACTIVE_CELL_CLASSES =
  "group -mx-2 flex w-[calc(100%+1rem)] cursor-pointer items-center gap-2 rounded-md px-2 py-1 text-left transition-colors hover:bg-muted";

const HoverChevron = () => (
  <ChevronRight className="ml-auto size-4 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" />
);

/** 表格首列展示资源名称、可选标识与状态；有链接或点击回调时可导航，ariaLabel 为操作提供独立的读屏名称。 */
export function IdentityCell({ title, subtitle, badge, onClick, href, className, titleClassName, ariaLabel }: IdentityCellProps) {
  const hasSubtitleRow = (subtitle != null && subtitle !== "") || badge != null;

  const body = (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className={cn("truncate text-sm font-medium text-foreground", titleClassName)}>{title}</span>
      {hasSubtitleRow && (
        <span className="flex min-w-0 items-center gap-2">
          {subtitle != null && subtitle !== "" && (
            <span className="truncate font-mono text-xs text-muted-foreground">{subtitle}</span>
          )}
          {badge}
        </span>
      )}
    </div>
  );

  if (href != null) {
    return <IdentityCellLink href={href} className={className} body={body} ariaLabel={ariaLabel} />;
  }

  if (onClick != null) {
    return (
      <button type="button" aria-label={ariaLabel} onClick={onClick} className={cn(INTERACTIVE_CELL_CLASSES, className)}>
        {body}
        <HoverChevron />
      </button>
    );
  }

  return <div className={cn("min-w-0", className)}>{body}</div>;
}

/** 为资源标题提供控制台内导航链接；沿用默认浏览器新标签行为，ariaLabel 可省略以继承内容名称。 */
function IdentityCellLink({ href, className, body, ariaLabel }: { href: string; className?: string; body: React.ReactNode; ariaLabel?: string }) {
  const handleClick = useEntityLinkClick(href);

  return (
    <a href={href} aria-label={ariaLabel} onClick={handleClick} className={cn(INTERACTIVE_CELL_CLASSES, className)}>
      {body}
      <HoverChevron />
    </a>
  );
}
