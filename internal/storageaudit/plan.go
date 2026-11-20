package storageaudit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sunqirui1987/xhub/internal/historypack"
)

const historyPrefix = historypack.Prefix
const legacyKeyPattern = "^" + historyPrefix + "[0-9a-f]{64}$"

var digestPattern = regexp.MustCompile("^[0-9a-f]{64}$")

// PlanOptions 固定旧历史的创建上界、单批条数及原文字节预算。MinRawBytes 和 OnlyDigest 限定大小区间或单键。写游标另绑定操作、数据库、筛选和备份清单。
type PlanOptions struct {
	Preimages    *Preimages
	MinRawBytes  int64
	OnlyDigest   string
	Before       time.Time
	Cursor       string
	Rows         int
	BatchBytes   int64
	RecordBytes  int64
	ReserveBytes int64
}

// Plan 返回一个已完成的试算、转换或还原批次。Selected 是有收益的候选，在 pack 模式下是已提交的新帧。
// Deferred 表示超大行，Covered 表示同键已有新格式；二者均未读取旧正文。AlreadyPacked、Malformed、Unbacked 与 Changed 说明跳过原因（含已被网关改写的行），Restored 是已还原数。
type Plan struct {
	Unbacked       int       `json:"not_backed_up_or_changed"`
	Mode           string    `json:"mode"`
	AlreadyPacked  int       `json:"already_packed"`
	Malformed      int       `json:"malformed"`
	Changed        int       `json:"changed_during_scan"`
	Restored       int       `json:"restored"`
	Before         time.Time `json:"created_before"`
	Codec          string    `json:"codec"`
	Reserve        int64     `json:"reserve_per_selected_row"`
	Scanned        int       `json:"scanned"`
	Selected       int       `json:"selected"`
	Kept           int       `json:"kept"`
	Deferred       int       `json:"oversized"`
	Covered        int       `json:"already_covered"`
	Stored         int64     `json:"scanned_stored_field_bytes"`
	Raw            int64     `json:"scanned_raw_bytes"`
	Read           int64     `json:"raw_bytes_read"`
	DeferredRaw    int64     `json:"oversized_raw_bytes"`
	DeferredStored int64     `json:"oversized_stored_field_bytes"`
	Frame          int64     `json:"selected_frame_bytes"`
	Candidate      int64     `json:"estimated_selected_new_bytes"`
	Savings        int64     `json:"estimated_selected_savings_bytes"`
	Next           string    `json:"next_cursor"`
	More           bool      `json:"has_more"`
}

// startKey 校验所有维护上限并核对游标的截止时间。返回绑定查询用的内部键，不向报告回显任意原始 ID。
func (o PlanOptions) startKey() (string, error) {
	if o.MinRawBytes < 0 || o.MinRawBytes > o.RecordBytes || (o.OnlyDigest != "" && !digestPattern.MatchString(o.OnlyDigest)) || o.Before.IsZero() || o.Before.Nanosecond() != 0 || o.Rows < 1 || o.Rows > 1000 || o.BatchBytes < 1 || o.BatchBytes > 256<<20 || o.RecordBytes < 1 || o.RecordBytes > historypack.MaxRawBytes || o.RecordBytes > o.BatchBytes || o.ReserveBytes < 0 || o.ReserveBytes > 1<<20 {
		return "", errors.New("invalid planning bounds")
	}
	if o.Cursor == "" {
		return historyPrefix, nil
	}
	cutoff, digest, ok := strings.Cut(o.Cursor, "/")
	if !ok || cutoff != o.Before.UTC().Format(time.RFC3339) || !digestPattern.MatchString(digest) {
		return "", errors.New("cursor does not match cutoff or history key format")
	}
	return historyPrefix + digest, nil
}

type candidate struct {
	key             string
	raw, stored     int64
	covered, packed bool
}

// hasPackedColumns 判断当前 schema 是否已完成可空字段升级。只读采样兼容旧库，部分字段表示升级异常。
func hasPackedColumns(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pg_attribute WHERE attrelid='kv'::regclass AND NOT attisdropped
      AND attname IN ('history_frame','history_raw_size','history_sha256')`).Scan(&count)
	if err == nil && count != 0 && count != 3 {
		return false, errors.New("incomplete packed history schema")
	}
	return count == 3, err
}

// candidateSelect 只取小型元数据。适配旧库、压缩库及反向还原，限定来源为已知的 UTC 历史，不把非规范旧键放入游标。
func candidateSelect(columns bool) string {
	fields := "octet_length(k.body),pg_column_size(k.body),false"
	if columns {
		fields = `CASE WHEN k.history_frame IS NULL THEN octet_length(k.body) ELSE k.history_raw_size END,
      CASE WHEN k.history_frame IS NULL THEN pg_column_size(k.body) ELSE pg_column_size(k.history_frame) END,k.history_frame IS NOT NULL`
	}
	return "SELECT k.id," + fields + `, EXISTS(SELECT 1 FROM conversation_history_manifests m WHERE m.id=k.id) FROM kv k
      WHERE k.kind='conversation_state' AND k.id ~ $2 AND k.created_at ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$'
      AND k.created_at<$3 AND k.body IS NOT NULL `
}

// Estimate 在只读一致快照中逐条试压。仅返回完整批次进度，错误后重用上次成功的游标。
func Estimate(ctx context.Context, db *sql.DB, o PlanOptions) (*Plan, error) {
	return processBatch(ctx, db, o, "plan")
}

// Convert 在单个事务中执行选定的 pack 或 restore 批次。只有备份检查及所有节点兼容读取后，操作员才从独立命令调用。
// 以与网关相同的响应键锁串行化，提交之前从数据库逐条读回核验，失败回滚整批。
func Convert(ctx context.Context, db *sql.DB, o PlanOptions, mode string) (*Plan, error) {
	if mode == "restore" && o.Preimages != nil {
		return nil, errors.New("preimage verification applies only to packing")
	}
	if mode != "pack" && mode != "restore" {
		return nil, errors.New("expected pack or restore")
	}
	return processBatch(ctx, db, o, mode)
}

// processBatch 共享预估与执行的预算、挑选及游标语义；写操作在取键锁后的新语句中重新核对行和清单。
// 相邻记录超过本批余量时留给下一批。已切换或被实时覆盖的行可以安全重跑。
func processBatch(ctx context.Context, db *sql.DB, o PlanOptions, mode string) (*Plan, error) {
	cursor := o.Cursor
	if mode != "plan" && cursor != "" {
		parts := strings.SplitN(cursor, "/", 3)
		if len(parts) == 3 {
			o.Cursor = parts[2]
		}
		if len(parts) != 3 || parts[0] != mode {
			return nil, errors.New("cursor operation mismatch")
		}
	}
	after, err := o.startKey()
	if err != nil {
		return nil, err
	}
	opts := sql.TxOptions{ReadOnly: mode == "plan", Isolation: sql.LevelReadCommitted}
	if opts.ReadOnly {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := boundedTx(ctx, db, opts)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var now time.Time
	var dbScope string
	if err = tx.QueryRowContext(ctx, `SELECT transaction_timestamp(), current_database()||'/'||current_schema()||'/'||d.oid::text||'/'||current_schema()::regnamespace::oid::text
	   FROM pg_database d WHERE datname=current_database()`).Scan(&now, &dbScope); err != nil {
		return nil, err
	}
	if o.Before.After(now) {
		return nil, errors.New("history cutoff is in the future")
	}
	cols, err := hasPackedColumns(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !cols && mode != "plan" {
		return nil, errors.New("packed history reader schema required")
	}
	cutoff := o.Before.UTC().Format(time.RFC3339)
	resumePrefix := ""
	if mode != "plan" {
		identity := fmt.Sprintf("%s/%s/%d/%d/%d", dbScope, o.OnlyDigest, o.MinRawBytes, o.RecordBytes, o.ReserveBytes)
		if o.Preimages != nil {
			identity += "/" + o.Preimages.id
		}
		sum := sha256.Sum256([]byte(identity))
		resumePrefix = fmt.Sprintf("%s/%x/", mode, sum[:])
		if cursor != "" && !strings.HasPrefix(cursor, resumePrefix) {
			return nil, errors.New("cursor database or selection bounds mismatch")
		}
	}
	selectRow := candidateSelect(cols)
	condition := ""
	if mode == "restore" {
		condition = " AND k.history_frame IS NOT NULL "
	}
	params := []any{after, legacyKeyPattern, cutoff, o.Rows + 1}
	if o.MinRawBytes > 0 {
		sizeExpr := "octet_length(k.body)"
		if cols {
			sizeExpr = "coalesce(k.history_raw_size,octet_length(k.body))"
		}
		condition += " AND " + sizeExpr + ">=$5 "
		params = append(params, o.MinRawBytes)
	}
	if o.OnlyDigest != "" {
		condition += fmt.Sprintf(" AND k.id=$%d ", len(params)+1)
		params = append(params, historyPrefix+o.OnlyDigest)
	}
	rows, err := tx.QueryContext(ctx, selectRow+condition+" AND k.id>$1 ORDER BY k.id LIMIT $4", params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batch []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.key, &c.raw, &c.stored, &c.packed, &c.covered); err != nil {
			return nil, err
		}
		batch = append(batch, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	p := &Plan{Before: o.Before.UTC(), Codec: "zstd", Reserve: o.ReserveBytes, Next: cursor, Mode: mode}
	for _, c := range batch {
		if p.Scanned == o.Rows {
			break
		}
		var expected *[sha256.Size]byte
		if o.Preimages != nil {
			if h, ok := o.Preimages.hashes[strings.TrimPrefix(c.key, historyPrefix)]; ok {
				expected = &h
			}
		}
		unbacked := o.Preimages != nil && expected == nil
		changed := false
		if !unbacked && mode != "plan" && !c.covered && c.packed == (mode == "restore") {
			if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", c.key); err != nil {
				return nil, err
			}
			err = tx.QueryRowContext(ctx, selectRow+" AND k.id=$1 FOR UPDATE OF k", c.key, legacyKeyPattern, cutoff).Scan(&c.key, &c.raw, &c.stored, &c.packed, &c.covered)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			changed = errors.Is(err, sql.ErrNoRows) || c.raw < o.MinRawBytes
		}
		switch {
		case changed || (mode == "restore" && !c.packed):
			p.Changed++
		case c.covered:
			p.Covered++
		case c.packed && mode != "restore":
			p.AlreadyPacked++
		case unbacked:
			p.Unbacked++
		case c.raw > o.RecordBytes:
			p.Deferred++
			p.DeferredRaw += c.raw
			p.DeferredStored += c.stored
		case mode != "restore" && (c.raw == 0 || c.stored <= o.ReserveBytes):
			p.Kept++
		default:
			if c.raw > o.BatchBytes-p.Read {
				p.More = true
			}
			if !p.More {
				if err = processRecord(ctx, tx, c, p, mode, expected); err != nil {
					return nil, err
				}
			}
		}
		if p.More {
			break
		}
		p.Scanned++
		p.Raw += c.raw
		p.Stored += c.stored
		p.Next = cutoff + "/" + strings.TrimPrefix(c.key, historyPrefix)
		if mode != "plan" {
			p.Next = resumePrefix + p.Next
		}
	}
	p.More = len(batch) > p.Scanned
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

// processRecord 只读取已获准的单行，校验内容后计量或写入。试算和实际写入都先验原文为历史数组，避免更改损坏数据；
// 对入库后的帧或回退正文再次读取并核对，任何触发器、编码或存储故障都使调用方回滚。
func processRecord(ctx context.Context, tx *sql.Tx, c candidate, p *Plan, mode string, expected *[sha256.Size]byte) error {
	p.Read += c.raw
	const where = " WHERE kind='conversation_state' AND id=$1"
	if mode == "restore" {
		if c.raw <= 0 || c.stored > c.raw+128 {
			return errors.New("invalid packed history bounds")
		}
		var marker string
		var frame, digest []byte
		if err := tx.QueryRowContext(ctx, "SELECT body,history_frame,history_sha256 FROM kv"+where+" AND octet_length(history_frame)::bigint<=history_raw_size::bigint+128", c.key).Scan(&marker, &frame, &digest); err != nil {
			return err
		}
		raw, err := historypack.Decode(ctx, marker, frame, c.raw, digest)
		if err != nil {
			return err
		}
		var restored []byte
		if _, err = tx.ExecContext(ctx, "UPDATE kv SET body=$2,history_frame=NULL,history_raw_size=NULL,history_sha256=NULL"+where, c.key, string(raw)); err != nil {
			return err
		}
		// 独立语句可见 AFTER 触发器的结果，也确认旧格式字段已完整恢复。
		if err = tx.QueryRowContext(ctx, "SELECT body FROM kv"+where+" AND history_frame IS NULL AND history_raw_size IS NULL AND history_sha256 IS NULL", c.key).Scan(&restored); err != nil {
			return err
		}
		if !bytes.Equal(restored, raw) {
			return errors.New("restored row verification failed")
		}
		p.Restored++
		return nil
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM kv"+where, c.key).Scan(&raw); err != nil {
		return err
	}
	if int64(len(raw)) != c.raw {
		return errors.New("history size changed during batch")
	}
	if !json.Valid(raw) || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		p.Malformed++
		return nil
	}
	frame, digest, err := historypack.Encode(raw)
	if err != nil {
		return err
	}
	// 备份之后即使同键在网关被重用，也只有锁内原文与已恢复验证的摘要相同才允许维护写入。
	if expected != nil && digest != *expected {
		p.Unbacked++
		return nil
	}
	// 预留至少足够放版本、摘要、原长和 varlena 头；默认的 1 KiB 还覆盖额外的行和 TOAST 开销。
	candidateSize := int64(len(frame)) + max(p.Reserve, int64(64))
	selected := candidateSize < c.stored
	marker, size, check := historypack.Marker, c.raw, digest[:]
	if mode == "pack" && selected {
		if _, err = tx.ExecContext(ctx, "UPDATE kv SET body=$2,history_frame=$3,history_raw_size=$4,history_sha256=$5"+where, c.key, marker, frame, size, check); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, "SELECT body,history_frame,history_raw_size,history_sha256 FROM kv"+where, c.key).Scan(&marker, &frame, &size, &check); err != nil {
			return err
		}
	}
	restored, err := historypack.Decode(ctx, marker, frame, size, check)
	if err != nil || !bytes.Equal(restored, raw) {
		return errors.New("history compression verification failed")
	}
	if !selected {
		p.Kept++
		return nil
	}
	p.Selected++
	p.Frame += int64(len(frame))
	p.Candidate += candidateSize
	p.Savings += c.stored - candidateSize
	return nil
}
