import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@/i18n";
import { translate } from "@/i18n/translate";
import { ApiError } from "@/lib/http/client";
import { ProtocolProbePanel } from "./ProtocolProbePanel";
const api = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("@/components/networking", () => ({ apiClient: api }));
beforeEach(() => {
  vi.resetAllMocks();
  api.get.mockResolvedValue({ data: [] });
  api.post.mockResolvedValue({});
});
/** renderPanel 为每条测试建立隔离查询缓存和双语上下文；参数语言/ID，返回清理后的 DOM 管理句柄，无远端请求。 */
function renderPanel(locale: "en" | "zh-CN", id?: string, automatic = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
  });
  return render(
    <I18nProvider initialLocale={locale}>
      <QueryClientProvider client={client}>
        <ProtocolProbePanel id={id} token="fixture" automatic={automatic} onChange={vi.fn()} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}
/** 双语组件验证未保存不能探测、保存 ID 是唯一请求参数、固定错误及旧成功保留提示；查询缓存随卸载回收。 */
it.each(["zh-CN", "en"] as const)("shows truthful status and safe requests in %s", async (locale) => {
  const tr = (k: string) => translate(locale, k);
  const user = userEvent.setup();
  const view = renderPanel(locale);
  expect(screen.getByRole("button", { name: tr("protocolProbe.manual") })).toBeDisabled();
  view.unmount();
  api.get.mockResolvedValue({
    data: [
      {
        model_info: {
          protocol_probe: {
            checked_at: "2026-11-11T08:00:00Z",
            verified: { "openai-chat": true },
            results: [{ protocol: "openai-chat", status: "temporarily_failed", stage: "stream", http_status: 503 }],
          },
        },
      },
    ],
  });
  renderPanel(locale, "saved-deployment");
  await screen.findByText(tr("protocolProbe.retained"));
  expect(screen.getByText(tr("protocolProbe.temporarily_failed"))).toBeVisible();
  expect(screen.getByText("Chat", { exact: true })).toBeVisible();
  expect(screen.getByText(translate(locale, "protocolProbe.httpStatus", { status: 503 }), { exact: false })).toBeVisible();
  await user.click(screen.getByRole("button", { name: tr("protocolProbe.manual") }));
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith("/model/protocol_probe", {
      accessToken: "fixture",
      body: { id: "saved-deployment" },
    }),
  );
  api.post.mockRejectedValue(new Error("private transport detail"));
  await waitFor(() => expect(screen.getByRole("button", { name: tr("protocolProbe.manual") })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: tr("protocolProbe.manual") }));
  expect(await screen.findByRole("alert")).toHaveTextContent(tr("protocolProbe.failed"));
  expect(screen.queryByText("private transport detail")).not.toBeInTheDocument();
});

/** 读取状态失败须明确提示，不能显示为已验证；错误详情含内部地址也不展示，组件卸载清理查询。 */
it("shows a safe error when detection status cannot be loaded", async () => {
  api.get.mockRejectedValue(new Error("private-service-url"));
  renderPanel("zh-CN", "saved");
  expect(await screen.findByRole("alert")).toHaveTextContent(translate("zh-CN", "protocolProbe.loadFailed"));
  expect(screen.queryByText("private-service-url")).not.toBeInTheDocument();
});

/** 双语验证不同 HTTP 失败使用可理解的分类提示，不展示内部正文；每次渲染使用隔离缓存并自动卸载。 */
it.each(["zh-CN", "en"] as const)("classifies detection failures in %s", async(locale)=>{
 for(const [status,key] of [[400,"configurationFailed"],[401,"unauthorized"],[403,"forbidden"],[404,"missing"],[409,"conflict"],[503,"unavailable"]] as const){
  api.post.mockRejectedValue(new ApiError("private-internal-detail",status,{}));
  const view=renderPanel(locale,"saved");
  await userEvent.setup().click(screen.getByRole("button",{name:translate(locale,"protocolProbe.manual")}));
  expect(await screen.findByRole("alert")).toHaveTextContent(translate(locale,"protocolProbe."+key));
  expect(screen.queryByText("private-internal-detail")).not.toBeInTheDocument();view.unmount();
 }
});

/** 未保存的开关不能启动状态轮询：后台仍为关闭时推进时钟，只允许初次读取；卸载后还原计时器。 */
it("does not poll based on an unsaved automatic toggle", async()=>{
 vi.useFakeTimers();const view=renderPanel("en","saved",true);
 try{
  await vi.advanceTimersByTimeAsync(1);
  await vi.advanceTimersByTimeAsync(6000);
  expect(api.get).toHaveBeenCalledTimes(1);
 }finally{view.unmount();vi.useRealTimers();}
});
