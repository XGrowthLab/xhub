package storageaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const historyPrefix = "session_route:v1:history:"
const legacyKeyPattern = "^" + historyPrefix + "[0-9a-f]{64}$"

var digestPattern = regexp.MustCompile("^[0-9a-f]{64}$")

// PlanOptions 固定旧历史的创建上界、单批条数及原文字节预算。Cursor 只含本次截止时间和网关生成的摘要。
type PlanOptions struct {
	Before       time.Time
	Cursor       string
	Rows         int
	BatchBytes   int64
	RecordBytes  int64
	ReserveBytes int64
}

// Plan 是一个完整只读批次的估算。只有 Selected 的新编码和每行预留开销计入估算收益。
// Deferred 表示超大行，Covered 表示同键已有新格式；二者均未读取旧正文。
type Plan struct {
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
	if o.Before.IsZero() || o.Before.Nanosecond() != 0 || o.Rows < 1 || o.Rows > 1000 || o.BatchBytes < 1 || o.BatchBytes > 256<<20 || o.RecordBytes < 1 || o.RecordBytes > 64<<20 || o.RecordBytes > o.BatchBytes || o.ReserveBytes < 0 || o.ReserveBytes > 1<<20 {
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

// Estimate 读取已存在的 PostgreSQL schema，对当前有界批次逐条试压。前一行耗尽预算时把下一行留给后续批次。
// 采样与正文在同一只读快照内；只有完整成功的批次才返回可推进的游标，失败后用同一组参数重试。
func Estimate(ctx context.Context, db *sql.DB, o PlanOptions) (*Plan, error) {
	after, err := o.startKey()
	if err != nil {
		return nil, err
	}
	tx, err := beginRead(ctx, db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var now time.Time
	if err = tx.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&now); err != nil {
		return nil, err
	}
	if o.Before.After(now) {
		return nil, errors.New("history cutoff is in the future")
	}
	cutoff := o.Before.UTC().Format(time.RFC3339)
	// 先取 TOAST 长度及索引状态。只有受限范围内且可能受益的正文才会从数据库传到编码器。
	rows, err := tx.QueryContext(ctx, `SELECT k.id,octet_length(k.body),pg_column_size(k.body),
      EXISTS(SELECT 1 FROM conversation_history_manifests m WHERE m.id=k.id)
      FROM kv k WHERE k.kind='conversation_state' AND k.id>$1 AND k.id ~ $2
       AND k.created_at ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$'
       AND k.created_at<$3 AND k.body IS NOT NULL ORDER BY k.id LIMIT $4`, after, legacyKeyPattern, cutoff, o.Rows+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type meta struct {
		key         string
		raw, stored int64
		covered     bool
	}
	batch := make([]meta, 0, o.Rows+1)
	for rows.Next() {
		var m meta
		if err = rows.Scan(&m.key, &m.raw, &m.stored, &m.covered); err != nil {
			return nil, err
		}
		batch = append(batch, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(uint64(max(o.RecordBytes, 1<<20))))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	p := &Plan{Before: o.Before.UTC(), Codec: "zstd", Reserve: o.ReserveBytes, Next: o.Cursor}
	for _, m := range batch {
		if p.Scanned == o.Rows {
			break
		}
		switch {
		case m.covered:
			p.Covered++
		case m.raw > o.RecordBytes:
			p.Deferred++
			p.DeferredRaw += m.raw
			p.DeferredStored += m.stored
		case m.stored <= o.ReserveBytes:
			p.Kept++
		default:
			if m.raw > o.BatchBytes-p.Read {
				p.More = true
				return p, tx.Commit()
			}
			var raw []byte
			if err = tx.QueryRowContext(ctx, `SELECT body FROM kv WHERE kind='conversation_state' AND id=$1`, m.key).Scan(&raw); err != nil {
				return nil, err
			}
			frame := enc.EncodeAll(raw, nil)
			restored, err := dec.DecodeAll(frame, nil)
			if err != nil || int64(len(raw)) != m.raw || len(restored) != len(raw) || sha256.Sum256(restored) != sha256.Sum256(raw) {
				return nil, errors.New("history compression verification failed")
			}
			p.Read += m.raw
			candidate := int64(len(frame)) + o.ReserveBytes
			if candidate < m.stored {
				p.Selected++
				p.Frame += int64(len(frame))
				p.Candidate += candidate
				p.Savings += m.stored - candidate
			} else {
				p.Kept++
			}
		}
		p.Scanned++
		p.Raw += m.raw
		p.Stored += m.stored
		p.Next = cutoff + "/" + strings.TrimPrefix(m.key, historyPrefix)
	}
	p.More = len(batch) > p.Scanned
	return p, tx.Commit()
}
