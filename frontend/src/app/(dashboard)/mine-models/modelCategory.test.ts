import { expect, it } from "vitest";
import { displayModelCategory } from "./modelCategory";

/** 验证同义类别、空值及未知类别的展示归一；纯函数不改输入或产生持久数据，无需清理。 */
it.each([
  ["llm", "chat"],
  ["chat", "chat"],
  ["embeddings", "embedding"],
  ["embedding", "embedding"],
  ["image_generation", "image"],
  ["", "other"],
  ["vendor-custom", "vendor-custom"],
])("normalizes %s to %s only for display", (input, output) => {
  expect(displayModelCategory(input)).toBe(output);
});
