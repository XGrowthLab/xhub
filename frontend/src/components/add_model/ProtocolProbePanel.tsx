"use client";
import { useState } from "react";
import { useT } from "@/i18n";
import { apiClient } from "@/components/networking";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { useQuery, useQueryClient } from "@tanstack/react-query";

type ProbeRecord = {
  checked_at?: string;
  verified?: Record<string, boolean>;
  results?: { protocol: string; status: string; stage: string; http_status?: number }[];
};
/** ProtocolProbePanel 提供管理员自动探测开关、保存后手动验证和固定诊断结果。
 * id/token 指向已保存部署与管理会话，automatic/onChange 接入编辑表单；没有 ID 时不能向任意草稿地址发请求。
 * 自动状态读取仅访问网关，不主动触发上游；手动请求失败清楚提示，双语文案不展示上游正文或密钥。 */
export function ProtocolProbePanel({
  id,
  token,
  automatic,
  onChange,
}: {
  id?: string;
  token?: string | null;
  automatic: boolean;
  onChange: (value: boolean) => void;
}) {
  const t = useT();
  const client = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const key = ["protocolProbe", id];
  const { data, isError } = useQuery({
    queryKey: key,
    enabled: !!id && !!token,
    refetchInterval: automatic ? 5000 : false,
    queryFn: async () => {
      const result = await apiClient.get<{ data?: { model_info?: { protocol_probe?: ProbeRecord } }[] }>(
        `/v2/model/info?modelId=${encodeURIComponent(id!)}`,
        { accessToken: token! },
      );
      return result.data?.[0]?.model_info?.protocol_probe ?? null;
    },
  });
  /** run 仅探测已保存部署；成功刷新当前状态及模型目录，失败不伪造支持能力，finally 恢复按钮。 */
  const run = async () => {
    setBusy(true);
    setFailed(false);
    try {
      await apiClient.post("/model/protocol_probe", { accessToken: token!, body: { id } });
      await client.invalidateQueries({ queryKey: key });
      await client.invalidateQueries({ queryKey: ["models"] });
      await client.invalidateQueries({ queryKey: ["mine-models"] });
    } catch {
      setFailed(true);
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="space-y-4 rounded-xl border bg-muted/20 p-4" aria-label={t("protocolProbe.title")}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h4 className="text-sm font-semibold">{t("protocolProbe.title")}</h4>
        <Button type="button" variant="outline" size="sm" disabled={!id || busy || !token} onClick={run}>
          {t(busy ? "protocolProbe.running" : "protocolProbe.manual")}
        </Button>
      </div>
      <div className="flex items-center gap-3">
        <Switch id="auto-protocol-probe" checked={automatic} onCheckedChange={onChange} />
        <Label htmlFor="auto-protocol-probe">{t("protocolProbe.automatic")}</Label>
      </div>
      <p className="text-xs leading-5 text-muted-foreground">{t("protocolProbe.help")}</p>
      {!id && <p className="text-xs text-muted-foreground">{t("protocolProbe.saveFirst")}</p>}
      {(failed || isError) && (
        <p role="alert" className="text-sm text-destructive">
          {t("protocolProbe.failed")}
        </p>
      )}
      <div className="grid gap-2 sm:grid-cols-3" aria-label={t("protocolProbe.status")}>
        {(data?.results ?? []).map((result) => (
          <div key={result.protocol} className="min-w-0 rounded-lg border bg-background p-3 text-xs">
            <p className="font-medium">
              {{ "openai-chat": "Chat", "openai-responses": "Responses", "anthropic-messages": "Messages" }[
                result.protocol
              ] ?? result.protocol}
            </p>
            <p
              className={`mt-2 ${result.status === "verified" ? "text-emerald-700 dark:text-emerald-400" : "text-muted-foreground"}`}
            >
              {t("protocolProbe." + result.status)}
            </p>
            <p className="mt-1 text-muted-foreground">
              {t("protocolProbe." + result.stage)}
              {result.http_status ? ` · HTTP ${result.http_status}` : ""}
            </p>
            {result.status !== "verified" && data?.verified?.[result.protocol] && (
              <p className="mt-2">{t("protocolProbe.retained")}</p>
            )}
          </div>
        ))}
      </div>
      <p className="text-xs text-muted-foreground">
        {data?.checked_at ? t("protocolProbe.checked", { time: data.checked_at }) : t("protocolProbe.empty")}
      </p>
      <p className="text-xs leading-5 text-muted-foreground">{t("protocolProbe.scope")}</p>
    </section>
  );
}
