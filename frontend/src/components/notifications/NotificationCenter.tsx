"use client";

import Link from "next/link";
import { Bell, Inbox, Wallet, Boxes, Check, ArrowUpRight, ChevronLeft, ChevronRight } from "lucide-react";
import { ConsolePageHeader, BudgetOverview, HelpDisclosure } from "@/components/console/ConsolePrimitives";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { uiHref } from "@/utils/uiHref";
import {
  getMarketNotificationDetail,
  listNotifications,
  markNotificationRead,
  type NotificationEntry,
  type NotificationPage,
} from "./api";
import { useNotificationSession } from "./session";

const PAGE_SIZE = 20;
const MAX_OFFSET = 10000;
const MARKET_ITEMS_LIMIT = 8;
const marketKinds = ["added", "delisted", "restored", "price_changed", "price_information_changed"] as const;
type MarketKind = (typeof marketKinds)[number];
type MarketChange = { kind: MarketKind; model: string };
type NotificationSession = { scope: symbol; accessToken: string; userId: string };

/** 从服务端事件提取可识别的模型变化；data 为个人消息数据，未知类型或畸形模型会被安全忽略。 */
function marketChanges(data: Record<string, unknown>): MarketChange[] {
  if (!Array.isArray(data.changes)) return [];
  return data.changes.flatMap((change: unknown) => {
    if (!change || typeof change !== "object" || Array.isArray(change)) return [];
    const { kind, model } = change as Record<string, unknown>;
    if (
      typeof kind !== "string" ||
      !marketKinds.includes(kind as MarketKind) ||
      typeof model !== "string" ||
      !model.trim()
    )
      return [];
    return [{ kind: kind as MarketKind, model }];
  });
}

/** 按可信类型显示来源变化；items 是已验证事件摘要，不读取或呈现完整报价。 */
function MarketChangeList({ items }: { items: MarketChange[] }) {
  const { t } = useI18n();
  return (
    <ul className="divide-y rounded-lg border text-sm">
      {items.map((change, index) => (
        <li key={`${change.kind}-${change.model}-${index}`} className="break-words px-3 py-2.5">
          {t(`notifications.marketKind.${change.kind}`)}: {change.model}
        </li>
      ))}
    </ul>
  );
}

/** 按需分段显示消息所属的原始变化；id 为消息 ID，换会话和卸载时中断请求并隐藏旧内容。 */
function MarketEventDetails({ id, session }: { id: string; session: NotificationSession }) {
  const { t } = useI18n();
  const controller = useRef<AbortController | null>(null);
  const [state, setState] = useState<{
    scope: symbol;
    text: string;
    offsets: number[];
    index: number;
    next: number | null;
    loading: boolean;
    error: boolean;
    retryOffset: number;
    retryIndex: number;
  } | null>(null);
  useEffect(() => () => controller.current?.abort(), [session.scope, id]);
  const current = state?.scope === session.scope ? state : null;
  /** 加载指定页，仅保留当前片段；状态绑定会话且较晚返回的旧请求不能覆盖新结果。 */
  const load = async (offset: number, offsets: number[], index: number) => {
    controller.current?.abort();
    const active = new AbortController();
    controller.current = active;
    setState({
      scope: session.scope,
      text: "",
      offsets,
      index,
      next: null,
      loading: true,
      error: false,
      retryOffset: offset,
      retryIndex: index,
    });
    try {
      const result = await getMarketNotificationDetail(session.accessToken, id, offset, active.signal);
      if (!active.signal.aborted)
        setState({
          scope: session.scope,
          text: result.text,
          offsets,
          index,
          next: result.next_offset,
          loading: false,
          error: false,
          retryOffset: offset,
          retryIndex: index,
        });
    } catch {
      if (!active.signal.aborted)
        setState({
          scope: session.scope,
          text: "",
          offsets,
          index,
          next: null,
          loading: false,
          error: true,
          retryOffset: offset,
          retryIndex: index,
        });
    }
  };
  if (!current)
    return (
      <Button variant="outline" size="sm" onClick={() => void load(0, [0], 0)}>
        {t("notifications.marketDetailsOpen")}
      </Button>
    );
  return (
    <section className="space-y-2 rounded-md bg-muted/40 p-3" aria-label={t("notifications.marketDetailsOpen")}>
      <p className="text-sm text-muted-foreground">{t("notifications.marketDetailsHelp")}</p>
      {current.loading && <p role="status">{t("notifications.marketDetailsLoading")}</p>}
      {current.error && (
        <div role="alert">
          <p>{t("notifications.marketDetailsFailed")}</p>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void load(current.retryOffset, current.offsets, current.retryIndex)}
          >
            {t("notifications.retry")}
          </Button>
        </div>
      )}
      {!current.loading && !current.error && (
        <>
          <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{current.text}</pre>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={current.index === 0}
              onClick={() => void load(current.offsets[current.index - 1], current.offsets, current.index - 1)}
            >
              {t("notifications.previous")}
            </Button>
            <span>{t("notifications.page", { page: current.index + 1 })}</span>
            <Button
              variant="outline"
              size="sm"
              disabled={current.next === null}
              onClick={() => {
                if (current.next !== null)
                  void load(
                    current.next,
                    [...current.offsets.slice(0, current.index + 1), current.next],
                    current.index + 1,
                  );
              }}
            >
              {t("notifications.next")}
            </Button>
          </div>
        </>
      )}
    </section>
  );
}

/** 判断是否能请求下一页；offset 为当前起点、total 为服务端总数，兼顾接口最大偏移限制。 */
export function canLoadNextNotificationsPage(offset: number, total: number): boolean {
  return offset + PAGE_SIZE <= MAX_OFFSET && offset + PAGE_SIZE < total;
}

/** 识别可展示的预算快照；参数为服务端 data，非法、负数、未知比例返回空，不展示未验证字段。 */
function budgetSnapshot(data: Record<string, unknown>) {
  const { threshold, spent, limit, remaining } = data;
  if (
    typeof threshold !== "number" ||
    !Number.isFinite(threshold) ||
    threshold <= 0 ||
    threshold > 100 ||
    typeof spent !== "number" ||
    !Number.isFinite(spent) ||
    spent < 0 ||
    typeof limit !== "number" ||
    !Number.isFinite(limit) ||
    limit < 0
  )
    return null;
  const balance = remaining === undefined ? Math.max(0, limit - spent) : remaining;
  if (typeof balance !== "number" || !Number.isFinite(balance) || balance < 0) return null;
  return { threshold, spent, limit, remaining: balance };
}

/** 只从可验证数据确定摘要；未知类型/损坏数据不能泄露原始字段，供列表与详情共用。 */
function notificationSummary(item: NotificationEntry, t: ReturnType<typeof useI18n>["t"]) {
  const snapshot = item.type === "budget_threshold" ? budgetSnapshot(item.data) : null;
  const changes = item.type === "market_update" ? marketChanges(item.data) : [];
  const total =
    Number.isSafeInteger(item.data.total_changes) && Number(item.data.total_changes) >= changes.length
      ? Number(item.data.total_changes)
      : changes.length;
  const market = item.type === "market_update" && Array.isArray(item.data.changes) && total > 0;
  let title = t("notifications.unknown"),
    description = "",
    preview = "";
  let Icon = Bell;
  if (snapshot) {
    title = t("notifications.budgetTitle");
    description =
      snapshot.limit === 0
        ? t("notifications.zeroBudgetMessage")
        : t("notifications.budgetMessage", { threshold: snapshot.threshold });
    preview =
      snapshot.limit === 0
        ? t("consoleUi.zeroPreview")
        : t("consoleUi.budgetPreview", { threshold: snapshot.threshold });
    Icon = Wallet;
  } else if (market) {
    title = t("notifications.marketTitle");
    description = t("notifications.marketSummary", { count: total });
    preview = t("consoleUi.marketPreview", { count: total });
    Icon = Boxes;
  }
  return { snapshot, changes, total, market, title, description, preview, Icon };
}

/** 按界面语言显示消息时间；非法时间用明确占位，完整时间始终保留在列表与详情中。 */
function notificationTime(value: string, locale: string, fallback: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? fallback
    : new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(date);
}

/** 消息详情仅呈现选中消息的安全字段；id 切换时卸载旧详情请求，已读仍需用户主动点击。
 * 参数为消息、会话及更新回调，返回预算卡或模型变更及固定站内链接，不修改授权或自动调价。 */
function NotificationRow({
  item,
  busy,
  onRead,
  session,
}: {
  item: NotificationEntry;
  busy: boolean;
  onRead: (id: string) => void;
  session: NotificationSession;
}) {
  const { t, locale } = useI18n();
  const { snapshot, changes, market, title, description, Icon } = notificationSummary(item, t);
  const time = notificationTime(item.created_at, locale, t("notifications.timeUnavailable"));
  return (
    <article aria-label={t("consoleUi.messageDetails")} className="min-w-0 space-y-5 p-5 lg:p-7">
      <div className="flex items-start gap-3">
        <div
          className={`flex size-10 shrink-0 items-center justify-center rounded-xl ${snapshot ? "bg-amber-500/10 text-amber-700 dark:text-amber-300" : "bg-primary/10 text-primary"}`}
        >
          <Icon className="size-5" aria-hidden />
        </div>
        <div className="min-w-0 flex-1">
          <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
          <p className="mt-1 text-xs text-muted-foreground">{t("notifications.createdAt", { time })}</p>
        </div>
        <span className="shrink-0 rounded-full bg-muted px-2 py-1 text-xs text-muted-foreground">
          {t(item.read ? "notifications.read" : "notifications.unread")}
        </span>
      </div>
      {description && <p className="text-sm leading-6">{description}</p>}
      {snapshot && (
        <>
          <BudgetOverview
            title={t("consoleUi.snapshot")}
            spent={snapshot.spent}
            limit={snapshot.limit}
            remaining={snapshot.remaining}
          />
          <HelpDisclosure title={t("consoleUi.notificationHelp")}>
            <p>{t("notifications.balanceHelp")}</p>
            <p>{t("notifications.samplingHelp")}</p>
          </HelpDisclosure>
        </>
      )}
      {market && (
        <>
          <MarketChangeList items={changes.slice(0, MARKET_ITEMS_LIMIT)} />
          {changes.length > MARKET_ITEMS_LIMIT && (
            <HelpDisclosure title={t("notifications.marketMore", { count: changes.length - MARKET_ITEMS_LIMIT })}>
              <MarketChangeList items={changes.slice(MARKET_ITEMS_LIMIT)} />
            </HelpDisclosure>
          )}
          <p className="rounded-lg bg-primary/5 p-3 text-sm leading-6 text-muted-foreground">
            {t("notifications.marketPriceHelp")}
          </p>
          {item.data.summary_truncated === true && (
            <p className="text-sm text-muted-foreground">{t("notifications.marketSummaryTruncated")}</p>
          )}
          <MarketEventDetails id={item.id} session={session} />
        </>
      )}
      <div className="flex flex-wrap items-center gap-2 border-t pt-4">
        {!item.read && (
          <Button variant="outline" size="sm" disabled={busy} onClick={() => onRead(item.id)}>
            <Check className="size-4" aria-hidden />
            {t(busy ? "notifications.marking" : "notifications.markRead")}
          </Button>
        )}
        {(snapshot || market) && (
          <Link
            href={uiHref(snapshot ? "usage" : "price-data")}
            className="ml-auto inline-flex items-center gap-1 rounded-md px-3 py-2 text-sm font-medium text-primary hover:bg-primary/5 focus-visible:outline-2 focus-visible:outline-ring"
          >
            {t(snapshot ? "notifications.usage" : "notifications.marketLink")}
            <ArrowUpRight className="size-4" aria-hidden />
          </Link>
        )}
      </div>
    </article>
  );
}

/** 消息中心读取当前会话的消息并标记已读；后端限定本人范围，前端不读取管理员全局列表。
 * 分页、重试和已读由真实接口完成；换账号立即隐藏旧数据，旧请求响应不能覆盖新会话。 */
export function NotificationCenter() {
  const { t, locale } = useI18n();
  const session = useNotificationSession();
  const scopeRef = useRef<symbol | null>(null);
  useEffect(() => {
    scopeRef.current = session?.scope ?? null;
    return () => {
      scopeRef.current = null;
    };
  }, [session]);
  const [selection, setSelection] = useState<{ scope: symbol; id: string } | null>(null);
  const [pagination, setPagination] = useState<{ scope: symbol; offset: number } | null>(null);
  const offset = pagination?.scope === session?.scope ? pagination?.offset ?? 0 : 0;
  const setOffset = (value: number) => {
    if (session) setPagination({ scope: session.scope, offset: value });
  };
  const [revision, setRevision] = useState(0);
  const [state, setState] = useState<{
    scope: symbol;
    offset: number;
    page: NotificationPage | null;
    failed: boolean;
  } | null>(null);
  const [pending, setPending] = useState<{ scope: symbol; id: string } | null>(null);
  const [markFailed, setMarkFailed] = useState<symbol | null>(null);
  useEffect(() => {
    if (!session) return;
    const controller = new AbortController();
    void listNotifications(session.accessToken, offset, PAGE_SIZE, controller.signal)
      .then((page) => {
        if (!controller.signal.aborted) setState({ scope: session.scope, offset, page, failed: false });
      })
      .catch(() => {
        if (!controller.signal.aborted) setState({ scope: session.scope, offset, page: null, failed: true });
      });
    return () => controller.abort();
  }, [session, offset, revision]);
  const current = state && session && state.scope === session.scope && state.offset === offset ? state : null;
  /** 标记已读后刷新当前列表与顶栏；失败保持未读，旧会话响应无副作用。 */
  const read = async (id: string) => {
    if (!session || pending?.scope === session.scope) return;
    setPending({ scope: session.scope, id });
    setMarkFailed(null);
    try {
      await markNotificationRead(session.accessToken, id);
      if (scopeRef.current === session.scope) {
        setRevision((v) => v + 1);
        window.dispatchEvent(new Event("xhub-notifications-changed"));
      }
    } catch {
      if (scopeRef.current === session.scope) setMarkFailed(session.scope);
    } finally {
      if (scopeRef.current === session.scope) setPending(null);
    }
  };
  const selected =
    current?.page?.items.find((item) => selection?.scope === session?.scope && item.id === selection?.id) ??
    current?.page?.items[0];
  return (
    <section className="mx-auto max-w-[1400px] space-y-5">
      <ConsolePageHeader title={t("notifications.title")} description={t("consoleUi.inboxDescription")} />
      <div className="overflow-hidden rounded-xl border bg-card shadow-xs">
        <div className="flex items-center justify-between gap-3 border-b px-5 py-4">
          <h2 className="flex items-center gap-2 text-sm font-semibold">
            <Inbox className="size-4 text-primary" aria-hidden />
            {t("consoleUi.inbox")}
          </h2>
          {current?.page && (
            <span className="text-xs text-muted-foreground">
              {t("consoleUi.messageCount", { count: current.page.total })}
            </span>
          )}
        </div>
        {(!session || !current) && (
          <p role="status" className="p-10 text-center text-sm text-muted-foreground">
            {t("notifications.loading")}
          </p>
        )}
        {current?.failed && (
          <div role="alert" className="space-y-3 p-10 text-center">
            <p>{t("notifications.loadFailed")}</p>
            <Button variant="outline" onClick={() => setRevision((v) => v + 1)}>
              {t("notifications.retry")}
            </Button>
          </div>
        )}
        {session && current?.page && (
          <>
            {markFailed === session.scope && (
              <p role="alert" className="border-b bg-destructive/5 px-5 py-3 text-sm text-destructive">
                {t("notifications.markFailed")}
              </p>
            )}
            {current.page.items.length === 0 ? (
              <div className="flex min-h-80 flex-col items-center justify-center gap-3 p-8 text-center">
                <div className="rounded-2xl bg-muted p-4">
                  <Inbox className="size-8 text-muted-foreground" aria-hidden />
                </div>
                <p className="font-medium">{t("notifications.empty")}</p>
                <p className="max-w-sm text-sm leading-6 text-muted-foreground">{t("consoleUi.emptyHint")}</p>
              </div>
            ) : (
              <div className="grid md:grid-cols-[minmax(240px,0.8fr)_minmax(0,1.4fr)]">
                <ul
                  aria-label={t("consoleUi.allMessages")}
                  className="max-h-72 divide-y overflow-y-auto border-b md:max-h-[680px] md:border-r md:border-b-0"
                >
                  {current.page.items.map((item) => {
                    const summary = notificationSummary(item, t);
                    const time = notificationTime(item.created_at, locale, t("notifications.timeUnavailable"));
                    const { Icon } = summary;
                    return (
                      <li key={item.id}>
                        <button
                          type="button"
                          aria-label={t("consoleUi.openMessage", { title: summary.title, time })}
                          aria-pressed={selected?.id === item.id}
                          onClick={() => setSelection({ scope: session.scope, id: item.id })}
                          className={`flex w-full gap-3 border-l-2 px-4 py-4 text-left outline-none transition-colors focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring ${selected?.id === item.id ? "border-primary bg-primary/5" : "border-transparent hover:bg-muted/50"}`}
                        >
                          <Icon aria-hidden className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                          <span className="min-w-0 flex-1">
                            <span className={`block text-sm ${item.read ? "font-medium" : "font-semibold"}`}>
                              {summary.snapshot || summary.market ? summary.title : t("consoleUi.unknownPreview")}
                            </span>
                            <span className="mt-1 block truncate text-xs leading-5 text-muted-foreground">
                              {summary.preview}
                            </span>
                            <span className="mt-2 block text-xs text-muted-foreground">{time}</span>
                          </span>
                          {!item.read && (
                            <span
                              className="mt-1.5 size-1.5 shrink-0 rounded-full bg-primary"
                              aria-label={t("notifications.unread")}
                            />
                          )}
                        </button>
                      </li>
                    );
                  })}
                </ul>
                {selected && (
                  <NotificationRow
                    key={selected.id}
                    item={selected}
                    busy={pending?.scope === session.scope && pending.id === selected.id}
                    onRead={(id) => void read(id)}
                    session={session}
                  />
                )}
              </div>
            )}
            {offset >= MAX_OFFSET && offset + PAGE_SIZE < current.page.total && (
              <p className="px-5 py-3 text-sm text-muted-foreground">{t("notifications.pageLimit")}</p>
            )}
            <div className="flex items-center justify-end gap-3 border-t px-5 py-3 text-sm text-muted-foreground">
              <Button
                variant="ghost"
                size="sm"
                disabled={offset === 0}
                onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
              >
                <ChevronLeft className="size-4" aria-hidden />
                {t("notifications.previous")}
              </Button>
              <span className="tabular-nums">
                {t("notifications.page", { page: Math.floor(offset / PAGE_SIZE) + 1 })}
              </span>
              <Button
                variant="ghost"
                size="sm"
                disabled={!canLoadNextNotificationsPage(offset, current.page.total)}
                onClick={() => setOffset(offset + PAGE_SIZE)}
              >
                {t("notifications.next")}
                <ChevronRight className="size-4" aria-hidden />
              </Button>
            </div>
          </>
        )}
      </div>
    </section>
  );
}
