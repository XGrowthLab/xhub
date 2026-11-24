import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, Shield, TriangleAlert } from "lucide-react";
import React, { useMemo, useState } from "react";
import { getGuardrailsUsageLogs } from "@/components/networking";
import { useGuardrailsUsageDetail } from "@/app/(dashboard)/hooks/guardrails/useGuardrailsUsage";
import { StatusBadge, type StatusTone } from "@/components/shared/table_cells/status_badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { UiLoadingSpinner } from "@/components/ui/ui-loading-spinner";
import { GuardrailUsageBreakdown } from "./GuardrailUsageBreakdown";
import { LogViewer } from "@/components/GuardrailsMonitor/LogViewer";
import { MetricCard } from "@/components/GuardrailsMonitor/MetricCard";
import type { LogEntry } from "@/components/GuardrailsMonitor/mockData";
import { useT } from "@/i18n";

interface GuardrailDetailProps {
  guardrailId: string;
  onBack: () => void;
  accessToken?: string | null;
  startDate: string;
  endDate: string;
}

const STATUS_TONE: Record<string, StatusTone> = {
  healthy: "success",
  warning: "warning",
  critical: "error",
};

/** 展示护栏的统计、用量和日志；参数为护栏标识、会话、日期和返回回调，按访问权限读取原始数据。 */
export function GuardrailDetail({ guardrailId, onBack, accessToken = null, startDate, endDate }: GuardrailDetailProps) {
  const t = useT();
  const [activeTab, setActiveTab] = useState("overview");
  const [logsPage] = useState(1);
  const logsPageSize = 50;

  const {
    data: detailData,
    isLoading: detailLoading,
    error: detailError,
  } = useGuardrailsUsageDetail(guardrailId, { accessToken, startDate, endDate });
  const { data: logsData, isLoading: logsLoading } = useQuery({
    queryKey: ["guardrails-usage-logs", guardrailId, logsPage, logsPageSize],
    queryFn: () =>
      getGuardrailsUsageLogs(accessToken!, {
        guardrailId,
        page: logsPage,
        pageSize: logsPageSize,
        startDate,
        endDate,
      }),
    enabled: !!accessToken && !!guardrailId,
  });

  const logs: LogEntry[] = useMemo(() => {
    const list = logsData?.logs ?? [];
    return list.map((l: Record<string, unknown>) => ({
      id: l.id as string,
      timestamp: l.timestamp as string,
      action: l.action as "blocked" | "passed" | "flagged",
      score: l.score as number | undefined,
      model: l.model as string | undefined,
      input_snippet: l.input_snippet as string | undefined,
      output_snippet: l.output_snippet as string | undefined,
      reason: l.reason as string | undefined,
      latency_ms: l.latency_ms as number | undefined,
      guardrail_name: l.guardrail_name as string | undefined,
      guardrail_provider: l.guardrail_provider as string | null | undefined,
      guardrail_mode: l.guardrail_mode as string | null | undefined,
    }));
  }, [logsData?.logs]);

  const data = detailData
    ? {
        name: detailData.guardrail_name,
        description: detailData.description ?? "",
        status: detailData.status,
        provider: detailData.provider,
        type: detailData.type,
        requestsEvaluated: detailData.requestsEvaluated,
        failRate: detailData.failRate,
        avgScore: detailData.avgScore,
        avgLatency: detailData.avgLatency,
      }
    : {
        name: guardrailId,
        description: "",
        status: "healthy",
        provider: "—",
        type: "—",
        requestsEvaluated: 0,
        failRate: 0,
        avgScore: undefined as number | undefined,
        avgLatency: undefined as number | undefined,
      };

  if (detailLoading && !detailData) {
    return (
      <div role="status" aria-busy="true" aria-label={t("Loading")} className="flex items-center justify-center py-12">
        <UiLoadingSpinner className="size-8 text-primary" />
      </div>
    );
  }
  if (detailError && !detailData) {
    return (
      <div>
        <Button variant="link" onClick={onBack} className="mb-4 pl-0">
          <ArrowLeft className="size-4" />
          {t("Back to Overview")}
        </Button>
        <p className="text-destructive">{t("Failed to load guardrail details.")}</p>
      </div>
    );
  }

  const logViewer = (filterAction?: "all") => (
    <LogViewer
      guardrailName={data.name}
      filterAction={filterAction}
      logs={logs}
      logsLoading={logsLoading}
      totalLogs={logsData?.total ?? 0}
      accessToken={accessToken}
      startDate={startDate}
      endDate={endDate}
    />
  );

  return (
    <div className="@container min-w-0">
      <div className="mb-6">
        <Button variant="link" onClick={onBack} className="mb-4 pl-0">
          <ArrowLeft className="size-4" />
          {t("Back to Overview")}
        </Button>

        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="mb-1 flex flex-wrap items-center gap-2">
              <Shield className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
              <h1 className="min-w-0 break-words text-xl font-semibold text-foreground">{data.name}</h1>
              <StatusBadge
                tone={STATUS_TONE[data.status] ?? "success"}
                label={t(`pages.guardrailsMonitor.status.${data.status}`)}
              />
            </div>
            {data.description && <p className="text-sm text-muted-foreground">{data.description}</p>}
          </div>
          <div className="flex items-center gap-2">
            <Badge variant="outline">{data.provider}</Badge>
          </div>
        </div>
      </div>

      <Tabs value={activeTab} onValueChange={(value) => setActiveTab(value as string)}>
        <TabsList variant="line">
          <TabsTrigger value="overview" className="flex-none">
            {t("Overview")}
          </TabsTrigger>
          <TabsTrigger value="logs" className="flex-none">
            {t("Logs")}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="mt-4 space-y-6">
          <div className="grid grid-cols-2 gap-3 md:grid-cols-3">
            <MetricCard compact label={t("Requests Evaluated")} value={data.requestsEvaluated.toLocaleString()} />
            <MetricCard
              compact
              label={t("Fail Rate")}
              value={`${data.failRate}%`}
              valueColor={data.failRate > 15 ? "text-destructive" : data.failRate > 5 ? "text-warning" : "text-success"}
              subtitle={t("{value0} blocked", { value0: (Math.round((data.requestsEvaluated * data.failRate) / 100).toLocaleString()) })}
              icon={data.failRate > 15 ? <TriangleAlert className="size-4 text-destructive" /> : undefined}
            />
            <MetricCard
              compact
              label={t("Avg. latency added")}
              value={data.avgLatency != null ? `${Math.round(data.avgLatency)}ms` : "—"}
              valueColor={
                data.avgLatency != null
                  ? data.avgLatency > 150
                    ? "text-destructive"
                    : data.avgLatency > 50
                      ? "text-warning"
                      : "text-success"
                  : "text-muted-foreground"
              }
              subtitle={data.avgLatency != null ? t("Per request (avg)") : t("No data")}
            />
          </div>

          {detailData && <GuardrailUsageBreakdown detail={detailData} />}

          {logViewer("all")}
        </TabsContent>

        <TabsContent value="logs" className="mt-4">
          {logViewer()}
        </TabsContent>
      </Tabs>

    </div>
  );
}
