package iam

import (
	"context"
	"encoding/json"
	"strings"
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

// TestLargeMarketEventSharedStorage 验证超长模型 ID 和超过 8 MiB 的来源报价完整投递且不会按管理员复制。
// 隔离 PostgreSQL 中验证两个收件人均能读取相同详情，单个消息删除不影响另一人，全部清理后回收共享数据。
func TestLargeMarketEventSharedStorage(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE proxy_config(namespace text NOT NULL,key text NOT NULL,value_json text NOT NULL,PRIMARY KEY(namespace,key))`,
		`INSERT INTO users(id,email,status,role) VALUES('large-a','la@test.example','active','admin'),('large-b','lb@test.example','active','admin')`,
	} {
		if _, err := db.Engine.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	model := strings.Repeat("m", 1024)
	raw, err := json.Marshal(map[string]any{"id": "large", "time": "2026-11-13T00:00:00Z", "changes": []any{map[string]any{"kind": "price_changed", "model": model, "current_price": strings.Repeat("p", 9<<20)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`INSERT INTO proxy_config VALUES('market_change_events','large',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	// 共享详情可在消息投递前准备，清理不能因尚无收件人而删除待投递数据。
	if err := db.prepareMarketEvent(ctx, "large"); err != nil {
		t.Fatal(err)
	}
	if err := db.CleanExpiredNotifications(ctx, time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	var prepared int64
	if _, err := db.Engine.SQL(`SELECT count(*) FROM notification_market_events WHERE id='large'`).Get(&prepared); err != nil || prepared != 1 {
		t.Fatalf("待投递详情被清理: %d %v", prepared, err)
	}
	if err := db.ScanMarketNotifications(ctx); err != nil {
		t.Fatalf("大事件未投递: %v", err)
	}
	rows, n, err := db.ListNotifications(ctx, "large-a", 20, 0)
	if err != nil || n != 1 || len(rows[0].Data) > 4096 {
		t.Fatalf("列表摘要不受限: %d %v", n, err)
	}
	part, next, err := db.MarketNotificationDetail(ctx, "large-a", rows[0].ID, 0)
	if err != nil || len([]rune(part)) != 16384 || next == nil || *next != 16384 {
		t.Fatalf("详情分页不正确: %v", err)
	}
	middle, next, err := db.MarketNotificationDetail(ctx, "large-a", rows[0].ID, 8<<20)
	if err != nil || len([]rune(middle)) != 16384 || next == nil {
		t.Fatalf("超大详情不能继续读取: %v", err)
	}
	if _, _, err := db.MarketNotificationDetail(ctx, "large-b", rows[0].ID, 0); err != ErrNotFound {
		t.Fatal("其他账户可读取详情")
	}
	var shared, refs, bytes int64
	if _, err := db.Engine.SQL(`SELECT count(*) FROM notification_market_events`).Get(&shared); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.SQL(`SELECT count(*) FROM user_notifications WHERE type='market_update'`).Get(&refs); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.SQL(`SELECT COALESCE(sum(length(data::text)),0) FROM user_notifications WHERE type='market_update'`).Get(&bytes); err != nil {
		t.Fatal(err)
	}
	if shared != 1 || refs != 2 || bytes > 1024 {
		t.Fatalf("共享消息体积异常: shared=%d refs=%d bytes=%d", shared, refs, bytes)
	}
	if _, err := db.Engine.Exec(`DELETE FROM user_notifications WHERE user_id='large-a'`); err != nil {
		t.Fatal(err)
	}
	if err := db.CleanExpiredNotifications(ctx, time.Now().Add(-time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, n, err := db.ListNotifications(ctx, "large-b", 20, 0); err != nil || n != 1 {
		t.Fatalf("清理损坏另一收件人: %d %v", n, err)
	}
	if err := db.CleanExpiredNotifications(ctx, time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.SQL(`SELECT count(*) FROM notification_market_events`).Get(&shared); err != nil || shared != 0 {
		t.Fatalf("无引用详情未回收: %d %v", shared, err)
	}
}

// TestMarketCleanupConcurrentDelivery 验证清理与投递提交、最后一条消息删除并发时不会提前删除共享详情。
// 使用真实 PostgreSQL 暂停写事务，在提交前后分别运行清理和查询；测试结束清理隔离 schema。
func TestMarketCleanupConcurrentDelivery(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE proxy_config(namespace text NOT NULL,key text NOT NULL,value_json text NOT NULL,PRIMARY KEY(namespace,key))`,
		`INSERT INTO users(id,email,status,role) VALUES('concurrent','concurrent@test.example','active','admin')`,
		`INSERT INTO proxy_config VALUES('market_change_events','race','{"id":"race","time":"2026-11-13T00:00:00Z","changes":[{"kind":"added","model":"race-model"}]}')`,
	} {
		if _, err := db.Engine.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.prepareMarketEvent(ctx, "race"); err != nil {
		t.Fatal(err)
	}
	tx := db.Engine.NewSession()
	defer tx.Close()
	if err := tx.Begin(); err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// 等价于投递事务的最后两步，故意停在提交前，让清理观察旧 outbox 快照。
	if _, err := tx.Exec(`INSERT INTO user_notifications(id,user_id,type,event_key,data) VALUES('race-notice','concurrent','market_update','market:race','{"event_id":"race"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM proxy_config WHERE namespace='market_change_events' AND key='race'`); err != nil {
		t.Fatal(err)
	}
	if err := db.CleanExpiredNotifications(ctx, time.Now().Add(-time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	var n int64
	if _, err := db.Engine.SQL(`SELECT count(*) FROM notification_market_events WHERE id='race'`).Get(&n); err != nil || n != 1 {
		t.Fatalf("并发投递期间提前删除详情: %d %v", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, count, err := db.ListNotifications(ctx, "concurrent", 20, 0)
	if err != nil || count != 1 || !strings.Contains(string(rows[0].Data), "race-model") {
		t.Fatalf("投递后详情不可读: %d %v", count, err)
	}
	deleteTx := db.Engine.NewSession()
	defer deleteTx.Close()
	if err := deleteTx.Begin(); err != nil {
		t.Fatal(err)
	}
	defer deleteTx.Rollback()
	if _, err := deleteTx.Exec(`DELETE FROM user_notifications WHERE id='race-notice'`); err != nil {
		t.Fatal(err)
	}
	if err := db.CleanExpiredNotifications(ctx, time.Now().Add(-time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.SQL(`SELECT count(*) FROM notification_market_events WHERE id='race'`).Get(&n); err != nil || n != 1 {
		t.Fatalf("最后引用删除提交前详情被清理: %d %v", n, err)
	}
	if err := deleteTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.CleanExpiredNotifications(ctx, time.Now().Add(-time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.SQL(`SELECT count(*) FROM notification_market_events WHERE id='race'`).Get(&n); err != nil || n != 0 {
		t.Fatalf("最后引用删除后未回收详情: %d %v", n, err)
	}
}

// TestMarketDetailUnicodePageBoundary 验证中文与 emoji 正好跨分页边界时不会拆断或遗漏。
// 使用隔离 PostgreSQL 生成真实消息，按字符偏移读取并与数据库完整内容对比；testDB 自动清理。
func TestMarketDetailUnicodePageBoundary(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE proxy_config(namespace text NOT NULL,key text NOT NULL,value_json text NOT NULL,PRIMARY KEY(namespace,key))`,
		`INSERT INTO users(id,email,status,role) VALUES('unicode','unicode@test.example','active','admin')`,
	} {
		if _, err := db.Engine.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(map[string]any{"id": "unicode-event", "time": "2026-11-13T00:00:00Z", "changes": []any{map[string]any{"kind": "price_changed", "model": "model", "current_price": strings.Repeat("a", 16400) + "中🙂" + strings.Repeat("b", 100)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`INSERT INTO proxy_config VALUES('market_change_events','unicode-event',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanMarketNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	rows, count, err := db.ListNotifications(ctx, "unicode", 20, 0)
	if err != nil || count != 1 {
		t.Fatalf("消息缺失: %d %v", count, err)
	}
	var full string
	if _, err := db.Engine.SQL(`SELECT data::text FROM notification_market_events WHERE id='unicode-event'`).Get(&full); err != nil {
		t.Fatal(err)
	}
	runes := []rune(full)
	index := -1
	for i, char := range runes {
		if char == '中' {
			index = i
			break
		}
	}
	if index < 16383 {
		t.Fatal("未构造跨页中文字符")
	}
	offset := index - 16383
	first, next, err := db.MarketNotificationDetail(ctx, "unicode", rows[0].ID, offset)
	if err != nil || next == nil || !strings.HasSuffix(first, "中") {
		t.Fatalf("首页结尾字符错误: %v", err)
	}
	second, last, err := db.MarketNotificationDetail(ctx, "unicode", rows[0].ID, *next)
	if err != nil || last != nil || !strings.HasPrefix(second, "🙂") || first+second != string(runes[offset:]) {
		t.Fatalf("跨页 Unicode 拼接丢失: %v", err)
	}
}

// TestMarketNotificationSlowInsertReleasesBillingLocks 验证多管理员投递遭遇慢 SQL 时及时回滚并释放记账锁。
// 使用本地 PostgreSQL 触发器制造慢写入，通过数据库等待状态确认故障已触发；修复后验证事件能重试。
func TestMarketNotificationSlowInsertReleasesBillingLocks(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE proxy_config(namespace text NOT NULL,key text NOT NULL,value_json text NOT NULL,PRIMARY KEY(namespace,key))`,
		`INSERT INTO users(id,email,status,role) SELECT 'admin-'||i,'a'||i||'@test.example','active','admin' FROM generate_series(1,20) i`,
		`INSERT INTO proxy_config VALUES('market_change_events','slow','{"id":"slow","time":"2026-11-13T00:00:00Z","changes":[{"kind":"added","model":"example"}]}')`,
		`CREATE FUNCTION slow_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(2); RETURN NEW; END $$`,
		`CREATE TRIGGER slow_notice BEFORE INSERT ON user_notifications FOR EACH ROW EXECUTE FUNCTION slow_notice()`,
	} {
		if _, err := db.Engine.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	errs := make(chan error, 1)
	go func() { errs <- db.ScanMarketNotifications(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	seen := false
	for time.Now().Before(deadline) {
		if _, err := db.Engine.SQL(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event='PgSleep' AND query LIKE '%INSERT INTO user_notifications%')`).Get(&seen); err != nil {
			t.Fatal(err)
		}
		if seen {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !seen {
		t.Fatal("未观察到注入的慢写入")
	}
	start := time.Now()
	if _, err := db.Engine.Exec(`UPDATE users SET spend=spend WHERE id='admin-1'`); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("慢通知投递长期阻塞记账")
	}
	if err := <-errs; err == nil {
		t.Fatal("慢写入应超时并保留事件")
	}
	var pending int64
	if _, err := db.Engine.SQL(`SELECT count(*) FROM proxy_config WHERE namespace='market_change_events'`).Get(&pending); err != nil || pending != 1 {
		t.Fatalf("超时事件丢失: %d %v", pending, err)
	}
	if _, err := db.Engine.Exec(`DROP TRIGGER slow_notice ON user_notifications`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`UPDATE notification_market_retry SET next_attempt=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanMarketNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.SQL(`SELECT count(*) FROM user_notifications WHERE type='market_update'`).Get(&pending); err != nil || pending != 20 {
		t.Fatalf("恢复后未完整投递: %d %v", pending, err)
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
