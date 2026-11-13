package iam

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestMarketNotificationsDelivery 使用隔离 PostgreSQL 验证原子投递、失败重试、并发去重及权限撤销。
// 测试注入约束故障、撤销管理员角色并核验个人预算消息仍可读取；testDB 在结束时清理独立 schema。
func TestMarketNotificationsDelivery(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE proxy_config(namespace text NOT NULL,key text NOT NULL,value_json text NOT NULL,PRIMARY KEY(namespace,key))`,
		`INSERT INTO users(id,email,status,role) VALUES('ma','ma@test.example','active','admin'),('mb','mb@test.example','active','admin'),('mu','mu@test.example','active','user')`,
		`INSERT INTO proxy_config VALUES('market_change_events','event1','{"id":"event1","time":"2026-11-13T00:00:00Z","changes":[{"kind":"added","model":"example"}]}')`,
		`ALTER TABLE user_notifications ADD CONSTRAINT reject_mb CHECK (user_id <> 'mb')`,
	} {
		if _, err := db.Engine.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ScanMarketNotifications(ctx); err == nil {
		t.Fatal("写入故障未报告")
	}
	var n int64
	if _, err := db.Engine.SQL(`SELECT count(*) FROM proxy_config WHERE namespace='market_change_events'`).Get(&n); err != nil || n != 1 {
		t.Fatalf("失败事件丢失: %d %v", n, err)
	}
	if _, err := db.Engine.SQL(`SELECT count(*) FROM user_notifications`).Get(&n); err != nil || n != 0 {
		t.Fatalf("投递失败发生部分提交: %d %v", n, err)
	}
	if _, err := db.Engine.Exec(`ALTER TABLE user_notifications DROP CONSTRAINT reject_mb`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`UPDATE notification_market_retry SET next_attempt=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- db.ScanMarketNotifications(ctx) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, total, err := db.ListNotifications(ctx, "ma", 20, 0)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Type != "market_update" {
		t.Fatalf("管理员消息缺失或重复: %d %#v %v", total, rows, err)
	}
	if _, total, err := db.ListNotifications(ctx, "mb", 20, 0); err != nil || total != 1 {
		t.Fatalf("第二管理员未收到消息: %d %v", total, err)
	}
	if _, total, err := db.ListNotifications(ctx, "mu", 20, 0); err != nil || total != 0 {
		t.Fatalf("普通用户收到市场消息: %d %v", total, err)
	}
	if _, err := db.Engine.SQL(`SELECT count(*) FROM proxy_config WHERE namespace='market_change_events'`).Get(&n); err != nil || n != 0 {
		t.Fatalf("已投递事件未回收: %d %v", n, err)
	}
	if _, err := db.Engine.Exec(`INSERT INTO proxy_config VALUES('market_change_events','event1','{"id":"event1","time":"2026-11-13T00:00:00Z","changes":[{"kind":"added","model":"example"}]}')`); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanMarketNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if _, total, err := db.ListNotifications(ctx, "ma", 20, 0); err != nil || total != 1 {
		t.Fatalf("重复事件造成重复消息: %d %v", total, err)
	}
	if _, err := db.Engine.Exec(`UPDATE users SET role='user' WHERE id='ma'`); err != nil {
		t.Fatal(err)
	}
	if _, total, err := db.ListNotifications(ctx, "ma", 20, 0); err != nil || total != 0 {
		t.Fatalf("撤权后仍能看到市场消息: %d %v", total, err)
	}
	if n, err := db.UnreadNotifications(ctx, "ma"); err != nil || n != 0 {
		t.Fatalf("撤权后未读数泄漏: %d %v", n, err)
	}
	if err := db.ReadNotification(ctx, "ma", rows[0].ID); err != ErrNotFound {
		t.Fatalf("撤权后仍可操作消息: %v", err)
	}
}

// TestMarketNotificationSkipsBusyAdministrator 验证管理员正在记账时投递立即退出且不遗漏其余管理员。
// 使用独立 PostgreSQL 行锁模拟记账，锁释放后重试应一次投递给两名管理员；测试 schema 自动清理。
func TestMarketNotificationSkipsBusyAdministrator(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE proxy_config(namespace text NOT NULL,key text NOT NULL,value_json text NOT NULL,PRIMARY KEY(namespace,key))`,
		`INSERT INTO users(id,email,status,role) VALUES('busy','busy@test.example','active','admin'),('free','free@test.example','active','admin')`,
		`INSERT INTO proxy_config VALUES('market_change_events','busy-event','{"id":"busy-event","time":"2026-11-13T00:00:00Z","changes":[{"kind":"added","model":"example"}]}')`,
	} {
		if _, err := db.Engine.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	lock := db.Engine.NewSession()
	defer lock.Close()
	if err := lock.Begin(); err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.Exec(`UPDATE users SET spend=spend WHERE id='busy'`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := db.ScanMarketNotifications(ctx); err == nil {
		t.Fatal("被锁定的管理员未触发重试")
	}
	if time.Since(start) > time.Second {
		t.Fatal("市场通知扫描等待了记账锁")
	}
	var n int64
	if _, err := db.Engine.SQL(`SELECT count(*) FROM user_notifications`).Get(&n); err != nil || n != 0 {
		t.Fatalf("管理员未齐时发生部分投递: %d %v", n, err)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`UPDATE notification_market_retry SET next_attempt=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanMarketNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.SQL(`SELECT count(*) FROM user_notifications WHERE type='market_update'`).Get(&n); err != nil || n != 2 {
		t.Fatalf("释放后未完整投递: %d %v", n, err)
	}
}

// TestMarketNotificationFailureDoesNotBlockOtherEvents 验证坏事件冷却后，其他事件仍能提交并且修复后可重试。
// 隔离 PostgreSQL 中注入无效事件与正常事件，验证消息和事件状态；testDB 自动清理。
func TestMarketNotificationFailureDoesNotBlockOtherEvents(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE proxy_config(namespace text NOT NULL,key text NOT NULL,value_json text NOT NULL,PRIMARY KEY(namespace,key))`,
		`INSERT INTO users(id,email,status,role) VALUES('admin','admin@test.example','active','admin')`,
		`INSERT INTO proxy_config VALUES('market_change_events','bad','{}'),('market_change_events','good','{"id":"good","time":"2026-11-13T00:00:00Z","changes":[{"kind":"added","model":"one"}]}')`,
	} {
		if _, err := db.Engine.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ScanMarketNotifications(ctx); err == nil {
		t.Fatal("坏事件未报告失败")
	}
	if _, n, err := db.ListNotifications(ctx, "admin", 20, 0); err != nil || n != 1 {
		t.Fatalf("坏事件阻塞其他消息: %d %v", n, err)
	}
	if _, err := db.Engine.Exec(`UPDATE proxy_config SET value_json='{"id":"bad","time":"2026-11-13T00:00:00Z","changes":[{"kind":"added","model":"two"}]}' WHERE key='bad'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`UPDATE notification_market_retry SET next_attempt=now()-interval '1 second' WHERE event_id='bad'`); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanMarketNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if _, n, err := db.ListNotifications(ctx, "admin", 20, 0); err != nil || n != 2 {
		t.Fatalf("修复后未重试: %d %v", n, err)
	}
}
