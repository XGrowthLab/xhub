"use client";

import React, { useEffect, useState } from "react";
import EnterpriseConfiguration from "./EnterpriseConfiguration";
import {listSSO, type SSOConfiguration} from "@/lib/http/enterprise";
import DirectoryManagement from "./DirectoryManagement";
import IntegrationOverview from "./IntegrationOverview";
import LoggingSettings from "./LoggingSettings/LoggingSettings";
import { Switch } from "@/components/ui/switch";
import { Dialog, DialogContent, DialogTitle, DialogDescription } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Copy, ShieldCheck, Inbox, Users, Trash2 } from "lucide-react";
import SettingsSelect from "./SettingsSelect";
import { useT } from "@/i18n";
import { createDirectoryConnection, deleteDirectoryConnection, listDirectoryConnections, configureDirectoryConnection, listDirectoryOrganizations, listDirectoryTeams, enterprisePublicOrigin, type DirectoryOrganization, type DirectoryTeam, type DirectoryConnection } from "@/lib/http/directory";

/** 展示独立 SSO、SCIM 与日志设置页签；真实集成状态、说明、新建及归属按需展开，无参数，返回可访问表单与状态。
 * 管理员页面使用，创建凭据仅保留当前组件内存；离开页面清除，失败不自动重试写操作。
 */
export default function DirectorySettings() {
  const t = useT();
  const [tab, setTab] = useState("sso");
  const [ssoSetup, setSSOSetup] = useState(false);
  const [ssoRows,setSSORows]=useState<SSOConfiguration[]>([]);
  const [ssoError,setSSOError]=useState(false);
  const [ssoLoading,setSSOLoading]=useState(true);
  const [scimSetup,setSCIMSetup]=useState(false);
  const [editing,setEditing]=useState<DirectoryConnection|null>(null);
  const [copyResult, setCopyResult] = useState<"copySuccess" | "copyError" | "tokenCopied" | "tokenCopyError" | null>(null);
  const [rows, setRows] = useState<DirectoryConnection[]>([]);
  const [organizations, setOrganizations] = useState<DirectoryOrganization[]>([]);
  const [teams, setTeams] = useState<DirectoryTeam[]>([]);
  const [publicURL, setPublicURL] = useState("");
  const publicOrigin = enterprisePublicOrigin(publicURL);
  const [organization, setOrganization] = useState("");
  const [name, setName] = useState("");
  const [token, setToken] = useState("");
  const [createdID, setCreatedID] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const [deleting, setDeleting] = useState<DirectoryConnection | null>(null);
  const [deleteError, setDeleteError] = useState(false);

  useEffect(() => {
    let alive = true;
    listSSO().then((items)=>{if(alive)setSSORows(items);}).catch(()=>{if(alive)setSSOError(true);}).finally(()=>{if(alive)setSSOLoading(false);});
    listDirectoryConnections().then((items) => { if (alive) setRows(items); }).catch(() => { if (alive) setError(true); });
    listDirectoryOrganizations().then((items) => { if (alive) setOrganizations(items); }).catch(() => { if (alive) setError(true); });
    listDirectoryTeams().then((items) => { if (alive) setTeams(items); }).catch(() => { if (alive) setError(true); });
    setPublicURL(window.location.protocol === "https:" ? window.location.origin : "");
    return () => { alive = false; };
  }, []);

  /** 提交连接创建；参数为表单事件，返回 Promise。仅发送原始名称，凭据不写入浏览器存储。 */
  async function create(event: React.FormEvent) {
    event.preventDefault(); setBusy(true); setError(false); setToken("");
    try {
      const result = await createDirectoryConnection(organization, name);
      setRows((items) => [...items, result.connection]); setToken(result.token); setCreatedID(result.connection.id);
    } catch { setError(true); } finally { setBusy(false); }
  }

  /** 切换连接启用；参数为目标连接，返回 Promise。成功后更新列表，失败保持原展示并告警。 */
  async function toggle(connection: DirectoryConnection) {
    setBusy(true); setError(false);
    try {
      await configureDirectoryConnection(connection, !connection.enabled);
      setRows((items) => items.map((item) => item.id === connection.id ? { ...item, enabled: !item.enabled } : item));
    } catch { setError(true); } finally { setBusy(false); }
  }

  /** 复制已校验的 SCIM 完整地址；参数为连接 ID，返回 Promise。剪贴板不可用时提示手动复制，不触及凭据。 */
  async function copyURL(id: string) {
    try { await navigator.clipboard.writeText(publicOrigin + "/scim/v2/connections/" + id); setCopyResult("copySuccess"); }
    catch { setCopyResult("copyError"); }
  }

  /** 复制当前弹窗的一次性 SCIM 凭据；无参数，返回 Promise。拒绝剪贴板时提示手动复制，不记录秘密。 */
  async function copyToken() {
    if (!token) return;
    try { await navigator.clipboard.writeText(token); setCopyResult("tokenCopied"); }
    catch { setCopyResult("tokenCopyError"); }
  }

  /** 关闭 SCIM 弹窗；无参数，无返回，清除一次性凭据和新建草稿，忙碌时保留以防误操作。 */
  function closeSCIM() {if(busy)return;setSCIMSetup(false);setEditing(null);setToken("");setCreatedID("");setName("");setOrganization("");setCopyResult(null);}

  /** 确认删除选中的连接；无参数，返回 Promise。成功才移除卡片及其一次性凭据，失败保留弹窗并显示脱密错误。
   * 管理员确认按钮调用；后台保留本地业务数据，忙碌期间阻止重复删除和关闭弹窗。
   */
  async function remove() {
    if (!deleting || busy) return;
    setBusy(true); setDeleteError(false);
    try {
      await deleteDirectoryConnection(deleting.id);
      setRows((items) => items.filter((item) => item.id !== deleting.id));
      if (createdID === deleting.id) {setToken(""); setCreatedID("");}
      setDeleting(null);
    } catch {setDeleteError(true);} finally {setBusy(false);}
  }

  return <section aria-labelledby="directory-title">
    <h2 id="directory-title" className="sr-only">{t("directory.title")}</h2>
    <Tabs value={tab} onValueChange={(value) => setTab(String(value))} className="gap-6">
    <div className="overflow-x-auto border-b pb-1.5">
      <TabsList variant="line" activateOnFocus aria-label={t("enterprise.settingsTabs")} className="group-data-horizontal/tabs:h-auto items-stretch gap-4">
        <div role="group" aria-label={t("enterprise.identitySection")}>
          <p aria-hidden="true" className="px-2 text-xs font-medium text-muted-foreground">{t("enterprise.identitySection")}</p>
          <div className="flex h-9 gap-1">
            <TabsTrigger value="sso" className="flex-none">{t("enterprise.tabs.sso")}</TabsTrigger>
            <TabsTrigger value="scim" className="flex-none">{t("enterprise.tabs.scim")}</TabsTrigger>
          </div>
        </div>
        <div role="group" aria-label={t("enterprise.storageSection")} className="border-s ps-3">
          <p aria-hidden="true" className="whitespace-nowrap px-2 text-xs font-medium text-muted-foreground">{t("enterprise.storageSection")}</p>
          <div className="flex h-9">
            <TabsTrigger value="logs" className="flex-none">{t("enterprise.tabs.logs")}</TabsTrigger>
          </div>
        </div>
      </TabsList>
    </div>
    {/* 保留页签实例与归属快照；一次性凭据仅存在 SCIM 弹窗内存，关闭即清除。 */}
    <TabsContent value="sso" keepMounted className="space-y-6">
      {ssoError ? <p role="alert">{t("enterprise.error")}</p> : ssoLoading ? <p role="status">{t("enterprise.loadingConfiguration")}</p> : ssoRows.length === 0 ? <div className="rounded-xl border bg-card p-6">
      <div className="flex items-start gap-3"><ShieldCheck aria-hidden="true" className="mt-0.5 size-5 text-muted-foreground" /><div><h3 className="text-base font-semibold">{t("enterprise.ssoHeading")}</h3><p className="mt-1 text-sm text-muted-foreground">{t("enterprise.ssoDescription")}</p></div></div>
      <div className="mt-6 flex min-h-72 flex-col items-center justify-center gap-3 rounded-lg border border-dashed p-6 text-center"><Inbox aria-hidden="true" className="mb-2 size-10 text-muted-foreground/40" /><h4 className="text-lg font-semibold">{t("enterprise.ssoEmpty")}</h4><p className="max-w-xl text-sm text-muted-foreground">{t("enterprise.ssoEmptyHint")}</p><Button type="button" className="mt-2" onClick={() => setSSOSetup(true)}>{t("enterprise.configureSSO")}</Button></div></div> : <><div className="flex justify-end"><Button onClick={()=>setSSOSetup(true)}>{t("enterprise.configureSSO")}</Button></div>{ssoRows.map((row) => <IntegrationOverview key={row.connection_id} id={row.connection_id} name={row.name} onDeleted={()=>setSSORows((items)=>items.filter((item)=>item.connection_id!==row.connection_id))} />)}</>}
    </TabsContent>
    <TabsContent value="scim" keepMounted className="rounded-xl border bg-card p-6 [&_label]:flex [&_label]:flex-col [&_label]:gap-2 [&_label]:text-sm [&_summary]:cursor-pointer [&_summary]:text-sm [&_summary]:font-medium">
    <div className="mb-6 flex items-start gap-3"><Users aria-hidden="true" className="mt-1 size-5 text-muted-foreground" /><div><h3 className="text-lg font-semibold">{t("enterprise.scimHeading")}</h3><p className="mt-1 text-sm text-muted-foreground">{t("enterprise.scimBrief")}</p></div></div>
    {error && <p role="alert">{t("directory.error")}</p>}
    {rows.length === 0 && !error && <div className="mb-6 flex min-h-72 flex-col items-center justify-center gap-3 rounded-lg border border-dashed p-6 text-center"><Inbox aria-hidden="true" className="mb-2 size-10 text-muted-foreground/40" /><h4 className="text-lg font-semibold">{t("enterprise.scimEmpty")}</h4><p className="max-w-xl text-sm text-muted-foreground">{t("enterprise.scimEmptyHint")}</p><Button className="mt-2" onClick={() => setSCIMSetup(true)}>{t("enterprise.configureSCIM")}</Button></div>}
    {rows.length>0 && <div className="mb-4 flex justify-end"><Button onClick={()=>setSCIMSetup(true)}>{t("enterprise.newConnection")}</Button></div>}
    <Dialog open={scimSetup || !!editing} onOpenChange={(open)=>{if(!open)closeSCIM();}}><DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-4xl [&_label]:flex [&_label]:flex-col [&_label]:gap-2 [&_label]:text-sm"><DialogTitle>{t("enterprise.configureSCIM")}</DialogTitle><DialogDescription>{t("enterprise.scimBrief")}</DialogDescription>
    {(scimSetup || editing) && <>
    <div className="max-w-2xl">
    <label>{t("enterprise.publicURL")}<Input type="url" placeholder="https://console.example.com" value={publicURL} onChange={(event) => setPublicURL(event.target.value)} /></label>
    <details className="mt-2 text-sm text-muted-foreground"><summary>{t("enterprise.setupHelp")}</summary><p className="mt-3">{t("enterprise.publicURLHint")}</p><p className="mt-3">{t("directory.scope")}</p><p className="mt-3">{t("enterprise.scimSteps")}</p></details>
    {publicURL && !publicOrigin && <p role="alert">{t("enterprise.invalidPublicURL")}</p>}
    </div>
    {!editing && !createdID && <div className="my-6">
    <form onSubmit={create} className="mt-4 grid max-w-2xl gap-4 sm:grid-cols-2">
      <label>{t("directory.organization")}<SettingsSelect required value={organization} onChange={(event) => setOrganization(event.target.value)}><option value="">{t("directory.choose")}</option>{organizations.map((org) => <option key={org.organization_id} value={org.organization_id}>{org.organization_alias || org.organization_id}</option>)}</SettingsSelect></label>
      <label>{t("directory.name")}<Input required value={name} onChange={(event) => setName(event.target.value)} /></label>
      <Button className="w-fit" disabled={busy} type="submit">{t("directory.create")}</Button>
    </form>
    </div>}
    {token && <div role="status" className="my-4 space-y-3 rounded-lg border bg-muted/40 p-4">
      <p>{t("directory.tokenWarning")}</p>
      <label>{t("directory.token")}<Input readOnly value={token} /></label>
      <Button variant="outline" type="button" onClick={copyToken}><Copy aria-hidden="true" />{t("enterprise.copyToken")}</Button>
      <Button variant="outline" type="button" onClick={() => setToken("")}>{t("directory.hideToken")}</Button>
    </div>}
    {(editing || createdID) && <><div className="flex flex-col gap-2 sm:flex-row sm:items-end [&>label]:flex-1"><label>{t("enterprise.scimURL")}<Input readOnly value={publicOrigin ? publicOrigin+"/scim/v2/connections/"+(editing?.id || createdID) : ""} placeholder="https://console.example.com/scim/v2/connections/..."/></label><Button variant="outline" disabled={!publicOrigin} onClick={()=>copyURL(editing?.id || createdID)}><Copy aria-hidden="true"/>{t("enterprise.copyURL")}</Button></div><details className="mt-4 border-t pt-4"><summary>{t("enterprise.assignments")}</summary><DirectoryManagement id={editing?.id || createdID} teams={teams.filter((team)=>team.organization_id===(editing?.organization_id || organization))}/></details></>}
    {copyResult && <p role={copyResult === "copyError" || copyResult === "tokenCopyError" ? "alert" : "status"}>{t("enterprise." + copyResult)}</p>}
    {error && <p role="alert">{t("directory.error")}</p>}
    <div className="flex justify-end"><Button variant="outline" disabled={busy} onClick={closeSCIM}>{t("enterprise.closeConfiguration")}</Button></div>
    </>}
    </DialogContent></Dialog>
    <ul className="space-y-4">{rows.map((connection) => <li key={connection.id} className="rounded-lg border p-4">
      <div className="mb-4 flex items-center justify-between gap-3"><div className="flex flex-wrap items-center gap-3"><span className="font-medium">{connection.name}</span><span className="rounded-full bg-muted px-2 py-1 text-xs text-muted-foreground">{t(connection.enabled ? "directory.enabled" : "directory.disabled")}</span></div>
      <div className="flex items-center gap-3"><Switch checked={connection.enabled} disabled={busy} onCheckedChange={() => toggle(connection)}
        aria-label={t(connection.enabled ? "directory.disableName" : "directory.enableName", { name: connection.name })}
      />
      <Button variant="outline" disabled={busy} onClick={()=>setEditing(connection)} aria-label={t("enterprise.editSCIMName",{name:connection.name})}>{t("enterprise.editConfiguration")}</Button>
      <Button variant="outline" type="button" disabled={busy} className="text-destructive" onClick={() => {setDeleting(connection); setDeleteError(false);}} aria-label={t("directory.deleteName", {name:connection.name})}><Trash2 aria-hidden="true" />{t("directory.delete")}</Button></div>
      </div>
    </li>)}</ul>
    </TabsContent>
    <TabsContent value="logs" keepMounted className="space-y-6"><IntegrationOverview /><LoggingSettings /></TabsContent>
    </Tabs>
    <Dialog open={ssoSetup} onOpenChange={setSSOSetup}><DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-3xl"><DialogTitle>{t("enterprise.configureSSO")}</DialogTitle><DialogDescription>{t("enterprise.ssoDescription")}</DialogDescription>{ssoSetup && <EnterpriseConfiguration sso organizations={organizations} embedded onSaved={(saved)=>{setSSORows((items)=>[...items,{connection_id:String(saved.connection_id),name:String(saved.name),enabled:!!saved.enabled}]);setSSOSetup(false);}}/>}</DialogContent></Dialog>
    <Dialog open={!!deleting} onOpenChange={(open) => {if (!open && !busy) setDeleting(null);}}><DialogContent><DialogTitle>{t("directory.deleteName", {name:deleting?.name ?? ""})}</DialogTitle><DialogDescription>{t("directory.deleteHint")}</DialogDescription>{deleteError && <p role="alert" className="text-sm text-destructive">{t("directory.error")}</p>}<div className="flex justify-end gap-2"><Button variant="outline" disabled={busy} onClick={() => setDeleting(null)}>{t("directory.cancel")}</Button><Button variant="destructive" disabled={busy} onClick={remove}>{t("directory.confirmDelete")}</Button></div></DialogContent></Dialog>
  </section>;
}
