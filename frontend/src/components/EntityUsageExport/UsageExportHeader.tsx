import type { DateRangePickerValue } from "@/components/shared/date_picker_types";
import { Download } from "lucide-react";
import React, { useEffect, useId, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Combobox,
  ComboboxChip,
  ComboboxChips,
  ComboboxChipsInput,
  ComboboxClear,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxItem,
  ComboboxList,
  ComboboxValue,
  useComboboxAnchor,
} from "@/components/ui/combobox";
import EntityUsageExportModal from "./EntityUsageExportModal";
import type { EntitySpendData, EntityType } from "./types";
import type { Team } from "@/components/key_team_helpers/key_list";
import { useT } from "@/i18n";

interface UsageExportHeaderProps {
  dateValue: DateRangePickerValue;
  entityType: EntityType;
  spendData: EntitySpendData;
  // Optional filter props
  showFilters?: boolean;
  filterLabel?: string;
  filterPlaceholder?: string;
  selectedFilters?: string[];
  onFiltersChange?: (filters: string[]) => void;
  filterOptions?: Array<{ label: string; value: string }>;
  filterSlot?: React.ReactNode;
  customTitle?: string;
  compactLayout?: boolean;
  teams?: Team[];
  disableExport?: boolean;
}

/** 展示实体筛选与导出入口；参数为范围、实体、用量、筛选回调及导出禁用状态，返回标题区。
 * 用量页调用；失败/不完整时只禁用导出，保留筛选以便清除失效条件；变更为禁用时关闭旧导出，不修改后台数据。 */
const UsageExportHeader: React.FC<UsageExportHeaderProps> = ({
  dateValue,
  entityType,
  spendData,
  showFilters = false,
  filterLabel,
  filterPlaceholder,
  selectedFilters = [],
  onFiltersChange,
  filterOptions = [],
  filterSlot,
  customTitle,
  teams = [],
  disableExport = false,
}) => {
  const t = useT();
  const anchor = useComboboxAnchor();
  const inputId = useId();
  const [isExportModalOpen, setIsExportModalOpen] = useState(false);

  useEffect(() => { if (disableExport) setIsExportModalOpen(false); }, [disableExport]);

  const hasFilters = filterSlot != null || showFilters;
  const optionValues = filterOptions.map((option) => option.value);
  const labelOf = (value: string) => filterOptions.find((option) => option.value === value)?.label ?? value;
  const hasNoOptions = filterOptions.length === 0;
  const emptyPlaceholder = t("usageLoad.noOptions");
  // A selection carried over from a range that did have options still scopes
  // the data below, so the control has to stay usable long enough to clear it.
  const isFilterDisabled = hasNoOptions && selectedFilters.length === 0;

  const filterList = (
    <ComboboxContent anchor={anchor}>
      <ComboboxEmpty>{t("No options found")}</ComboboxEmpty>
      <ComboboxList>
        {(value: string) => (
          <ComboboxItem key={value} value={value}>
            {labelOf(value)}
          </ComboboxItem>
        )}
      </ComboboxList>
    </ComboboxContent>
  );

  const builtInFilter = (
    <Combobox
      multiple
      disabled={isFilterDisabled}
      items={optionValues}
      value={selectedFilters}
      onValueChange={(next: string[]) => onFiltersChange?.(next)}
    >
      <ComboboxChips render={<div ref={anchor} />} className="w-full">
        <ComboboxValue>
          {(selected: string[]) =>
            selected.map((value) => (
              <ComboboxChip key={value} aria-label={labelOf(value)}>
                {labelOf(value)}
              </ComboboxChip>
            ))
          }
        </ComboboxValue>
        <ComboboxChipsInput
          id={inputId}
          placeholder={hasNoOptions ? emptyPlaceholder : filterPlaceholder}
          aria-label={hasNoOptions ? emptyPlaceholder : filterPlaceholder}
        />
        {selectedFilters.length > 0 && <ComboboxClear aria-label={t("Clear {value0}", { value0: (filterLabel ?? t("filters")) })} />}
      </ComboboxChips>
      {filterList}
    </Combobox>
  );

  return (
    <>
      <div className={hasFilters ? "flex flex-wrap items-end gap-3 rounded-lg border bg-card px-4 py-3" : "flex justify-end"}>
        {hasFilters && (
          <div className="min-w-0 grow basis-56 space-y-1.5">
            {filterLabel && (filterSlot == null
              ? <label htmlFor={inputId} className="block text-sm font-medium text-muted-foreground">{filterLabel}</label>
              : <p className="text-sm font-medium text-muted-foreground">{filterLabel}</p>)}
            {filterSlot ?? builtInFilter}
          </div>
        )}
        <Button
          variant="outline"
          className="ml-auto shrink-0"
          disabled={disableExport}
          onClick={() => setIsExportModalOpen(true)}
        >
          <Download />
          {t("Export Data")}
        </Button>
      </div>

      <EntityUsageExportModal
        isOpen={isExportModalOpen && !disableExport}
        onClose={() => setIsExportModalOpen(false)}
        entityType={entityType}
        spendData={spendData}
        dateRange={dateValue}
        selectedFilters={selectedFilters}
        customTitle={customTitle}
        teams={teams}
      />
    </>
  );
};

export default UsageExportHeader;
