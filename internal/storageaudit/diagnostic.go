package storageaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sunqirui1987/xhub/internal/logframe"
	"github.com/sunqirui1987/xhub/internal/verifiedzstd"
)

// DiagnosticOptions 用截止时间及固定用量事件主键上界界定扫描。首批 Through 为零时从已提交的最大主键取值。
// 后续使用上批返回的 Through 与 Next，读取的原文字节总数和单条大小分别受限。Preimages 绑定已恢复验证的原件。
type DiagnosticOptions struct {
	Before                            time.Time
	After, Through                    int64
	Rows                              int
	BatchBytes, RecordBytes, MinBytes int64
	Preimages                         *Preimages
}

// DiagnosticReport 只返回聚合及数字游标。StoredBefore/After 对仅有收益的候选计量，plan 的 After 为文本编码上界。
// pack 的 After 来自更新后的 PostgreSQL 物理字段，恢复及不适合转换的行另行计数。
type DiagnosticReport struct {
	Mode         string    `json:"mode"`
	Before       time.Time `json:"before"`
	Through      int64     `json:"through"`
	Next         int64     `json:"next"`
	More         bool      `json:"has_more"`
	Scanned      int       `json:"scanned"`
	Selected     int       `json:"selected"`
	Kept         int       `json:"kept"`
	Deferred     int       `json:"oversized"`
	Changed      int       `json:"changed_or_unbacked"`
	Restored     int       `json:"restored"`
	Read         int64     `json:"raw_bytes_read"`
	StoredBefore int64     `json:"selected_stored_before"`
	StoredAfter  int64     `json:"selected_stored_after"`
}

// diagnosticMetadata 只将已知格式的原长、当前物理大小和已编码标志返回给维护任务。普通文本的内容不会进入元数据。
const diagnosticMetadata = `CASE WHEN starts_with(left(l.upstream_response,32),$3) THEN coalesce(substring(left(l.upstream_response,64) from '^xhub:diagnostic:zstd:1:([0-9]{1,8}):')::bigint,-1)
 ELSE coalesce(octet_length(l.upstream_response),0) END,coalesce(pg_column_size(l.upstream_response),0),coalesce(starts_with(left(l.upstream_response,32),$3),false)`

type diagnosticRow struct {
	id, raw, stored int64
	packed          bool
}

// DiagnosticBatch 在一个小事务内访问后台日志。plan 为只读；pack/restore 先锁单条日志再重读并校验。
// 使用当前用量事件的稳定 ID，只修改 upstream_response，保留可搜索错误、授权、采集期限和账单。
func DiagnosticBatch(ctx context.Context, db *sql.DB, o DiagnosticOptions, mode string) (*DiagnosticReport, error) {
	if mode != "plan" && mode != "pack" && mode != "restore" || o.Before.IsZero() || o.Before.Nanosecond() != 0 || o.After < 0 || o.Through < 0 || o.After > o.Through || o.Rows < 1 || o.Rows > 500 || o.MinBytes < 1 || o.RecordBytes < o.MinBytes || o.RecordBytes > verifiedzstd.MaxRawBytes || o.BatchBytes < o.RecordBytes || o.BatchBytes > 256<<20 || (mode == "restore" && o.Preimages != nil) {
		return nil, OperatorError("invalid diagnostic batch bounds")
	}
	opts := sql.TxOptions{ReadOnly: mode == "plan"}
	if opts.ReadOnly {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := boundedTx(ctx, db, opts)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var future bool
	if err = tx.QueryRowContext(ctx, "SELECT $1::timestamptz>transaction_timestamp()", o.Before).Scan(&future); err != nil {
		return nil, err
	}
	if future {
		return nil, OperatorError("diagnostic cutoff is in the future")
	}
	if o.Through == 0 {
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(id),0) FROM usage_events").Scan(&o.Through); err != nil {
			return nil, err
		}
	}
	r := &DiagnosticReport{Mode: mode, Before: o.Before.UTC(), Through: o.Through, Next: o.After}
	// 先限定事件主键的一页，用量在截止点之外、缺少日志和无诊断的记录都可推进游标。
	rows, err := tx.QueryContext(ctx, `WITH page AS MATERIALIZED (SELECT id,request_id,ts FROM usage_events WHERE id>$1 AND id<=$2 ORDER BY id LIMIT $4)
 SELECT p.id,`+diagnosticMetadata+` FROM page p LEFT JOIN request_logs l ON l.request_id=p.request_id AND p.ts<$5 ORDER BY p.id`, o.After, o.Through, logframe.Family, o.Rows+1, o.Before)
	if err != nil {
		return nil, err
	}
	var page []diagnosticRow
	for rows.Next() {
		var d diagnosticRow
		if err = rows.Scan(&d.id, &d.raw, &d.stored, &d.packed); err != nil {
			rows.Close()
			return nil, err
		}
		page = append(page, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, d := range page {
		if r.Scanned == o.Rows {
			break
		}
		if mode != "plan" && d.raw > 0 && d.packed == (mode == "restore") && d.raw <= o.RecordBytes && (d.packed || d.raw >= o.MinBytes) {
			// 异步任务可能在分页后更新同一条日志。等待行锁后的新语句读取其最终版本，不聚合或重写用量。
			err = tx.QueryRowContext(ctx, `SELECT `+diagnosticMetadata+` FROM request_logs l JOIN usage_events u USING(request_id) WHERE u.id=$1 AND u.ts<$2 FOR UPDATE OF l`, d.id, o.Before, logframe.Family).Scan(&d.raw, &d.stored, &d.packed)
			if errors.Is(err, sql.ErrNoRows) {
				d.raw = 0
			} else if err != nil {
				return nil, err
			}
		}
		if d.raw < 0 {
			return nil, OperatorError("invalid diagnostic frame header")
		}
		if d.packed != (mode == "restore") || d.raw == 0 || (!d.packed && d.raw < o.MinBytes) {
			r.Kept++
		} else if d.raw > o.RecordBytes {
			r.Deferred++
		} else if d.raw > o.BatchBytes-r.Read {
			break
		} else if err = processDiagnostic(ctx, tx, d, o, r); err != nil {
			return nil, err
		}
		r.Scanned++
		r.Next = d.id
	}
	r.More = len(page) > r.Scanned
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}

// processDiagnostic 读取当前单行文本，校验原长、摘要与完整回读，再选取能覆盖 64 B 行开销的编码。
// 输入元数据来自同一事务。出错使整个批次回滚，正常请求覆盖过的版本不能通过旧清单。
func processDiagnostic(ctx context.Context, tx *sql.Tx, d diagnosticRow, o DiagnosticOptions, r *DiagnosticReport) error {
	const where = " FROM request_logs l JOIN usage_events u USING(request_id) WHERE u.id=$1"
	var before, requestID string
	if err := tx.QueryRowContext(ctx, "SELECT l.upstream_response,l.request_id"+where, d.id).Scan(&before, &requestID); err != nil {
		return err
	}
	r.Read += d.raw
	next := before
	if r.Mode == "restore" {
		var large bool
		var err error
		next, large, err = logframe.Read(ctx, before, d.raw)
		if err != nil || large || int64(len(next)) != d.raw {
			return OperatorError("diagnostic restoration verification failed")
		}
	} else {
		if int64(len(before)) != d.raw {
			return OperatorError("diagnostic changed during scan")
		}
		if o.Preimages != nil {
			key := sha256.Sum256([]byte(requestID))
			if hash, ok := o.Preimages.hashes[hex.EncodeToString(key[:])]; !ok || sha256.Sum256([]byte(before)) != hash {
				r.Changed++
				return nil
			}
		}
		if !strings.HasPrefix(strings.TrimSpace(before), "{") || !json.Valid([]byte(before)) {
			r.Kept++
			return nil
		}
		var err error
		next, err = logframe.Encode(before)
		if err != nil {
			return err
		}
		plain, over, err := logframe.Read(ctx, next, d.raw)
		if err != nil || over || plain != before {
			return OperatorError("diagnostic encoding verification failed")
		}
		if int64(len(next))+64 >= d.stored {
			r.Kept++
			return nil
		}
	}
	stored := int64(len(next)) + 64
	if r.Mode != "plan" {
		if _, err := tx.ExecContext(ctx, "UPDATE request_logs SET upstream_response=$2 WHERE request_id=$1", requestID, next); err != nil {
			return err
		}
		var actual string
		if err := tx.QueryRowContext(ctx, "SELECT l.upstream_response,pg_column_size(l.upstream_response)"+where, d.id).Scan(&actual, &stored); err != nil {
			return err
		}
		if actual != next {
			return OperatorError("diagnostic database read-back failed")
		}
	}
	if r.Mode == "restore" {
		r.Restored++
		return nil
	}
	r.Selected++
	r.StoredBefore += d.stored
	r.StoredAfter += stored
	return nil
}
