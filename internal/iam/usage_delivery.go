package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"xorm.io/xorm"
)

// ErrDeliveryMarkerGone 表示待确认标记已被转换或恢复，调用方必须核实已有账单，不得凭此创建新账单。
var ErrDeliveryMarkerGone = errors.New("delivery marker no longer exists")

// DeliveryResultRecorded 检查未知磁盘标记是否已有同归属的持久最终快照，供重启恢复决定是否仅清理旧标记。
// ctx 控制只读查询，marker 必须是零费用未知标记；返回已记录状态或错误，不修改原账单、费用和正文。
// 先读待结算再读账本，覆盖另一实例刚完成结算的情况；请求 ID 相同但归属不同必须保留证据并报冲突。
func (db *DB) DeliveryResultRecorded(ctx context.Context, marker UsageRecord) (bool, error) {
	if err := validatePendingUsage(marker); err != nil {
		return false, err
	}
	if !marker.DeliveryUnconfirmed || marker.Cost != 0 || marker.Status != "error" {
		return false, fmt.Errorf("invalid unknown delivery marker")
	}
	rows, err := db.Engine.Context(ctx).QueryString(`SELECT payload::text AS payload FROM usage_pending WHERE request_id = ?`, marker.RequestID)
	if err != nil {
		return false, err
	}
	var final UsageRecord
	if len(rows) > 0 {
		if err := json.Unmarshal([]byte(rows[0]["payload"]), &final); err != nil {
			return false, err
		}
		if err := validatePendingUsage(final); err != nil {
			return false, err
		}
	} else {
		var event UsageEvent
		found, err := db.Engine.Context(ctx).Where("request_id = ?", marker.RequestID).Get(&event)
		if err != nil || !found {
			return false, err
		}
		final = UsageRecord{RequestID: event.RequestID, KeyID: event.KeyID, UserID: event.UserID, TeamID: event.TeamID, OrganizationID: event.OrganizationID, ProjectID: event.ProjectID, OwnerType: event.OwnerType, Model: event.Model, CallType: event.CallType}
	}
	if final.RequestID != marker.RequestID || final.KeyID != marker.KeyID || final.UserID != marker.UserID || final.TeamID != marker.TeamID || final.OrganizationID != marker.OrganizationID || final.ProjectID != marker.ProjectID || ownerType(final) != ownerType(marker) || final.Model != marker.Model || (marker.CallType != "" && final.CallType != marker.CallType) {
		return false, fmt.Errorf("%w: recorded delivery owner differs", ErrConflict)
	}
	return true, nil
}

// DiscardRecordedDelivery 仅在相同请求已有持久账单或待结算快照时删除未确认标记。
// ctx 控制数据库操作，id 为调用 ID；返回错误供调用方记录，不能清理由内存推断的未记录结果。
func (db *DB) DiscardRecordedDelivery(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("empty delivery identity")
	}
	_, err := db.Engine.Context(ctx).Exec(`DELETE FROM usage_delivery_pending WHERE request_id = ? AND (EXISTS (SELECT 1 FROM usage_events WHERE request_id = ?) OR EXISTS (SELECT 1 FROM usage_pending WHERE request_id = ?))`, id, id, id)
	return err
}

// PrepareDelivery 在响应写出前独立持久化零费用恢复标记；record 是调用的归属和计费事实，expires 为恢复截止时间。
// 返回存储或输入错误；调用方必须在成功后才写出响应，不能把恢复标记放入自动结算队列。
// 标记不保存请求、响应、请求头或上游正文；未确认的价格快照也不保存，超时只能恢复为结果未知的失败。
func (db *DB) PrepareDelivery(ctx context.Context, record UsageRecord, expires time.Time) error {
	if !expires.After(time.Now()) {
		return fmt.Errorf("invalid delivery deadline")
	}
	record, err := UnconfirmedDelivery(record)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	result, err := db.Engine.Context(ctx).Exec(`INSERT INTO usage_delivery_pending(request_id,payload,expires_at) VALUES (?,?::jsonb,?) ON CONFLICT(request_id) DO NOTHING`, record.RequestID, string(payload), expires)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("%w: delivery marker already exists", ErrConflict)
	}
	return nil
}

// UnconfirmedDelivery 构造不含正文和费用的恢复快照；record 为上游产生的结果，返回独立副本或验证错误。
// 数据库与磁盘准备阶段共用；不得把该快照当作客户端明确停止或已经收到响应的证据。
func UnconfirmedDelivery(record UsageRecord) (UsageRecord, error) {
	if err := validatePendingUsage(record); err != nil {
		return UsageRecord{}, err
	}
	record.TS = stamp(record.TS)
	record.Status, record.HTTPStatus, record.Cost = "error", 0, 0
	record.RequestBody, record.ResponseBody, record.ProxyRequest, record.UpstreamResponse = "", "", "", ""
	record.PriceSnapshot, record.Guardrail = "", ""
	// 用户备注可能包含任意文本，不能作为恢复标记长期保存。
	record.Label = ""
	record.Error = `{"type":"delivery_unconfirmed","message":"client delivery was not confirmed"}`
	return record, nil
}

// stageDelivery 将单条最终快照放入同一事务中的待结算队列；参数是活动事务与已验证账单。
// 返回持久化或快照冲突错误；相同请求已有不同内容时必须回滚，避免替换原有账单。
func stageDelivery(s *xorm.Session, record UsageRecord) (pendingUsage, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return pendingUsage{}, err
	}
	if _, err = s.Exec(`INSERT INTO usage_pending(request_id,payload) VALUES (?,?::jsonb) ON CONFLICT(request_id) DO NOTHING`, record.RequestID, string(payload)); err != nil {
		return pendingUsage{}, err
	}
	rows, err := s.QueryString(`SELECT payload = CAST(? AS jsonb) AS same, payload::text AS frozen FROM usage_pending WHERE request_id = ?`, string(payload), record.RequestID)
	if err != nil {
		return pendingUsage{}, err
	}
	if len(rows) != 1 || rows[0]["same"] != "true" {
		return pendingUsage{}, fmt.Errorf("%w: pending delivery snapshot differs", ErrConflict)
	}
	return pendingUsage{RequestID: record.RequestID, Payload: rows[0]["frozen"]}, nil
}

// ConfirmedDelivery 是由既有标记事务生成的冻结凭据，只在当前调用内传递，无需再次按 ID 读取公开表。
// 只允许 ConfirmDeliveryForSettlement 构造；确认失败返回零值，Settle 不接受零凭据。
type ConfirmedDelivery struct {
	db    *DB
	saved pendingUsage
}

// ConfirmDeliveryForSettlement 原子核对交付标记并保存终态，返回已提交事务实际使用的不可变快照和同一配置库。
// 同步处理器通过返回值严格结算；失败不产生成功凭据，沿用原有磁盘或 Redis 恢复路径。
func (db *DB) ConfirmDeliveryForSettlement(ctx context.Context, record UsageRecord) (ConfirmedDelivery, error) {
	saved, err := db.confirmDelivery(ctx, &record)
	if err != nil {
		return ConfirmedDelivery{}, err
	}
	return ConfirmedDelivery{db: db, saved: saved}, nil
}

// Settle 在原配置库严格结算确认事务返回的完整快照；ctx 控制等待，零凭据或快照冲突返回错误。
// 外部先删除待办仍须按原内容恢复或证明账本一致，不能仅凭 request_id 确认成功。
func (d ConfirmedDelivery) Settle(ctx context.Context) error {
	if d.db == nil {
		return fmt.Errorf("missing confirmed delivery snapshot")
	}
	// 凭据可以在确认后延迟使用或重试。本次调用开始前已存在的领写事务必须重新核对数据库围栏。
	d.saved.observedAt = time.Now()
	return d.db.settlePendingUsage(ctx, d.saved)
}

// ConfirmDelivery 将已确认写出或明确写失败的最终账单原子转入待结算队列。
// 参数 ctx 为数据库上下文，record 为最终快照；返回错误时不删除恢复标记，也不产生部分账单。
// 记录必须属于原请求的相同密钥和账单归属；结果已被恢复或同 ID 内容冲突时拒绝重复结算。
func (db *DB) ConfirmDelivery(ctx context.Context, record UsageRecord) error {
	_, err := db.confirmDelivery(ctx, &record)
	return err
}

// confirmDelivery 将最终快照转入待结算队列并回填标记中冻结的时间；参数 record 会被更新。
// 供确认和恢复路径使用，事务失败保留原标记，不对外泄露请求或响应正文。
func (db *DB) confirmDelivery(ctx context.Context, record *UsageRecord) (pendingUsage, error) {
	if err := validatePendingUsage(*record); err != nil {
		return pendingUsage{}, err
	}
	if record.Cost > 0 && (record.Status != "success" || record.HTTPStatus >= 400) {
		return pendingUsage{}, fmt.Errorf("invalid delivery charge for failed response")
	}
	s := db.Engine.NewSession().Context(ctx)
	defer s.Close()
	if err := s.Begin(); err != nil {
		return pendingUsage{}, err
	}
	defer s.Rollback()
	rows, err := s.QueryString(`SELECT payload::text AS payload FROM usage_delivery_pending WHERE request_id = ? FOR UPDATE`, record.RequestID)
	if err != nil {
		return pendingUsage{}, err
	}
	if len(rows) != 1 {
		return pendingUsage{}, ErrDeliveryMarkerGone
	}
	var initial UsageRecord
	if err := json.Unmarshal([]byte(rows[0]["payload"]), &initial); err != nil {
		return pendingUsage{}, err
	}
	if initial.RequestID != record.RequestID || initial.KeyID != record.KeyID || initial.UserID != record.UserID || initial.TeamID != record.TeamID || initial.ProjectID != record.ProjectID || initial.OrganizationID != record.OrganizationID || initial.OwnerType != record.OwnerType || initial.Model != record.Model || (initial.CallType != "" && initial.CallType != record.CallType) || initial.BudgetReservationID != record.BudgetReservationID {
		return pendingUsage{}, fmt.Errorf("%w: delivery owner differs", ErrConflict)
	}
	// 首次准备时固定发生时间，故障恢复不会把账单移到其他时间范围。
	record.TS = initial.TS
	saved, err := stageDelivery(s, *record)
	if err != nil {
		return pendingUsage{}, err
	}
	if _, err := s.Exec(`DELETE FROM usage_delivery_pending WHERE request_id = ?`, record.RequestID); err != nil {
		return pendingUsage{}, err
	}
	if err := s.Commit(); err != nil {
		return pendingUsage{}, err
	}
	saved.observedAt = time.Now()
	return saved, nil
}

// SettleConfirmedDelivery 恢复已持久化的客户端写出终态；ctx 控制数据库操作，record 为完整冻结快照。
// 先原子转换待确认标记；仅独立磁盘在断库时保存的快照可在没有标记时创建账单，其他情况必须已存在持久账单。
// 全量快照由严格指纹核对，冲突保留队列并返回错误，不能伪造确认或重复计费。
func (db *DB) SettleConfirmedDelivery(ctx context.Context, record UsageRecord) error {
	saved, err := db.confirmDelivery(ctx, &record)
	if err == nil {
		return db.settleObservedPending(ctx, record, saved)
	}
	if !errors.Is(err, ErrDeliveryMarkerGone) {
		return err
	}
	// 另一份 PG 或磁盘记录可以持有同一时刻的其他时区表示；精确核对统一使用冻结的 UTC。
	record.TS = stamp(record.TS)
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	// 在一次真实查询中核对恢复依据，返回相同待办的 PG 原文，便于和正在工作的前台共享同一观察。
	// 发现不同待办仍交给原有严格核对，包含人工核销与原预占，不用 ID 替代计费证据。
	rows, err := db.Engine.Context(ctx).QueryString(`SELECT COALESCE((SELECT payload::text FROM usage_pending WHERE request_id=?), '') AS pending,
		EXISTS(SELECT 1 FROM usage_events WHERE request_id=?) AS committed,
		EXISTS(SELECT 1 FROM usage_pending WHERE request_id=? AND payload=?::jsonb) AS same`, record.RequestID, record.RequestID, record.RequestID, string(raw))
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return fmt.Errorf("missing delivery settlement state")
	}
	if rows[0]["pending"] == "" && rows[0]["committed"] != "true" && !record.DeliverySpoolOnly {
		return fmt.Errorf("%w: delivery marker and ledger are missing", ErrConflict)
	}
	saved = pendingUsage{RequestID: record.RequestID, Payload: string(raw), observedAt: time.Now()}
	if rows[0]["same"] == "true" {
		saved.Payload = rows[0]["pending"]
	}
	return db.settleObservedPending(ctx, record, saved)
}

// RecoverExpiredDeliveries 将过期且尚未确认的记录原子转为零费用失败，返回已转移数量和错误。
// ctx 控制数据库操作，now 为观察时间，limit 必须为正；多个实例通过行锁互斥，不调用上游。
// 无 Redis 守卫时只处理不归属 Redis/磁盘的标记；已转移记录由 FlushPendingUsage 精确结算，迟到确认不能再次收费。
func (db *DB) RecoverExpiredDeliveries(ctx context.Context, now time.Time, limit int) (int, error) {
	return db.RecoverExpiredDeliveriesGuarded(ctx, now, limit, nil)
}

// DeliveryRecoveryGuard 在数据库行锁内原子竞争一批请求的恢复权，返回允许恢复的 ID 集合。
// Redis 实现必须与确认入队共用不可过期的去重标识；错误或未返回的 ID 均不得写零费用。
// 已取得的恢复权不因数据库回滚释放，后续恢复可以重试，但迟到的队列确认不能冒充已持久化成功。
type DeliveryRecoveryGuard func(context.Context, []string) (map[string]bool, error)

// RecoverExpiredDeliveriesGuarded 恢复到期未知结果；guard 与 Redis 确认入队串行裁决同一请求。
// 参数 ctx/now/limit 控制事务、到期时间和检查批量；返回实际清理或转换数量及错误。
// 不改变已有账单；缺少 Redis 守卫时跳过归属 Redis 的标记。跳过记录排到后续批次，磁盘标记仍由持卷实例处理。
func (db *DB) RecoverExpiredDeliveriesGuarded(ctx context.Context, now time.Time, limit int, guard DeliveryRecoveryGuard) (int, error) {
	if limit < 1 {
		return 0, fmt.Errorf("invalid recovery batch size")
	}
	s := db.Engine.NewSession().Context(ctx)
	defer s.Close()
	if err := s.Begin(); err != nil {
		return 0, err
	}
	defer s.Rollback()
	// 磁盘独占的标记必须由持有该卷的实例恢复；其他实例看不到其已确认终态，不能猜测失败。
	rows, err := s.QueryString(`SELECT request_id,payload::text AS payload FROM usage_delivery_pending WHERE expires_at <= ? AND COALESCE((payload->>'DeliverySpoolOnly')::boolean, false) = false ORDER BY recovery_checked_at NULLS FIRST,expires_at,request_id LIMIT ? FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return 0, err
	}
	recovered := 0
	records := make([]UsageRecord, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		// 已有最终账单的失败请求可能在清理标记前退出，不能用另一份未知快照覆盖或重复结算。
		existing, err := s.QueryString(`SELECT EXISTS (SELECT 1 FROM usage_events WHERE request_id = ?) OR EXISTS (SELECT 1 FROM usage_pending WHERE request_id = ?) AS present`, row["request_id"], row["request_id"])
		if err != nil {
			return 0, err
		}
		if len(existing) == 1 && existing[0]["present"] == "true" {
			if _, err := s.Exec(`DELETE FROM usage_delivery_pending WHERE request_id = ?`, row["request_id"]); err != nil {
				return 0, err
			}
			recovered++
			continue
		}
		var record UsageRecord
		if err := json.Unmarshal([]byte(row["payload"]), &record); err != nil {
			return 0, err
		}
		if err := validatePendingUsage(record); err != nil {
			return 0, err
		}
		if record.RequestID != row["request_id"] || record.Status != "error" || record.Cost != 0 {
			return 0, fmt.Errorf("invalid delivery recovery snapshot")
		}
		records = append(records, record)
		ids = append(ids, record.RequestID)
	}
	var allowed map[string]bool
	if guard != nil && len(ids) > 0 {
		allowed, err = guard(ctx, ids)
		if err != nil {
			return 0, err
		}
	}
	for _, record := range records {
		if (guard != nil && !allowed[record.RequestID]) || (guard == nil && record.DeliveryRedisGuard) {
			// 不延长原截止时间，只更新检查顺序；超过一批的冲突不会饿死后面的未知结果。
			if _, err := s.Exec(`UPDATE usage_delivery_pending SET recovery_checked_at = clock_timestamp() WHERE request_id = ?`, record.RequestID); err != nil {
				return 0, err
			}
			continue
		}
		if _, err := stageDelivery(s, record); err != nil {
			return 0, err
		}
		if _, err := s.Exec(`DELETE FROM usage_delivery_pending WHERE request_id = ?`, record.RequestID); err != nil {
			return 0, err
		}
		recovered++
	}
	if err := s.Commit(); err != nil {
		return 0, err
	}
	return recovered, nil
}
