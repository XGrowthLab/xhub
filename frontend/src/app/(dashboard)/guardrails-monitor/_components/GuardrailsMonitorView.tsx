import type { DateRangePickerValue } from "@/components/shared/date_picker_types";
import { parseAsString, useQueryState } from "nuqs";
import React, { useCallback, useMemo, useState } from "react";
import { formatDate } from "@/components/networking";
import AdvancedDatePicker from "@/components/shared/advanced_date_picker";
import { useT } from "@/i18n";
import { GuardrailDetail } from "./GuardrailDetail";
import { GuardrailsOverview } from "./GuardrailsOverview";

interface GuardrailsMonitorViewProps {
  accessToken?: string | null;
}

const defaultEnd = new Date();
const defaultStart = new Date();
defaultStart.setDate(defaultStart.getDate() - 7);

/** 切换监控总览及指定护栏；参数为会话，详情标识随 URL，两个视图共享同一日期范围。 */
export default function GuardrailsMonitorView({ accessToken = null }: GuardrailsMonitorViewProps) {
  const t = useT();
  const [selectedGuardrailId, setSelectedGuardrailId] = useQueryState(
    "guardrail",
    parseAsString.withOptions({ history: "push" }),
  );

  const initialFrom = useMemo(() => new Date(defaultStart), []);
  const initialTo = useMemo(() => new Date(defaultEnd), []);

  const [dateValue, setDateValue] = useState<DateRangePickerValue>({
    from: initialFrom,
    to: initialTo,
  });

  const startDate = dateValue.from ? formatDate(dateValue.from) : "";
  const endDate = dateValue.to ? formatDate(dateValue.to) : "";

  const handleDateChange = useCallback((newValue: DateRangePickerValue) => {
    setDateValue(newValue);
  }, []);

  const handleSelectGuardrail = (id: string) => {
    void setSelectedGuardrailId(id);
  };

  const handleBack = () => {
    void setSelectedGuardrailId(null, { history: "replace" });
  };

  const dateRangeControl = (
    <AdvancedDatePicker
      className="w-full [&>p]:sr-only @md:w-auto @max-md:[&_[data-slot=advanced-date-picker-panel]]:max-w-full"
      value={dateValue}
      onValueChange={handleDateChange}
      label={t("pages.guardrailsMonitor.dateRange")}
      showTimeRange={false}
    />
  );

  return (
    <div className="w-full min-w-0 flex-1">
      {!selectedGuardrailId ? (
        <GuardrailsOverview
          accessToken={accessToken}
          startDate={startDate}
          endDate={endDate}
          onSelectGuardrail={handleSelectGuardrail}
          dateRangeControl={dateRangeControl}
        />
      ) : (
        <>
          <div className="@container mb-4 flex w-full min-w-0 items-center justify-end">{dateRangeControl}</div>
          <GuardrailDetail
            guardrailId={selectedGuardrailId}
            onBack={handleBack}
            accessToken={accessToken}
            startDate={startDate}
            endDate={endDate}
          />
        </>
      )}
    </div>
  );
}
