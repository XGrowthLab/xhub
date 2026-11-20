import { expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync, renameSync } from "node:fs";
import path from "node:path";

/** 在运行器创建的独立 schema 中执行固定测试 SQL，返回单行结果。避免读取其他运行的数据。 */
export function runStorageSQL(statement: string): string {
  const schema = readFileSync(path.join(process.env.E2E_RUN_DIR!, "schema.txt"), "utf8").trim();
  expect(schema).toMatch(/^e2e_[0-9]+_[0-9]+$/);
  return execFileSync("docker", ["exec", "-i", "xhub-postgres", "psql", "-X", "-U", "xhub", "-d",
    process.env.E2E_DATABASE || "xhub", "-qAt", "-v", "ON_ERROR_STOP=1"], {
    input: `SET search_path TO "${schema}"; ${statement}`, encoding: "utf8",
  }).trim();
}

/** 以当前浏览器私有 schema 运行真实运维 CLI。仅返回聚合 JSON，配置只传给子进程，构建由 Go 缓存复用。 */
export function storageReport(args: string[], program: "storage-report" | "history-pack" | "diagnostic-pack" = "storage-report", input?: string) {
  const dir = process.env.E2E_RUN_DIR!;
  const schema = readFileSync(path.join(dir, "schema.txt"), "utf8").trim();
  expect(schema).toMatch(/^e2e_[0-9]+_[0-9]+$/);
  const dsn: string = JSON.parse(readFileSync(path.join(dir, "c.yaml"), "utf8")).general_settings.database_url;
  expect(new URL(dsn).searchParams.get("search_path") === schema).toBe(true);
  const target = path.join(dir, "maint-" + program);
  if (!existsSync(target)) {
    const temp = target + "." + process.pid;
    execFileSync("go", ["build", "-o", temp, "./cmd/" + program], { cwd: path.resolve(__dirname, "../.."), timeout: 90_000 });
    renameSync(temp, target);
  }
  return JSON.parse(execFileSync(target, args, {
    cwd: path.resolve(__dirname, "../.."), env: { ...process.env, XHUB_DATABASE_URL: dsn },
    encoding: "utf8", input, timeout: 60_000,
  }));
}
