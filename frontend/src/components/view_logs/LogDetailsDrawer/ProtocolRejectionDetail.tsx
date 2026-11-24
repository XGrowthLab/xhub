import { useT } from "@/i18n";

interface ProtocolRejection {
  stage: string;
  source_protocol: string;
  problems: Array<{ field?: string; target_protocol?: string; details: string }>;
}

/** 将后端持久化的兼容拒绝展示成可读列表；只渲染确有原因的记录。字段、协议 ID 和原始诊断保持原样。 */
export function ProtocolRejectionDetail({ rejection }: { rejection?: ProtocolRejection }) {
  const t = useT();
  if (!rejection?.problems?.length) return null;
  return (
    <section aria-label={t("logDiagnostics.protocolTitle")} className="mb-6 space-y-3 rounded-lg border p-4">
      <h3 className="font-semibold">{t("logDiagnostics.protocolTitle")}</h3>
      <p className="text-sm text-muted-foreground">
        {t(rejection.stage === "request_conversion" ? "logDiagnostics.rejectedConversion" : "logDiagnostics.rejectedValidation",
          { protocol: rejection.source_protocol })}
      </p>
      <ul className="space-y-2">
        {rejection.problems.map(({ field, target_protocol, details }) => (
          <li key={JSON.stringify([field, target_protocol, details])} className="space-y-1 rounded-md border bg-muted/25 p-3 text-sm">
            {field && <p>{t("logDiagnostics.rejectedField")} <code className="break-all font-medium">{field}</code></p>}
            {target_protocol && <p>{t("logDiagnostics.targetProtocol")} <code>{target_protocol}</code></p>}
            <p className="break-words text-muted-foreground">{details}</p>
          </li>
        ))}
      </ul>
    </section>
  );
}
