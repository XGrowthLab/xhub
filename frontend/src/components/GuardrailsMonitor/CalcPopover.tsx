import { CircleHelp } from "lucide-react";
import React, { type ReactNode } from "react";
import { Popover, PopoverContent, PopoverTitle, PopoverTrigger } from "@/components/ui/popover";
import type { MathRow } from "./usageUnits";
import { useT } from "@/i18n";

/** 展示计价公式及明细；接收标题、公式和明细节点，返还支持键盘与触屏的说明按钮。 */
export function CalcPopover({ title, formula, children }: { title: string; formula: string; children: ReactNode }) {
  const t = useT();
  return (
    <Popover>
      <PopoverTrigger
        openOnHover
        delay={200}
        closeDelay={150}
        render={
          <button
            type="button"
            className="mt-2 inline-flex w-fit cursor-help items-start gap-1 text-left text-xs text-muted-foreground hover:text-foreground"
          />
        }
      >
        <CircleHelp className="mt-px size-3.5 shrink-0" />
        {t("How is this calculated?")}
      </PopoverTrigger>
      <PopoverContent side="bottom" align="start" className="w-80 max-w-[calc(100dvw_-_2rem)] gap-3">
        <PopoverTitle>{title}</PopoverTitle>
        <code className="w-fit rounded bg-muted px-2 py-1 text-[11px] text-muted-foreground">{formula}</code>
        {children}
      </PopoverContent>
    </Popover>
  );
}

/** 渲染计价分项及合计；参数为展示行与格式化总额，长名称在窄窗中折行。 */
export function MathTable({ rows, total }: { rows: readonly MathRow[]; total: string }) {
  const t = useT();
  const width = 1 + Math.max(...rows.map((row) => row.parts.length), 1);
  return (
    <table className="w-full text-xs">
      <tbody>
        {rows.map((row) => (
          <React.Fragment key={row.label}>
            <tr>
              <td className="py-0.5 pr-3 [overflow-wrap:anywhere]">{row.label}</td>
              {row.parts.map((part, i) => (
                <td key={i} className="py-0.5 pl-3 text-right whitespace-nowrap tabular-nums">
                  {part}
                </td>
              ))}
            </tr>
            {row.note && (
              <tr>
                <td colSpan={width} className="pb-1 text-[11px] text-warning">
                  {row.note}
                </td>
              </tr>
            )}
          </React.Fragment>
        ))}
      </tbody>
      <tfoot>
        <tr className="border-t border-border font-medium">
          <td className="pt-1.5 pr-3" colSpan={width - 1}>
            {t("Total")}
          </td>
          <td className="pt-1.5 pl-3 text-right whitespace-nowrap tabular-nums">{total}</td>
        </tr>
      </tfoot>
    </table>
  );
}
