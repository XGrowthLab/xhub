"use client";
import { useT } from "@/i18n";
import { SearchSelect } from "@/components/shared/SearchSelect";
import type { ModelEndpoint } from "./fetch_models";

/** endpointTitle 生成简短协议标签；参数真实绑定和已国际化流式后缀，返回可搜索名称，不把路径堆进主标题。 */
export function endpointTitle(endpoint: ModelEndpoint, streamLabel: string): string {
  const names: Record<string, string> = {
    "openai-chat": "OpenAI Chat",
    "openai-responses": "OpenAI Responses",
    "anthropic-messages": "Anthropic Messages",
    gemini: "Gemini",
    vertex: "Vertex AI",
  };
  const title = names[endpoint.protocol] ?? endpoint.protocol;
  return endpoint.path.includes(":streamGenerateContent") ? `${title} · ${streamLabel}` : title;
}
/** EndpointPicker 展示当前模型真实入口；先区分标准/透传，再选择简短协议，完整路径独立显示。
 * 参数 endpoints/value/onChange 为后端绑定、当前路径与选择回调，label/disabled 控制可访问名称及可用状态。
 * 返回响应式选择区；切组只选择组内真实入口，不创造未支持端点，不修改用户对话内容。 */
export function EndpointPicker({
  endpoints,
  value,
  onChange,
  label,
  disabled = false,
}: {
  endpoints: ModelEndpoint[];
  value?: string | null;
  onChange: (value: string | null) => void;
  label?: string;
  disabled?: boolean;
}) {
  const t = useT();
  const selected = endpoints.find((e) => e.path === value);
  // 根据后端入口标识分类；旧绑定无标识时才用已注册路径兜底，不据供应商名称猜测。
  const isBypass = (e: ModelEndpoint) => e.endpoint_id?.startsWith("bypass:") ?? e.path.startsWith("/bypass/");
  const bypass = selected ? isBypass(selected) : false;
  const options = endpoints.map((e) => ({
    value: e.path,
    label: endpointTitle(e, t("protocolProbe.streamMode")) + (isBypass(e) ? ` · ${t("protocolProbe.bypass")}` : ""),
    sublabel: e.path,
  }));
  return (
    <section
      className="min-w-0 space-y-3 rounded-xl border bg-card p-4"
      aria-label={label ?? t("protocolProbe.select")}
    >
      <div className="flex w-fit gap-1 rounded-lg bg-muted p-1" role="group" aria-label={t("protocolProbe.select")}>
        {[false, true].map((mode) => (
          <button
            type="button"
            key={String(mode)}
            aria-pressed={mode === bypass}
            disabled={disabled || !endpoints.some((e) => isBypass(e) === mode)}
            onClick={() => onChange(endpoints.find((e) => isBypass(e) === mode)?.path ?? null)}
            className={`rounded-md px-3 py-1.5 text-sm font-medium transition-colors disabled:opacity-40 ${mode === bypass ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"}`}
          >
            {t(mode ? "protocolProbe.bypass" : "protocolProbe.standard")}
          </button>
        ))}
      </div>
      <SearchSelect
        options={options}
        value={selected?.path ?? null}
        onValueChange={onChange}
        disabled={disabled}
        allowClear={false}
        aria-label={label ?? t("protocolProbe.select")}
        placeholder={t("Select an endpoint")}
        emptyText={t("protocolProbe.noProtocols")}
        className="h-10"
      />
      {selected && (
        <div className="min-w-0 rounded-md bg-muted/50 px-3 py-2">
          <span className="mr-2 text-xs font-semibold text-primary">{selected.method}</span>
          <code aria-label={t("protocolProbe.path")} className="break-all text-xs leading-5">
            {selected.path}
          </code>
        </div>
      )}
      <p className="text-xs leading-5 text-muted-foreground">
        {t(bypass ? "protocolProbe.bypassHelp" : "protocolProbe.standardHelp")}
      </p>
    </section>
  );
}
