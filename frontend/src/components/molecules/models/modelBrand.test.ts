import { describe, expect, it } from "vitest";
import { resolveModelBrand } from "./modelBrand";

describe("resolveModelBrand", () => {
  /** 用常见模型及 OpenAI 兼容标识复现原厂识别错误；返回真实品牌与本地图标，无外部资源。 */
  it.each([
    ["moonshotai/kimi-k3.1-pro", "Moonshot AI", "moonshot"],
    ["openai/deepseek-v4-pro", "DeepSeek", "deepseek"],
    ["grok-4.7", "xAI", "xai"],
    ["qwen3.5-72b", "Qwen", "qwen"],
    ["claude-opus-4-7", "Anthropic", "anthropic"],
    ["gemini-3-pro", "Google", "google"],
    ["meta-llama/llama-3.3", "Meta", "meta_llama"],
    ["mistralai/mistral-small", "Mistral AI", "mistral"],
    ["gpt-6.1-sol", "OpenAI", "openai"],
    ["whisper-1", "OpenAI", "openai"],
    ["dall-e-3", "OpenAI", "openai"],
  ])("resolves %s independently of the compatibility provider", (model, name, logo) => {
    expect(resolveModelBrand({ model, provider: "openai" })).toEqual({ name, logo: expect.stringContaining(logo) });
  });

  /** 目录声明优先，已知原厂别名归一化；未收录发行方保留名称及空图标，无持久数据。 */
  it("prefers a declared issuer and preserves an unlisted issuer", () => {
    expect(resolveModelBrand({ model: "gpt-personal-alias", issuer: "Moonshot-Kimi", provider: "openai" }))
      .toEqual({ name: "Moonshot AI", logo: expect.stringContaining("moonshot") });
    expect(resolveModelBrand({ model: "kimi-custom", issuer: "Independent Labs" }))
      .toEqual({ name: "Independent Labs", logo: "" });
  });

  /** 前置来源缺失、损坏或只有兼容协议；应回落为未知，真实原厂命名空间和专用标识仍可识别。 */
  it("does not infer OpenAI for anonymous or non-OpenAI embedding models", () => {
    expect(resolveModelBrand({ model: "openai/internal-alias", provider: "openai", issuer: [] })).toBeNull();
    expect(resolveModelBrand({ model: "text-embedding-v3", provider: "openai" })).toBeNull();
    expect(resolveModelBrand({ model: "", provider: "openai_like" })).toBeNull();
    expect(resolveModelBrand({ model: "glm-5", provider: "openai" })).toEqual({ name: "Z.ai", logo: "" });
    expect(resolveModelBrand({ model: "gateway/moonshotai/release-42" })?.name).toBe("Moonshot AI");
    expect(resolveModelBrand({ model: "internal-launch", provider: "xai" })?.name).toBe("xAI");
  });
});
