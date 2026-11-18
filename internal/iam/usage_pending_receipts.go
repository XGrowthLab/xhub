package iam

import (
	"context"
	"encoding/json"
	"strconv"

	"xorm.io/xorm"
)

// verifiedPendingReceipts 供并发恢复使用：在实际 IAM 串行事务内批查每份观察到的快照，返回有充分已结算证据的原始序号。
// 要求匹配完整指纹、pending 已确认、没有残留预占和核销结论。跨周期的已入账事件同样可确认；缺失、冲突、无指纹项走原恢复逻辑。
func verifiedPendingReceipts(ctx context.Context, db *DB, rows []pendingUsage) (map[int]bool, error) {
	type candidate struct {
		Ord         int    `json:"ord"`
		ID          string `json:"id"`
		Fingerprint string `json:"fp"`
		Hold        string `json:"hold"`
		Resolution  string `json:"resolution"`
	}
	var candidates []candidate
	for i, row := range rows {
		var r UsageRecord
		if err := json.Unmarshal([]byte(row.Payload), &r); err != nil || r.RequestID != row.RequestID || validatePendingUsage(r) != nil || r.TS.IsZero() {
			continue
		}
		r.TS = r.TS.UTC()
		fp, err := usageFingerprint(r)
		if err != nil {
			continue
		}
		hold := r.BudgetReservationID
		if hold == "" {
			hold = r.RequestID
		}
		candidates = append(candidates, candidate{i, r.RequestID, fp, hold, r.BudgetReservationID})
	}
	verified := map[int]bool{}
	if len(candidates) == 0 {
		return verified, nil
	}
	raw, err := json.Marshal(candidates)
	if err != nil {
		return nil, err
	}
	// 其他副本可能正持 schema 写锁创建预占或登记人工核销。普通 MVCC 查询会看到旧的“无围栏”状态。
	// 这里和逐笔结算一样等待前序事务，也必须在出具收据前处理到期预算，不能用读取快照代替这两个副作用。
	err = db.tx(ctx, func(s *xorm.Session) error {
		result, err := s.QueryString(`SELECT c.ord FROM jsonb_to_recordset(?::jsonb) AS c(ord int,id text,fp text,hold text,resolution text)
		JOIN usage_events e ON e.request_id=c.id AND e.usage_fingerprint=c.fp
		WHERE NOT EXISTS (SELECT 1 FROM usage_pending p WHERE p.request_id=c.id)
		AND NOT EXISTS (SELECT 1 FROM budget_reservations h WHERE h.request_id=c.hold)
		AND NOT EXISTS (SELECT 1 FROM budget_reconciliations f WHERE f.call_id=c.id)
		AND NOT EXISTS (SELECT 1 FROM budget_reconciliations f WHERE c.resolution<>'' AND f.reservation_id=c.resolution)`, string(raw))
		if err != nil {
			return err
		}
		for _, v := range result {
			i, err := strconv.Atoi(v["ord"])
			if err != nil {
				return err
			}
			verified[i] = true
		}
		return nil
	})
	return verified, err
}
