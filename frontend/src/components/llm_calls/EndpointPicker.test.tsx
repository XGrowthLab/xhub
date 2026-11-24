import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi } from "vitest";
import { I18nProvider } from "@/i18n";
import { translate } from "@/i18n/translate";
import { EndpointPicker, endpointTitle } from "./EndpointPicker";
import { endpointUIType } from "./model_endpoints";
import { EndpointType } from "@/components/chat_ui/mode_endpoint_mapping";
import type { ModelEndpoint } from "./fetch_models";
const bindings: ModelEndpoint[] = [
  {
    endpoint_id: "chat",
    protocol: "openai-chat",
    kind: "adapted",
    family: "chat",
    transport: "bypass_openai_responses",
    method: "POST",
    path: "/v1/chat/completions",
  },
  {
    endpoint_id: "bypass:openai-chat",
    protocol: "openai-chat",
    kind: "bypass",
    family: "chat",
    transport: "bypass_openai_chat",
    method: "POST",
    path: "/bypass/openai/v1/chat/completions",
  },
];
/** 双语组件验证简短标签、模式、路径、真实选择及禁用边界；DOM 自动清理，不访问后台。 */
describe.each(["zh-CN", "en"] as const)("EndpointPicker %s", (locale) => {
  /** 前置同时支持两种协议模式，切换后只列出该模式的路径，保留可读全路径；组件测试不使用网络。 */
  it("keeps paths and protocol selection unambiguous", async () => {
    const t = (key: string) => translate(locale, key);
    const change = vi.fn();
    const user = userEvent.setup();
    const view = render(
      <I18nProvider initialLocale={locale}>
        <EndpointPicker endpoints={bindings} value={bindings[1].path} onChange={change} />
      </I18nProvider>,
    );
    expect(screen.getByLabelText(t("protocolProbe.path"))).toHaveTextContent(bindings[1].path);
    const picker = screen.getByRole("combobox");
    await user.click(picker);
    expect(within(screen.getByRole("listbox")).getAllByRole("option")).toHaveLength(1);
    expect(screen.getByRole("option")).toHaveTextContent(bindings[1].path);
    expect(screen.getByText(bindings[1].path, { selector: "span" })).toHaveClass("break-all");
    await user.keyboard("{Escape}");
    const standard = screen.getByRole("button", { name: t("protocolProbe.standard"), exact: true });
    await user.click(standard);
    expect(change).toHaveBeenLastCalledWith(bindings[0].path);
    view.rerender(
      <I18nProvider initialLocale={locale}>
        <EndpointPicker endpoints={bindings} value={bindings[0].path} onChange={change} />
      </I18nProvider>,
    );
    expect(standard).toHaveAttribute("aria-pressed", "true");
    await user.click(picker);
    expect(within(screen.getByRole("listbox")).getAllByRole("option")).toHaveLength(1);
    expect(screen.getByRole("option")).toHaveTextContent(bindings[0].path);
    expect(screen.getByRole("option")).not.toHaveTextContent("/bypass/");
  });
  /** 前置仅有标准入口，验证透传禁用，且不创建额外路径；渲染由测试框架回收。 */
  it("does not invent a missing bypass endpoint", () => {
    const t = (key: string) => translate(locale, key);
    render(
      <I18nProvider initialLocale={locale}>
        <EndpointPicker endpoints={[bindings[0]]} value={bindings[0].path} onChange={vi.fn()} />
      </I18nProvider>,
    );
    expect(screen.getByRole("button", { name: t("protocolProbe.bypass"), exact: true })).toBeDisabled();
  });
});
/** 验证 Chat 透传启用对话表单、Google 流式不混同普通动作；纯函数，无需清理。 */
it("maps native Chat and distinguishes streaming actions", () => {
  expect(endpointUIType(bindings[1])).toBe(EndpointType.CHAT);
  expect(
    endpointTitle({ ...bindings[0], protocol: "gemini", path: "/v1beta/models/test:streamGenerateContent" }, "流式"),
  ).toBe("Gemini · 流式");
});
