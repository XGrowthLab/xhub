"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { uiHref } from "@/utils/uiHref";
import { listNotifications, markNotificationRead, type NotificationEntry, type NotificationPage } from "./api";
import { useNotificationSession } from "./session";

const PAGE_SIZE = 20;
const MAX_OFFSET = 10000;
const MARKET_ITEMS_LIMIT = 8;
const marketKinds = ["added", "delisted", "restored", "price_changed", "price_information_changed"] as const;
type MarketKind = (typeof marketKinds)[number];
type MarketChange = { kind: MarketKind; model: string; previous: unknown; current: unknown };

/** 返回来源报价的前后值；键仅用于查询翻译，缺失值交给展示函数处理。 */
function marketPriceSides(change: MarketChange) {
  return [["before", change.previous], ["after", change.current]] as const;
}

/** 从服务端事件提取可识别的模型变化；data 为个人消息数据，未知类型或畸形模型会被安全忽略。 */
function marketChanges(data: Record<string, unknown>): MarketChange[] {
  if (!Array.isArray(data.changes)) return [];
  return data.changes.flatMap((change: unknown) => {
    if (!change || typeof change !== "object" || Array.isArray(change)) return [];
    const { kind, model, previous_price: previous, current_price: current } = change as Record<string, unknown>;
    if (typeof kind !== "string" || !marketKinds.includes(kind as MarketKind) || typeof model !== "string" || !model.trim()) return [];
    return [{ kind: kind as MarketKind, model, previous, current }];
  });
}

/** 按可信类型显示来源变化；items 是已验证事件项，报价以可折叠的只读原始规则提供。 */
function MarketChangeList({ items }: { items: MarketChange[] }) {
  const { t } = useI18n();
  return <ul className="space-y-1 text-sm text-muted-foreground">
    {items.map((change, index) => <li key={`${change.kind}-${change.model}-${index}`} className="break-all">
      {t(`notifications.marketKind.${change.kind}`)}: {change.model}
      {(change.kind === "price_changed" || change.kind === "price_information_changed") && <details className="mt-1 rounded-md bg-muted/40 p-2">
        <summary className="cursor-pointer text-primary">{t("notifications.marketPriceDetails")}</summary>
        {marketPriceSides(change).map(([side, value]) => {
          const price = priceText(value);
          return <div key={side} className="mt-2">
            <span>{t(`notifications.marketPrice.${side}`)}</span>
            {price ? <><pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-all">{price.text}</pre>{price.truncated && <span>{t("notifications.marketPriceTruncated")}</span>}</> : <span> {t("notifications.marketPriceUnknown")}</span>}
          </div>;
        })}
      </details>}
    </li>)}
  </ul>;
}

/** 对来源原始价格生成有限长度的只读文本；缺失返回空，防止超长内容影响消息列表。 */
function priceText(value: unknown): { text: string; truncated: boolean } | null {
  if (value == null) return null;
  try {
    const text = JSON.stringify(value, null, 2);
    if (!text) return null;
    return { text: text.slice(0, 4000), truncated: text.length > 4000 };
  } catch {
    return null;
  }
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

/** 展示一条安全的个人通知；预算只渲染经过验证的数值和固定用量链接，未知类型不展示原始数据。 */
function NotificationRow({
  item,
  busy,
  onRead,
}: {
  item: NotificationEntry;
  busy: boolean;
  onRead: (id: string) => void;
}) {
  const { t, locale } = useI18n();
  const snapshot = item.type === "budget_threshold" ? budgetSnapshot(item.data) : null;
  const changes = item.type === "market_update" ? marketChanges(item.data) : [];
  const money = (value: number) =>
    new Intl.NumberFormat(locale, { style: "currency", currency: "USD", maximumFractionDigits: 12 }).format(value);
  const date = new Date(item.created_at);
  const time = Number.isNaN(date.getTime())
    ? t("notifications.timeUnavailable")
    : new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(date);
  return (
    <li className="rounded-xl border bg-card p-4 shadow-sm">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-2">
          <div className="flex items-center gap-2">
            <h2 className="font-semibold">{snapshot ? t("notifications.budgetTitle") : changes.length ? t("notifications.marketTitle") : t("notifications.unknown")}</h2>
            <span className="rounded-full bg-muted px-2 py-0.5 text-xs">
              {t(item.read ? "notifications.read" : "notifications.unread")}
            </span>
          </div>
          {snapshot && (
            <>
              <p>
                {snapshot.limit === 0
                  ? t("notifications.zeroBudgetMessage")
                  : t("notifications.budgetMessage", { threshold: snapshot.threshold })}
              </p>
              <p className="text-sm text-muted-foreground">
                {t("notifications.snapshot", {
                  spent: money(snapshot.spent),
                  limit: money(snapshot.limit),
                  remaining: money(snapshot.remaining),
                })}
              </p>
              <p className="text-sm text-muted-foreground">{t("notifications.balanceHelp")}</p>
              <p className="text-sm text-muted-foreground">{t("notifications.samplingHelp")}</p>
            </>
          )}
          {changes.length > 0 && (
            <>
              <p>{t("notifications.marketSummary", { count: changes.length })}</p>
              <MarketChangeList items={changes.slice(0, MARKET_ITEMS_LIMIT)} />
              {changes.length > MARKET_ITEMS_LIMIT && <details className="text-sm text-muted-foreground">
                <summary className="cursor-pointer text-primary">{t("notifications.marketMore", { count: changes.length - MARKET_ITEMS_LIMIT })}</summary>
                <MarketChangeList items={changes.slice(MARKET_ITEMS_LIMIT)} />
              </details>}
              <p className="text-sm text-muted-foreground">{t("notifications.marketPriceHelp")}</p>
            </>
          )}
          <p className="text-xs text-muted-foreground">{t("notifications.createdAt", { time })}</p>
        </div>
        <div className="flex items-center gap-2">
          {snapshot && (
            <Link href={uiHref("usage")} className="rounded-md px-3 py-2 text-sm text-primary hover:underline">
              {t("notifications.usage")}
            </Link>
          )}
          {changes.length > 0 && (
            <Link href={uiHref("price-data")} className="rounded-md px-3 py-2 text-sm text-primary hover:underline">
              {t("notifications.marketLink")}
            </Link>
          )}
          {!item.read && (
            <Button variant="outline" size="sm" disabled={busy} onClick={() => onRead(item.id)}>
              {t(busy ? "notifications.marking" : "notifications.markRead")}
            </Button>
          )}
        </div>
      </div>
    </li>
  );
}

/** 消息中心读取当前会话的消息并标记已读；后端限定本人范围，前端不读取管理员全局列表。
 * 分页、重试和已读由真实接口完成；换账号立即隐藏旧数据，旧请求响应不能覆盖新会话。 */
export function NotificationCenter() {
  const { t } = useI18n();
  const session = useNotificationSession();
  const scopeRef = useRef<symbol | null>(null);
  useEffect(() => {
    scopeRef.current = session?.scope ?? null;
    return () => {
      scopeRef.current = null;
    };
  }, [session]);
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
  return (
    <section className="mx-auto max-w-4xl space-y-6">
      <h1 className="text-2xl font-semibold">{t("notifications.title")}</h1>
      {(!session || !current) && <p role="status">{t("notifications.loading")}</p>}
      {current?.failed && (
        <div role="alert" className="space-y-2">
          <p>{t("notifications.loadFailed")}</p>
          <Button variant="outline" onClick={() => setRevision((v) => v + 1)}>
            {t("notifications.retry")}
          </Button>
        </div>
      )}
      {session && current?.page && (
        <>
          {markFailed === session.scope && <p role="alert">{t("notifications.markFailed")}</p>}
          {current.page.items.length === 0 ? (
            <p>{t("notifications.empty")}</p>
          ) : (
            <ul className="space-y-3">
              {current.page.items.map((item) => (
                <NotificationRow
                  key={item.id}
                  item={item}
                  busy={pending?.scope === session.scope && pending.id === item.id}
                  onRead={(id) => void read(id)}
                />
              ))}
            </ul>
          )}
          {offset >= MAX_OFFSET && offset + PAGE_SIZE < current.page.total && (
            <p className="text-sm text-muted-foreground">{t("notifications.pageLimit")}</p>
          )}
          <div className="flex items-center justify-end gap-3">
            <Button
              variant="outline"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
            >
              {t("notifications.previous")}
            </Button>
            <span>{t("notifications.page", { page: Math.floor(offset / PAGE_SIZE) + 1 })}</span>
            <Button
              variant="outline"
              disabled={!canLoadNextNotificationsPage(offset, current.page.total)}
              onClick={() => setOffset(offset + PAGE_SIZE)}
            >
              {t("notifications.next")}
            </Button>
          </div>
        </>
      )}
    </section>
  );
}
