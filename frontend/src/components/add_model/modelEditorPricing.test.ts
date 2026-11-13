import { setActiveLocale } from "@/i18n";
import { beforeEach, describe, expect, it } from "vitest";
import {
  buildEditorModel,
  catalogFieldValue,
  catalogRows,
  editorDefaults,
  PRICE_FIELDS,
  suggestedModelName,
  type EditorModel,
} from "./modelEditorPricing";

const baseValues = {
  supplier: "supplier-a",
  custom_llm_provider: "openai",
  model: "upstream",
  model_name: "public-name",
  endpoint_id: "chat",
  transport: "bypass_openai_chat",
  endpoint_types: ["chat"],
  pricing_source: "manual",
  pricing_format: "flat",
  billing_mode: "token",
};

describe("modelEditorPricing", () => {
  /** 目的：在 React 渲染之外直接读取价格字段；前置真实模块导入；结果为完整且不重复的配置，非法 Hook 会使导入失败；纯数据无需清理。 */
  it("loads price fields outside React rendering", () => {
    expect(PRICE_FIELDS.length).toBeGreaterThan(0);
    expect(new Set(PRICE_FIELDS.map(field => field.name)).size).toBe(PRICE_FIELDS.length);
  });
  /** 前置普通、带协议前缀及路径模型 ID；验证只修改公开名称、空值及点路径边界；纯函数无需清理。 */
  it("generates safe public names without changing upstream identifiers", () => {
    expect(suggestedModelName("openai/group/model:latest", "openai")).toBe("group/model:latest");
    expect(suggestedModelName("group/openai/model", "openai")).toBe("group/openai/model");
    expect(suggestedModelName(" gpt-4o " )).toBe("gpt-4o");
    expect(suggestedModelName("")).toBe("");
    expect(suggestedModelName("..")).toBe("model");
  });

  /** 前置有效计价和对话声明；验证空段及点段给出中文提示、路径和冒号公开名保存原值、媒体路径名可保留；无持久数据。 */
  it("validates dialogue aliases before submission and preserves native media names", () => {
    for (const model_name of ["group//model", "group/../model", ".", "..", "/model", "model/"]) {
      expect(() => buildEditorModel({...baseValues, model_name})).toThrow("对外模型名称的路径段不能留空");
    }
    const values = {...baseValues, model: "group/model:latest", model_name: "group/model:latest", input_cost_per_token: "1", output_cost_per_token: "2"};
    for (const model_name of ["group/model:latest", "byteplus/seedance-2.0/text-to-video", "名称/版本：最新"]) {
      expect(buildEditorModel({...values, model_name}).model_name).toBe(model_name);
    }
    expect(buildEditorModel(values).litellm_params.model).toBe("group/model:latest");
    expect(buildEditorModel({...values, model_name: "group/image", endpoint_types: ["image_generation"], transport: "openai_image_generation"}).model_name).toBe("group/image");
  });
  it("converts per-million prices, preserves zero, and leaves missing cache/peak prices null", () => {
    const result = buildEditorModel({
      ...baseValues,
      input_cost_per_token: "2.5",
      output_cost_per_token: "0",
      input_cost_per_token_peak: "",
      output_cost_per_token_peak: "",
      cache_read_input_token_cost: "",
      cache_creation_input_token_cost: "",
    });
    expect(result.litellm_params).toMatchObject({
      input_cost_per_token: 0.0000025,
      output_cost_per_token: 0,
      input_cost_per_token_peak: null,
      output_cost_per_token_peak: null,
      cache_read_input_token_cost: null,
      cache_creation_input_token_cost: null,
    });
  });

  it.each([
    ["image", "input_cost_per_image", "output_cost_per_image"],
    ["second", "input_cost_per_second", "output_cost_per_second"],
  ])("saves %s prices in native units", (billingMode, input, output) => {
    const result = buildEditorModel({ ...baseValues, billing_mode: billingMode, [input]: "0.12", [output]: "0" });
    expect(result.litellm_params[input]).toBe(0.12);
    expect(result.litellm_params[output]).toBe(0);
  });

  it("loads and stores an advanced mixed rates table with cache, peak, media, and variants intact", () => {
    const rates = [
      { measure: "token", side: "input", window: "offpeak", usd: 0.000002 },
      { measure: "token", side: "input", window: "peak", usd: 0.000004 },
      { measure: "token", side: "cache_read", window: "all", variant: "long-context", usd: 0 },
      { measure: "token", side: "cache_write", window: "all", usd: 0.000005 },
      { measure: "picture", side: "output", window: "all", variant: "1024x1024", usd: 0.08 },
      { measure: "second", side: "output", window: "all", variant: "720p", usd: 0.12 },
    ];
    const original: EditorModel = {
      model_name: "public-name",
      litellm_params: {
        model: "upstream",
        custom_llm_provider: "openai",
        litellm_credential_name: "supplier-a",
        rates,
      },
      model_info: { endpoint_types: ["chat"], transport: "bypass_openai_chat", pricing_source: "manual" },
    };
    const defaults = editorDefaults(original);
    expect(defaults).toMatchObject({ pricing_format: "rates", manual_rates: rates });
    expect(buildEditorModel(defaults, original).litellm_params.rates).toEqual(rates);
  });

  it("does not flatten qualified variants into a base price", () => {
    expect(
      catalogFieldValue(
        { rates: [{ measure: "token", side: "input", window: "all", variant: "long-context", usd: 0.00001 }] },
        "input_cost_per_token",
      ),
    ).toBe("");
  });

  it("clears embedded credentials when changing supplier", () => {
    const original: EditorModel = {
      model_name: "public-name",
      litellm_params: {
        model: "upstream",
        custom_llm_provider: "azure",
        litellm_credential_name: "old",
        api_key: "secret",
        api_base: "https://old.example",
        aws_region_name: "east",
        organization: "org",
      },
      model_info: { endpoint_types: ["chat"], transport: "bypass_openai_chat" },
    };
    const result = buildEditorModel({ ...baseValues, input_cost_per_token: "1" }, original);
    expect(result.litellm_params).toMatchObject({
      litellm_credential_name: "supplier-a",
      api_key: null,
      api_base: null,
      aws_region_name: null,
      organization: null,
    });
  });

  it("accepts catalog arrays and removes retired rows", () => {
    const rows = catalogRows([
      { id: "anthropic/claude", litellm_provider: "anthropic" },
      { id: "relay-a/private", litellm_provider: "openai", supplier_name: "relay-a" },
      { id: "relay-b/private", litellm_provider: "openai", supplier_name: "relay-b" },
      { id: "removed", removed: true },
    ]);
    expect(Object.keys(rows)).toEqual(["anthropic/claude", "relay-a/private", "relay-b/private"]);
    expect(rows).not.toHaveProperty("removed");
  });
});

/** 前置中文校验语境；只重置翻译状态，不访问外部资源。 */
beforeEach(() => setActiveLocale("zh-CN"));

/** 前置纯配置，不依赖 React 或后台；验证默认兼容、已有策略回显、保存和非法策略双语错误；无外部副作用。 */
it.each(["zh-CN","en"] as const)("%s validates web search metadata", (locale) => {
  setActiveLocale(locale);
  expect(editorDefaults()).toMatchObject({web_search_support:"inherit",web_search_fallback:"reject"});
  const original:EditorModel={model_name:"original",litellm_params:{},model_info:{web_search_support:"unsupported",web_search_fallback:"drop_optional"}};
  expect(editorDefaults(original)).toMatchObject(original.model_info);
  const values={...baseValues,input_cost_per_token:"1",web_search_support:"unsupported",web_search_fallback:"drop_optional"};
  expect(buildEditorModel(values).model_info).toMatchObject(original.model_info);
  for(const invalid of [{web_search_support:"guess"},{web_search_fallback:true}]) expect(()=>buildEditorModel({...values,...invalid})).toThrow(locale==="en"?"Select a valid web search":"请选择有效的联网搜索");
  expect(()=>buildEditorModel({...values,web_search_support:"supported"})).toThrow(locale==="en"?"Native web search requires":"原生联网搜索需要");
  setActiveLocale("zh-CN");
});

/** 前置双语纯配置；验证推理桥接默认关闭、已有能力回显持久化及非法值/非原生协议拒绝；不访问后台，无资源清理。 */
it.each(["zh-CN", "en"] as const)("%s validates and preserves explicit Messages reasoning mode", (locale) => {
  setActiveLocale(locale);
  expect(editorDefaults().messages_reasoning_mode).toBe("disabled");
  const values = { ...baseValues, input_cost_per_token: "1", transport: "bypass_anthropic_messages", messages_reasoning_mode: "adaptive" };
  const saved = buildEditorModel(values);
  expect(saved.model_info.messages_reasoning_mode).toBe("adaptive");
  expect(editorDefaults(saved).messages_reasoning_mode).toBe("adaptive");
  expect(buildEditorModel(editorDefaults(saved), saved).model_info.messages_reasoning_mode).toBe("adaptive");
  expect(buildEditorModel({ ...values, messages_reasoning_mode: "disabled", transport: "bypass_openai_chat" }).model_info.messages_reasoning_mode).toBe("disabled");
  for (const mode of ["enabled", true, 0, [], ["adaptive"], { mode: "adaptive" }]) {
    expect(() => buildEditorModel({ ...values, messages_reasoning_mode: mode })).toThrow(locale === "en" ? "Select a valid Messages" : "请选择有效的 Messages");
  }
  expect(() => buildEditorModel({ ...values, transport: "bypass_openai_chat" })).toThrow(locale === "en" ? "requires a native Anthropic" : "需要原生 Anthropic");
  expect(buildEditorModel({ ...baseValues, input_cost_per_token: "1" }).model_info.messages_reasoning_mode).toBe("disabled");
  setActiveLocale("zh-CN");
});

/** 自动探测开关只由显式布尔值启用；草稿默认关闭，读取及保存保持同一值，纯函数无资源清理。 */
it("preserves the explicit automatic protocol detection setting",()=>{
 expect(editorDefaults().auto_probe).toBe(false);
 const original={model_name:"probe",litellm_params:{model:"test"},model_info:{auto_probe:true}};
 expect(editorDefaults(original).auto_probe).toBe(true);
 const values={...baseValues,input_cost_per_token:"1"};
 expect(buildEditorModel(values).model_info.auto_probe).toBe(false);
 expect(buildEditorModel({...values,auto_probe:true}).model_info.auto_probe).toBe(true);
 expect(buildEditorModel({...values,auto_probe:"true"}).model_info.auto_probe).toBe(false);
});
