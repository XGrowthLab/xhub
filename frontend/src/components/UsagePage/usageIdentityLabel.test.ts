import { afterEach, describe, expect, it } from "vitest";
import { setActiveLocale } from "@/i18n";
import { usageModelLabel } from "./usageIdentityLabel";

/** 每例恢复英文运行时，避免本地化状态污染其他确定性单元测试。 */
afterEach(() => setActiveLocale("en"));
describe("usageModelLabel", () => {
  /** 真实模型名称不可翻译或重写，空值与后端 unknown 保留费用但解释为缺失归属；无外部依赖。 */
  it.each(["en", "zh-CN"] as const)("explains missing attribution in %s", (locale) => {
    setActiveLocale(locale);
    expect(usageModelLabel("gpt-6.1-terra")).toBe("gpt-6.1-terra");
    for (const value of ["", " ", null, undefined, "unknown"]) expect(usageModelLabel(value)).toBe(locale === "en" ? "Model not recorded" : "未记录模型名称");
  });
});
