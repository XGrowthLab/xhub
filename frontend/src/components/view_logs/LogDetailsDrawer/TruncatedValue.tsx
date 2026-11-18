import CopyButton from "@/components/shared/CopyButton";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { DEFAULT_MAX_WIDTH, FONT_FAMILY_MONO } from "./constants";
import { useT } from "@/i18n";

interface TruncatedValueProps {
  value?: string;
  maxWidth?: number;
}

/**
 * 供日志字段显示长标识及地址；value 是原始值，maxWidth 限制文字宽度，同时受父容器约束。
 * 返回可复制的文本和完整值提示；空值以占位符显示。
 */
export function TruncatedValue({ value, maxWidth = DEFAULT_MAX_WIDTH }: TruncatedValueProps) {
  const t = useT();
  if (!value) return <span className="text-muted-foreground">-</span>;

  return (
    <TooltipProvider delay={300}>
      <Tooltip>
        <TooltipTrigger
          render={
            <span className="inline-flex max-w-full items-center gap-1 align-bottom">
              <span className="truncate text-xs" style={{ maxWidth, fontFamily: FONT_FAMILY_MONO }}>
                {value}
              </span>
              <CopyButton value={value} label={t("Copy")} className="size-4 shrink-0" iconClassName="size-3" />
            </span>
          }
        />
        <TooltipContent>{value}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
