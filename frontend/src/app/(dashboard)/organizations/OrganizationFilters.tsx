import { FilterInput } from "@/components/common_components/Filters/FilterInput";
import { FiltersButton } from "@/components/common_components/Filters/FiltersButton";
import { ResetFiltersButton } from "@/components/common_components/Filters/ResetFiltersButton";
import { Hash, Search } from "lucide-react";
import { useT } from "@/i18n";

interface OrganizationFiltersProps {
  filters: FilterState;
  showFilters: boolean;
  onToggleFilters: (toggle: boolean) => void;
  onChange: <K extends keyof FilterState>(key: K, value: FilterState[K]) => void;
  onReset: () => void;
}

type FilterState = {
  org_id: string;
  org_alias: string;
};

/** 展示组织名称搜索与可选 ID 过滤；接收受控条件与回调，重置只清理本页的查询条件。 */
const OrganizationFilters = ({
  filters,
  showFilters,
  onToggleFilters,
  onChange,
  onReset,
}: OrganizationFiltersProps) => {
  const t = useT();
  const hasActiveFilters = !!(filters.org_id || filters.org_alias);

  return (
    <div className="flex flex-col gap-3 rounded-xl border bg-card p-3">
      {/* Search and Filter Controls */}
      <div className="flex flex-wrap items-center gap-3">
        <FilterInput
          placeholder={t("Search by Organization Name")}
          value={filters.org_alias}
          onChange={(value) => onChange("org_alias", value)}
          icon={Search}
          className="w-full sm:max-w-xs"
        />

        <FiltersButton
          onClick={() => onToggleFilters(!showFilters)}
          active={showFilters}
          hasActiveFilters={hasActiveFilters}
        />

        <ResetFiltersButton onClick={onReset} />
      </div>

      {/* Additional Filters */}
      {showFilters && (
        <div className="flex flex-wrap items-center gap-3 border-t pt-3">
          <FilterInput
            placeholder={t("Search by Organization ID")}
            value={filters.org_id}
            onChange={(value) => onChange("org_id", value)}
            icon={Hash}
            className="w-full sm:max-w-xs"
          />
        </div>
      )}
    </div>
  );
};

export default OrganizationFilters;
export type { FilterState };
