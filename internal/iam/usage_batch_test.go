package iam

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/cmd/regression/testsupport"
	"xorm.io/xorm"
)

// holdUsageWriter 在指定 db 上持有真正的账本串行事务，供测试可重复地制造短批次。
// 返回幂等释放函数并注册清理；事务中不动业务表，等待其退出才允许 schema 清理。
func holdUsageWriter(t *testing.T, db *DB) func() {
	t.Helper()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- db.tx(t.Context(), func(*xorm.Session) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("无法持有测试写事务: %v", err)
	}
	stop := sync.OnceFunc(func() {
		close(release)
		if err := <-done; err != nil {
			t.Errorf("测试写事务未正常结束: %v", err)
		}
	})
	t.Cleanup(stop)
	return stop
}

// waitUserQueue 等待真实请求进入指定用户的独立队列；n 仅计算等候项，不含已进入事务的领写者。
// 业务不依赖此观察器；测试用它控制取消和并发时序，超时抛出包含用户及数量的失败。
func waitUserQueue(t *testing.T, db *DB, user string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		db.userWrites.mu.Lock()
		q := db.userWrites.queues[user]
		ready := q != nil && len(q.pending) == n
		db.userWrites.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("用户 %s 的结算队列未达到 %d 项", user, n)
}

// TestUserUsageBatchSharedBudget 验证两个用户共用组织、团队、项目时的五层额度和硬预占。
// 两个独立网关接入同一真实 PG schema，交错并发结算并重复投递；逐层对账、预算耗尽仍拒绝，测试回收连接与 schema。
func TestUserUsageBatchSharedBudget(t *testing.T) {
	db := testDB(t)
	other, err := Open(t.Context(), db.Engine.DataSourceName())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	org, err := db.CreateOrg(t.Context(), Actor{}, "shared", quotaLimit(8))
	if err != nil {
		t.Fatal(err)
	}
	team, err := db.CreateTeam(t.Context(), Actor{}, TeamInput{OrganizationID: org.ID, Name: "shared-team"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(t.Context(), Actor{}, ProjectInput{TeamID: team.ID, Name: "shared-project", MaxBudget: quotaLimit(8)})
	if err != nil {
		t.Fatal(err)
	}
	dbs := []*DB{db, other}
	records := make([][]UsageRecord, 2)
	for i := range records {
		user, err := db.CreateUser(t.Context(), Actor{}, UserInput{Email: fmt.Sprintf("batch-%d@example.com", i), Password: "password123", TeamID: team.ID})
		if err != nil {
			t.Fatal(err)
		}
		key, _, err := db.CreateKey(t.Context(), Actor{}, KeyInput{OwnerType: OwnerPersonal, UserID: user.ID, TeamID: team.ID, ProjectID: project.ID})
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 8; j++ {
			record := usageRecord(fmt.Sprintf("shared-batch-%d-%d", i, j), user.ID, .5)
			record.KeyID, record.TeamID, record.OrganizationID, record.ProjectID = key.ID, team.ID, org.ID, project.ID
			hold, err := db.ReserveBudget(t.Context(), record.RequestID, "key", key.ID, .5)
			if err != nil {
				t.Fatal(err)
			}
			record.BudgetReservationID = hold.RequestID
			records[i] = append(records[i], record)
		}
	}
	if _, err := other.ReserveBudget(t.Context(), "excess-held", "key", records[0][0].KeyID, .01); !errors.Is(err, ErrBudgetUnavailable) {
		t.Fatalf("并发结算前的共享预占未受限: %v", err)
	}
	release := holdUsageWriter(t, db)
	var wg sync.WaitGroup
	for i, userRecords := range records {
		for _, record := range userRecords {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := dbs[i].RecordUsageExact(t.Context(), []UsageRecord{record}); err != nil {
					t.Errorf("结算 %s 失败: %v", record.RequestID, err)
				}
			}()
		}
		waitUserQueue(t, dbs[i], userRecords[0].UserID, len(userRecords)-1)
	}
	release()
	wg.Wait()
	for i, userRecords := range records {
		for _, record := range userRecords {
			if err := dbs[1-i].RecordUsageExact(t.Context(), []UsageRecord{record}); err != nil {
				t.Fatalf("跨实例重复投递失败: %v", err)
			}
		}
	}
	rows, err := db.Engine.QueryString(`SELECT (SELECT spend FROM organizations WHERE id=?)::text AS org,
	(SELECT spend FROM teams WHERE id=?)::text AS team,(SELECT spend FROM projects WHERE id=?)::text AS project,
	(SELECT COUNT(*) FROM users WHERE spend=4) AS users,(SELECT COUNT(*) FROM api_keys WHERE spend=4) AS keys,
	(SELECT COUNT(*) FROM budget_reservations) AS held,COUNT(*) AS events,SUM(cost) AS cost,
	COUNT(DISTINCT xmin::text) AS transactions FROM usage_events`, org.ID, team.ID, project.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("读取五层账本失败: %v %v", rows, err)
	}
	got := rows[0]
	for _, name := range []string{"org", "team", "project", "cost"} {
		if got[name] != "8.0" && got[name] != "8" {
			t.Fatalf("共享消费 %s 错误: %v", name, got)
		}
	}
	if got["events"] != "16" || got["users"] != "2" || got["keys"] != "2" || got["held"] != "0" || got["transactions"] != "4" {
		t.Fatalf("应有4笔数据库事务、16条唯一账单和五层一致的消费: %v", got)
	}
	if _, err := db.ReserveBudget(t.Context(), "excess-spent", "key", records[1][0].KeyID, .01); !errors.Is(err, ErrBudgetUnavailable) {
		t.Fatalf("当前共享预算已用尽仍可预占: %v", err)
	}
}

// TestUserUsageBatchFaultIsolation 验证数据库坏单、旧快照、在途取消各自收到正确结果，正常请求可独立提交。
// 预先保存真实待结算记录和数据库约束，随后用新连接在解除故障后恢复；测试 schema、连接和阻塞事务自动清理。
func TestUserUsageBatchFaultIsolation(t *testing.T) {
	db := testDB(t)
	u, err := db.CreateUser(t.Context(), Actor{}, UserInput{Email: "batch-fault@example.com", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	old := usageRecord("already", u.ID, .25)
	if err = db.RecordUsageExact(t.Context(), []UsageRecord{old}); err != nil {
		t.Fatal(err)
	}
	conflict := old
	conflict.RequestBody = "different frozen request"
	bad := usageRecord("blocked-entry", u.ID, .5)
	if _, err = db.Engine.Exec("ALTER TABLE request_logs ADD CONSTRAINT reject_batch_entry CHECK (request_id <> 'blocked-entry')"); err != nil {
		t.Fatal(err)
	}
	if durable, err := db.RecordUsageDurably(t.Context(), bad); !durable || err == nil {
		t.Fatalf("坏单必须保留可恢复快照: %t %v", durable, err)
	}
	release := holdUsageWriter(t, db)
	run := func(ctx context.Context, r UsageRecord) <-chan error {
		done := make(chan error, 1)
		go func() { done <- db.RecordUsageExact(ctx, []UsageRecord{r}) }()
		return done
	}
	head := run(t.Context(), usageRecord("head", u.ID, .25))
	waitUserQueue(t, db, u.ID, 0)
	ok := run(t.Context(), usageRecord("other-good", u.ID, .25))
	blocked := run(t.Context(), bad)
	stale := run(t.Context(), conflict)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	canceled := run(ctx, usageRecord("canceled", u.ID, 1))
	waitUserQueue(t, db, u.ID, 4)
	cancel()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未及时返回: %v", err)
	}
	release()
	if err := <-head; err != nil {
		t.Fatal(err)
	}
	if err := <-ok; err != nil {
		t.Fatalf("有效请求被坏单阻断: %v", err)
	}
	if err := <-blocked; err == nil {
		t.Fatal("数据库拒绝的账单被确认为成功")
	}
	if err := <-stale; !errors.Is(err, ErrConflict) {
		t.Fatalf("快照冲突未保留: %v", err)
	}
	if userSpend(t, db, u.ID) != .75 || countEvents(t, db, bad.RequestID) != 0 || countEvents(t, db, "canceled") != 0 || pendingCount(t, db) != 1 {
		t.Fatal("事务故障丢失持久证据、产生部分扣款或写入取消账单")
	}
	if _, err = db.Engine.Exec("ALTER TABLE request_logs DROP CONSTRAINT reject_batch_entry"); err != nil {
		t.Fatal(err)
	}
	dsn := db.Engine.DataSourceName()
	waitUserBatchIdle(t, db, u.ID)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.FlushPendingUsage(t.Context(), 50); err != nil {
		t.Fatal(err)
	}
	if userSpend(t, reopened, u.ID) != 1.25 || countEvents(t, reopened, bad.RequestID) != 1 || pendingCount(t, reopened) != 0 {
		t.Fatal("重启恢复后的账本、消费与快照不一致")
	}
}

// BenchmarkUserUsageBatch 在独立 PostgreSQL schema 内对比同一用户的逐笔和并发合批；每笔更新实际五层消费并使用独立请求 ID。
// 去掉准备、结算核验及清理耗时；每轮校验写入笔数并报告每事务账单数，数据库和连接由基准夹具回收。
func BenchmarkUserUsageBatch(b *testing.B) {
	for _, batch := range []bool{false, true} {
		name := "individual"
		if batch {
			name = "coalesced"
		}
		b.Run(name, func(b *testing.B) {
			db, err := Open(b.Context(), testsupport.Postgres(b, "bill_bench"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = db.Close() })
			org, err := db.CreateOrg(b.Context(), Actor{}, "bench-org", nil)
			if err != nil {
				b.Fatal(err)
			}
			team, err := db.CreateTeam(b.Context(), Actor{}, TeamInput{OrganizationID: org.ID, Name: "bench-team"})
			if err != nil {
				b.Fatal(err)
			}
			project, err := db.CreateProject(b.Context(), Actor{}, ProjectInput{TeamID: team.ID, Name: "bench-project"})
			if err != nil {
				b.Fatal(err)
			}
			u, err := db.CreateUser(b.Context(), Actor{}, UserInput{Email: "bench@example.com", Password: "password123", TeamID: team.ID})
			if err != nil {
				b.Fatal(err)
			}
			key, _, err := db.CreateKey(b.Context(), Actor{}, KeyInput{OwnerType: OwnerPersonal, UserID: u.ID, TeamID: team.ID, ProjectID: project.ID})
			if err != nil {
				b.Fatal(err)
			}
			var seq atomic.Int64
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					r := usageRecord(fmt.Sprintf("bench-%d", seq.Add(1)), u.ID, .000062)
					r.OrganizationID, r.TeamID, r.ProjectID, r.KeyID = org.ID, team.ID, project.ID, key.ID
					var err error
					if batch {
						err = db.recordUserUsage(b.Context(), r)
					} else {
						err = db.recordUsage(b.Context(), []UsageRecord{r}, true)
					}
					if err != nil {
						b.Errorf("写入账单失败: %v", err)
						return
					}
				}
			})
			b.StopTimer()
			var n struct {
				Rows         int64
				Transactions int64
			}
			got, err := db.Engine.SQL("SELECT count(*) AS rows,count(DISTINCT xmin::text) AS transactions FROM usage_events").Get(&n)
			if err != nil || !got || n.Rows != int64(b.N) || n.Transactions < 1 {
				b.Fatalf("微基准未完整入账: %+v %v", n, err)
			}
			b.ReportMetric(float64(n.Rows)/float64(n.Transactions), "rows/tx")
		})
	}
}

// TestUserUsageBatchFullQueue 验证每个用户的等待区有界，池满后还能取消，并且监控消费按 NUMERIC 精确相加。
// 持住账本事务后发满等待区，再取消额外请求；放行并检查事务数、消费和空闲队列回收，私有 schema 自动删除。
func TestUserUsageBatchFullQueue(t *testing.T) {
	db := testDB(t)
	const user = "queue-user"
	release := holdUsageWriter(t, db)
	done := make(chan error, maxUserUsageBatch+1)
	for i := 0; i <= maxUserUsageBatch; i++ {
		r := usageRecord(fmt.Sprintf("queue-%d", i), user, .1)
		go func() { done <- db.RecordUsageExact(t.Context(), []UsageRecord{r}) }()
	}
	waitUserQueue(t, db, user, maxUserUsageBatch)
	// 额外调用已满，等待空位的分支必须服从调用者截止时间，不能挤入无界待办。
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	err := db.RecordUsageExact(ctx, []UsageRecord{usageRecord("full-canceled", user, .2)})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("满队列未取消: %v", err)
	}
	waitUserQueue(t, db, user, maxUserUsageBatch)
	release()
	for range maxUserUsageBatch + 1 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	match, err := db.Engine.QueryString(`SELECT SUM(cost)=(SELECT SUM(cost) FROM model_monitor_buckets) AS equal,
	COUNT(DISTINCT xmin::text) AS txs,COUNT(*) AS events FROM usage_events`)
	if err != nil || len(match) != 1 || match[0]["equal"] != "true" || match[0]["txs"] != "2" || match[0]["events"] != "17" {
		t.Fatalf("满队列未分为两次精确结算: %+v %v", match, err)
	}
	waitUserBatchIdle(t, db, user)
}

// waitUserBatchIdle 等待最后一批通知完所有调用方后删除空队列。仅供单测检查短命工作协程是否排空，无外部副作用。
func waitUserBatchIdle(t *testing.T, db *DB, user string) {
	t.Helper()
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		db.userWrites.mu.Lock()
		idle := db.userWrites.queues[user] == nil
		db.userWrites.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("用户 %s 的空闲结算队列未释放", user)
}

// TestUserUsageBatchCancelDuringTransaction 验证合批真正开始写 PG 后，非首位调用取消只影响它自己。
// 在隔离 schema 锁住日志表并观察数据库锁等待；取消后必须撤回原交易并重试剩余好单，随后释放事务并清理。
func TestUserUsageBatchCancelDuringTransaction(t *testing.T) {
	db := testDB(t)
	tx, err := db.Engine.DB().DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), "LOCK TABLE request_logs IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	good := userUsageWrite{ctx: t.Context(), record: usageRecord("live-good", "cancel-user", .1), done: make(chan error, 1)}
	interrupted := userUsageWrite{ctx: ctx, record: usageRecord("live-cancel", "cancel-user", .2), done: make(chan error, 1)}
	second := userUsageWrite{ctx: t.Context(), record: usageRecord("live-second", "cancel-user", .125), done: make(chan error, 1)}
	third := userUsageWrite{ctx: t.Context(), record: usageRecord("live-third", "cancel-user", .25), done: make(chan error, 1)}
	go db.writeUserUsageBatch([]userUsageWrite{good, interrupted, second, third})
	// 已取得事务 ID 且等候 request_logs 写锁，证明取消发生在真实 INSERT 之后。
	waiter := func() string {
		rows, err := db.Engine.QueryString(`SELECT activity.backend_xid::text AS xid FROM pg_locks l
			JOIN pg_stat_activity activity ON l.pid=activity.pid WHERE l.relation='request_logs'::regclass
			AND l.mode='RowExclusiveLock' AND NOT l.granted`)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			return ""
		}
		return rows[0]["xid"]
	}
	var first string
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); {
		if first = waiter(); first != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if first == "" {
		t.Fatal("未观察到合批事务等待日志表锁")
	}
	cancel()
	for end := time.Now().Add(3 * time.Second); waiter() == first; {
		if time.Now().After(end) {
			t.Fatal("其中一项取消后原合批事务仍未回滚")
		}
		time.Sleep(time.Millisecond)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-good.done; err != nil {
		t.Fatalf("另一笔的取消阻断了正常提交: %v", err)
	}
	if err := <-interrupted.done; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的调用收到错误结果: %v", err)
	}
	for _, job := range []userUsageWrite{second, third} {
		if err := <-job.done; err != nil {
			t.Fatalf("剩余同批记录未能重试: %v", err)
		}
	}
	rows, err := db.Engine.QueryString("SELECT COUNT(*) AS n, COUNT(DISTINCT xmin::text) AS txs FROM usage_events")
	if err != nil || len(rows) != 1 || rows[0]["n"] != "3" || rows[0]["txs"] != "1" {
		t.Fatalf("取消后仍存活的三笔未在同一个新事务内提交: %v %v", rows, err)
	}
	if countEvents(t, db, good.record.RequestID) != 1 || countEvents(t, db, interrupted.record.RequestID) != 0 {
		t.Fatal("合批中途取消后有漏单或部分提交")
	}
}

// TestBatchSpendPeriodAndRollback 在真实 PostgreSQL 创建五层归属和不同预算周期，验证逐事件筛选与十进制精度。
// 故意让第五层更新失败后复核所有表均已回滚，再重新结算并重放重复 ID；夹具回收 schema、触发器和连接。
func TestBatchSpendPeriodAndRollback(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	org, err := db.CreateOrg(ctx, Actor{}, "period-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	team, err := db.CreateTeam(ctx, Actor{}, TeamInput{OrganizationID: org.ID, Name: "period-team"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, Actor{}, ProjectInput{TeamID: team.ID, Name: "period-project"})
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, Actor{}, UserInput{Email: "period@example.com", Password: "password123", TeamID: team.ID})
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := db.CreateKey(ctx, Actor{}, KeyInput{OwnerType: OwnerPersonal, UserID: user.ID, TeamID: team.ID, ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	if _, err := db.Engine.Exec("UPDATE organizations SET budget_period_start=? WHERE id=?", start, org.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec("UPDATE users SET budget_period_start=? WHERE id=?", start.Add(time.Second), user.ID); err != nil {
		t.Fatal(err)
	}
	var records []UsageRecord
	for i, cost := range []float64{0.1, 0.2, 0.00000000000003, 0} {
		r := usageRecord(fmt.Sprintf("period-batch-%d", i), user.ID, cost)
		r.TS = start.Add(time.Duration(i-1) * time.Second)
		r.OrganizationID, r.TeamID, r.ProjectID, r.KeyID = org.ID, team.ID, project.ID, key.ID
		records = append(records, r)
	}
	if _, err := db.Engine.Exec("ALTER TABLE api_keys ADD CONSTRAINT block_charge CHECK (spend=0)"); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsageExact(ctx, records); err == nil {
		t.Fatal("密钥层拒绝写入时应回滚整批")
	}
	rows, err := db.Engine.QueryString("SELECT (SELECT COUNT(*) FROM usage_events) AS events, (SELECT SUM(spend) FROM teams) AS team")
	if err != nil || len(rows) != 1 || rows[0]["events"] != "0" || rows[0]["team"] != "0" {
		t.Fatalf("第五层写入失败后出现已提交的部分消费: %v %v", rows, err)
	}
	if _, err := db.Engine.Exec("ALTER TABLE api_keys DROP CONSTRAINT block_charge"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.RecordUsageExact(ctx, append(records, records[0])); err != nil {
			t.Fatal(err)
		}
	}
	rows, err = db.Engine.QueryString(`SELECT (SELECT spend=0.20000000000003 FROM organizations WHERE id=?) AS org,
		(SELECT spend=0.00000000000003 FROM users WHERE id=?) AS usr, (SELECT spend=0.30000000000003 FROM api_keys WHERE id=?) AS key,
		(SELECT spend=0.30000000000003 FROM teams WHERE id=?) AS team,(SELECT spend=0.30000000000003 FROM projects WHERE id=?) AS project,
		(SELECT SUM(cost)=0.30000000000003 AND COUNT(*)=4 FROM usage_events) AS ledger`, org.ID, user.ID, key.ID, team.ID, project.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("无法核对五层精确消费: %v %v", rows, err)
	}
	for scope, ok := range rows[0] {
		if ok != "true" && ok != "t" {
			t.Errorf("%s 的消费或周期不匹配: %v", scope, rows[0])
		}
	}
}
