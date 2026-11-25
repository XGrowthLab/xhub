import useAuthorized from "@/app/(dashboard)/hooks/useAuthorized";
import { UsageHistoryNote } from "../UsageHistoryNote";
import { UsageRankingBars } from "../UsageRankingBars";
import { DataTable } from "@/components/shared/DataTable";
import { IdCell, MoneyCell } from "@/components/shared/table_cells";
import { ChevronDownIcon, ChevronUpIcon } from "@heroicons/react/outline";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { SimpleTooltip } from "@/components/ui/tooltip";
import React, { useState } from "react";
import { formatNumberWithCommas } from "../../../../utils/dataUtils";
import { transformKeyInfo } from "../../../key_team_helpers/transform_key_info";
import { keyInfoV1Call } from "../../../networking";
import KeyInfoView from "../../../templates/key_info_view";
import { TagUsage } from "../../types";
import { useT } from "@/i18n";

const TOP_KEYS_LIMITS = [5, 10, 25, 50] as const;

interface TopKeyViewProps {
  topKeys: any[];
  teams: any[] | null;
  showTags?: boolean;
  topKeysLimit: number;
  setTopKeysLimit: (limit: number) => void;
}

/** 展示密钥排行及表格/图表切换；参数为已授权用量与数量回调，返回视图。
 * 工具栏不足一行时按控件组换行；读取密钥详情的既有权限与调用行为保持不变。 */
const TopKeyView: React.FC<TopKeyViewProps> = ({ topKeys, teams, showTags = false, topKeysLimit, setTopKeysLimit }) => {
  const t = useT();
  const { accessToken } = useAuthorized();
  const [isModalOpen, setIsModalOpen] = useState<boolean>(false);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [keyData, setKeyData] = useState<any | undefined>(undefined);
  const [viewMode, setViewMode] = useState<"chart" | "table">("table");
  const [detailsUnavailable, setDetailsUnavailable] = useState(false);
  const [expandedTags, setExpandedTags] = useState<Set<string>>(new Set());

  const toggleTagsExpansion = (apiKey: string) => {
    setExpandedTags((prev) => {
      const newSet = new Set(prev);
      if (newSet.has(apiKey)) {
        newSet.delete(apiKey);
      } else {
        newSet.add(apiKey);
      }
      return newSet;
    });
  };

  /** 读取仍可访问的密钥详情；输入为历史排行项，空 ID 不发请求，失败明确提示且保留历史账单。 */
  const handleKeyClick = async (item: any) => {
    if (!accessToken || !item.api_key) return;
    setDetailsUnavailable(false);

    try {
      const keyInfo = await keyInfoV1Call(accessToken, item.api_key);
      const transformedKeyData = transformKeyInfo(keyInfo);

      setKeyData(transformedKeyData);
      setSelectedKey(item.api_key);
      setIsModalOpen(true); // Open modal when key is clicked
    } catch (error) {
      setDetailsUnavailable(true);
      console.error("Error fetching key info:", error);
    }
  };

  const handleClose = () => {
    setIsModalOpen(false);
    setSelectedKey(null);
    setKeyData(undefined);
  };

  // Handle clicking outside the modal
  const handleOutsideClick = (e: React.MouseEvent<HTMLDivElement>) => {
    if (e.target === e.currentTarget) {
      handleClose();
    }
  };

  // Handle escape key
  React.useEffect(() => {
    const handleEscapeKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && isModalOpen) {
        handleClose();
      }
    };

    document.addEventListener("keydown", handleEscapeKey);
    return () => document.removeEventListener("keydown", handleEscapeKey);
  }, [isModalOpen]);

  // Define columns for the table view
  const baseColumns = [
    {
      header: t("Key ID"),
      accessorKey: "api_key",
      cell: (info: any) => info.getValue() ? <IdCell value={info.getValue()} onClick={() => handleKeyClick(info.row.original)} /> : <span className="whitespace-nowrap text-muted-foreground">{t("usagePolish.unlinkedKey")}</span>,
    },
    {
      header: t("usagePolish.recordedName"),
      accessorKey: "key_alias",
      cell: (info: any) => <span className="block max-w-52 truncate" title={info.getValue() || undefined}>{info.getValue() && info.getValue() !== "-" ? info.getValue() : t(info.row.original.api_key ? "usagePolish.unnamedKey" : "usagePolish.unlinkedKey")}</span>,
    },
  ];

  const tagsColumn = {
    header: t("Tags"),
    accessorKey: "tags",
    cell: (info: any) => {
      const tags = info.getValue() as TagUsage[] | undefined;
      const apiKey = info.row.original.api_key;
      const isExpanded = expandedTags.has(apiKey);

      if (!tags || tags.length === 0) {
        return "-";
      }

      const sortedTags = tags.sort((a, b) => b.usage - a.usage);
      const displayTags = isExpanded ? sortedTags : sortedTags.slice(0, 2);
      const hasMoreTags = tags.length > 2;

      return (
        <div className="overflow-hidden">
          <div className="flex flex-wrap items-center gap-1">
            {displayTags.map((tag, index) => (
              <SimpleTooltip
                key={index}
                content={
                  <div>
                    <div>
                      <span className="text-muted-foreground">{t("Tag Name:")}</span> {tag.tag}
                    </div>
                    <div>
                      <span className="text-muted-foreground">{t("Spend:")}</span>{" "}
                      {tag.usage > 0 && tag.usage < 0.01 ? "<$0.01" : `$${formatNumberWithCommas(tag.usage, 2)}`}
                    </div>
                  </div>
                }
              >
                <span className="px-2 py-1 bg-muted rounded-full text-xs">{tag.tag.slice(0, 7)}...</span>
              </SimpleTooltip>
            ))}
            {hasMoreTags && (
              <button
                onClick={() => toggleTagsExpansion(apiKey)}
                className="ml-1 p-1 hover:bg-accent rounded-full transition-colors"
                title={isExpanded ? t("Show fewer tags") : t("Show all tags")}
              >
                {isExpanded ? (
                  <ChevronUpIcon className="h-3 w-3 text-muted-foreground" />
                ) : (
                  <ChevronDownIcon className="h-3 w-3 text-muted-foreground" />
                )}
              </button>
            )}
          </div>
        </div>
      );
    },
  };

  const spendColumn = {
    header: t("Spend (USD)"),
    accessorKey: "spend",
    meta: { numeric: true },
    cell: (info: any) => <MoneyCell value={info.getValue()} decimals={2} />,
  };

  const columns = showTags ? [...baseColumns, tagsColumn, spendColumn] : [...baseColumns, spendColumn];

  // 历史统计可以没有密钥 ID；仅为仍关联的记录保留详情动作。
  const rankedKeys = topKeys.map((key, index) => ({
    id: key.api_key || "unlinked:" + index,
    label: key.key_alias && key.key_alias !== "-" ? key.key_alias : t(key.api_key ? "usagePolish.unnamedKey" : "usagePolish.unlinkedKey"),
    value: key.spend,
    details: key.api_key ? [t("Key ID"), key.api_key] : undefined,
    interactive: Boolean(key.api_key),
  }));

  return (
    <>
      <div className="mb-3"><UsageHistoryNote /></div>
      {detailsUnavailable && <p role="status" className="mb-3 rounded-lg bg-muted p-3 text-sm text-muted-foreground">{t("usagePolish.keyDetailsUnavailable")}</p>}
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <RadioGroup
          aria-label={t("Number of top keys to show")}
          value={String(topKeysLimit)}
          onValueChange={(limit: unknown) => setTopKeysLimit(Number(limit))}
          className="inline-flex max-w-full shrink-0 flex-wrap items-center gap-1 rounded-lg bg-muted p-[3px]"
        >
          {TOP_KEYS_LIMITS.map((limit) => (
            <Label
              key={limit}
              className="flex h-8 cursor-pointer items-center rounded-md px-3 text-sm font-medium text-foreground/60 transition-colors has-data-checked:bg-background has-data-checked:text-foreground has-data-checked:shadow-sm"
            >
              <RadioGroupItem value={String(limit)} className="sr-only" />
              {limit}
            </Label>
          ))}
        </RadioGroup>
        <div className="flex shrink-0 gap-1 rounded-lg bg-muted p-1">
          <button
            onClick={() => setViewMode("table")}
            aria-pressed={viewMode === "table"}
            className={`shrink-0 h-8 whitespace-nowrap rounded-md px-3 text-sm ${viewMode === "table" ? "bg-info/15 text-info" : "bg-muted text-foreground"}`}
          >
            {t("usagePolish.table")}
          </button>
          <button
            onClick={() => setViewMode("chart")}
            aria-pressed={viewMode === "chart"}
            className={`shrink-0 h-8 whitespace-nowrap rounded-md px-3 text-sm ${viewMode === "chart" ? "bg-info/15 text-info" : "bg-muted text-foreground"}`}
          >
            {t("usagePolish.chart")}
          </button>
        </div>
      </div>

      {viewMode === "chart" ? (
        <UsageRankingBars
          items={rankedKeys}
          formatValue={value => "$" + formatNumberWithCommas(value, 2)}
          onSelect={id => {
            const key = topKeys.find(item => item.api_key === id);
            if (key) void handleKeyClick(key);
          }}
        />
      ) : (
        <DataTable columns={columns} data={topKeys} isLoading={false} maxBodyHeight={600} size="compact" />
      )}

      {isModalOpen && selectedKey && keyData && (
        <div
          className="fixed inset-0 bg-black/50 flex items-center justify-center z-overlay"
          onClick={handleOutsideClick}
        >
          <div className="bg-card rounded-lg shadow-xl relative w-11/12 max-w-6xl max-h-[90vh] overflow-y-auto min-h-[750px]">
            {/* Close button */}
            <button
              onClick={handleClose}
              className="absolute top-4 right-4 text-muted-foreground hover:text-foreground focus:outline-hidden"
              aria-label={t("Close")}
            >
              <svg className="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M6 18L18 6M6 6l12 12" />
              </svg>
            </button>

            {/* Content */}
            <div className="p-6 h-full">
              <KeyInfoView keyId={selectedKey} onClose={handleClose} keyData={keyData} teams={teams} />
            </div>
          </div>
        </div>
      )}
    </>
  );
};

export default TopKeyView;
