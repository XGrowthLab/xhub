import type { KeyMetadata } from "./types";

/** 从历史元数据选择请求时名称；参数为快照及缺省值，返回原始别名、邮箱或调用方回退值，不推断当前密钥状态。 */
export function keyActivityLabel(
  metadata: Pick<KeyMetadata, "key_alias" | "user_email"> | null | undefined,
  fallback = "-",
): string {
  return metadata?.key_alias || metadata?.user_email || fallback;
}
