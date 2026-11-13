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
		err := db.prepareMarketEvent(ctx, item.ID)
		if err == nil {
			err = db.deliverMarketEvent(ctx, item.ID)
		}
		if err != nil {
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

// prepareMarketEvent 在锁定用户之前验证并存储共享事件详情；id 来自内部发件箱。
// 大事件写入不会持有管理员行锁，失败保留原事件供后续重试；无原事件时直接返回。
func (db *DB) prepareMarketEvent(ctx context.Context, id string) error {
	// 已准备的详情不可变；重试无需反复传输和写入可能很大的原始报价。
	var prepared bool
	if _, err := db.Engine.Context(ctx).SQL(`SELECT EXISTS(SELECT 1 FROM notification_market_events WHERE id=?)`, id).Get(&prepared); err != nil {
		return err
	}
	if prepared {
		return nil
	}
	var raw string
	found, err := db.Engine.Context(ctx).SQL(`SELECT value_json FROM proxy_config WHERE namespace='market_change_events' AND key=?`, id).Get(&raw)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	var event struct {
		ID      string    `json:"id"`
		Time    time.Time `json:"time"`
		Changes []struct {
			Kind  string `json:"kind"`
			Model string `json:"model"`
		} `json:"changes"`
	}
	if json.Unmarshal([]byte(raw), &event) != nil || event.ID != id || event.Time.IsZero() || len(event.Changes) == 0 {
		return fmt.Errorf("invalid market event")
	}
	for _, change := range event.Changes {
		if change.Model == "" {
			return fmt.Errorf("invalid market event model")
		}
		switch change.Kind {
		case "added", "delisted", "restored", "price_changed", "price_information_changed":
		default:
			return fmt.Errorf("invalid market event kind")
		}
	}
	// 列表仅返回有界摘要，完整报价继续保存在共享表，按需分段读取。
	summaryChanges := make([]map[string]string, 0, min(len(event.Changes), 20))
	truncated := len(event.Changes) > 20
	for _, change := range event.Changes {
		if len(summaryChanges) == 20 {
			break
		}
		name := []rune(change.Model)
		if len(name) > 128 {
			name = name[:128]
			truncated = true
		}
		summaryChanges = append(summaryChanges, map[string]string{"kind": change.Kind, "model": string(name)})
	}
	summary, err := json.Marshal(map[string]any{"event_id": id, "changes": summaryChanges, "total_changes": len(event.Changes), "summary_truncated": truncated})
	if err != nil {
		return err
	}
	_, err = db.Engine.Context(ctx).Exec(`INSERT INTO notification_market_events(id,data,summary) VALUES(?,?::jsonb,?::jsonb) ON CONFLICT(id) DO NOTHING`, id, raw, string(summary))
	return err
}

// deliverMarketEvent 锁定一条已准备的事件，id 来自服务端事件表，成功时原子生成所有消息并删除事件。
// 当前没有活跃平台管理员或数据库失败时回滚并返回错误，供调用方退避重试。
func (db *DB) deliverMarketEvent(ctx context.Context, id string) error {
	// 为整个事务设置总期限，而非只限制单条 SQL；超时会回滚并释放用户锁。
	ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	s := db.Engine.NewSession().Context(ctx)
	defer s.Close()
	if err := s.Begin(); err != nil {
		return err
	}
	defer s.Rollback()
	if _, err := s.Exec(`SET LOCAL statement_timeout = '500ms'`); err != nil {
		return err
	}
	var locked string
	found, err := s.SQL(`SELECT key FROM proxy_config WHERE namespace='market_change_events' AND key=? FOR UPDATE SKIP LOCKED`, id).Get(&locked)
	if err != nil {
		return err
	}
	if !found {
		return s.Commit()
	}
	var exists bool
	if _, err := s.SQL(`SELECT EXISTS(SELECT 1 FROM notification_market_events WHERE id=?)`, id).Get(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("market event detail unavailable")
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
	// 每个变化事件的完整内容只准备一份；个人收件箱只存引用，避免人数乘以事件体积。
	ids := make([]string, 0, len(admins))
	for _, admin := range admins {
		ids = append(ids, admin.ID)
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	// 单条批量 SQL 缩短锁定窗口；用户列表来自上面的已锁定快照，而非客户端。
	_, err = s.Exec(`INSERT INTO user_notifications(id,user_id,type,event_key,data) SELECT 'market:' || md5(? || ':' || u.id),u.id,'market_update','market:' || ?,jsonb_build_object('event_id',?::text) FROM jsonb_array_elements_text(?::jsonb) AS u(id) ON CONFLICT(user_id,event_key) DO NOTHING`, id, id, id, string(encoded))
	if err != nil {
		return err
	}
	if _, err = s.Exec(`DELETE FROM proxy_config WHERE namespace='market_change_events' AND key=?`, id); err != nil {
		return err
	}
	if _, err = s.Exec(`DELETE FROM notification_market_retry WHERE event_id=?`, id); err != nil {
		return err
	}
	return s.Commit()
}

// MarketNotificationDetail 按字符分页读取当前管理员自己消息的共享原始详情，不接受事件 ID 或其他收件人。
// userID 来自会话，id 为消息标识，offset 为字符偏移；每页最多 16384 个 Unicode 字符。
// 不存在、跨账户、失效或撤权均返回 ErrNotFound；存储失败返回错误，供处理器区分。
func (db *DB) MarketNotificationDetail(ctx context.Context, userID, id string, offset int) (string, *int, error) {
	if userID == "" || id == "" || len(id) > 128 || offset < 0 || offset > 1<<30 {
		return "", nil, ErrInvalid
	}
	const pageSize = 16384
	var part string
	found, err := db.Engine.Context(ctx).SQL(`SELECT substring(m.data::text FROM ?::integer FOR ?::integer) FROM user_notifications n JOIN notification_market_events m ON m.id=n.data->>'event_id' JOIN users u ON u.id=n.user_id WHERE n.id=? AND n.user_id=? AND n.type='market_update' AND u.status='active' AND u.role='admin'`, offset+1, pageSize+1, id, userID).Get(&part)
	if err != nil {
		return "", nil, err
	}
	if !found {
		return "", nil, ErrNotFound
	}
	characters := []rune(part)
	if len(characters) > pageSize {
		next := offset + pageSize
		return string(characters[:pageSize]), &next, nil
	}
	return part, nil, nil
}
