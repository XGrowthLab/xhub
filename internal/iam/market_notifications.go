package iam

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ScanMarketNotifications 将市场同步事务写入的事件批量投递给当前活跃的平台管理员。
// ctx 控制扫描时间；每条事件单独提交，多个实例通过行锁和唯一事件键去重；失败进入短暂冷却并在后续重试。
// 投递和清除事件在同一数据库事务中执行，任何失败都不修改模型、权限或预算。
func (db *DB) ScanMarketNotifications(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// 多实例间可能先确认事件、后写入失败冷却；分批回收不再对应任何事件的冷却记录。
	if _, err := db.Engine.Context(ctx).Exec(`DELETE FROM notification_market_retry WHERE event_id IN (SELECT r.event_id FROM notification_market_retry r WHERE NOT EXISTS (SELECT 1 FROM proxy_config p WHERE p.namespace='market_change_events' AND p.key=r.event_id) LIMIT 100)`); err != nil {
		return err
	}
	var pending []struct {
		ID string `xorm:"'key'"`
	}
	if err := db.Engine.Context(ctx).SQL(`SELECT p.key FROM proxy_config p LEFT JOIN notification_market_retry r ON r.event_id=p.key WHERE p.namespace='market_change_events' AND (r.next_attempt IS NULL OR r.next_attempt <= now()) ORDER BY r.failures ASC NULLS FIRST,p.key LIMIT 50`).Find(&pending); err != nil {
		return err
	}
	failures := 0
	for _, item := range pending {
		if err := db.deliverMarketEvent(ctx, item.ID); err != nil {
			failures++
			if ctx.Err() != nil {
				break
			}
			// 逐事件冷却防止有问题的事件长期占据队首；错误详情不记录来源内容。
			_, _ = db.Engine.Context(ctx).Exec(`INSERT INTO notification_market_retry(event_id,next_attempt,failures) VALUES(?,now()+interval '1 minute',1) ON CONFLICT(event_id) DO UPDATE SET next_attempt=now()+interval '1 minute',failures=notification_market_retry.failures+1`, item.ID)
		}
	}
	if ctx.Err() != nil {
		return fmt.Errorf("market notification scan incomplete: %w", ctx.Err())
	}
	if failures != 0 {
		return fmt.Errorf("market notification scan failed for %d events", failures)
	}
	return nil
}

// deliverMarketEvent 锁定并验证一条待投递事件，id 来自服务端事件表，成功时原子生成所有消息并删除事件。
// 当前没有活跃平台管理员、数据无效或数据库失败时回滚并返回错误，供调用方退避重试。
func (db *DB) deliverMarketEvent(ctx context.Context, id string) error {
	s := db.Engine.NewSession().Context(ctx)
	defer s.Close()
	if err := s.Begin(); err != nil {
		return err
	}
	defer s.Rollback()
	if _, err := s.Exec(`SET LOCAL statement_timeout = '2s'`); err != nil {
		return err
	}
	var raw string
	found, err := s.SQL(`SELECT value_json FROM proxy_config WHERE namespace='market_change_events' AND key=? FOR UPDATE SKIP LOCKED`, id).Get(&raw)
	if err != nil {
		return err
	}
	if !found {
		return s.Commit()
	}
	var event struct {
		ID      string    `json:"id"`
		Time    time.Time `json:"time"`
		Changes []struct {
			Kind  string `json:"kind"`
			Model string `json:"model"`
		} `json:"changes"`
	}
	if len(raw) > 8<<20 || json.Unmarshal([]byte(raw), &event) != nil || event.ID != id || event.Time.IsZero() || len(event.Changes) == 0 {
		return fmt.Errorf("invalid market event")
	}
	for _, change := range event.Changes {
		if change.Model == "" || len(change.Model) > 512 {
			return fmt.Errorf("invalid market event model")
		}
		switch change.Kind {
		case "added", "delisted", "restored", "price_changed", "price_information_changed":
		default:
			return fmt.Errorf("invalid market event kind")
		}
	}
	var expected int64
	if _, err := s.SQL(`SELECT count(*) FROM users WHERE status='active' AND role='admin'`).Get(&expected); err != nil {
		return err
	}
	var admins []struct {
		ID string `xorm:"'id'"`
	}
	// 跳过正在记账或调权的用户行，数量不完整时整轮保留事件；不得遗漏被锁定的管理员。
	if err := s.SQL(`SELECT id FROM users WHERE status='active' AND role='admin' ORDER BY id FOR SHARE SKIP LOCKED`).Find(&admins); err != nil {
		return err
	}
	if expected == 0 || int64(len(admins)) != expected {
		return fmt.Errorf("platform administrator snapshot unavailable")
	}
	// 唯一键覆盖重启和重复扫描；只将来源事实写入站内信，不携带密钥或可执行链接。
	for _, admin := range admins {
		_, err = s.Exec(`INSERT INTO user_notifications(id,user_id,type,event_key,data) VALUES('market:' || md5(? || ':' || ?),?,'market_update','market:' || ?,?::jsonb) ON CONFLICT(user_id,event_key) DO NOTHING`, id, admin.ID, admin.ID, id, raw)
		if err != nil {
			return err
		}
	}
	if _, err = s.Exec(`DELETE FROM proxy_config WHERE namespace='market_change_events' AND key=?`, id); err != nil {
		return err
	}
	if _, err = s.Exec(`DELETE FROM notification_market_retry WHERE event_id=?`, id); err != nil {
		return err
	}
	return s.Commit()
}
