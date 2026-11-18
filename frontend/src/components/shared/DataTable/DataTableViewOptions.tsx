"use client";

import { useId } from "react";
import { Menu } from "@base-ui/react/menu";
import type { Table } from "@tanstack/react-table";
import { Check, Columns3 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useT, t } from "@/i18n";

interface DataTableViewOptionsProps<TData> {
  table: Table<TData>;
  label?: string;
  className?: string;
}

/** 渲染表格列选择菜单；参数为表格实例和可选按钮文案，返回可多选且不关闭的菜单，无可隐藏列时返回 null。
 * 作为非模态菜单保留页面滚动条，避免开启时列宽抖动；长列表在菜单内部滚动，列状态由调用表格持有。 */
export function DataTableViewOptions<TData>({ table, label = t("View"), className }: DataTableViewOptionsProps<TData>) {
  const t = useT();
  const headingId = useId();
  const hideableColumns = table.getAllLeafColumns().filter((column) => column.getCanHide());

  if (hideableColumns.length === 0) {
    return null;
  }

  return (
    <Menu.Root modal={false}>
      <Menu.Trigger
        render={
          <Button variant="outline" size="sm" className={className} data-testid="view-options-trigger">
            <Columns3 />
            {label}
          </Button>
        }
      />
      <Menu.Portal>
        <Menu.Positioner side="bottom" align="end" sideOffset={4} className="isolate z-popup">
          <Menu.Popup
            data-slot="dropdown-menu-content"
            aria-labelledby={headingId}
            className="max-h-[min(var(--available-height),24rem)] min-w-[14rem] overflow-y-auto overscroll-contain rounded-xl border border-border bg-popover p-1.5 text-sm text-popover-foreground shadow-lg outline-hidden"
          >
            <div id={headingId} className="px-2 py-1.5 text-xs font-medium text-muted-foreground">
              {t("consoleDetails.columnsTitle")}
            </div>
            {hideableColumns.map((column) => (
              <Menu.CheckboxItem
                key={column.id}
                checked={column.getIsVisible()}
                onCheckedChange={(checked) => column.toggleVisibility(checked)}
                closeOnClick={false}
                data-testid={`view-option-${column.id}`}
                className="relative flex cursor-default items-center gap-2 rounded-md py-2 pr-3 pl-8 whitespace-nowrap outline-hidden select-none data-highlighted:bg-accent data-highlighted:text-accent-foreground"
              >
                <Menu.CheckboxItemIndicator className="absolute left-2 flex size-4 items-center justify-center">
                  <Check className="size-3.5" />
                </Menu.CheckboxItemIndicator>
                {column.columnDef.meta?.title ??
                  (typeof column.columnDef.header === "string" ? column.columnDef.header : column.id)}
              </Menu.CheckboxItem>
            ))}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}
