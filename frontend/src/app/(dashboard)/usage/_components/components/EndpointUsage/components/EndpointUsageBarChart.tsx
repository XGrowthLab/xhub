import React from "react";
import { BarChart, CustomLegend, CustomTooltip } from "@/components/shared/charts";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { MetricWithMetadata } from "@/components/UsagePage/types";
import { useT } from "@/i18n";

interface EndpointUsageBarChartProps {
  endpointData?: Record<string, MetricWithMetadata>;
}

/** 将汇总后的端点成功及失败数绘制为堆叠柱；空值由图表框架处理，随容器变化换行图例。 */
const EndpointUsageBarChart: React.FC<EndpointUsageBarChartProps> = ({ endpointData }) => {
  const t = useT();
  // Transform endpoint data into chart format
  const chartData = React.useMemo(() => {
    return Object.entries(endpointData || {}).map(([endpoint, data]) => ({
      endpoint,
      "metrics.successful_requests": data.metrics.successful_requests,
      "metrics.failed_requests": data.metrics.failed_requests,
      metrics: {
        successful_requests: data.metrics.successful_requests,
        failed_requests: data.metrics.failed_requests,
      },
    }));
  }, [endpointData]);

  const valueFormatter = (value: number) => value.toLocaleString();

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle className="text-base font-semibold">{t("Success vs Failed Requests by Endpoint")}</CardTitle>
          <CustomLegend
            categories={["metrics.successful_requests", "metrics.failed_requests"]}
            colors={["green", "red"]}
          />
        </div>
      </CardHeader>
      <CardContent>
        <BarChart
          data={chartData}
          index="endpoint"
          categories={["metrics.successful_requests", "metrics.failed_requests"]}
          colors={["green", "red"]}
          valueFormatter={valueFormatter}
          customTooltip={CustomTooltip}
          showLegend={false}
          stack={true}
          yAxisWidth="auto"
        />
      </CardContent>
    </Card>
  );
};

export default EndpointUsageBarChart;
