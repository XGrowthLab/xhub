package store

import (
	"context"
	"database/sql"
)

// MarketTransaction 在配置库事务中串行执行市场同步和价格确认。
// ctx 控制等待与超时，apply 接收事务并返回错误；提交成功后清理模型与配置缓存。
// 供模型管理使用；失败全部回滚，锁限于当前 schema，绝不跨环境阻塞。
func (s *Store) MarketTransaction(ctx context.Context, apply func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext(current_schema()), hashtext('market-price-sync'))"); err != nil {
		return err
	}
	if err = apply(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.bust(new(proxyModelRow), new(configRow))
	return nil
}
