// Package storageaudit 供运维工具使用，只在已有的 PostgreSQL 上读取容量聚合和有界压缩样本。
package storageaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Route 是按入口、上游、显式透传和流式状态归类的现存日志字段；空流式状态表示尚未关联用量。
type Route struct {
	Day         string `json:"day_utc"`
	Inbound     string `json:"inbound"`
	Outbound    string `json:"outbound"`
	Bypass      bool   `json:"explicit_bypass"`
	Streamed    *bool  `json:"streamed"`
	Calls       int64  `json:"calls"`
	Diagnostics int64  `json:"diagnostic_field_bytes"`
	Embedded    int64  `json:"embedded_body_field_bytes"`
	Captured    int64  `json:"capture_field_bytes"`
	Captures    int64  `json:"captures"`
}

// Cohort 汇总现存会话的创建日期与占用；匿名旧键无法细分到具体调用的上游或流式状态。
type Cohort struct {
	Day     string `json:"creation_bucket_utc"`
	Kind    string `json:"kind"`
	Records int64  `json:"records"`
	Bytes   int64  `json:"stored_field_bytes"`
}

// Snapshot 同时输出当前物理大小和按请求日期归属的字段大小；两种数字各自独立，不能相加。
type Snapshot struct {
	Version    int              `json:"version"`
	Source     string           `json:"source_fingerprint"`
	At         time.Time        `json:"observed_at"`
	Since      time.Time        `json:"window_start"`
	Routes     []Route          `json:"routes"`
	History    []Cohort         `json:"conversation_state"`
	Physical   map[string]int64 `json:"physical_bytes"`
	Difference map[string]int64 `json:"physical_delta_bytes,omitempty"`
	PreviousAt *time.Time       `json:"previous_observed_at,omitempty"`
}

//go:embed protocol.sql
var protocolSQL string

//go:embed routes.sql
var routeSQL string

// storedFields 构建预定义列的物理字段大小表达式。空串和缺失行均计零，长度查询不拉取 TOAST 正文。
// 调用点只提供源码内的固定列名，不接受外部 SQL 标识符。
func storedFields(fields ...string) string {
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		terms = append(terms, "(CASE WHEN octet_length("+f+")>0 THEN pg_column_size("+f+")::bigint ELSE 0 END)")
	}
	return strings.Join(terms, " + ")
}

// routeQuery 将同一份封闭协议分类用于两个路径。仅替换固定的 SQL 路径标记，保留复杂资源的固定标签。
func routeQuery() string {
	return strings.NewReplacer("{{inbound}}", strings.ReplaceAll(protocolSQL, "{{path}}", "inbound"),
		"{{outbound}}", strings.ReplaceAll(protocolSQL, "{{path}}", "outbound"),
		"{{diagnostics}}", storedFields("l.error", "l.upstream_response"),
		"{{embedded}}", storedFields("l.request_body", "l.response_body", "l.proxy_request"),
		"{{capture}}", storedFields("c.request_document", "c.response_document")).Replace(routeSQL)
}

// beginRead 建立有锁等待和单条语句上限的一致只读快照；调用方负责关闭事务，ctx 控制整次采集寿命。
func beginRead(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='15s'; SET LOCAL lock_timeout='750ms'; SET LOCAL max_parallel_workers_per_gather=0`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// Capture 按指定部署标记和最近 days 个 UTC 日读取当前 schema。只返回脱敏分组和物理快照，错误时整份丢弃。
// 包括当天至采样时刻；读不到 WAL 目录的普通只读角色仍可取得其余指标。
func Capture(ctx context.Context, db *sql.DB, deployment string, days int) (*Snapshot, error) {
	if strings.TrimSpace(deployment) == "" || days < 1 || days > 31 {
		return nil, errors.New("invalid snapshot window or deployment")
	}
	tx, err := beginRead(ctx, db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	s := &Snapshot{Version: 1, Routes: []Route{}, History: []Cohort{}, Physical: map[string]int64{}}
	var identity string
	var databaseBytes int64
	var mayReadWAL bool
	err = tx.QueryRowContext(ctx, `SELECT transaction_timestamp(), d.oid::text||'/'||n.oid::text, pg_database_size(d.oid),
      has_function_privilege('pg_catalog.pg_ls_waldir()', 'EXECUTE')
      FROM pg_database d JOIN pg_namespace n ON n.nspname=current_schema() WHERE d.datname=current_database()`).Scan(&s.At, &identity, &databaseBytes, &mayReadWAL)
	if err != nil {
		return nil, err
	}
	s.At = s.At.UTC()
	s.Since = s.At.Truncate(24*time.Hour).AddDate(0, 0, 1-days)
	s.Source = fmt.Sprintf("%x", sha256.Sum256([]byte(deployment+"\x00"+identity)))
	s.Physical["database"] = databaseBytes
	if mayReadWAL {
		// 只有角色具有 EXECUTE 时才提交含 pg_ls_waldir 的语句，普通只读角色无需获得集群监控权限。
		var wal int64
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(size),0) FROM pg_catalog.pg_ls_waldir()").Scan(&wal); err != nil {
			return nil, err
		}
		s.Physical["cluster.wal_files"] = wal
	}
	// 对本 schema 的表逐个计主表、TOAST（含自己的索引）及主表索引；其余应用表匿名合并。
	rows, err := tx.QueryContext(ctx, `SELECT bucket, sum(table_bytes-toast_bytes)::bigint, sum(toast_bytes)::bigint, sum(index_bytes)::bigint FROM (
      SELECT CASE WHEN c.relname IN ('kv','usage_events','request_logs','request_log_captures','conversation_history_blocks','conversation_history_manifests')
        THEN c.relname ELSE 'other_tables' END bucket, pg_table_size(c.oid) table_bytes,
        coalesce(pg_total_relation_size(nullif(c.reltoastrelid,0)),0) toast_bytes, pg_indexes_size(c.oid) index_bytes
      FROM pg_class c WHERE c.relnamespace=current_schema()::regnamespace AND c.relkind IN ('r','m')
    ) sizes GROUP BY bucket`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var heap, toast, index int64
		if err = rows.Scan(&kind, &heap, &toast, &index); err != nil {
			return nil, err
		}
		s.Physical[kind+".heap"] = heap
		s.Physical[kind+".toast"] = toast
		s.Physical[kind+".indexes"] = index
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	routes, err := tx.QueryContext(ctx, routeQuery(), s.Since, s.At)
	if err != nil {
		return nil, err
	}
	defer routes.Close()
	for routes.Next() {
		var r Route
		if err = routes.Scan(&r.Day, &r.Inbound, &r.Outbound, &r.Bypass, &r.Streamed, &r.Calls, &r.Diagnostics, &r.Embedded, &r.Captured, &r.Captures); err != nil {
			return nil, err
		}
		s.Routes = append(s.Routes, r)
	}
	if err = routes.Err(); err != nil {
		return nil, err
	}
	histories, err := tx.QueryContext(ctx, `SELECT
      CASE WHEN created_at IS NULL OR created_at !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' THEN 'unknown'
        WHEN created_at<$1 THEN 'before_window' ELSE left(created_at,10) END AS day,
      CASE WHEN starts_with(id,'session_route:v1:history:') AND id !~ $2 THEN 'unrecognized_legacy_key'
        WHEN split_part(id,':',3) IN ('response','owner','messages','history','preference') AND starts_with(id,'session_route:v1:') THEN split_part(id,':',3)
        ELSE 'other_conversation' END AS bucket,
      count(*), coalesce(sum(pg_column_size(body)::bigint),0) FROM kv WHERE kind='conversation_state' GROUP BY 1,2
      UNION ALL SELECT 'undated','compact_blocks',count(*),coalesce(sum(pg_column_size(payload)::bigint),0) FROM conversation_history_blocks
      UNION ALL SELECT 'undated','compact_manifests',count(*),coalesce(sum(pg_column_size(digests)::bigint),0) FROM conversation_history_manifests
      ORDER BY 1,2`, s.Since.Format(time.RFC3339), legacyKeyPattern)
	if err != nil {
		return nil, err
	}
	defer histories.Close()
	for histories.Next() {
		var c Cohort
		if err = histories.Scan(&c.Day, &c.Kind, &c.Records, &c.Bytes); err != nil {
			return nil, err
		}
		s.History = append(s.History, c)
	}
	if err = histories.Err(); err != nil {
		return nil, err
	}
	return s, tx.Commit()
}

// Compare 在当前快照记录同源、同版本且较早的基线及有符号物理差值；可选指标须两次都有才计算。
func (s *Snapshot) Compare(previous *Snapshot) error {
	if previous == nil || s.Source == "" || s.Source != previous.Source || previous.Version != s.Version || previous.At.IsZero() || !s.At.After(previous.At) || len(previous.Physical) == 0 {
		return errors.New("incompatible baseline source, version or timestamp")
	}
	s.Difference = make(map[string]int64)
	for name, now := range s.Physical {
		if before, ok := previous.Physical[name]; ok {
			s.Difference[name] = now - before
		}
	}
	s.PreviousAt = &previous.At
	return nil
}
