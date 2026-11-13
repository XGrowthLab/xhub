-- 只保存用户自己的消息及预算扫描游标；不包含对话正文、凭据或任意跳转链接。
CREATE TABLE IF NOT EXISTS user_notifications (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    event_key TEXT NOT NULL,
    data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at TIMESTAMPTZ,
    UNIQUE(user_id, event_key)
);
CREATE INDEX IF NOT EXISTS user_notifications_listing ON user_notifications (user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS user_notifications_unread ON user_notifications (user_id) WHERE read_at IS NULL;
CREATE INDEX IF NOT EXISTS user_notifications_expiry ON user_notifications (created_at);
-- 大型市场变化只保存一份详情；个人消息仅保存事件 ID，查询时按权限关联。
CREATE TABLE IF NOT EXISTS notification_market_events (
    id TEXT PRIMARY KEY,
    data JSONB NOT NULL,
    summary JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE notification_market_events ADD COLUMN IF NOT EXISTS summary JSONB NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS notification_market_references ON user_notifications (type, ((data->>'event_id')));
-- 租约在扫描事务之外持久化，避免扫描持锁期间额外占用连接池连接。
CREATE TABLE IF NOT EXISTS notification_scan_lease (
    name TEXT PRIMARY KEY,
    owner TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS notification_budget_state (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    budget NUMERIC,
    period_start TIMESTAMPTZ,
    reset_day INTEGER NOT NULL DEFAULT 0,
    generation BIGINT NOT NULL DEFAULT 0,
    notified_threshold INTEGER NOT NULL DEFAULT 0,
    last_checked_at TIMESTAMPTZ
);
ALTER TABLE notification_budget_state ADD COLUMN IF NOT EXISTS last_checked_at TIMESTAMPTZ;
-- 失败事件冷却后重试，避免单条坏事件阻塞其余事件；投递成功后删除。
CREATE TABLE IF NOT EXISTS notification_market_retry (
    event_id TEXT PRIMARY KEY,
    next_attempt TIMESTAMPTZ NOT NULL,
    failures INTEGER NOT NULL DEFAULT 1
);
