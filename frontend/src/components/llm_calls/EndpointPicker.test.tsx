import { render, screen } from "@testing-library/react";
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
  it("keeps paths and protocol selection unambiguous", async () => {
    const t = (key: string) => translate(locale, key);
    const change = vi.fn();
    const user = userEvent.setup();
    render(
      <I18nProvider initialLocale={locale}>
        <EndpointPicker endpoints={bindings} value={bindings[1].path} onChange={change} />
      </I18nProvider>,
    );
    expect(screen.getByLabelText(t("protocolProbe.path"))).toHaveTextContent(bindings[1].path);
    await user.click(screen.getByRole("button", { name: t("protocolProbe.standard"), exact: true }));
    expect(change).toHaveBeenLastCalledWith(bindings[0].path);
    await user.click(screen.getByRole("combobox"));
    await user.click(screen.getByRole("option", { name: /^OpenAI Chat\s*\/v1\/chat\/completions$/ }));
  });
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
