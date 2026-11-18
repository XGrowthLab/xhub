package iam

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"xorm.io/xorm"
)

// usageFingerprint 对完整冻结UsageRecord计算稳定SHA256，供账单写入与严格重放一致性确认。
// 参数须已通过用量校验/冻结TS；返回摘要或序列化错误，不将正文写入日志或调用外部服务。
func usageFingerprint(record UsageRecord) (string, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// exactUsageState 在既有 schema 写锁下同时核对待结算 JSON 和已提交指纹，返回是否已入账。
// record 是调用方已验证并冻结时间的快照。内容冲突或旧账单缺少指纹时返回错误；
// 重放已确认快照可以省去 INSERT，预占冲突和人工核销仍分别由原结算步骤校验。
func exactUsageState(s *xorm.Session, record UsageRecord) (bool, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(payload)
	rows, err := s.QueryString(`SELECT
        EXISTS (SELECT 1 FROM usage_pending WHERE request_id=? AND payload <> CAST(? AS jsonb)) AS conflicting,
        COALESCE((SELECT CASE WHEN usage_fingerprint=? THEN 'same' ELSE 'different' END
                   FROM usage_events WHERE request_id=?), 'new') AS ledger`,
		record.RequestID, string(payload), hex.EncodeToString(sum[:]), record.RequestID)
	if err != nil {
		return false, err
	}
	if len(rows) != 1 {
		return false, fmt.Errorf("missing usage snapshot state")
	}
	if rows[0]["conflicting"] == "true" {
		return false, fmt.Errorf("%w: pending usage snapshot differs", ErrConflict)
	}
	if rows[0]["ledger"] == "different" {
		return false, fmt.Errorf("%w: committed usage snapshot differs or has no fingerprint", ErrConflict)
	}
	return rows[0]["ledger"] == "same", nil
}

// checkCommittedUsageSnapshot 在insertEvent发现重复ID后严格核对完整指纹；SQL插入冲突等待并发事务完成，当前事务保证判重后才退款。
// 旧无指纹记录不能可靠重建完整正文/预占ID，因此返回ErrConflict保留磁盘证据，由人工核销fence处理。
func checkCommittedUsageSnapshot(s *xorm.Session, record UsageRecord) error {
	fingerprint, err := usageFingerprint(record)
	if err != nil {
		return err
	}
	var existing UsageEvent
	found, err := s.Where("request_id = ?", record.RequestID).Cols("usage_fingerprint").Get(&existing)
	if err != nil {
		return err
	}
	if !found || existing.UsageFingerprint == "" || existing.UsageFingerprint != fingerprint {
		return fmt.Errorf("%w: committed usage snapshot differs or has no fingerprint", ErrConflict)
	}
	return nil
}
