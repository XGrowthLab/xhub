package storageaudit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/sunqirui1987/xhub/internal/historypack"
	"time"
)

// RecoverOriginal 从已完成认证解密的备份中恢复指定一条损坏帧。参数是旧键摘要、严格 UTC 创建时间、原始字节与独立校验的清单。
// 同一把网关锁内检查创建时间与入库时的原文摘要。普通、新写入、已有清单或版本不符返回 false；故障回滚。
func RecoverOriginal(ctx context.Context, db *sql.DB, key, created string, original []byte, verified *Preimages) (bool, error) {
	parsed, e := time.Parse(time.RFC3339, created)
	if e != nil || parsed.UTC().Format(time.RFC3339) != created || !digestPattern.MatchString(key) || verified == nil || len(original) == 0 || len(original) > historypack.MaxRawBytes || !json.Valid(original) || !bytes.HasPrefix(bytes.TrimSpace(original), []byte("[")) {
		return false, errors.New("invalid verified recovery input")
	}
	digest, present := verified.hashes[key]
	if !present || sha256.Sum256(original) != digest {
		return false, errors.New("backup record does not match verified manifest")
	}
	tx, err := boundedTx(ctx, db, sql.TxOptions{})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	id := historyPrefix + key
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", id); err != nil {
		return false, err
	}
	// 仅修当下具有同一原始版本元数据的帧。损坏的帧无法自解压，备份后发生的正常写入不能被旧备份覆盖。
	const eligible = ` WHERE kind='conversation_state' AND id=$1 AND created_at=$2 AND body=$3 AND history_frame IS NOT NULL
 AND history_raw_size=$4 AND history_sha256=$5 AND NOT EXISTS (SELECT 1 FROM conversation_history_manifests WHERE id=$1)`
	res, err := tx.ExecContext(ctx, "UPDATE kv SET body=$6,history_frame=NULL,history_raw_size=NULL,history_sha256=NULL"+eligible, id, created, historypack.Marker, len(original), digest[:], string(original))
	if err != nil {
		return false, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, tx.Commit()
	}
	var valid bool
	if err = tx.QueryRowContext(ctx, `SELECT history_frame IS NULL AND history_raw_size IS NULL AND history_sha256 IS NULL
 AND sha256(convert_to(body,'UTF8'))=$2 FROM kv WHERE kind='conversation_state' AND id=$1`, id, digest[:]).Scan(&valid); err != nil || !valid {
		return false, errors.New("backup restoration failed database verification")
	}
	return true, tx.Commit()
}
