package iam

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

// TestBudgetNotificationThreshold 验证有限、无上限、零上限与无效数值；纯计算，无清理副作用。
func TestBudgetNotificationThreshold(t *testing.T) {
	limit, zero := 100.0, 0.0
	for _, tc := range []struct {
		spend float64
		cap   *float64
		want  int
		valid bool
	}{{79, &limit, 0, true}, {80, &limit, 80, true}, {95, &limit, 95, true}, {101, &limit, 100, true}, {10, nil, 0, true}, {0, &zero, 100, true}, {math.NaN(), &limit, 0, false}, {-1, &limit, 0, false}} {
		if got, ok := budgetThreshold(tc.spend, tc.cap); got != tc.want || ok != tc.valid {
			t.Fatalf("threshold spend=%v got=%d/%v", tc.spend, got, ok)
		}
	}
}

// TestBudgetNotificationsPersistence 使用隔离 PostgreSQL 验证阈值、并发去重、调额重触发、已读隔离和清理。
// 直接设置测试用户计数，失败不改变账单；testDB 自动清理 schema，所有并发完成后释放数据库。
func TestBudgetNotificationsPersistence(t *testing.T) {
	db := testDB(t)
	// 一条连接模拟生产最紧张的连接池，租约不能独占连接造成死锁。
	db.Engine.DB().DB.SetMaxOpenConns(1)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO users(id,email,status,role,max_budget,spend) VALUES('notify-a','a@test.example','active','user',100,81)`,
		`INSERT INTO users(id,email,status,role,max_budget,spend) VALUES('notify-b','b@test.example','active','user',NULL,500)`,
		`INSERT INTO users(id,email,status,role,max_budget,spend) VALUES('notify-zero','z@test.example','active','user',0,0)`,
	} {
		if _, err := db.Engine.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- db.ScanBudgetNotifications(ctx) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, total, err := db.ListNotifications(ctx, "notify-a", 20, 0)
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("并发产生重复或读取失败 total=%d rows=%v err=%v", total, rows, err)
	}
	if n, err := db.UnreadNotifications(ctx, "notify-b"); err != nil || n != 0 {
		t.Fatalf("无上限不应提示 %d %v", n, err)
	}
	if n, err := db.UnreadNotifications(ctx, "notify-zero"); err != nil || n != 1 {
		t.Fatalf("零额度应提示 %d %v", n, err)
	}
	var checked time.Time
	if _, err := db.Engine.SQL(`SELECT last_checked_at FROM notification_budget_state WHERE user_id='notify-a'`).Get(&checked); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	var checkedAgain time.Time
	if _, err := db.Engine.SQL(`SELECT last_checked_at FROM notification_budget_state WHERE user_id='notify-a'`).Get(&checkedAgain); err != nil || !checked.Equal(checkedAgain) {
		t.Fatalf("无变动不应重复写入扫描游标: %v", err)
	}
	var states int64
	if _, err := db.Engine.SQL(`SELECT count(*) FROM notification_budget_state WHERE user_id='notify-b'`).Get(&states); err != nil || states != 0 {
		t.Fatalf("无个人预算不应建立扫描游标: %d %v", states, err)
	}
	if err := db.ReadNotification(ctx, "notify-b", rows[0].ID); err != ErrNotFound {
		t.Fatalf("跨用户消息可读: %v", err)
	}
	if err := db.ReadNotification(ctx, "notify-a", rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ReadNotification(ctx, "notify-a", rows[0].ID); err != nil {
		t.Fatal("重复标记不幂等", err)
	}
	if n, err := db.UnreadNotifications(ctx, "notify-a"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if _, err := db.Engine.Exec(`UPDATE users SET spend=96 WHERE id='notify-a'`); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if _, n, err := db.ListNotifications(ctx, "notify-a", 20, 0); err != nil || n != 2 {
		t.Fatalf("95阈值提醒缺失 %d %v", n, err)
	}
	if _, err := db.Engine.Exec(`UPDATE users SET max_budget=200 WHERE id='notify-a'`); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`UPDATE users SET spend=161 WHERE id='notify-a'`); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if _, n, err := db.ListNotifications(ctx, "notify-a", 20, 0); err != nil || n != 3 {
		t.Fatalf("调额后的新周期缺提醒 %d %v", n, err)
	}
	if err := db.CleanExpiredNotifications(ctx, time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if n, err := db.UnreadNotifications(ctx, "notify-a"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	var spend, cap float64
	if _, err := db.Engine.SQL(`SELECT spend FROM users WHERE id='notify-a'`).Get(&spend); err != nil || spend != 161 {
		t.Fatalf("清理不应改变已消费金额: %v %v", spend, err)
	}
	if _, err := db.Engine.SQL(`SELECT max_budget FROM users WHERE id='notify-a'`).Get(&cap); err != nil || cap != 200 {
		t.Fatalf("清理不应改变预算: %v %v", cap, err)
	}
}

// TestBudgetNotificationScanContinuesAfterUserFailure 验证一名用户的消息插入失败不会阻止其他用户提醒。
// PostgreSQL 约束仅拒绝测试账号，扫描返回错误供重试，同时验证后续用户消息持久化；结束清理私有 schema。
func TestBudgetNotificationScanContinuesAfterUserFailure(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.Engine.Exec(`INSERT INTO users(id,email,status,role,max_budget,spend) VALUES('a-bad','bad@test.example','active','user',100,90),('z-good','good@test.example','active','user',100,90)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`ALTER TABLE user_notifications ADD CONSTRAINT bad_user CHECK (user_id <> 'a-bad')`); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanBudgetNotifications(ctx); err == nil {
		t.Fatal("部分失败未返回错误")
	}
	n, err := db.UnreadNotifications(ctx, "z-good")
	if err != nil || n != 1 {
		t.Fatalf("坏用户阻断其他用户: %d %v", n, err)
	}
}

// TestBudgetNotificationSkipsLockedUser 验证用户正在记账时扫描立即跳过，提交后下次扫描能正常产生消息。
// 测试用隔离 PostgreSQL 事务模拟账单行锁，显式回滚锁，私有 schema 由 testDB 清理。
func TestBudgetNotificationSkipsLockedUser(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.Engine.Exec(`INSERT INTO users(id,email,status,role,max_budget,spend) VALUES('locked','locked@test.example','active','user',100,90)`); err != nil {
		t.Fatal(err)
	}
	s := db.Engine.NewSession()
	defer s.Close()
	if err := s.Begin(); err != nil {
		t.Fatal(err)
	}
	defer s.Rollback()
	if _, err := s.Exec(`UPDATE users SET spend=90 WHERE id='locked'`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("通知扫描等待了账单锁")
	}
	if n, err := db.UnreadNotifications(ctx, "locked"); err != nil || n != 0 {
		t.Fatalf("被锁账户不应产生消息: %d %v", n, err)
	}
	if err := s.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := db.UnreadNotifications(ctx, "locked"); err != nil || n != 1 {
		t.Fatalf("锁释放后应重试: %d %v", n, err)
	}
}

// TestBudgetNotificationWriteTimeout 验证通知数据库写入异常缓慢时及时取消并释放用户锁，账单字段不变。
// 隔离 PostgreSQL 触发器模拟卡住的消息写入，schema 随 testDB 自动清理。
func TestBudgetNotificationWriteTimeout(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.Engine.Exec(`INSERT INTO users(id,email,status,role,max_budget,spend) VALUES('slow','slow@test.example','active','user',100,90)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`CREATE FUNCTION slow_notification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(3); RETURN NEW; END $$; CREATE TRIGGER slow_notify BEFORE INSERT ON user_notifications FOR EACH ROW EXECUTE FUNCTION slow_notification()`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := db.ScanBudgetNotifications(ctx); err == nil {
		t.Fatal("慢写入应失败并等待下次重试")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("通知慢写入没有及时释放账单锁")
	}
	if _, err := db.Engine.Exec(`UPDATE users SET spend=91 WHERE id='slow'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`DROP TRIGGER slow_notify ON user_notifications`); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := db.UnreadNotifications(ctx, "slow"); err != nil || n != 1 {
		t.Fatalf("慢写入恢复后未重试: %d %v", n, err)
	}
}

// TestBudgetNotificationMigrationAndLeaseRecovery 验证重复迁移与异常退出留下的过期租约恢复。
// 使用隔离 PostgreSQL schema；主动取消的扫描不得发送消息，测试结束自动清理。
func TestBudgetNotificationMigrationAndLeaseRecovery(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	if _, err := db.Engine.Exec(`INSERT INTO users(id,email,status,role,max_budget,spend) VALUES('recover','recover@test.example','active','user',100,90)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`INSERT INTO notification_scan_lease(name,owner,expires_at) VALUES('budget','old',now()-interval '1 second')`); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := db.ScanBudgetNotifications(canceled); err == nil {
		t.Fatal("取消的扫描不应报告成功")
	}
	if n, err := db.UnreadNotifications(ctx, "recover"); err != nil || n != 0 {
		t.Fatalf("取消后意外产生消息: %d %v", n, err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := db.UnreadNotifications(ctx, "recover"); err != nil || n != 1 {
		t.Fatalf("过期租约恢复失败: %d %v", n, err)
	}
}

// TestBudgetNotificationBoundedConcurrentScans 验证多实例同时扫描时每轮最多处理 200 个候选，失败账户移至队尾。
// 私有 PostgreSQL schema 中创建 205 个失败用户和 1 个正常用户，测试结束自动删除 schema。
func TestBudgetNotificationBoundedConcurrentScans(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.Engine.Exec(`INSERT INTO users(id,email,status,role,max_budget,spend) SELECT 'bad-'||n,'bad-'||n||'@test.example','active','user',100,90 FROM generate_series(1,205) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`INSERT INTO users(id,email,status,role,max_budget,spend) VALUES('z-good','good-batch@test.example','active','user',100,90)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`ALTER TABLE user_notifications ADD CONSTRAINT reject_batch_bad CHECK (user_id NOT LIKE 'bad-%')`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = db.ScanBudgetNotifications(ctx) }()
	}
	wg.Wait()
	var checked int64
	if _, err := db.Engine.SQL(`SELECT count(*) FROM notification_budget_state WHERE user_id LIKE 'bad-%'`).Get(&checked); err != nil || checked != 200 {
		t.Fatalf("并发扫描应仅处理一批: %d %v", checked, err)
	}
	if err := db.ScanBudgetNotifications(ctx); err == nil {
		t.Fatal("失败账户应报告错误")
	}
	if n, err := db.UnreadNotifications(ctx, "z-good"); err != nil || n != 1 {
		t.Fatalf("失败用户不应永久阻塞队尾: %d %v", n, err)
	}
}

// TestBudgetNotificationNewPeriod 验证到期旧周期不发提醒、实际重置后新周期可重新提醒，且扫描不修改账单。
// 使用隔离 PostgreSQL 和测试用户；测试结束由 testDB 删除私有 schema。
func TestBudgetNotificationNewPeriod(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := db.Engine.Exec(`INSERT INTO users(id,email,status,role,max_budget,spend,budget_reset_day,budget_reset_at) VALUES('cycle','cycle@test.example','active','user',100,96,1,?)`, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := db.UnreadNotifications(ctx, "cycle"); err != nil || n != 0 {
		t.Fatalf("到期旧周期不应提醒: %d %v", n, err)
	}
	if err := db.ResetUserBudgets(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`UPDATE users SET spend=81 WHERE id='cycle'`); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := db.UnreadNotifications(ctx, "cycle"); err != nil || n != 1 {
		t.Fatalf("新周期提醒缺失: %d %v", n, err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := db.UnreadNotifications(ctx, "cycle"); err != nil || n != 1 {
		t.Fatalf("同周期重复提醒: %d %v", n, err)
	}
	if _, err := db.Engine.Exec(`UPDATE users SET budget_reset_at=? WHERE id='cycle'`, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.ResetUserBudgets(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`UPDATE users SET spend=81,budget_period_start=? WHERE id='cycle'`, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.ScanBudgetNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := db.UnreadNotifications(ctx, "cycle"); err != nil || n != 2 {
		t.Fatalf("下一周期同档位未重新提醒: %d %v", n, err)
	}
	var spend float64
	if _, err := db.Engine.SQL(`SELECT spend FROM users WHERE id='cycle'`).Get(&spend); err != nil || spend != 81 {
		t.Fatalf("扫描不应重置消费: %v %v", spend, err)
	}
}
