"use client";

import React, { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2, Trash2, Link2 } from "lucide-react";
import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { toast } from "@/lib/toast";
import { deleteMCPOAuthUserCredential, listMCPUserCredentials, MCPUserCredentialListItem } from "../networking";
import { useI18n, type Locale, type TFunction } from "@/i18n";
import { uiHref } from "@/utils/uiHref";

const MCP_CREDENTIALS_QUERY_KEY = "mcp-user-credentials";

interface Props {
  accessToken: string;
}

/** 格式化连接时间；isoString 为服务端时间、locale 为界面语言。凭据表调用，缺失或无效时间由调用方显示占位符。 */
function relativeTime(isoString: string | null | undefined, locale: Locale): string {
  if (!isoString) return "";
  const elapsed = Date.now() - Date.parse(isoString);
  if (!Number.isFinite(elapsed)) return "";
  const seconds = Math.max(0, Math.floor(elapsed / 1_000));
  const format = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  if (seconds < 60) return format.format(0, "second");
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return format.format(-minutes, "minute");
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return format.format(-hours, "hour");
  return format.format(-Math.floor(hours / 24), "day");
}

/** 本地化凭据有效期；isoString 为 UTC 时间，t 为当前语言翻译函数。凭据表调用，缺少过期时间表示长期有效。 */
function expiryLabel(
  isoString: string | null | undefined,
  t: TFunction,
): {
  text: string;
  variant: "secondary" | "destructive" | "outline";
} {
  if (!isoString) return { text: t("connect.credentialsNoExpiry"), variant: "secondary" };
  const remaining = Date.parse(isoString) - Date.now();
  if (!Number.isFinite(remaining)) return { text: t("connect.credentialsExpiryUnknown"), variant: "secondary" };
  if (remaining <= 0) return { text: t("connect.credentialsExpired"), variant: "destructive" };
  const minutes = Math.max(1, Math.ceil(remaining / 60_000));
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  if (days > 0) return { text: t("connect.credentialsExpiresDays", { count: days }), variant: "outline" };
  if (hours > 0) return { text: t("connect.credentialsExpiresHours", { count: hours }), variant: "outline" };
  return { text: t("connect.credentialsExpiresMinutes", { count: minutes }), variant: "outline" };
}

/** 当前用户的 OAuth 凭据列表；accessToken 为会话凭据。供对话凭据页使用，区分加载、失败、空列表和现有连接。 */
const MCPCredentialsTab: React.FC<Props> = ({ accessToken }) => {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const [revoking, setRevoking] = useState<Set<string>>(new Set());

  const {
    data: credentials = [],
    isLoading: loading,
    isError,
    refetch,
  } = useQuery({
    queryKey: [MCP_CREDENTIALS_QUERY_KEY, accessToken],
    queryFn: () => listMCPUserCredentials(accessToken),
    enabled: !!accessToken,
    retry: false,
  });

  /** 吊销一个服务的当前用户凭据；serverId 为服务标识。确认对话框调用，仅在后端成功后更新列表。 */
  const handleRevoke = async (serverId: string) => {
    setRevoking((prev) => new Set(prev).add(serverId));
    try {
      await deleteMCPOAuthUserCredential(accessToken, serverId);
      queryClient.setQueryData<MCPUserCredentialListItem[]>([MCP_CREDENTIALS_QUERY_KEY, accessToken], (prev) =>
        (prev ?? []).filter((c) => c.server_id !== serverId),
      );
    } catch {
      toast.error(t("connect.credentialsRevokeFailed"));
    } finally {
      setRevoking((prev) => {
        const n = new Set(prev);
        n.delete(serverId);
        return n;
      });
    }
  };

  /** 读取凭据展示名；c 为凭据对象。别名缺失时用服务名或原始 ID，不修改值。 */
  const displayName = (c: MCPUserCredentialListItem) => c.alias || c.server_name || c.server_id;

  return (
    <div className="w-full">
      <div className="mb-4">
        <h2 className="text-base font-semibold text-foreground mb-0.5">{t("connect.credentialsTitle")}</h2>
        <p className="text-sm text-muted-foreground m-0">{t("connect.credentialsDescription")}</p>
      </div>

      {loading && (
        <div className="rounded-lg border overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow className="bg-muted/50">
                <TableHead className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                  {t("App")}
                </TableHead>
                <TableHead className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                  {t("connect.credentialsConnectedAt")}
                </TableHead>
                <TableHead className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                  {t("Status")}
                </TableHead>
                <TableHead className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground text-right">
                  {t("Actions")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {Array.from({ length: 3 }, (_, i) => (
                <TableRow key={i}>
                  <TableCell>
                    <Skeleton className="h-4 w-24" />
                  </TableCell>
                  <TableCell>
                    <Skeleton className="h-4 w-16" />
                  </TableCell>
                  <TableCell>
                    <Skeleton className="h-4 w-20" />
                  </TableCell>
                  <TableCell className="text-right">
                    <Skeleton className="h-4 w-8 ml-auto" />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      {!loading && isError && (
        <div role="alert" className="rounded-lg border p-6 text-sm">
          <p>{t("connect.credentialsLoadFailed")}</p>
          <Button type="button" variant="outline" size="sm" className="mt-3" onClick={() => void refetch()}>
            {t("connect.retry")}
          </Button>
        </div>
      )}
      {!loading && !isError && !credentials?.length && (
        <div className="rounded-lg border border-dashed p-6 text-center text-sm">
          <Link2 aria-hidden className="mx-auto mb-3 size-6 text-muted-foreground" />
          <p className="font-medium">
            {t(credentials === null ? "connect.credentialsUnavailable" : "connect.credentialsEmpty")}
          </p>
          <p className="mt-1 text-muted-foreground">{t("connect.credentialsEmptyHelp")}</p>
          <Link
            href={uiHref("connect")}
            className={buttonVariants({ variant: "outline", size: "sm", className: "mt-4" })}
          >
            {t("connect.openConnections")}
          </Link>
        </div>
      )}
      {!loading && !isError && !!credentials?.length && (
        <div className="rounded-lg border overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow className="bg-muted/50">
                <TableHead className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                  {t("App")}
                </TableHead>
                <TableHead className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                  {t("connect.credentialsConnectedAt")}
                </TableHead>
                <TableHead className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                  {t("Status")}
                </TableHead>
                <TableHead className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground text-right">
                  {t("Actions")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {credentials.map((cred) => {
                const isRevoking = revoking.has(cred.server_id);
                const exp = expiryLabel(cred.expires_at, t);
                return (
                  <TableRow key={cred.server_id}>
                    <TableCell className="text-sm font-medium">{displayName(cred)}</TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {relativeTime(cred.connected_at, locale) || "\u2014"}
                    </TableCell>
                    <TableCell>
                      <Badge variant={exp.variant}>{exp.text}</Badge>
                    </TableCell>
                    <TableCell className="text-right">
                      <AlertDialog>
                        <AlertDialogTrigger
                          render={
                            <Button
                              variant="outline"
                              size="icon-sm"
                              disabled={isRevoking}
                              aria-label={t("connect.credentialsRevokeAction")}
                              title={t("connect.credentialsRevokeAction")}
                              className="text-muted-foreground hover:text-destructive hover:border-destructive/50"
                            >
                              {isRevoking ? (
                                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                              ) : (
                                <Trash2 className="h-3.5 w-3.5" />
                              )}
                            </Button>
                          }
                        />
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>{t("connect.credentialsRevokeTitle")}</AlertDialogTitle>
                            <AlertDialogDescription>
                              {t("connect.credentialsRevokeBody", { server: displayName(cred) })}
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>{t("Cancel")}</AlertDialogCancel>
                            <AlertDialogAction variant="destructive" onClick={() => handleRevoke(cred.server_id)}>
                              {t("connect.credentialsRevokeConfirm")}
                            </AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
};

export default MCPCredentialsTab;
