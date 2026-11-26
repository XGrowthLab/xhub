import { useT } from "@/i18n";
import type { LogEntry } from "./columns";

const phaseKeys: Readonly<Record<string, string>> = {
  request_body: "requestBodyPhase",
  before_upstream: "beforeUpstreamPhase",
  upstream_request: "upstreamRequestPhase",
  response_transfer: "responseTransferPhase",
  client_write: "clientWritePhase",
  request_validation: "requestValidationPhase",
  request_conversion: "requestConversionPhase",
  route_selection: "routeSelectionPhase",
  response_conversion: "responseConversionPhase",
};

/** 展示失败的补充证据。参数 log 为授权日志，返回阶段、部署冷却数和上游 HTTP 状态。
 * 列表摘要和详情共用。上游状态为 0 时尚未观察到 HTTP 响应，不把它当成状态或补造原因。 */
export function RequestLogDiagnosticFacts({ log }: { log: LogEntry }) {
  const t = useT();
  const { diagnosis, metadata } = log;
  const upstream = metadata?.upstream_response;
  const status = Number(diagnosis?.upstream_status ?? upstream?.status_code);
  const phase = diagnosis?.phase ?? upstream?.failure?.phase;
  const cooling = diagnosis?.cooling_deployments;
  const compatible = diagnosis?.compatible_deployments;
  const observedPhase = typeof phase === "string" && Object.hasOwn(phaseKeys, phase) ? phaseKeys[phase] : undefined;
  return (
    <>
      {diagnosis?.request_method && log.inbound_endpoint && (
        <p className="break-all">{t("logsUX.observedEndpoint", { method: diagnosis.request_method, path: log.inbound_endpoint })}</p>
      )}
      {typeof cooling === "number" && typeof compatible === "number" && compatible > 0 && (
        <p>{t("logsUX.coolingDeploymentCount", { cooling, compatible })}</p>
      )}
      {status >= 100 && status <= 599 && <p>{t("logsUX.observedUpstreamStatus", { status })}</p>}
      {observedPhase && <p>{t(`logsUX.${observedPhase}`)}</p>}
    </>
  );
}
