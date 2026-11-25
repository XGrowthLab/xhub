import { useT } from "@/i18n";
import { cn } from "@/lib/cva.config";

interface RankingItem {
  id: string;
  label: string;
  value: number;
  details?: string[];
  interactive?: boolean;
}

interface UsageRankingBarsProps {
  items: RankingItem[];
  formatValue: (value: number) => string;
  onSelect?: (id: string) => void;
}

/**
 * 用量榜单调用。接收同一指标的排名及其格式化函数，返回随卡片宽度变化的条形图。
 * 没有数据时显示本地化状态，零值不绘制条形；可选点击只上报稳定标识，不自行请求或修改数据。
 */
export function UsageRankingBars({ items, formatValue, onSelect }: UsageRankingBarsProps) {
  const t = useT();
  const max = items.reduce((largest, item) => Math.max(largest, item.value), 0);

  if (!items.length) {
    return <p className="rounded-lg border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">{t("usagePolish.noRankingData")}</p>;
  }

  return (
    <ul className="max-h-[22rem] min-w-0 space-y-1 overflow-y-auto overscroll-contain pr-1">
      {items.map(({ id, label, value, details, interactive = true }) => {
        const selectable = !!onSelect && interactive;
        const Row = selectable ? "button" : "div";
        const width = max > 0 ? Math.max(0, value / max) * 100 : 0;
        return (
          <li key={id}>
            <Row
              type={selectable ? "button" : undefined}
              onClick={selectable ? () => onSelect?.(id) : undefined}
              className={cn("block w-full min-w-0 rounded-md px-2 py-2 text-left", selectable && "focus-visible:ring-2 focus-visible:ring-ring hover:bg-muted")}
            >
              <span className="mb-1.5 flex min-w-0 items-baseline justify-between gap-3 text-sm">
                <span className="min-w-0 truncate font-medium" title={[label, ...(details ?? [])].join("\n")}>{label}</span>
                <span className="shrink-0 tabular-nums text-muted-foreground">{formatValue(value)}</span>
              </span>
              <span aria-hidden="true" className="block h-1.5 overflow-hidden rounded-full bg-muted">
                <span className="block h-full rounded-full bg-info" style={{ width: `${width}%` }} />
              </span>
            </Row>
          </li>
        );
      })}
    </ul>
  );
}
