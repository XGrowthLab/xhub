/** 统一模型列表使用的展示类别；category 为授权接口的原始类别，返回筛选和分组共用的值。
 * 仅合并既有同义词，空值归为 other，未知类型原样保留；不修改模型配置、权限或原始请求字段。 */
export function displayModelCategory(category: string): string {
  const aliases: Record<string, string> = { llm: "chat", embeddings: "embedding", image_generation: "image" };
  return aliases[category] ?? (category || "other");
}
