import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";
import { Check, ChevronsUpDown, LayoutGrid } from "lucide-react";
import { usePluginMode } from "@/contexts/PluginModeContext";
import { useT } from "@/i18n";

const GATEWAY = "ai-gateway";

/** 展示网关名称和已安装的插件工作区；无参数，返回导航标签或菜单。
 * 控制台顶部调用，插件选择只更新当前工作区。没有插件时显示静态标签。
 */
export default function ViewSwitcher() {
  const t = useT();
  const { mode, setMode, plugins } = usePluginMode();
  const gatewayLabel = t("header.viewGateway");

  if (plugins.length === 0) {
    return (
      <span className="flex h-10 items-center gap-2 px-2 text-base font-medium text-foreground">
        <LayoutGrid aria-hidden className="size-4 flex-none text-primary" />
        {gatewayLabel}
      </span>
    );
  }

  const activeLabel = plugins.find((plugin) => plugin.name === mode)?.display_name ?? gatewayLabel;
  const items = [
    { key: GATEWAY, label: gatewayLabel },
    ...plugins.map((plugin) => ({ key: plugin.name, label: plugin.display_name })),
  ];

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="ghost" className="h-10 max-w-[240px] rounded-sm px-2 text-base font-medium" />}
      >
        <LayoutGrid aria-hidden className="size-4 flex-none text-primary" />
        <span className="truncate">{activeLabel}</span>
        <ChevronsUpDown aria-hidden className="size-4 flex-none text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent className="w-auto">
        {items.map((item) => (
          <DropdownMenuItem key={item.key} onClick={() => setMode(item.key)}>
            <div className="flex w-full items-center justify-between gap-6 py-0.5">
              <span className="font-medium">{item.label}</span>
              {item.key === mode && <Check aria-hidden className="size-4 text-info" />}
            </div>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
