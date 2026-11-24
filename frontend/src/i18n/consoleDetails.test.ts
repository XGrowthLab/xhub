import { describe, expect, it } from "vitest";
import { translate } from "./translate";

describe("console detail translations", () => {
  /** 检查多个低频筛选入口的中文不混入旧机器翻译，英文仍可完整切换；纯函数测试，无数据清理。 */
  it.each([
    ["Copy to Clipboard", "复制到剪贴板"],
    ["Generated Code", "调用示例"],
    ["Generated code type", "示例代码格式"],
    ["Narrow down discovered tools", "筛选已发现的工具"],
    ["Narrow down models + endpoints", "筛选模型与接口"],
    ["Narrow down virtual keys", "筛选虚拟密钥"],
    ["Narrow down workflow runs", "筛选工作流运行记录"],
    ["Narrow down your budgets", "筛选预算"],
    ["Narrow down your teams", "筛选团队"],
    ["Narrows every target above to requests for these models", "以上目标仅统计这些模型的请求"],
    ["Create New Project", "创建项目"],
    ["Key Name", "密钥名称"],
    ["No reset", "不重置"],
    ["Search by key name…", "搜索密钥名称…"],
  ])("translates %s", (key, expected) => {
    expect(translate("zh-CN", key)).toBe(expected);
    expect(translate("en", key)).toBe(key);
  });
});

/** 验证多计费单位说明和表头同时提供两种语言；纯翻译函数，无外部依赖或清理。 */
it.each([
  ["zh-CN", "单价", "计费单位以各行标注为准"],
  ["en", "Unit price", "Each row shows its billing unit"],
] as const)("uses unit-neutral pricing copy in %s", (locale, label, note) => {
  expect(translate(locale, "myModels.unitPrice")).toBe(label);
  expect(translate(locale, "myModels.priceNote")).toContain(note);
});
