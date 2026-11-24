"use client";

import React, { useEffect, useState } from "react";
import { Archive, Inbox, Pencil, ShieldCheck, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useT } from "@/i18n";
import { samlFailureKey } from "@/lib/enterpriseSSO";
import { deleteSSO, readEnterprise, saveEnterprise, type ArchiveStatus, type EnterpriseConfiguration as Configuration } from "@/lib/http/enterprise";
import EnterpriseConfiguration from "./EnterpriseConfiguration";

/** 读取真实集成状态并展示空态、摘要和编辑表单；id/name 指向 SSO，省略则为全局 Kodo。
 * publicOrigin 用于生成回调，onDeleted 通知列表刷新，SSO 与归档均使用独立弹窗；读取失败禁止写入，开关成功才变更摘要。
 * 凭据只存在编辑表单内存；摘要按已保存协议区分 SAML 实体标识和 OIDC 客户端，组件卸载阻止异步读取更新。
 */
export default function IntegrationOverview({id, name, publicOrigin = "", onDeleted}: {id?: string; name?: string; publicOrigin?: string; onDeleted?:()=>void}) {
  const t = useT();
  const oidc = !!id;
  const path = oidc ? "/admin/enterprise/sso/" + encodeURIComponent(id!) : "/admin/archive/kodo";
  const [configuration, setConfiguration] = useState<Configuration | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const [failureKey, setFailureKey] = useState("enterprise.error");
  const [editing, setEditing] = useState(false);
  const [deleting,setDeleting]=useState(false);

  /** 删除当前 SSO；无参数，返回 Promise，失败保持确认窗及原卡片，不触及 SCIM，确认按钮调用。 */
  async function remove() {
    if (!id || busy) return;setBusy(true);setError(false);
    try {await deleteSSO(id);setDeleting(false);onDeleted?.();}catch{setFailureKey("enterprise.error");setError(true);}finally{setBusy(false);}
  }
  useEffect(() => {
    let alive = true;
    setLoading(true); setError(false);
    readEnterprise<Configuration | ArchiveStatus>(path).then((snapshot) => {
      if (alive) setConfiguration(oidc ? snapshot as Configuration : (snapshot as ArchiveStatus).configuration);
    }).catch((failure: unknown) => {
      if (!alive) return;
      // 尚未保存的 OIDC 返回标准 404，权限、网络和存储失败不可伪装为空态。
      if (oidc && (failure as {status?: number})?.status === 404) setConfiguration(null);
      else {setFailureKey("enterprise.error");setError(true);}
    }).finally(() => {if (alive) setLoading(false);});
    return () => {alive = false;};
  }, [path, oidc]);

  /** 保存已加载配置的启用位；无参数，返回 Promise。后台继续校验 discovery/凭据及 IdP 证书。
   * 失败保持原状态，已知证书错误展示固定翻译，空配置不允许启用，不输出后台原始错误。
   */
  async function toggle() {
    if (!configuration) return;
    setBusy(true); setError(false);
    const next = {...configuration, enabled: !configuration.enabled};
    try {await saveEnterprise(path, next); setConfiguration(next);} catch (failure) {setFailureKey(samlFailureKey(failure) || "enterprise.error");setError(true);} finally {setBusy(false);}
  }

  /** 采用保存接口返回的配置更新当前卡片，清除错误并关闭编辑弹窗；只在保存成功后调用。 */
  function completeEdit(saved: Configuration) {
    setConfiguration(saved);
    setError(false);
    setEditing(false);
  }

  const saml = oidc && configuration?.protocol === "saml";
  const certificateExpired = saml && configuration?.idp_certificate_expired === true;
  const summaryLabels: Record<string, string> = saml ? {issuer: "samlIssuer", client_id: "samlEntityID"} : {};
  const summaryFields = oidc ? ["issuer", "client_id"] : ["bucket", "prefix"];
  const title = t(oidc ? "enterprise.ssoHeading" : "enterprise.archiveTitle");
  const Icon = oidc ? ShieldCheck : Archive;
  const displayName = String(configuration?.name ?? name ?? "");
  const action = oidc ? t("enterprise.configureName", {name: displayName}) : t("enterprise.configureArchive");
  const caption = oidc ? t("enterprise.ssoDescription") : t("enterprise.archiveBrief");
  return <section className="min-w-0 rounded-xl border bg-card p-4 sm:p-5" aria-label={oidc ? t("enterprise.ssoOverviewName", {name: displayName}) : title}>
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="flex min-w-0 items-start gap-3">
        <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary">
          <Icon aria-hidden="true" className="size-4" />
        </span>
        <div className="min-w-0">
          <h3 className="wrap-anywhere text-base font-semibold">{oidc && displayName ? displayName : title}</h3>
          <p className="mt-1 text-sm text-muted-foreground">{caption}</p>
        </div>
      </div>
      {!loading && configuration && (
        <div className="flex items-center gap-2 rounded-md bg-muted/50 px-2.5 py-1.5 text-sm">
          <span className="text-muted-foreground">{t(configuration.enabled ? "directory.enabled" : "directory.disabled")}</span>
          <Switch checked={!!configuration.enabled} disabled={busy || editing} onCheckedChange={toggle} aria-label={oidc ? t("enterprise.ssoEnableName", {name: displayName}) : t("enterprise.archiveEnable")} />
        </div>
      )}
    </div>
    {loading ? <p role="status" className="mt-4 text-sm text-muted-foreground">{t("enterprise.loadingConfiguration")}</p> : <>
      {configuration ? <div className="mt-4 space-y-4 border-t pt-4">
        <dl className="grid gap-3 text-sm sm:grid-cols-2">
          {summaryFields.map((key) => <div key={key} className="min-w-0 rounded-lg bg-muted/35 p-3">
            <dt className="text-xs font-medium text-muted-foreground">{t("enterprise." + (summaryLabels[key] || key))}</dt>
            <dd className="mt-1 wrap-anywhere font-medium">{String(configuration[key] ?? "") || "—"}</dd>
          </div>)}
        </dl>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => setEditing(true)} aria-label={action}><Pencil aria-hidden="true" />{t("enterprise.editConfiguration")}</Button>
          {oidc && <Button size="sm" variant="ghost" className="text-destructive hover:bg-destructive/10" disabled={busy} onClick={() => {setError(false);setDeleting(true);}} aria-label={t("enterprise.deleteSSOName", {name: displayName})}><Trash2 aria-hidden="true" />{t("directory.delete")}</Button>}
        </div>
      </div> : !error && <div className="mt-4 flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed px-4 py-7 text-center">
        <Inbox aria-hidden="true" className="size-6 text-muted-foreground" />
        <h4 className="text-sm font-semibold">{t(oidc ? "enterprise.ssoEmpty" : "enterprise.archiveEmpty")}</h4>
        <p className="max-w-xl text-sm text-muted-foreground">{t(oidc ? "enterprise.ssoEmptyHint" : "enterprise.archiveEmptyHint")}</p>
        <Button size="sm" onClick={() => setEditing(true)} aria-label={action}>{t(oidc ? "enterprise.configureSSO" : "enterprise.configureArchive")}</Button>
      </div>}
    </>}
    <Dialog open={editing} onOpenChange={setEditing}>
      <DialogContent className="flex max-h-[calc(100dvh-2rem)] min-w-0 flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
        <DialogHeader className="border-b px-5 py-4 pe-12 sm:px-6 sm:pe-14">
          <DialogTitle className="text-lg font-semibold">{t(oidc ? "enterprise.configureSSO" : "enterprise.configureArchive")}</DialogTitle>
          <DialogDescription>{t(oidc ? "enterprise.ssoFor" : "enterprise.archiveBrief", {name: displayName})}</DialogDescription>
        </DialogHeader>
        <div className="min-h-0 overflow-y-auto px-5 py-4 sm:px-6">
          {editing && <EnterpriseConfiguration id={id} embedded publicOrigin={publicOrigin} initialConfiguration={configuration ?? undefined} onSaved={completeEdit} />}
        </div>
      </DialogContent>
    </Dialog>
    <Dialog open={deleting} onOpenChange={(open)=>{if(!busy)setDeleting(open);}}><DialogContent><DialogTitle>{t("enterprise.deleteSSOName",{name: displayName})}</DialogTitle><DialogDescription>{t("enterprise.deleteSSOHint")}</DialogDescription>{error && <p role="alert">{t("enterprise.error")}</p>}<div className="flex justify-end gap-2"><Button variant="outline" disabled={busy} onClick={()=>setDeleting(false)}>{t("directory.cancel")}</Button><Button variant="destructive" disabled={busy} onClick={remove}>{t("directory.confirmDelete")}</Button></div></DialogContent></Dialog>
    {certificateExpired && !(error && failureKey === "enterprise.samlCertificateExpired") && <p role="alert" className="mt-4 rounded-md border border-destructive/25 bg-destructive/5 px-3 py-2 text-sm text-destructive">{t("enterprise.samlCertificateExpired")}</p>}
    {error && !deleting && <p role="alert" className="mt-4 text-sm text-destructive">{t(failureKey)}</p>}
  </section>;
}
