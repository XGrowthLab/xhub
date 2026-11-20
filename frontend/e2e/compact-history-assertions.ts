import { expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

/** 读取独立浏览器 schema 中各范围的重复历史引用总数。供有前后对照的多轮场景使用，旧格式套件返回零。这里观察库中最终的引用复用，数据库插入候选由后端触发器回归独立核查。 */
export function historyReuseReferences(): number {
  if (process.env.XHUB_COMPACT_HISTORY !== "1") return 0;
  const schema = fs.readFileSync(path.join(process.env.E2E_RUN_DIR!, "schema.txt"), "utf8").trim();
  expect(schema).toMatch(/^e2e_[0-9]+_[0-9]+$/);
  const counts = execFileSync("docker", ["exec", "-i", "xhub-postgres", "psql", "-X", "-U", "xhub", "-d", process.env.E2E_DATABASE || "xhub", "-qAt", "-v", "ON_ERROR_STOP=1"], {
    input: `SET search_path TO "${schema}"; SELECT COALESCE(sum(GREATEST(m.refs - b.blocks, 0)), 0) FROM (SELECT scope, sum(jsonb_array_length(digests)) AS refs FROM conversation_history_manifests GROUP BY scope) m JOIN (SELECT scope, count(*) AS blocks FROM conversation_history_blocks GROUP BY scope) b USING (scope);`, encoding: "utf8",
  }).trim();
  return Number(counts);
}

/** 在新格式套件中验证当前会话比可选基线多出复用引用；既有独立场景缺省从零开始，无写入。 */
export function expectCompactHistoryReuse(previous = 0) {
  if (process.env.XHUB_COMPACT_HISTORY === "1") expect(historyReuseReferences()).toBeGreaterThan(previous);
}
