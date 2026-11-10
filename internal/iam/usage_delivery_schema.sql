-- 等待客户端写出结果的恢复标记；payload 只包含零费用元数据，不保存请求或响应正文。
CREATE TABLE IF NOT EXISTS usage_delivery_pending (
    request_id TEXT PRIMARY KEY,
    payload JSONB NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS usage_delivery_pending_expiry_idx ON usage_delivery_pending (expires_at, request_id);
-- 跳过有确认快照的标记后，下一批优先检查其他请求，避免固定队头永久占据恢复批次。
ALTER TABLE usage_delivery_pending ADD COLUMN IF NOT EXISTS recovery_checked_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS usage_delivery_pending_recovery_idx ON usage_delivery_pending (recovery_checked_at NULLS FIRST, expires_at, request_id);
