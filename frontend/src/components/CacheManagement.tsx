"use client";

import { useEffect, useState } from "react";
import { apiClient } from "@/components/networking";
import { RefreshCw, Trash2 } from "lucide-react";
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
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

/** 展示实际进程缓存并提供原有清理能力；accessToken为当前登录凭据，返回双语管理视图。
 * 不提供未接执行器的配置表单；接口失败或未知响应不得显示成功，卸载/切换身份忽略旧查询。
 */
export function CacheManagement({ accessToken }: { accessToken: string | null }) {
  const t = useT();
  const [generation, setGeneration] = useState(0);
  const [status, setStatus] = useState<{ token: string; ready: boolean } | null>(null);
  const [result, setResult] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    /** 读取运行状态；无参数返回Promise；失败保持不可清理，忽略卸载后的结果，不改配置。 */
    async function load() {
      if (!accessToken) return;
      try {
        const data = await apiClient.get<{
          enabled: boolean;
          cache_type: string;
          scope: string;
          configurable: boolean;
        }>("/cache/settings", { accessToken });
        const processMemory = data.cache_type === "memory" && data.scope === "process";
        const ready = processMemory && data.enabled === true && data.configurable === false;
        if (!cancelled) setStatus({ token: accessToken, ready });
      } catch {
        if (!cancelled) setStatus({ token: accessToken, ready: false });
      }
    }
    void load();
    return () => {
      cancelled = true;
    };
  }, [accessToken, generation]);

  /** 清理当前进程响应缓存；点击调用，无参数；只有原成功契约确认后显示成功，不改预算和用量。 */
  async function flush() {
    if (!accessToken || status?.token !== accessToken || !status.ready || busy) return;
    setBusy(true);
    setResult("");
    try {
      const data = await apiClient.post<{ status: string; message: string }>("/flushall", { accessToken, body: {} });
      if (data.status === "ok" && data.message === "cache flushed") {
        setResult("flushed");
        setGeneration((value) => value + 1);
      } else {
        setResult("flushFailed");
      }
    } catch {
      setResult("flushFailed");
    } finally {
      setBusy(false);
    }
  }

  /** 手动重试状态查询；清除旧状态与提示后递增查询版本，无参数及返回，不执行清理。 */
  function refresh() {
    setStatus(null);
    setResult("");
    setGeneration((value) => value + 1);
  }

  const current = status?.token === accessToken ? status : null;
  let stateKey = "loading";
  if (current) stateKey = current.ready ? "ready" : "loadFailed";
  return (
    <section className="space-y-4 p-6">
      <h1 className="text-2xl font-semibold">{t("cacheAdmin.title")}</h1>
      <p>{t("cacheAdmin.scope")}</p>
      <p>{t("cacheAdmin.unsupported")}</p>
      <div className="grid gap-4 lg:grid-cols-2">
        <section className="space-y-3 rounded-lg border p-4">
          <h2 className="font-semibold">{t("cacheAdmin.statusTitle")}</h2>
          <p role={current && !current.ready ? "alert" : "status"}>{t(`cacheAdmin.${stateKey}`)}</p>
          <Button variant="outline" onClick={refresh} disabled={busy}>
            <RefreshCw aria-hidden="true" /> {t("cacheAdmin.refresh")}
          </Button>
        </section>
        <section className="space-y-3 rounded-lg border border-destructive/25 p-4">
          <h2 className="font-semibold">{t("cacheAdmin.flushSection")}</h2>
          <p className="text-sm text-muted-foreground">{t("cacheAdmin.flushScope")}</p>
          <AlertDialog>
            <AlertDialogTrigger render={<Button variant="destructive" />} disabled={!current?.ready || busy}>
              <Trash2 aria-hidden="true" /> {t(`cacheAdmin.${busy ? "flushing" : "flush"}`)}
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>{t("cacheAdmin.flushTitle")}</AlertDialogTitle>
                <AlertDialogDescription>{t("cacheAdmin.flushScope")}</AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{t("cacheAdmin.cancel")}</AlertDialogCancel>
                <AlertDialogAction variant="destructive" onClick={() => void flush()}>
                  {t("cacheAdmin.flushConfirm")}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
          {result && (
            <p role={result === "flushFailed" ? "alert" : "status"} data-testid="cache-result">
              {t(`cacheAdmin.${result}`)}
            </p>
          )}
        </section>
      </div>
    </section>
  );
}
