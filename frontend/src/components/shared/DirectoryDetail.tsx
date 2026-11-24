"use client";

import type { ReactNode } from "react";
import { ArrowLeft } from "lucide-react";
import CopyButton from "@/components/shared/CopyButton";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";

/** 展示目录资源的标题、可复制标识与可选管理操作；由用户、团队和组织详情使用，不改变路由或权限。 */
export function DirectoryDetailHeader({ title, id, copyLabel, backLabel, onBack, actions }: {
  title: string;
  id: string;
  copyLabel: string;
  backLabel: string;
  onBack: () => void;
  actions?: ReactNode;
}) {
  return (
    <header className="min-w-0 space-y-3">
      <Button variant="ghost" size="sm" onClick={onBack} className="-ml-2">
        <ArrowLeft aria-hidden="true" />
        {backLabel}
      </Button>
      <div className="flex min-w-0 flex-wrap items-end justify-between gap-x-6 gap-y-3">
        <div className="min-w-0 max-w-full">
          <h1 className="text-2xl font-semibold tracking-tight [overflow-wrap:anywhere]">{title}</h1>
          <div className="mt-1 flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
            <code className="min-w-0 [overflow-wrap:anywhere]">{id}</code>
            <CopyButton value={id} label={copyLabel} className="shrink-0" />
          </div>
        </div>
        {actions && <div className="flex min-w-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
    </header>
  );
}

/** 统一目录概览和详情的卡片。标题与操作由调用方翻译，children 可为可读字段或可操作的成员列表。 */
export function DirectoryPanel({ title, actions, children }: { title: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <Card size="sm" className="min-w-0">
      <CardHeader className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">{title}</h2>
        {actions}
      </CardHeader>
      <CardContent className="min-w-0">{children}</CardContent>
    </Card>
  );
}

/** 将只读字段以键值网格呈现。fields 由详情页准备，长 ID、路径和大金额可换行，不挤压旁列。 */
export function DirectoryFacts({ fields }: { fields: readonly { label: string; value: ReactNode }[] }) {
  return (
    <dl className="grid min-w-0 gap-x-6 gap-y-4 sm:grid-cols-2">
      {fields.map(({ label, value }) => (
        <div key={label} className="min-w-0">
          <dt className="mb-1 text-xs font-medium text-muted-foreground">{label}</dt>
          <dd className="text-sm tabular-nums [overflow-wrap:anywhere]">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** 共享账户、团队和组织的编辑容器；关闭只回调调用方，保留当前列表和页签。长表单可独立滚动。 */
export function DirectoryEditDialog({ open, title, onClose, children, busy = false }: {
  open: boolean;
  title: string;
  onClose: () => void;
  children: ReactNode;
  busy?: boolean;
}) {
  return (
    <Dialog open={open} disablePointerDismissal={busy} onOpenChange={(next) => !next && !busy && onClose()}>
      <DialogContent
        className="flex max-h-[calc(100dvh-2rem)] min-w-0 flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl"
        showCloseButton={!busy}
      >
        <DialogHeader className="shrink-0 border-b px-4 py-4 pr-12 sm:px-6 sm:pr-14">
          <DialogTitle className="text-lg font-semibold tracking-tight">{title}</DialogTitle>
        </DialogHeader>
        <div className="min-h-0 min-w-0 overflow-y-auto overscroll-contain p-4 sm:p-6">{children}</div>
      </DialogContent>
    </Dialog>
  );
}
