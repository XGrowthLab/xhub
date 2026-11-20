package storageaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/cmd/regression/testsupport"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/store"
)

// fixture 初始化真实网关表，返回当前测试的私有数据库连接。t 注册关闭连接及删除 schema，其他测试的数据不会参与统计。
func fixture(t *testing.T) *sql.DB {
	t.Helper()
	dsn := testsupport.Postgres(t, "capacity")
	id, err := iam.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { id.Close() })
	st, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Engine.Close() })
	return st.DB
}

// TestSnapshotProtocolBytes 构造各种协议、孤立采集、跨日及未识别路径，验证调用计数、空字段、正文和隐私归因。
// 真实 PostgreSQL 以统一的只读事务采样，所有夹具随测试 schema 回收。
func TestSnapshotProtocolBytes(t *testing.T) {
	db := fixture(t)
	ctx := t.Context()
	dep := "private-installation"
	base, err := Capture(ctx, db, dep, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range []struct{ path, want string }{
		{"/bypass/openai/v1/chat/completions/", "chat"}, {"/v1/responses?token=private-marker", "responses"},
		{"/v1/messages", "messages"}, {"/v1/responses/private-marker", "responses_resource"},
		{"/v1/messages/count_tokens", "messages_count"}, {"/v1/private-marker", "other"}, {"", "unrecorded"},
		{"/v1/models/fixture:streamGenerateContent", "gemini"}, {"/v1/images/generations", "images"},
		{"/v1/embeddings", "embeddings"}, {"/v1/audio/speech", "audio"}, {"/v1/videos/123", "video"},
		{"/v1/uploads/123", "files"}, {"/v1/rerank", "rerank"}, {"/v1/moderations", "moderations"},
	} {
		id := fmt.Sprintf("private-marker-%d", i)
		if _, err = db.ExecContext(ctx, `INSERT INTO usage_events(request_id,ts,owner_type,inbound_endpoint,outbound_endpoint,streamed)
          VALUES($1,$2,'personal',$3,'/v1/responses',true)`, id, base.Since, p.path); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO request_logs(request_id,error,request_body) VALUES($1,'err','abc')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO request_log_captures(request_id,source,created_at,expires_at,request_document,size_bytes)
          VALUES($1,'global',now(),now(),'abc',3)`, id); err != nil {
			t.Fatal(err)
		}
		// 逐个验证分类，避免两个相邻协议互相误归类而总数仍碰巧相等。
		s, err := Capture(ctx, db, dep, 2)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, r := range s.Routes {
			if r.Inbound == p.want && r.Outbound == "responses" && r.Bypass == (i == 0) && r.Streamed != nil && *r.Streamed && r.Calls == 1 && r.Captures == 1 && r.Captured == 4 && r.Diagnostics == 4 && r.Embedded == 4 && r.Day == base.Since.Format("2006-01-02") {
				found = true
			}
		}
		if !found {
			t.Fatalf("协议 %s 字段、时间或计数错误: %+v", p.want, s.Routes)
		}
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO request_log_captures(request_id,source,created_at,expires_at,response_document,size_bytes)
      VALUES('private-marker-orphan','global',now(),now(),'xyz',3);
      INSERT INTO usage_events(request_id,ts,owner_type) VALUES('old',now()-interval '10 days','session');
      INSERT INTO kv(kind,id,body,created_at) VALUES('conversation_state','session_route:v1:history:private-marker','[]','malformed')`); err != nil {
		t.Fatal(err)
	}
	got, err := Capture(ctx, db, dep, 2)
	if err != nil {
		t.Fatal(err)
	}
	var orphan bool
	var total int64
	for _, r := range got.Routes {
		total += r.Calls
		if r.Streamed == nil {
			orphan = r.Inbound == "unrecorded" && r.Calls == 0 && r.Captures == 1 && r.Captured == 4
		}
	}
	if !orphan || total != 15 {
		t.Fatalf("孤立采集或过期请求被重复计入，孤立=%t 调用=%d", orphan, total)
	}
	if err = got.Compare(base); err != nil || got.Physical["database"] == 0 || got.Difference["usage_events.heap"] <= 0 {
		t.Fatalf("物理快照不能比较: %v %+v", err, got.Difference)
	}
	if len(got.History) != 3 || got.History[2].Kind != "unrecognized_legacy_key" || got.History[2].Day != "unknown" {
		t.Fatalf("旧键归类错误: %+v", got.History)
	}
	raw, _ := json.Marshal(got)
	for _, secret := range []string{dep, "private-marker"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("报告泄露原始输入 %s", secret)
		}
	}
	if _, err = Capture(ctx, db, "", 2); err == nil {
		t.Fatal("接受了空部署标记")
	}
	tx, err := beginRead(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM request_logs"); err == nil {
		t.Fatal("数据库没有强制只读性质")
	}
}

// TestPlanByteBoundaries 在隔离 PostgreSQL 构造大小、已覆盖和截止时间各异的行。核对只读、游标、逐条预算及失败重试；退出清理整个 schema。
func TestPlanByteBoundaries(t *testing.T) {
	db := fixture(t)
	ctx := t.Context()
	cutoff := time.Now().UTC().Truncate(time.Second)
	o := PlanOptions{Before: cutoff, Rows: 10, BatchBytes: 1000, RecordBytes: 900, ReserveBytes: 64}
	sizes := []int{1200, 700, 600, 3, 770, 20, 200}
	for i, n := range sizes {
		key := fmt.Sprintf("%s%064x", historyPrefix, i)
		date := cutoff.Add(-time.Minute)
		if i == 6 {
			date = cutoff
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO kv(kind,id,body,created_at) VALUES('conversation_state',$1,$2,$3)`, key, strings.Repeat("a", n), date.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		if i == 4 {
			if _, err := db.ExecContext(ctx, `INSERT INTO conversation_history_manifests VALUES($1,'fixture','[]')`, key); err != nil {
				t.Fatal(err)
			}
		}
	}
	first, err := Estimate(ctx, db, o)
	if err != nil || !first.More || first.Scanned != 2 || first.Deferred != 1 || first.Selected != 1 || first.Read != 700 || first.DeferredRaw != 1200 || first.Savings <= 0 || first.Candidate != first.Frame+64 {
		t.Fatalf("首批限流或收益有误: %+v %v", first, err)
	}
	o.Cursor = first.Next
	second, err := Estimate(ctx, db, o)
	if err != nil || second.More || second.Scanned != 4 || second.Selected != 1 || second.Kept != 2 || second.Covered != 1 || second.Read != 600 {
		t.Fatalf("续算有遗漏或重复: %+v %v", second, err)
	}
	again, err := Estimate(ctx, db, o)
	if err != nil || *again != *second {
		t.Fatalf("重试重复推进或结果变动: %+v %v", again, err)
	}
	o.Cursor = second.Next
	last, err := Estimate(ctx, db, o)
	if err != nil || last.More || last.Scanned != 0 || last.Next != o.Cursor {
		t.Fatalf("末页重复或游标丢失: %+v %v", last, err)
	}
	o.Cursor = ""
	o.Rows = 1
	small, err := Estimate(ctx, db, o)
	if err != nil || small.Scanned != 1 || small.Deferred != 1 || !small.More || small.Read != 0 {
		t.Fatalf("超限首行阻塞或被读取: %+v %v", small, err)
	}
	var total, nmanifest, nblocks int
	if err = db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM kv WHERE kind='conversation_state'),(SELECT count(*) FROM conversation_history_manifests),(SELECT count(*) FROM conversation_history_blocks)`).Scan(&total, &nmanifest, &nblocks); err != nil || total != 7 || nmanifest != 1 || nblocks != 0 {
		t.Fatalf("试算意外写入数据库: %d %d %d %v", total, nmanifest, nblocks, err)
	}
	o.Before = cutoff.Add(time.Hour)
	if _, err = Estimate(ctx, db, o); err == nil {
		t.Fatal("接受了未来的截止点")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if p, err := Estimate(cancelled, db, o); err == nil || p != nil {
		t.Fatal("取消时返回了可推进的游标")
	}
}

// TestPlanCursorAndSnapshotIdentity 验证明文、异步任务错用游标以及跨部署、乱序快照均被拒绝。纯内存夹具无需清理。
func TestPlanCursorAndSnapshotIdentity(t *testing.T) {
	before := time.Now().UTC().Truncate(time.Second)
	o := PlanOptions{Before: before, Rows: 1, RecordBytes: 1, BatchBytes: 1}
	for _, cursor := range []string{"secret", before.Format(time.RFC3339) + "/user-name", before.Add(-time.Hour).Format(time.RFC3339) + "/" + strings.Repeat("a", 64)} {
		o.Cursor = cursor
		if _, err := o.startKey(); err == nil {
			t.Fatal("接受了不匹配的游标")
		}
	}
	o.Cursor = ""
	o.RecordBytes = 2
	if _, err := o.startKey(); err == nil {
		t.Fatal("单条可超过整批预算")
	}
	s := Snapshot{Version: 1, Source: "one", At: before, Physical: map[string]int64{"kv.heap": 2, "new": 3}}
	p := Snapshot{Version: 1, Source: "one", At: before.Add(-time.Second), Physical: map[string]int64{"kv.heap": 4}}
	if err := s.Compare(&p); err != nil || len(s.Difference) != 1 || s.Difference["kv.heap"] != -2 {
		t.Fatal("缩小及缺列的物理快照被误报", err, s.Difference)
	}
	p.Source = "two"
	if s.Compare(&p) == nil || s.Compare(&s) == nil || s.Compare(nil) == nil {
		t.Fatal("接受了跨来源或乱序的基线")
	}
}

// TestSnapshotMonitorPrivilegesAndLateCapture 使用 PostgreSQL 自带的普通读数据角色，验证无 WAL 权限仍能盘点。
// 设置跨窗请求和采集，验证已有协议、零重复调用与遗漏处理。角色仅在私有连接生效，结束后重置并回收 schema。
func TestSnapshotMonitorPrivilegesAndLateCapture(t *testing.T) {
	db := fixture(t)
	db.SetMaxOpenConns(1)
	ctx := t.Context()
	_, err := db.ExecContext(ctx, `INSERT INTO usage_events(request_id,ts,owner_type,inbound_endpoint,outbound_endpoint)
 VALUES ('late-capture',now()-interval '5 days','personal','/v1/messages','/v1/responses'),('old-capture',now(),'personal','/v1/embeddings','/v1/embeddings');
 INSERT INTO request_log_captures(request_id,source,created_at,expires_at,request_document,size_bytes)
 VALUES('late-capture','global',now(),now(),'abc',3),('old-capture','global',now()-interval '5 days',now(),'abc',3)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "SET ROLE pg_read_all_data"); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "RESET ROLE")
	report, err := Capture(ctx, db, "plain-reader", 1)
	if err != nil {
		t.Fatal("受限角色无法读取快照", err)
	}
	if _, present := report.Physical["cluster.wal_files"]; present {
		t.Fatal("普通读取角色拿到了特权 WAL 指标")
	}
	var late, old bool
	for _, r := range report.Routes {
		if r.Inbound == "messages" {
			late = r.Calls == 0 && r.Captures == 1 && r.Captured == 4
		}
		if r.Inbound == "embeddings" {
			old = r.Calls == 1 && r.Captures == 0 && r.Captured == 0
		}
	}
	if !late || !old {
		t.Fatal("跨窗采集被漏记或重复", report.Routes)
	}
}
