package storageaudit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/historypack"
	"github.com/sunqirui1987/xhub/internal/store"
)

// packFixture 在本例 PostgreSQL schema 写两条原样历史和一条普通路由，给出可重复的截止点和单批预算。
// 合成数据带工具参数及空白，以便区分字节一致与仅 JSON 语义一致；结束时 fixture 清理所有行。
func packFixture(t *testing.T) (*sql.DB, *store.Store, PlanOptions, []string, string) {
	t.Helper()
	db := fixture(t)
	s := &store.Store{DB: db}
	when := time.Now().UTC().Truncate(time.Second)
	o := PlanOptions{Before: when, Rows: 50, BatchBytes: 1 << 20, RecordBytes: 256 << 10, ReserveBytes: 64}
	value := " [ {\"role\":\"user\", \"content\":\"" + strings.Repeat("tool result 中文 ", 90) + "\"}, {\"n\":9007199254740993} ] \n"
	keys := []string{fmt.Sprintf("%s%064x", historyPrefix, 1), fmt.Sprintf("%s%064x", historyPrefix, 2)}
	for _, key := range keys {
		if _, err := db.Exec(`INSERT INTO kv(kind,id,body,created_at) VALUES('conversation_state',$1,$2,$3)`, key, value, when.Add(-time.Hour).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.WriteConversation(t.Context(), "session_route:v1:owner:fixture", "route-owned"); err != nil {
		t.Fatal(err)
	}
	return db, s, o, keys, value
}

// TestPackedHistoryLifecycle 核对压缩游标、重新运行、原键读取及反向还原；改写一条已压缩记录后，回退不能覆盖新值。
// 真实数据库保留原创建时间、普通归属与新旧写入，快照能单列已压缩体积，测试库随后回收。
func TestPackedHistoryLifecycle(t *testing.T) {
	db, s, o, keys, value := packFixture(t)
	ctx := t.Context()
	o.Rows = 1
	p, err := Convert(ctx, db, o, "pack")
	if err != nil || p.Selected != 1 || !p.More {
		t.Fatalf("首批未转换: %+v %v", p, err)
	}
	o.Cursor = p.Next
	narrow := o
	narrow.MinRawBytes = 16
	if _, e := Convert(ctx, db, narrow, "pack"); e == nil {
		t.Fatal("改变选取规则后仍可使用旧游标")
	}

	if _, err = Convert(ctx, db, o, "restore"); err == nil {
		t.Fatal("转换游标被用于反向还原")
	}
	p, err = Convert(ctx, db, o, "pack")
	if err != nil || p.Selected != 1 || p.More {
		t.Fatalf("第二批未转换: %+v %v", p, err)
	}
	o.Rows = 10
	o.Cursor = ""
	p, err = Convert(ctx, db, o, "pack")
	if err != nil || p.AlreadyPacked != 2 || p.Selected != 0 {
		t.Fatal("重跑重复转换", p, err)
	}
	for _, key := range keys {
		got, e := s.ReadHistory(ctx, key, "fixture")
		if e != nil || got != value {
			t.Fatal("原键读取失败", e)
		}
		actual, e := s.CompareConversation(ctx, key, "", "should-not-overwrite")
		if e != nil || actual != value {
			t.Fatal("首次归属比较覆盖了压缩正文", e)
		}
		actual, e = s.CompareConversation(ctx, key, "mismatch", "should-not-overwrite")
		if e != nil || actual != value {
			t.Fatal("比较未返回当前历史", e)
		}
	}
	snap, e := Capture(ctx, db, "fixture", 2)
	if e != nil {
		t.Fatal(e)
	}
	var packed Cohort
	for _, c := range snap.History {
		if c.Kind == "packed_history" {
			packed = c
		}
	}
	if packed.Records != 2 || packed.Bytes <= 0 || packed.Bytes >= int64(len(value)*2) {
		t.Fatalf("物理字段归因不正确: %+v", packed)
	}
	if got, e := s.CompareConversation(ctx, keys[0], value, `[{"new":1}]`); e != nil || got != `[{"new":1}]` {
		t.Fatal("比较后替换失败", e)
	}
	p, e = Convert(ctx, db, o, "restore")
	if e != nil || p.Restored != 1 {
		t.Fatal("反向还原未按当前状态执行", p, e)
	}
	for i, want := range []string{`[{"new":1}]`, value} {
		got, e := s.ReadConversation(ctx, keys[i])
		if e != nil || got != want {
			t.Fatal("反向还原覆盖了新值或改变了原始字节", e)
		}
	}
	p, e = Convert(ctx, db, o, "restore")
	if e != nil || p.Restored != 0 || p.More {
		t.Fatal("反向还原无法安全重跑", p, e)
	}
	if got, e := s.ReadConversation(ctx, "session_route:v1:owner:fixture"); e != nil || got != "route-owned" {
		t.Fatal("非历史路由受到影响", e)
	}
}

// TestPackedHistoryRollbackAndRewrite 令第二条记录的写入触发校验错误，验证整批事务回滚和改写后的权威版本。
// 解除本例故障后复跑，再分别用旧格式和分块格式改写，检查过期帧不会复活；schema 自动清理。
func TestPackedHistoryRollbackAndRewrite(t *testing.T) {
	db, s, o, keys, value := packFixture(t)
	ctx := t.Context()
	if _, err := db.Exec(`CREATE FUNCTION corrupt_frame() RETURNS trigger LANGUAGE plpgsql AS $$
      BEGIN IF right(NEW.id,1)='2' AND NEW.history_frame IS NOT NULL THEN NEW.history_sha256='bad'::bytea; END IF; RETURN NEW; END $$;
      CREATE TRIGGER corrupt_test BEFORE UPDATE ON kv FOR EACH ROW EXECUTE FUNCTION corrupt_frame()`); err != nil {
		t.Fatal(err)
	}
	if p, e := Convert(ctx, db, o, "pack"); e == nil || p != nil {
		t.Fatal("数据库回读损坏仍发布了成功游标")
	}
	var count int
	if e := db.QueryRow("SELECT count(*) FROM kv WHERE history_frame IS NOT NULL").Scan(&count); e != nil || count != 0 {
		t.Fatal("事务留下了部分转换", count, e)
	}
	if _, e := db.Exec("DROP TRIGGER corrupt_test ON kv"); e != nil {
		t.Fatal(e)
	}
	if p, e := Convert(ctx, db, o, "pack"); e != nil || p.Selected != 2 {
		t.Fatal("解除故障后无法重试", p, e)
	}
	if _, n, e := s.MigrateLegacyHistories(ctx, "", 50); e != nil || n != 0 {
		t.Fatal("旧迁移器把压缩标记当作历史", n, e)
	}
	if _, e := db.Exec("UPDATE kv SET history_raw_size=0 WHERE id=$1", keys[1]); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReadHistory(ctx, keys[1], "fixture"); e == nil {
		t.Fatal("损坏帧返回成功")
	}
	if p, e := Convert(ctx, db, o, "restore"); e == nil || p != nil {
		t.Fatal("回滚损坏的帧仍报告成功")
	}
	if e := s.WriteHistory(ctx, keys[0], "fixture", `["current"]`, true); e != nil {
		t.Fatal(e)
	}
	if got, e := s.ReadHistory(ctx, keys[0], "fixture"); e != nil || got != `["current"]` {
		t.Fatal("分块写入后出现陈旧帧", e)
	}
	if e := s.WriteHistory(ctx, keys[1], "fixture", value, false); e != nil {
		t.Fatal(e)
	}
	if got, e := s.ReadHistory(ctx, keys[1], "fixture"); e != nil || got != value {
		t.Fatal("新写入的旧格式不可读", e)
	}
	if e := db.QueryRow("SELECT count(*) FROM kv WHERE history_frame IS NOT NULL").Scan(&count); e != nil || count != 0 {
		t.Fatal("写入后仍残留陈旧压缩帧", count, e)
	}
}

// TestPackedBatchInterrupted 用另一事务锁住第二个响应键，观察第一行已处理后取消任务，验证重新执行不会漏转。
// 同一响应键仍由网关和维护程序共享事务锁。所有连接与锁在结束时释放，schema 由夹具回收。
func TestPackedBatchInterrupted(t *testing.T) {
	db, s, o, keys, value := packFixture(t)
	ctx := t.Context()
	blocker, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	if _, e = blocker.Exec("SELECT pg_advisory_xact_lock(hashtextextended($1,0))", keys[1]); e != nil {
		t.Fatal(e)
	}
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Convert(c, db, o, "pack"); done <- err }()
	waitForHistoryLock(t, blocker)
	cancel()
	if e = <-done; e == nil {
		t.Fatal("取消的批次被视为成功")
	}
	if e = blocker.Rollback(); e != nil {
		t.Fatal(e)
	}
	var packed int
	if e = db.QueryRow("SELECT count(*) FROM kv WHERE body=$1", historypack.Marker).Scan(&packed); e != nil || packed != 0 {
		t.Fatal("取消后第一条未回滚", packed, e)
	}
	p, e := Convert(ctx, db, o, "pack")
	if e != nil || p.Selected != 2 {
		t.Fatal("中断续跑遗漏或重复", p, e)
	}
	if got, e := s.ReadHistory(ctx, keys[1], "fixture"); e != nil || got != value {
		t.Fatal("重试后正文不一致", e)
	}
}

// TestOldSchemaAndOversizeBands 验证旧库仍可只读盘点，未部署兼容读时拒绝写；同时验证独立大小区间、单键筛选及坏旧正文原样保留。
// 所有 DDL 和数据均在本例私有 PostgreSQL schema 中，结束随夹具回收。
func TestOldSchemaAndOversizeBands(t *testing.T) {
	db, _, o, keys, value := packFixture(t)
	ctx := t.Context()
	o.MinRawBytes = 100
	o.OnlyDigest = strings.TrimPrefix(keys[0], historyPrefix)
	p, e := Convert(ctx, db, o, "pack")
	if e != nil || p.Selected != 1 {
		t.Fatal("按大小及单键选取失败", p, e)
	}
	o.Cursor = ""
	p, e = Convert(ctx, db, o, "restore")
	if e != nil || p.Restored != 1 {
		t.Fatal("选定范围不能回退", p, e)
	}
	o.MinRawBytes = int64(len(value) + 1)
	p, e = Estimate(ctx, db, o)
	if e != nil || p.Scanned != 0 {
		t.Fatal("访问了过小的行", p, e)
	}
	o.OnlyDigest = ""
	o.MinRawBytes = 0
	if _, e = db.Exec("UPDATE kv SET body=repeat('broken ',150) WHERE id=$1", keys[1]); e != nil {
		t.Fatal(e)
	}
	p, e = Estimate(ctx, db, o)
	if e != nil || p.Malformed != 1 || p.Selected != 1 {
		t.Fatal("试算没有区分可转换的历史和损坏的正文", p, e)
	}
	p, e = Convert(ctx, db, o, "pack")
	if e != nil || p.Malformed != 1 || p.Selected != 1 {
		t.Fatal("不识别的旧正文未保持原样", p, e)
	}
	if _, e = Convert(ctx, db, o, "restore"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("ALTER TABLE kv DROP COLUMN history_frame, DROP COLUMN history_raw_size, DROP COLUMN history_sha256"); e != nil {
		t.Fatal(e)
	}
	if _, e = Capture(ctx, db, "old", 2); e != nil {
		t.Fatal("旧库无法盘点", e)
	}
	if _, e = Estimate(ctx, db, o); e != nil {
		t.Fatal("旧库无法试压", e)
	}
	if _, e = Convert(ctx, db, o, "pack"); e == nil {
		t.Fatal("未升级的数据库允许写压缩格式")
	}
	if _, e = db.Exec("ALTER TABLE kv ADD COLUMN history_frame BYTEA"); e != nil {
		t.Fatal(e)
	}
	if _, e = Estimate(ctx, db, o); e == nil {
		t.Fatal("部分升级被当做可用的库")
	}
}

// waitForHistoryLock 轮询持锁事务在 pg_locks 中的精确等待者，用于在键重查前确定并发提交顺序。
// 参数为测试和持锁事务，超时或数据库错误使测试失败；调用方负责释放锁和回收私有 schema。
func waitForHistoryLock(t *testing.T, blocker *sql.Tx) {
	t.Helper()
	start := time.Now()
	for {
		var waiting bool
		// 用持锁事务的 PID 精确识别本例第二个键，其他并行测试不会触发取消。
		if e := blocker.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_locks w JOIN pg_locks h USING(database,classid,objid,objsubid)
          WHERE w.locktype='advisory' AND h.pid=pg_backend_pid() AND NOT w.granted)`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("第二个响应键未进入等待")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestPackedHistoryConcurrentReplacement 在维护程序等候共享键锁时，模拟网关先提交一个过期时间之外的新值。
// 同一事务同时改写另一条仍符合条件的旧值，核对维护程序使用最新元数据及字节；两条记录随夹具清理。
func TestPackedHistoryConcurrentReplacement(t *testing.T) {
	db, s, o, keys, value := packFixture(t)
	ctx := t.Context()
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err = writer.Exec("SELECT pg_advisory_xact_lock(hashtextextended($1,0))", keys[0]); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		plan *Plan
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		plan, err := Convert(ctx, db, o, "pack")
		done <- outcome{plan, err}
	}()
	waitForHistoryLock(t, writer)
	if _, err = writer.Exec("UPDATE kv SET body='[]',created_at=$2 WHERE id=$1", keys[0], o.Before.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	latest := strings.ReplaceAll(value, "tool result", "new data")
	if _, err = writer.Exec("UPDATE kv SET body=$2 WHERE id=$1", keys[1], latest); err != nil {
		t.Fatal(err)
	}
	if err = writer.Commit(); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil || result.plan.Changed != 1 || result.plan.Selected != 1 {
		t.Fatal("并发提交后未按最新候选状态转换", result.plan, result.err)
	}
	for i, want := range []string{"[]", latest} {
		got, err := s.ReadConversation(ctx, keys[i])
		if err != nil || got != want {
			t.Fatal("维护批次覆盖了并发写入", i, err)
		}
	}
}
