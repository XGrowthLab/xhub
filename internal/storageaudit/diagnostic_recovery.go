package storageaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sunqirui1987/xhub/internal/logframe"
	"github.com/sunqirui1987/xhub/internal/verifiedzstd"
	"strings"
)

// RecoverDiagnostic 修复一个无法自解压的诊断。event 是稳定用量序号，original 是已在可信主机完成认证还原的原文。
// 只有请求身份、原文长度及 SHA-256 同时与清单和库中版本头一致才允许替换。已有普通文本和后续结果原样保留。
func RecoverDiagnostic(ctx context.Context, db *sql.DB, event int64, original []byte, proof *Preimages) (bool, error) {
	if event < 1 || proof == nil || len(original) == 0 || len(original) > verifiedzstd.MaxRawBytes || !json.Valid(original) || !strings.HasPrefix(strings.TrimSpace(string(original)), "{") {
		return false, OperatorError("invalid verified diagnostic original")
	}
	tx, err := boundedTx(ctx, db, sql.TxOptions{})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var identity, current string
	// 仅读取短版本头即可修复损坏载荷；行锁阻止本批覆盖同时完成的异步更新。
	err = tx.QueryRowContext(ctx, `SELECT l.request_id,left(l.upstream_response,112) FROM request_logs l JOIN usage_events u USING(request_id) WHERE u.id=$1 FOR UPDATE OF l`, event).Scan(&identity, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	key := sha256.Sum256([]byte(identity))
	digest := sha256.Sum256(original)
	expected, exists := proof.hashes[hex.EncodeToString(key[:])]
	if !exists || expected != digest {
		return false, OperatorError("verified diagnostic digest mismatch")
	}
	if !strings.HasPrefix(current, fmt.Sprintf("%s%d:%x:", logframe.Prefix, len(original), digest)) {
		return false, tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE request_logs SET upstream_response=$2 WHERE request_id=$1", identity, string(original)); err != nil {
		return false, err
	}
	var valid bool
	if err = tx.QueryRowContext(ctx, "SELECT sha256(convert_to(upstream_response,'UTF8'))=$2 FROM request_logs WHERE request_id=$1", identity, digest[:]).Scan(&valid); err != nil || !valid {
		return false, OperatorError("verified diagnostic restoration failed")
	}
	return true, tx.Commit()
}
