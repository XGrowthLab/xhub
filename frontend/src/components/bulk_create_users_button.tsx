import React, { useRef, useState, useEffect } from "react";
import Papa from "papaparse";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cva.config";
import { useT } from "@/i18n";
import { userCreateCall } from "./networking";
import { listSSO, type SSOConfiguration } from "@/lib/http/enterprise";
import { downloadUserCsv, parseUserCsv, userCsvTemplate, type CsvParse, type CsvRow } from "./user_csv";

/** 用户页面传入的管理员凭据、团队范围和刷新回调；保留角色参数以兼容现有调用方。 */
interface BulkCreateUsersProps {
  accessToken: string;
  teams: { team_id: string; team_alias?: string | null }[] | null;
  possibleUIRoles: null | Record<string, Record<string, string>>;
  onUsersCreated?: () => void;
}

/** 预览列保留固定的可读比例，横向空间不足时由表格区域滚动。 */
const previewColumns = [
  ["row", "w-[5%]"],
  ["email", "w-[16%]"],
  ["alias", "w-[12%]"],
  ["team", "w-[12%]"],
  ["role", "w-[9%]"],
  ["budget", "w-[9%]"],
  ["resetDay", "w-[11%]"],
  ["enterpriseIdentity", "w-[14%]"],
  ["status", "w-[12%]"],
] as const;

/** 用户CSV导入弹窗；接收管理员令牌、可见团队和刷新回调，返回可下载模板和逐行结果的表单。
 * 全表校验通过才提交；逐行创建允许部分失败，但同一轮不可重复提交。关闭/换文件清除密码和状态。 */
export default function BulkCreateUsersButton({ accessToken, teams, onUsersCreated }: BulkCreateUsersProps) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [parsed, setParsed] = useState<CsvParse>({ rows: [] });
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [page, setPage] = useState(0);
  const [connections, setConnections] = useState<SSOConfiguration[]>([]);
  const [connectionsLoading, setConnectionsLoading] = useState(false);
  const [connectionsError, setConnectionsError] = useState(false);
  const generation = useRef(0);
  const submitting = useRef(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const inputID = React.useId();
  const tr = (key: string, vars?: Record<string, string | number>) => t(`userCsv.${key}`, vars);
  const roleLabels = new Map([["admin", tr("adminRole")], ["user", tr("userRole")]]);
  // 弹窗关闭后忽略连接请求结果，避免旧请求覆盖下一次打开的状态。
  useEffect(() => {
    if (!open) return;
    let alive = true;
    setConnectionsLoading(true); setConnectionsError(false);
    listSSO().then((rows) => { if (alive) setConnections(rows); }).catch(() => { if (alive) { setConnections([]); setConnectionsError(true); } }).finally(() => { if (alive) setConnectionsLoading(false); });
    return () => { alive = false; };
  }, [open]);
  /** 重置导入状态并使未完成读取失效；无参数/返回值，仅清除当前弹窗数据。 */
  const reset = () => {
    generation.current++;
    setParsed({ rows: [] });
    setDone(false);
    setPage(0);
  };
  /** 读取文件并严格解析；参数为浏览器文件，无返回值；过期读取不覆盖较新的选择。 */
  const upload = async (file?: File) => {
    reset();
    if (!file) return;
    const version = generation.current;
    if (!/\.csv$/i.test(file.name) || file.size > 5 * 1024 * 1024) {
      setParsed({ rows: [], error: "file" });
      return;
    }
    try {
      const text = await file.text();
      if (version === generation.current) setParsed(parseUserCsv(text, teams?.map((team) => team.team_id) ?? [], connections.map((connection) => connection.connection_id)));
    } catch {
      if (version === generation.current) setParsed({ rows: [], error: "structure" });
    }
  };
  const invalid = parsed.rows.some((row) => row.errors.length);
  /** 提交校验后的整份列表；无参数/返回值，逐行持久化并刷新列表，不在日志或结果中暴露密码。 */
  const create = async () => {
    if (submitting.current || done || parsed.error || invalid || !parsed.rows.length) return;
    submitting.current = true;
    setBusy(true);
    const rows: CsvRow[] = parsed.rows.map((row) => ({ ...row, user: { ...row.user } }));
    try {
      for (const row of rows) {
        try {
          const response = await userCreateCall(accessToken, null, { ...row.user });
          if (!response?.user_id) throw new Error(t("errors.invalidCreateResponse"));
          row.user_id = response.user_id;
          row.status = "success";
        } catch {
          row.status = "failed";
        }
        // 已处理的密码立即丢弃，结果导出只包含白名单字段。
        row.user.password = "";
        setParsed({ rows: rows.map((item) => ({ ...item })) });
      }
      setDone(true);
      if (rows.some((row) => row.status === "success")) onUsersCreated?.();
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  };
  /** 导出非敏感结果；无参数/返回值，对电子表格公式转义，绝不导出密码或虚构Key。 */
  const results = () =>
    downloadUserCsv(
      Papa.unparse(
        parsed.rows.map((row) => ({
          user_email: row.user.user_email,
          user_id: row.user_id ?? "",
          status: row.status,
          error: row.status === "failed" ? tr("requestError") : "",
        })),
        { escapeFormulae: true },
      ),
      "users-results.csv",
    );
  const pages = Math.max(1, Math.ceil(parsed.rows.length / 10));
  return (
    <>
      <Button
        onClick={() => {
          reset();
          setOpen(true);
        }}
      >
        {tr("title")}
      </Button>
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!busy) {
            reset();
            setOpen(value);
          }
        }}
      >
        <DialogContent className="flex max-h-[90dvh] flex-col gap-4 overflow-hidden p-4 sm:max-w-[min(72rem,calc(100%_-_2rem))] sm:p-6" showCloseButton={false}>
          <DialogHeader className="shrink-0">
            <DialogTitle>{tr("title")}</DialogTitle>
            <DialogDescription>{tr("privacy")}</DialogDescription>
          </DialogHeader>
          {/* 小窗口只在表格内横向滚动。正文可独立纵向滚动，分页与提交留在固定布局的页脚。 */}
          <div className="min-h-0 min-w-0 space-y-3 overflow-y-auto px-1">
            <details className="rounded-lg border p-3 text-muted-foreground">
              <summary className="cursor-pointer font-medium text-foreground">{tr("instructions")}</summary>
              <p className="mt-2">{tr("help")}</p>
              <p className="mt-2">{tr("enterpriseIdentityHelp")}</p>
              {!connectionsError && (connectionsLoading || connections.length === 0) && (
                <p className="mt-2">{tr(connectionsLoading ? "enterpriseIdentityLoading" : "enterpriseIdentityNone")}</p>
              )}
              {connections.length > 0 && (
                <ul className="mt-2 space-y-1">
                  {connections.map((item) => (
                    <li key={item.connection_id} className="break-all">
                      <span className="font-medium text-foreground">{item.name}</span> <code>{item.connection_id}</code>
                    </li>
                  ))}
                </ul>
              )}
            </details>
            {connectionsError && <p role="alert" className="text-destructive">{tr("enterpriseIdentityLoadError")}</p>}
            <div className="flex flex-wrap items-end gap-3">
              <Button variant="outline" disabled={busy} onClick={() => downloadUserCsv(userCsvTemplate(), "users-template.csv")}>
                {tr("template")}
              </Button>
              <Button
                variant="outline"
                disabled={busy || connectionsLoading}
                onClick={() => fileInput.current?.click()}
                onDragOver={(event) => event.preventDefault()}
                onDrop={(event) => {
                  event.preventDefault();
                  if (!busy && !connectionsLoading) void upload(event.dataTransfer.files?.[0]);
                }}
              >
                {tr("upload")}
              </Button>
              <label className="sr-only" htmlFor={inputID}>{tr("upload")}</label>
              <input
                className="hidden"
                ref={fileInput}
                id={inputID}
                type="file"
                accept=".csv"
                disabled={busy || connectionsLoading}
                onChange={(event) => {
                  void upload(event.target.files?.[0]);
                  event.target.value = "";
                }}
              />
            </div>
            {parsed.error && <p role="alert" className="text-destructive">{tr(parsed.error, parsed.vars)}</p>}
            {invalid && <p role="alert" className="text-destructive">{tr("blocked")}</p>}
            {parsed.rows.length > 0 && (
              <div role="region" aria-label={t("userCsv.preview")} tabIndex={0} className="overflow-x-auto rounded-lg border focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring">
                <table className="w-full min-w-[60rem] table-fixed text-sm [&_td]:px-3 [&_td]:py-2 [&_td]:align-top [&_th]:px-3 [&_th]:py-2 [&_th]:text-start">
                  <thead className="bg-muted/60">
                    <tr>
                      {previewColumns.map(([key, width]) => (
                        <th key={key} scope="col" className={width}>{tr(key)}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {parsed.rows.slice(page * 10, page * 10 + 10).map((row) => {
                      const team = teams?.find((item) => item.team_id === row.user.team_id);
                      const connection = connections.find((item) => item.connection_id === row.user.sso_connection_id);
                      return (
                        <tr key={row.row} className="border-t [overflow-wrap:anywhere]">
                          <td className="tabular-nums text-muted-foreground">{row.row}</td>
                          <td>{row.user.user_email}</td>
                          <td>{row.user.user_alias || "—"}</td>
                          <td>{team?.team_alias || row.user.team_id || "—"}</td>
                          <td>{roleLabels.get(row.user.user_role) ?? (row.user.user_role || "—")}</td>
                          <td className="tabular-nums">{row.user.max_budget ?? "—"}</td>
                          <td>{row.user.budget_reset_day ? t("userBudgetReset.day", { day: row.user.budget_reset_day }) : t("userBudgetReset.none")}</td>
                          <td>
                            {row.user.sso_external_id ? (
                              <>
                                <div className="font-medium">{row.user.sso_external_id}</div>
                                {connection && <div className="text-xs text-muted-foreground">{connection.name}</div>}
                                <code className="text-xs text-muted-foreground">{row.user.sso_connection_id}</code>
                              </>
                            ) : "—"}
                          </td>
                          <td className={cn((row.errors.length > 0 || row.status === "failed") && "text-destructive")}>
                            {row.errors.length ? row.errors.map((key) => <p key={key}>{tr(key)}</p>) : tr(row.status ?? "pending")}
                            {row.status === "failed" && <p>{tr("requestError")}</p>}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
          <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-t pt-3">
            {parsed.rows.length > 0 && (
              <div className="flex items-center gap-2">
                <Button size="sm" variant="outline" disabled={page === 0} onClick={() => setPage(page - 1)}>
                  {tr("previous")}
                </Button>
                <span aria-live="polite" className="text-muted-foreground">{tr("page", { page: page + 1, total: pages })}</span>
                <Button size="sm" variant="outline" disabled={page + 1 === pages} onClick={() => setPage(page + 1)}>
                  {tr("next")}
                </Button>
              </div>
            )}
            {done && (
              <p role="status">
                {tr("summary", {
                  success: parsed.rows.filter((row) => row.status === "success").length,
                  failed: parsed.rows.filter((row) => row.status === "failed").length,
                })}
              </p>
            )}
            <DialogFooter className="ml-auto flex-row flex-wrap justify-end">
              <Button variant="ghost" disabled={busy} onClick={() => { reset(); setOpen(false); }}>
                {tr("close")}
              </Button>
              {parsed.rows.length > 0 && <Button variant="outline" disabled={busy} onClick={reset}>{tr("reset")}</Button>}
              {done && <Button onClick={results}>{tr("results")}</Button>}
              {parsed.rows.length > 0 && !done && (
                <Button disabled={busy || invalid || connectionsLoading} onClick={create}>
                  {busy ? tr("busy") : tr("create", { count: parsed.rows.length })}
                </Button>
              )}
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
