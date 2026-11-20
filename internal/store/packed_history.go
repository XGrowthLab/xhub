package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/sunqirui1987/xhub/internal/historypack"
)

// packedRow 接受普通连接、事务和 RETURNING 的单行扫描结果，供持久会话读取复用同一套校验。
type packedRow interface{ Scan(...any) error }

// readConversationRow 从同一次 SQL 快照还原普通正文或带版本的压缩旧历史。缺失按既有接口返回空值；
// 身份已由原调用方编码进 key，其他种类即使正好保存相同文本也不会被解压。
func readConversationRow(ctx context.Context, row packedRow, key string) (string, error) {
	var body string
	var frame, digest []byte
	var size sql.NullInt64
	err := row.Scan(&body, &frame, &size, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(key, historypack.Prefix) || (frame == nil && !size.Valid && digest == nil && !strings.HasPrefix(body, "xhub:packed-history:")) {
		return body, nil
	}
	raw, err := historypack.Decode(ctx, body, frame, size.Int64, digest)
	return string(raw), err
}

// SQL 先检查 TOAST 元数据再取帧，损坏的超长字段不会在应用校验之前占用请求内存。
const conversationColumns = `body,CASE WHEN history_raw_size BETWEEN 1 AND 67108864 AND octet_length(history_frame)::bigint<=history_raw_size::bigint+128
 THEN history_frame END,history_raw_size,CASE WHEN octet_length(history_sha256)=32 THEN history_sha256 END`
const selectConversation = "SELECT " + conversationColumns + " FROM kv WHERE kind=$1 AND id=$2"

// compareHistoryValue 兼容极少数直接比较历史正文的旧调用。按行锁读取并验证当前帧，比较原文后原子恢复为普通格式。
// 未命中或内容不等返回实际赢家，其他归属类型继续使用原有的单语句比较路径。
func (s *Store) compareHistoryValue(ctx context.Context, key, old, value string) (string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = lockHistoryKey(ctx, tx, key); err != nil {
		return "", err
	}
	actual, err := readConversationRow(ctx, tx.QueryRowContext(ctx, selectConversation+" FOR UPDATE", conversationKind, key), key)
	if err != nil {
		return "", err
	}
	if actual == old {
		if _, err = tx.ExecContext(ctx, `UPDATE kv SET body=$3,history_frame=NULL,history_raw_size=NULL,history_sha256=NULL WHERE kind=$1 AND id=$2`, conversationKind, key, value); err != nil {
			return "", err
		}
		actual = value
	}
	return actual, tx.Commit()
}
