"use client";

import type { ReactNode } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/cva.config";

export interface ConsoleSelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

/** 控制台筛选共用的单选控件；label 为已翻译的无障碍名称，options 为值与显示名称，
 * value/onValueChange 控制筛选状态，icon/className 仅影响触发器。返回支持键盘的下拉框；
 * 不锁定页面滚动，不修改无效或空选项，不触发后台写入。用于模型列表和监控筛选。 */
export function ConsoleSelect({ label, value, options, onValueChange, className, icon, disabled = false, id }: {
  label: string;
  value: string;
  options: ConsoleSelectOption[];
  onValueChange: (value: string) => void;
  className?: string;
  icon?: ReactNode;
  disabled?: boolean;
  id?: string;
}) {
  return (
    <Select value={value} onValueChange={(next) => { if (next !== null) onValueChange(next); }} items={options} disabled={disabled || options.length === 0} modal={false}>
      <SelectTrigger id={id} aria-label={label} className={cn("w-full min-w-0 gap-2 bg-background px-3", className)}>
        {icon}
        <SelectValue className="min-w-0 truncate" />
      </SelectTrigger>
      <SelectContent align="start" className="max-w-[calc(100vw-2rem)] p-1">
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value} disabled={option.disabled} className="rounded-md py-2 [&_span]:min-w-0 [&_span]:whitespace-normal [&_span]:break-words">
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
