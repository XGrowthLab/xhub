package requestlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sunqirui1987/xhub/internal/logframe"
)

// LegacyRetentionBatch 是一次有界扫描的统计。Next 是最后检查的用量事件序号，
// Cleared 只计算已经提交的修改，UnchangedDiagnostics 是需要另行核对的诊断行数。
type LegacyRetentionBatch struct {
	Scanned              int   `json:"scanned"`
	Eligible             int   `json:"eligible"`
	Cleared              int   `json:"cleared"`
	LegacyBodyBytes      int64 `json:"legacy_body_bytes"`
	UnchangedDiagnostics int   `json:"unchanged_diagnostics"`
	Next                 int64 `json:"next"`
	Done                 bool  `json:"done"`
}

// ProcessLegacyLogBatch 用用量事件主键游标扫描最多 limit 条记录。cutoff 是包含在清理范围内的时间上界，
// after 是上一批 Next，execute 决定是否写入；false 使用数据库只读事务。供离线运维命令调用。
// 每次提交只清理遗留正文和旧错误副本。失败不返回可前移的游标，适合按同一批重试。
func ProcessLegacyLogBatch(ctx context.Context, db *sql.DB, cutoff time.Time, after int64, limit int, execute bool) (LegacyRetentionBatch, error) {
	if cutoff.IsZero() || after < 0 || limit < 1 || limit > 500 {
		return LegacyRetentionBatch{}, fmt.Errorf("invalid retention batch boundary")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !execute})
	if err != nil {
		return LegacyRetentionBatch{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '1000ms'"); err != nil {
		return LegacyRetentionBatch{}, err
	}
	// 先由主键限定检查范围，再按请求时间查正文。即使旧正文稀疏，每批也不扫描整张日志或账单表。
	// 锁繁忙时回滚整批而不跳过行，避免游标前移导致未清理记录被遗漏。
	query := `WITH page AS MATERIALIZED (
	SELECT id, request_id, ts FROM usage_events WHERE id > $1 ORDER BY id LIMIT $2
)
SELECT p.id, COALESCE(legacy.request_id, ''), COALESCE(legacy.body_bytes, 0),
       COALESCE(legacy.error_summary_source, ''), COALESCE(legacy.diagnostic, ''),
       COALESCE(legacy.diagnostic_too_large, false), COALESCE(legacy.has_legacy_body, false)
FROM page p
LEFT JOIN LATERAL (
	SELECT request_id,
	       octet_length(request_body)::bigint + octet_length(response_body) + octet_length(proxy_request) AS body_bytes,
	       CASE WHEN NOT $4 THEN '' WHEN octet_length(error) <= 65536 THEN error
	            ELSE 'upstream error details exceed the summary limit' END AS error_summary_source,
	       CASE WHEN octet_length(upstream_response) <= 65536 THEN upstream_response ELSE '' END AS diagnostic,
	       octet_length(upstream_response) > 65536 AS diagnostic_too_large,
	       (request_body <> '' OR response_body <> '' OR proxy_request <> '' OR error <> '') AS has_legacy_body
	FROM request_logs WHERE request_id = p.request_id AND p.ts <= $3 AND body_capture_status = ''
	  AND (request_body <> '' OR response_body <> '' OR proxy_request <> '' OR error <> ''
	       OR CASE WHEN octet_length(upstream_response) > 65536 THEN true
	               ELSE starts_with(upstream_response,$5) OR strpos(upstream_response, '"error":') > 0 END)
	LIMIT 1`
	if execute {
		query += " FOR UPDATE"
	}
	query += ") legacy ON true ORDER BY p.id"
	rows, err := tx.QueryContext(ctx, query, after, limit, cutoff, execute, logframe.Family)
	if err != nil {
		return LegacyRetentionBatch{}, err
	}
	defer rows.Close()
	type update struct {
		id, summary, diagnostic string
		replaceDiagnostic       bool
		markExpired             bool
	}
	var updates []update
	result := LegacyRetentionBatch{Next: after}
	for rows.Next() {
		var id int64
		var bodyBytes int64
		var requestID, previousError, diagnostic string
		var diagnosticTooLarge, hasLegacyBody bool
		if err := rows.Scan(&id, &requestID, &bodyBytes, &previousError, &diagnostic, &diagnosticTooLarge, &hasLegacyBody); err != nil {
			return LegacyRetentionBatch{}, err
		}
		result.Scanned++
		result.Next = id
		if requestID == "" {
			continue
		}
		if !diagnosticTooLarge {
			// 编码后的短字段也可能对应超大的旧诊断。使用原长和此前的 64 KiB 阈值决定是否参与错误摘要清理。
			diagnostic, diagnosticTooLarge, err = logframe.Read(ctx, diagnostic, 65536)
			if err != nil {
				return LegacyRetentionBatch{}, err
			}
		}
		cleanDiagnostic, replace, recognized := summarizeLegacyDiagnostic(diagnostic)
		if diagnosticTooLarge || !recognized {
			result.UnchangedDiagnostics++
		}
		// 旧传输诊断可能独立保留上游错误。当只有诊断副本时，保持正文状态为空，
		// 且再次检查已规范化的摘要不会重复写入。
		if !hasLegacyBody && !replace {
			continue
		}
		result.Eligible++
		result.LegacyBodyBytes += bodyBytes
		if execute {
			updates = append(updates, update{requestID, FailureSummary(previousError), cleanDiagnostic, replace, hasLegacyBody})
		}
	}
	if err := rows.Err(); err != nil {
		return LegacyRetentionBatch{}, err
	}
	rows.Close()
	for _, row := range updates {
		change, err := tx.ExecContext(ctx, `UPDATE request_logs
SET request_body = '', response_body = '', proxy_request = '', error = $2,
    body_capture_status = CASE WHEN $5 THEN 'expired' ELSE body_capture_status END,
    upstream_response = CASE WHEN $3 THEN $4 ELSE upstream_response END
WHERE request_id = $1 AND body_capture_status = ''`,
			row.id, row.summary, row.replaceDiagnostic, row.diagnostic, row.markExpired)
		if err != nil {
			return LegacyRetentionBatch{}, err
		}
		if rowsAffected, err := change.RowsAffected(); err != nil || rowsAffected != 1 {
			return LegacyRetentionBatch{}, fmt.Errorf("legacy body update did not affect one locked row")
		}
	}
	if err := tx.Commit(); err != nil {
		return LegacyRetentionBatch{}, err
	}
	result.Cleared = len(updates)
	result.Done = result.Scanned < limit
	return result, nil
}

// summarizeLegacyDiagnostic 将旧诊断的上游错误副本转换为已有的有界错误摘要。
// raw 为完整的旧诊断 JSON；返回替代内容、是否替换、是否识别。保留状态、用量和其他诊断属性。
// 用于清理命令。无法识别的旧格式由调用方计数并保留，避免丢失排障信息。
func summarizeLegacyDiagnostic(raw string) (string, bool, bool) {
	if raw == "" {
		return "", false, true
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return "", false, false
	}
	source, exists := fields["error"]
	if !exists || string(source) == "null" {
		return "", false, true
	}
	summary := FailureSummary(string(source))
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(summary), &object); err != nil || object == nil {
		encoded, err := json.Marshal(map[string]any{"error": map[string]string{"message": summary}})
		if err != nil {
			return "", false, false
		}
		summary = string(encoded)
	}
	if summary == string(source) {
		return "", false, true
	}
	fields["error"] = json.RawMessage(summary)
	encoded, err := json.Marshal(fields)
	return string(encoded), err == nil, err == nil
}
