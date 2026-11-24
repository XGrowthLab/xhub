"use client";

import { useEffect, useRef, useState } from "react";
import { ArrowDownToLine, CircleCheck, RotateCcw, Server } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { useT } from "@/i18n";
import { ApiError } from "@/lib/http/client";
import { submitVersionUpdate, versionUpdateStatus, type UpdateAction, type UpdateChannel, type UpdateTarget, type VersionUpdateStatus } from "@/lib/http/versionUpdates";

const activeStages = new Set(["checking", "pulling", "switching", "recovering"]);
const failures = new Set(["interrupted", "rolled_back", "recovery_required", "check_failed", "prepare_failed", "invalid_target"]);

type Confirmation = { action: "apply"; version: string; revision: string } | { action: "rollback"; version: string };

/** 展示候选版本、兼容性和纯文本发布说明。由状态面板在已检查到新版本时渲染，只读。 */
function ReleasePreview({ candidate }: { candidate: NonNullable<VersionUpdateStatus["candidate"]> }) {
  const t = useT();
  return (
    <section aria-label={t("versionUpdates.target")} className="space-y-2 rounded-lg border p-4">
      <h4 className="flex flex-wrap items-center gap-2 font-medium">
        {t("versionUpdates.available", { version: candidate.version })}
        {candidate.prerelease && <span className="rounded-full bg-amber-100 px-2 py-1 text-xs text-amber-900">{t("versionUpdates.prerelease")}</span>}
      </h4>
      {!candidate.compatible && <p className="text-sm text-muted-foreground">{t("versionUpdates.incompatible")}</p>}
      {candidate.notes && (
        <details>
          <summary className="cursor-pointer text-sm">{t("versionUpdates.notes")}</summary>
          <p className="mt-2 max-h-52 overflow-auto whitespace-pre-wrap break-words text-sm">{candidate.notes}</p>
        </details>
      )}
    </section>
  );
}

/** 显示主机状态、检查与渠道按钮，并把需确认的镜像切换交给父组件。忙或恢复时禁用操作。 */
function UpdateDetails({ status, active, busy, onCheck, onChannel, onConfirm }: {
  status: VersionUpdateStatus;
  active: boolean;
  busy: boolean;
  onCheck: () => void;
  onChannel: (channel: UpdateChannel) => void;
  onConfirm: (target: Confirmation) => void;
}) {
  const t = useT();
  const { candidate, current, previous, stage, error } = status;
  const blocked = busy || status.recovery_required;
  const checkedCurrent = status.checked && !candidate && stage === "idle";
  return (
    <>
      <dl className="grid gap-3 rounded-lg border bg-muted/20 p-4 sm:grid-cols-3">
        <div><dt className="text-sm text-muted-foreground">{t("versionUpdates.current")}</dt><dd className="font-medium">{current ?? t("versionUpdates.unknown")}</dd></div>
        <div><dt className="text-sm text-muted-foreground">{t("versionUpdates.channel")}</dt><dd className="font-medium">{t(status.channel === "beta" ? "versionUpdates.beta" : "versionUpdates.stable")}</dd></div>
        <div><dt className="text-sm text-muted-foreground">{t("versionUpdates.previous")}</dt><dd className="font-medium">{previous ?? t("versionUpdates.none")}</dd></div>
      </dl>
      {!current && <p role="status">{t("versionUpdates.bootstrap")}</p>}
      {active && <p role="status" className="font-medium">{t("versionUpdates.stage." + stage)}</p>}
      {stage === "succeeded" && <p role="status" className="flex items-center gap-2"><CircleCheck aria-hidden className="size-4" />{t("versionUpdates.success", { version: current ?? "" })}</p>}
      {error && <p role="alert" className="text-sm text-destructive">{t(failures.has(error) ? "versionUpdates.errors." + error : "versionUpdates.unavailable")}</p>}
      {Boolean(checkedCurrent) && <p role="status">{t("versionUpdates.currentRelease")}</p>}
      {candidate && <ReleasePreview candidate={candidate} />}
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" disabled={blocked} onClick={onCheck}>
          {t("versionUpdates.check")}
        </Button>
        {status.channel_switch_enabled && <Button type="button" variant="outline" disabled={blocked}
          onClick={() => onChannel(status.channel === "beta" ? "stable" : "beta")}>
          {t(status.channel === "beta" ? "versionUpdates.switchToStable" : "versionUpdates.switchToBeta")}
        </Button>}
        {candidate && <Button type="button" disabled={blocked || !candidate.compatible}
          onClick={() => onConfirm({ action: "apply", version: candidate.version, revision: candidate.revision })}>
          <ArrowDownToLine aria-hidden className="size-4" />{t("versionUpdates.apply")}
        </Button>}
        {previous && <Button type="button" variant="outline" disabled={blocked}
          onClick={() => onConfirm({ action: "rollback", version: previous })}>
          <RotateCcw aria-hidden className="size-4" />{t("versionUpdates.rollback")}
        </Button>}
      </div>
    </>
  );
}

/** 在管理员页检查、应用或回退版本，并允许主机授权的渠道切换。展示持久化状态与发布说明。
 * 选中页签时读取状态，即使网关重启也继续恢复轮询。仅响应用户点击提交，不重试写操作。
 */
export default function VersionUpdates() {
  const t = useT();
  const [status, setStatus] = useState<VersionUpdateStatus>();
  const [requestError, setRequestError] = useState<"unavailable" | "conflict" | "channelLocked" | null>(null);
  const [pending, setPending] = useState(false);
  const [confirm, setConfirm] = useState<Confirmation | null>(null);
  const activity = useRef(0);
  const alive = useRef(false);
  const stage = status?.stage;
  const active = !!stage && activeStages.has(stage);
  const busy = pending || active;

  useEffect(() => {
    alive.current = true;
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    /** 重新读取持久化阶段；无参数。断网继续刷新，网关恢复后显示结果，不重复提交部署。 */
    async function refresh() {
      const epoch = activity.current;
      try {
        const next = await versionUpdateStatus();
        if (!disposed && epoch === activity.current) {
          setStatus(next);
          setRequestError((existing) => existing === "unavailable" ? null : existing);
        }
      } catch {
        if (!disposed && epoch === activity.current) setRequestError("unavailable");
      } finally {
        if (!disposed) timer = setTimeout(() => void refresh(), 2500);
      }
    }
    void refresh();
    return () => {
      disposed = true;
      alive.current = false;
      clearTimeout(timer);
    };
  }, []);

  /** 发送一次已选操作。渠道锁定和并发冲突给出对应提示，连接中断时由读状态确认结果。 */
  async function submit(action: UpdateAction, target: UpdateTarget = {}) {
    if (busy) return;
    const epoch = ++activity.current;
    setPending(true);
    setRequestError(null);
    setConfirm(null);
    try {
      const next = await submitVersionUpdate(action, target);
      if (alive.current && epoch === activity.current) setStatus(next);
    } catch (error) {
      if (alive.current && epoch === activity.current) {
        const response = error instanceof ApiError ? error : null;
        const type = (response?.body as { error?: { type?: string } } | null)?.error?.type;
        setRequestError(response?.status === 409 ? "conflict" : type === "update_channel_locked" ? "channelLocked" : "unavailable");
      }
    } finally {
      if (epoch === activity.current) activity.current++;
      if (alive.current) setPending(false);
    }
  }

  /** 确认弹窗触发一次已选操作；弹窗无有效目标时直接返回。 */
  function confirmTarget() {
    if (!confirm) return;
    const { action, ...target } = confirm;
    void submit(action, target);
  }

  return (
    <Card aria-labelledby="version-updates-title">
      <CardHeader>
        <div className="flex items-center gap-2">
          <Server aria-hidden className="size-5 text-muted-foreground" />
          <CardTitle><h3 id="version-updates-title">{t("versionUpdates.title")}</h3></CardTitle>
        </div>
        <CardDescription>{t("versionUpdates.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {!status && !requestError && <p role="status">{t("versionUpdates.loading")}</p>}
        {requestError && <p role="alert" className="text-sm text-destructive">{t("versionUpdates." + requestError)}</p>}
        {status && !status.enabled && <p className="text-sm text-muted-foreground">{t("versionUpdates.disabled")}</p>}
        {status?.enabled && (
          <UpdateDetails status={status} active={active} busy={busy} onCheck={() => void submit("check")}
            onChannel={(channel) => void submit("channel", { channel })} onConfirm={setConfirm} />
        )}
        <Dialog open={!!confirm} onOpenChange={(open) => { if (!open) setConfirm(null); }}>
          <DialogContent>
            <DialogTitle>{t(confirm?.action === "rollback" ? "versionUpdates.confirmRollback" : "versionUpdates.confirmApply", { version: confirm?.version ?? "" })}</DialogTitle>
            <DialogDescription>{t("versionUpdates.confirmDescription")}</DialogDescription>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={() => setConfirm(null)}>{t("versionUpdates.cancel")}</Button>
              <Button disabled={busy || !confirm} type="button" onClick={confirmTarget}>{t("versionUpdates.confirm")}</Button>
            </div>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  );
}
