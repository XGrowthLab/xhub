package iam

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/sunqirui1987/xhub/internal/util"
)

// NotificationBudgetData 是生成提醒时已记账的个人预算快照；不包含请求正文、密钥或可执行链接。
type NotificationBudgetData struct {
	Threshold   int        `json:"threshold"`
	Spent       float64    `json:"spent"`
	Limit       float64    `json:"limit"`
	Remaining   float64    `json:"remaining"`
	PeriodStart *time.Time `json:"period_start"`
	ResetAt     *time.Time `json:"reset_at"`
}

// Notification 是数据库中的单用户消息；列表只向消息所属用户返回。
type Notification struct {
	ID        string          `xorm:"'id'" json:"id"`
	Type      string          `xorm:"'type'" json:"type"`
	CreatedAt time.Time       `xorm:"'created_at'" json:"created_at"`
	Read      bool            `xorm:"'is_read'" json:"read"`
	Data      json.RawMessage `xorm:"-" json:"data"`
	DataRaw   string          `xorm:"'data_raw'" json:"-"`
}

type notificationBudgetState struct {
	Budget      *float64   `xorm:"'budget'"`
	PeriodStart *time.Time `xorm:"'period_start'"`
	ResetDay    int        `xorm:"'reset_day'"`
	Generation  int64      `xorm:"'generation'"`
	Threshold   int        `xorm:"'notified_threshold'"`
}

// budgetThreshold 判定个人已记账预算的最高提醒档位；nil 不提醒，零额度视为用尽，坏数据拒绝。
// 返回档位和合法性；只计算快照，不代表上级限额或尚未结算的实际可用余额。
func budgetThreshold(spend float64, limit *float64) (int, bool) {
	if math.IsNaN(spend) || math.IsInf(spend, 0) || spend < 0 {
		return 0, false
	}
	if limit == nil {
		return 0, true
	}
	if math.IsNaN(*limit) || math.IsInf(*limit, 0) || *limit < 0 {
		return 0, false
	}
	if *limit == 0 || spend >= *limit {
		return 100, true
	}
	if spend >= *limit*0.95 {
		return 95, true
	}
	if spend >= *limit*0.80 {
		return 80, true
	}
	return 0, true
}

// equalBudgetPeriods 判断可空预算周期是否相同；用于预算提醒去重，不受时区表示影响。
func equalBudgetPeriods(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// equalNotificationBudget 判断可空预算是否相同；扫描时用以识别额度变更并开启新提醒代次。
func equalNotificationBudget(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ScanBudgetNotifications 独立扫描启用账户的已记账个人预算，产生一次最高档位提醒。
// ctx 控制全部查询；多个实例通过每用户事务及唯一键去重；失败返回错误供后台重试，不参与计费或修改预算。
// 新预算、周期或刷新日启用新代次；没有个人上限不推断上级剩余，失效账户不收新消息。
func (db *DB) ScanBudgetNotifications(ctx context.Context) error {
	// 使用短租约协调实例，不在等待逐用户事务时占有数据库连接；扫描超时早于租约到期。
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	owner := util.CallID()
	var acquired string
	got, err := db.Engine.Context(ctx).SQL(`INSERT INTO notification_scan_lease(name,owner,expires_at) VALUES('budget',?,now()+interval '60 seconds') ON CONFLICT(name) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at WHERE notification_scan_lease.expires_at < now() RETURNING owner`, owner).Get(&acquired)
	if err != nil {
		return err
	}
	if !got || acquired != owner {
		return nil
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_, _ = db.Engine.Context(cleanup).Exec(`DELETE FROM notification_scan_lease WHERE name='budget' AND owner=?`, owner)
	}()
	var users []struct {
		ID string `xorm:"'id'"`
	}
	// 只锁定额度配置改变或跨过新档位的候选用户；未配置个人上限的用户不产生扫描游标。
	// 最久未处理优先，减少大用户量时一次扫描超时造成队尾饥饿。
	if err := db.Engine.Context(ctx).SQL(`SELECT u.id FROM users u LEFT JOIN notification_budget_state s ON s.user_id=u.id
		WHERE u.status = ? AND (u.budget_reset_day = 0 OR u.budget_reset_at IS NULL OR u.budget_reset_at > now())
		AND ((u.max_budget IS NULL AND s.budget IS NOT NULL) OR
		(u.max_budget IS NOT NULL AND (s.user_id IS NULL OR s.budget IS DISTINCT FROM u.max_budget
		OR s.period_start IS DISTINCT FROM u.budget_period_start OR s.reset_day <> u.budget_reset_day
		OR (CASE WHEN u.max_budget = 0 OR u.spend >= u.max_budget THEN 100
		WHEN u.spend >= u.max_budget * 0.95 THEN 95 WHEN u.spend >= u.max_budget * 0.80 THEN 80 ELSE 0 END) > s.notified_threshold)))
		ORDER BY s.last_checked_at ASC NULLS FIRST, u.id LIMIT 200`, StatusActive).Find(&users); err != nil {
		return err
	}
	failures := 0
	for _, user := range users {
		if err := db.scanBudgetNotification(ctx, user.ID); err != nil {
			// 单个坏账户不能阻止其他人提醒；不把用户标识拼入日志，汇总错误供维护任务重试。
			failures++
			if ctx.Err() != nil {
				break
			}
			// 将失败账户移至队尾；若存储故障导致该写入失败，错误仍由本轮汇总并在下次重试。
			_, _ = db.Engine.Context(ctx).Exec(`INSERT INTO notification_budget_state(user_id,last_checked_at) VALUES (?,now()) ON CONFLICT(user_id) DO UPDATE SET last_checked_at=excluded.last_checked_at`, user.ID)
		}
	}
	if ctx.Err() != nil {
		return fmt.Errorf("notification scan incomplete: %w", ctx.Err())
	}
	if failures > 0 {
		// 数据库错误的详情可能包含用户或消息内容，不写入维护日志。
		return fmt.Errorf("notification scan failed for %d accounts", failures)
	}
	return nil
}

// scanBudgetNotification 在短事务中锁定单个账户和消息游标，原子保存提醒并推进档位。
// userID 来自服务端用户表；不存在或停用时不生成消息，任何数据库错误只回滚通知自身并由下次扫描重试。
func (db *DB) scanBudgetNotification(ctx context.Context, userID string) error {
	s := db.Engine.NewSession().Context(ctx)
	defer s.Close()
	if err := s.Begin(); err != nil {
		return err
	}
	defer s.Rollback()
	// 限制每条语句耗时；若通知写入卡住，尽快释放用户行锁，避免长时间阻挡记账。
	if _, err := s.Exec(`SET LOCAL statement_timeout = '500ms'`); err != nil {
		return err
	}
	// 跳过正在记账或调额的账户，由下一轮重试；持锁期间仅提交快照与通知。
	var user User
	found, err := s.SQL(`SELECT id, status, max_budget, spend, budget_period_start, budget_reset_at, budget_reset_day FROM users WHERE id=? FOR SHARE SKIP LOCKED`, userID).Get(&user)
	if err != nil {
		return err
	}
	if !found || user.Status != StatusActive {
		return s.Commit()
	}
	if _, err = s.Exec(`INSERT INTO notification_budget_state(user_id) VALUES(?) ON CONFLICT(user_id) DO NOTHING`, userID); err != nil {
		return err
	}
	// 到期的旧周期不能在预算维护完成前发送错误提醒；记录检查时间以免占用扫描队首。
	if user.BudgetResetDay > 0 && user.BudgetResetAt != nil && !user.BudgetResetAt.After(time.Now()) {
		if _, err := s.Exec(`UPDATE notification_budget_state SET last_checked_at=now() WHERE user_id=?`, userID); err != nil {
			return err
		}
		return s.Commit()
	}
	threshold, valid := budgetThreshold(user.Spend, user.MaxBudget)
	if !valid {
		return fmt.Errorf("invalid personal budget data")
	}
	var state notificationBudgetState
	found, err = s.SQL(`SELECT budget,period_start,reset_day,generation,notified_threshold FROM notification_budget_state WHERE user_id=? FOR UPDATE`, userID).Get(&state)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if !equalNotificationBudget(state.Budget, user.MaxBudget) || !equalBudgetPeriods(state.PeriodStart, user.BudgetPeriodStart) || state.ResetDay != user.BudgetResetDay {
		state.Generation++
		state.Threshold = 0
	}
	if threshold > state.Threshold && user.MaxBudget != nil {
		data := NotificationBudgetData{Threshold: threshold, Spent: user.Spend, Limit: *user.MaxBudget, Remaining: math.Max(0, *user.MaxBudget-user.Spend), PeriodStart: user.BudgetPeriodStart, ResetAt: user.BudgetResetAt}
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("budget:%d:%d", state.Generation, threshold)
		if _, err = s.Exec(`INSERT INTO user_notifications(id,user_id,type,event_key,data) VALUES(?,?,?, ?,?::jsonb) ON CONFLICT(user_id,event_key) DO NOTHING`, util.CallID(), userID, "budget_threshold", key, string(raw)); err != nil {
			return err
		}
		state.Threshold = threshold
	}
	if _, err = s.Exec(`UPDATE notification_budget_state SET budget=?,period_start=?,reset_day=?,generation=?,notified_threshold=?,last_checked_at=now() WHERE user_id=?`, user.MaxBudget, user.BudgetPeriodStart, user.BudgetResetDay, state.Generation, state.Threshold, userID); err != nil {
		return err
	}
	return s.Commit()
}

// ListNotifications 返回账户私有消息和总数；userID 来自认证会话，limit/offset 已由处理器限制。
// 所有过滤在 SQL 中执行；市场消息还实时检查当前平台管理员身份，撤权后立即不可见。
// 存储或非法 JSON 出错时返回错误，不返回其他用户数据。
func (db *DB) ListNotifications(ctx context.Context, userID string, limit, offset int) ([]Notification, int64, error) {
	if userID == "" || limit < 1 || limit > 100 || offset < 0 || offset > 10000 {
		return nil, 0, ErrInvalid
	}
	var rows []Notification
	if err := db.Engine.Context(ctx).SQL(`SELECT n.id,n.type,n.created_at,(n.read_at IS NOT NULL) AS is_read,(CASE WHEN n.type='market_update' THEN m.summary ELSE n.data END)::text AS data_raw FROM user_notifications n LEFT JOIN notification_market_events m ON n.type='market_update' AND m.id=n.data->>'event_id' WHERE n.user_id=? AND (n.type <> 'market_update' OR EXISTS (SELECT 1 FROM users u WHERE u.id=n.user_id AND u.status='active' AND u.role='admin')) ORDER BY n.created_at DESC,n.id DESC LIMIT ? OFFSET ?`, userID, limit, offset).Find(&rows); err != nil {
		return nil, 0, err
	}
	for i := range rows {
		if !json.Valid([]byte(rows[i].DataRaw)) {
			return nil, 0, fmt.Errorf("invalid notification data")
		}
		rows[i].Data = json.RawMessage(rows[i].DataRaw)
	}
	var count int64
	_, err := db.Engine.Context(ctx).SQL(`SELECT count(*) FROM user_notifications n WHERE n.user_id=? AND (n.type <> 'market_update' OR EXISTS (SELECT 1 FROM users u WHERE u.id=n.user_id AND u.status='active' AND u.role='admin'))`, userID).Get(&count)
	if err != nil {
		return nil, 0, err
	}
	if rows == nil {
		rows = []Notification{}
	}
	return rows, count, nil
}

// UnreadNotifications 返回当前用户可见消息的未读数量；userID 必须为当前会话，撤权后不计入市场消息，查询失败不会伪装成零。
func (db *DB) UnreadNotifications(ctx context.Context, userID string) (int64, error) {
	if userID == "" {
		return 0, ErrInvalid
	}
	var count int64
	_, err := db.Engine.Context(ctx).SQL(`SELECT count(*) FROM user_notifications n WHERE n.user_id=? AND n.read_at IS NULL AND (n.type <> 'market_update' OR EXISTS (SELECT 1 FROM users u WHERE u.id=n.user_id AND u.status='active' AND u.role='admin'))`, userID).Get(&count)
	return count, err
}

// ReadNotification 只将当前用户可见的一条消息标为已读；市场消息实时检查平台管理员资格，重复操作幂等，未找到与越权统一 ErrNotFound。
func (db *DB) ReadNotification(ctx context.Context, userID, id string) error {
	if userID == "" || id == "" || len(id) > 128 {
		return ErrInvalid
	}
	result, err := db.Engine.Context(ctx).Exec(`UPDATE user_notifications n SET read_at=COALESCE(n.read_at,now()) WHERE n.user_id=? AND n.id=? AND (n.type <> 'market_update' OR EXISTS (SELECT 1 FROM users u WHERE u.id=n.user_id AND u.status='active' AND u.role='admin'))`, userID, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CleanExpiredNotifications 分批清除过期站内消息，cutoff 为保留边界，limit 限制单轮工作量。
// 后台维护调用，多个实例并发不会影响账单、预算或已读状态；失败返回错误待下轮重试。
func (db *DB) CleanExpiredNotifications(ctx context.Context, cutoff time.Time, limit int) error {
	if cutoff.IsZero() || limit < 1 || limit > 10000 {
		return ErrInvalid
	}
	_, err := db.Engine.Context(ctx).Exec(`DELETE FROM user_notifications WHERE id IN (SELECT id FROM user_notifications WHERE created_at < ? ORDER BY created_at LIMIT ?)`, cutoff.UTC(), limit)
	if err != nil {
		return err
	}
	// 消息过期或用户删除后才移除共享内容；按引用判断，避免影响仍可读取的消息。
	var hasConfig bool
	if _, err = db.Engine.Context(ctx).SQL(`SELECT to_regclass('proxy_config') IS NOT NULL`).Get(&hasConfig); err != nil {
		return err
	}
	if hasConfig {
		_, err = db.Engine.Context(ctx).Exec(`DELETE FROM notification_market_events WHERE id IN (SELECT m.id FROM notification_market_events m WHERE NOT EXISTS (SELECT 1 FROM user_notifications n WHERE n.type='market_update' AND n.data->>'event_id'=m.id) AND NOT EXISTS (SELECT 1 FROM proxy_config p WHERE p.namespace='market_change_events' AND p.key=m.id) LIMIT ?)`, limit)
	} else {
		_, err = db.Engine.Context(ctx).Exec(`DELETE FROM notification_market_events WHERE id IN (SELECT m.id FROM notification_market_events m WHERE NOT EXISTS (SELECT 1 FROM user_notifications n WHERE n.type='market_update' AND n.data->>'event_id'=m.id) LIMIT ?)`, limit)
	}
	return err
}
