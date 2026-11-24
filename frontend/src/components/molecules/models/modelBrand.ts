import { getProviderLogoAndName } from "@/components/provider_info_helpers";

const names = {
  openai: "OpenAI",
  anthropic: "Anthropic",
  gemini: "Google",
  deepseek: "DeepSeek",
  xai: "xAI",
  zai: "Z.ai",
  dashscope: "Qwen",
  moonshot: "Moonshot AI",
  minimax: "MiniMax",
  volcengine: "ByteDance",
  meta_llama: "Meta",
  mistral: "Mistral AI",
  cohere: "Cohere",
} as const;

type KnownBrand = keyof typeof names;

const aliases: Record<string, KnownBrand> = {
  aliyun: "dashscope",
  qwen: "dashscope",
  google: "gemini",
  bytedance: "volcengine",
  kimi: "moonshot",
  "moonshot-ai": "moonshot",
  "moonshot-kimi": "moonshot",
  moonshotai: "moonshot",
  "x-ai": "xai",
  "z-ai": "zai",
  "z.ai": "zai",
  "meta-llama": "meta_llama",
  meta: "meta_llama",
  mistralai: "mistral",
};

const families: readonly (readonly [RegExp, KnownBrand])[] = [
  [/^(?:gpt-|o[134](?:-|$)|text-embedding-(?:ada-002|3-(?:small|large))(?:$|-))/, "openai"],
  [/^(?:chatgpt-|dall-e-|sora-|omni-moderation-|whisper-1$|tts-1(?:-hd)?$|(?:babbage|davinci)-002$)/, "openai"],
  [/^claude-/, "anthropic"],
  [/^(?:gemini-|imagen-|veo-)/, "gemini"],
  [/^deepseek-/, "deepseek"],
  [/^grok-/, "xai"],
  [/^glm-/, "zai"],
  [/^qwen(?:\d|[-_]|$)/, "dashscope"],
  [/^kimi[-_]/, "moonshot"],
  [/^(?:minimax-|abab)/, "minimax"],
  [/^(?:doubao-|seedance-|seedream-)/, "volcengine"],
  [/^llama(?:\d|[-_]|$)/, "meta_llama"],
  [/^(?:mistral-|mixtral-|codestral-)/, "mistral"],
  [/^command-(?:a|r)(?:-|$)/, "cohere"],
];

// 这些执行标识可以代理其他厂商的模型，不能凭该字段给未知型号加原厂品牌。
const protocolBrands = new Set<KnownBrand>(["openai", "anthropic", "gemini"]);

/** 将原厂名或模型命名空间转换为已知品牌标识。输入可省略，未知别名返回空值。 */
function knownBrand(input: string | undefined): KnownBrand | undefined {
  const key = input?.trim().toLowerCase().replace(/\s+/g, "-");
  if (!key) return;
  if (Object.hasOwn(names, key)) return key as KnownBrand;
  if (Object.hasOwn(aliases, key)) return aliases[key];
}

/** 解析展示用的原厂及本地标志。model 为公开型号，issuer 为明确的目录原厂，provider 为旧接口提供的执行标识。
 * 优先使用目录原厂，其次根据型号或命名空间识别。缺少原厂证据且只有协议标识时返回空值，无读取或修改部署的副作用。
 */
export function resolveModelBrand({
  model,
  issuer,
  provider,
}: {
  model: string;
  issuer?: unknown;
  provider?: string | null;
}): { name: string; logo: string } | null {
  const declared = typeof issuer === "string" ? issuer.trim() : "";
  if (declared) {
    const brand = knownBrand(declared);
    return { name: brand ? names[brand] : declared, logo: getProviderLogoAndName(brand ?? declared).logo };
  }
  const parts = model.trim().toLowerCase().split("/");
  const family = families.find(([pattern]) => pattern.test(parts.at(-1) ?? ""))?.[1];
  const namespace = parts.slice(0, -1).reverse().map(knownBrand).find((brand) => brand && !protocolBrands.has(brand));
  const legacy = knownBrand(provider ?? undefined);
  const nativeProvider = legacy && !protocolBrands.has(legacy) ? legacy : undefined;
  const brand = family ?? namespace ?? nativeProvider;
  if (brand) return { name: names[brand], logo: getProviderLogoAndName(brand).logo };
  return null;
}
