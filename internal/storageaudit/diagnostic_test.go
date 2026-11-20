package storageaudit

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/gateway/requestlog"
	"github.com/sunqirui1987/xhub/internal/logframe"
)

// diagnosticFixture 生成具有随机形状工具计数的可重复诊断，供真实 PostgreSQL 核对 TOAST、完整字节及日志清理。
// 参数为测试及重复次数，返回合法 JSON 文本，无用户数据或外部资源。
func diagnosticFixture(t *testing.T, repeats int) string {
	t.Helper()
	var seed strings.Builder
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&seed, "%x", sha256.Sum256([]byte(fmt.Sprint(i))))
	}
	b, err := json.Marshal(map[string]any{"status_code": 502, "trace": strings.Repeat(seed.String(), repeats)})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDiagnosticStorageLifecycle 在独立 PG 创建跨协议、错误、费用和到期诊断，测试原件限制、全量还原、分页和可重试性。
// 人工注入写后损坏时，前一行必须随整批回滚；所有测试数据由 schema 自动回收。
func TestDiagnosticStorageLifecycle(t *testing.T) {
	db := fixture(t)
	ctx := t.Context()
	cut := time.Now().UTC().Truncate(time.Second)
	raw := diagnosticFixture(t, 3)
	o := DiagnosticOptions{Before: cut, Rows: 2, MinBytes: 256, RecordBytes: 1 << 20, BatchBytes: 1 << 20}
	for i, route := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", "/bypass/openai/v1/responses"} {
		id := fmt.Sprintf("diagnostic-test-%d", i)
		if _, err := db.Exec("INSERT INTO usage_events(request_id,ts,owner_type,inbound_endpoint,cost) VALUES($1,$2,'personal',$3,1.2)", id, cut.Add(-time.Hour), route); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO request_logs(request_id,upstream_response,error) VALUES($1,$2,'fixed failure')", id, raw); err != nil {
			t.Fatal(err)
		}
	}
	p, e := DiagnosticBatch(ctx, db, o, "plan")
	if e != nil || p.Selected != 2 || p.Through == 0 || !p.More || p.StoredBefore <= p.StoredAfter {
		t.Fatal("只读试算未发现有收益的诊断", p, e)
	}
	if _, e = db.Exec(`CREATE FUNCTION break_diagnostic() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.request_id='diagnostic-test-1' THEN NEW.upstream_response=NEW.upstream_response||'!'; END IF; RETURN NEW; END $$;CREATE TRIGGER damage BEFORE UPDATE ON request_logs FOR EACH ROW EXECUTE FUNCTION break_diagnostic()`); e != nil {
		t.Fatal(e)
	}
	if p, e = DiagnosticBatch(ctx, db, o, "pack"); p != nil || e == nil {
		t.Fatal("写后读回错误未触发整批回滚")
	}
	var regular int
	if e = db.QueryRow("SELECT count(*) FROM request_logs WHERE upstream_response=$1", raw).Scan(&regular); e != nil || regular != 4 {
		t.Fatal("写入失败遗留了部分批次", regular, e)
	}
	if _, e = db.Exec("DROP TRIGGER damage ON request_logs"); e != nil {
		t.Fatal(e)
	}
	key := sha256.Sum256([]byte("diagnostic-test-0"))
	proof, e := ReadPreimages(strings.NewReader(fmt.Sprintf("%x %x", key, sha256.Sum256([]byte(raw)))))
	if e != nil {
		t.Fatal(e)
	}
	o.Preimages = proof
	p, e = DiagnosticBatch(ctx, db, o, "pack")
	if e != nil || p.Selected != 1 || p.Changed != 1 {
		t.Fatal("未按恢复清单约束诊断转换", p, e)
	}
	o.Preimages = nil
	p, e = DiagnosticBatch(ctx, db, o, "pack")
	if e != nil || p.Selected != 1 || p.Kept != 1 {
		t.Fatal("重试重复转换", p, e)
	}
	o.Through = p.Through
	o.After = p.Next
	p, e = DiagnosticBatch(ctx, db, o, "pack")
	if e != nil || p.Selected != 2 || p.More {
		t.Fatal("第二批遗漏", p, e)
	}
	var persisted string
	if e = db.QueryRow("SELECT upstream_response FROM request_logs WHERE request_id='diagnostic-test-0'").Scan(&persisted); e != nil {
		t.Fatal(e)
	}
	got, over, e := logframe.Read(ctx, persisted, o.RecordBytes)
	if e != nil || over || got != raw {
		t.Fatal("写后日志内容不一致", e)
	}
	if _, e = db.Exec("UPDATE request_logs SET upstream_response='newer async result' WHERE request_id='diagnostic-test-1'"); e != nil {
		t.Fatal(e)
	}
	o.After = 0
	o.Rows = 10
	p, e = DiagnosticBatch(ctx, db, o, "restore")
	if e != nil || p.Restored != 3 {
		t.Fatal("反向还原写入了已变化的诊断", p, e)
	}
	var cost float64
	var ok int
	if e = db.QueryRow("SELECT sum(cost),count(*) FILTER(WHERE error='fixed failure') FROM usage_events JOIN request_logs USING(request_id)").Scan(&cost, &ok); e != nil || cost != 4.8 || ok != 4 {
		t.Fatal("错误搜索或费用改变", cost, ok, e)
	}
	if e = db.QueryRow("SELECT count(*) FROM request_logs WHERE upstream_response=$1", raw).Scan(&regular); e != nil || regular != 3 {
		t.Fatal("恢复后字节不同", regular, e)
	}
}

// TestDiagnosticBatchBounds 验证空库、未来时间、非法模式、超大记录和预算边界。私有 schema 在结束时回收。
func TestDiagnosticBatchBounds(t *testing.T) {
	db := fixture(t)
	ctx := t.Context()
	cut := time.Now().UTC().Truncate(time.Second)
	o := DiagnosticOptions{Before: cut, Rows: 10, RecordBytes: 16384, BatchBytes: 16384, MinBytes: 1024}
	if p, e := DiagnosticBatch(ctx, db, o, "plan"); e != nil || p.Scanned != 0 || p.More {
		t.Fatal("空批次不能结束", p, e)
	}
	if _, e := DiagnosticBatch(ctx, db, o, "unknown"); e == nil {
		t.Fatal("接受未知操作")
	}
	future := o
	future.Before = cut.Add(time.Hour)
	if _, e := DiagnosticBatch(ctx, db, future, "pack"); e == nil {
		t.Fatal("接受未来截止点")
	}
	for i, raw := range []string{diagnosticFixture(t, 3), diagnosticFixture(t, 1), diagnosticFixture(t, 1), "tiny", "unstructured" + strings.Repeat("z", 1500)} {
		id := fmt.Sprint(i)
		if _, e := db.Exec("INSERT INTO usage_events(request_id,ts,owner_type) VALUES($1,$2,'personal')", id, cut.Add(-time.Second)); e != nil {
			t.Fatal(e)
		}
		if _, e := db.Exec("INSERT INTO request_logs(request_id,upstream_response) VALUES($1,$2)", id, raw); e != nil {
			t.Fatal(e)
		}
	}
	p, e := DiagnosticBatch(ctx, db, o, "plan")
	if e != nil || p.Deferred != 1 || p.Scanned != 2 || !p.More {
		t.Fatal("预算被超出或跳过下一行", p, e)
	}
	o.Through = p.Through
	o.After = p.Next
	p, e = DiagnosticBatch(ctx, db, o, "plan")
	if e != nil || p.More || p.Scanned != 3 {
		t.Fatal("续跑遗漏记录", p, e)
	}
}

// TestPackedDiagnosticRetention 保持旧日志 64 KiB 的原始清理边界。相似的短编码可对应大小两种原始错误，
// 小诊断的旧私有错误副本被清除，大诊断依原先规则保留，账务及其他字段可见；所有样本随 schema 删除。
func TestPackedDiagnosticRetention(t *testing.T) {
	db := fixture(t)
	ctx := t.Context()
	for i, repeat := range []int{4, 4500} {
		id := fmt.Sprint(i)
		v, _ := json.Marshal(map[string]any{"status_code": 503, "trace": strings.Repeat("synthetic tracking detail ", repeat), "error": map[string]any{"error": map[string]any{"code": "capacity", "message": "busy", "payload": map[string]any{"token": "remove-private-value"}}}})
		value, e := logframe.Encode(string(v))
		if e != nil {
			t.Fatal(e)
		}
		if len(value) >= 65536 {
			t.Fatal("夹具应覆盖短压缩帧")
		}
		if _, e = db.Exec("INSERT INTO usage_events(request_id,owner_type,ts) VALUES($1,'personal',now()-interval '2 days')", id); e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec("INSERT INTO request_logs(request_id,upstream_response,request_body) VALUES($1,$2,'expired')", id, value); e != nil {
			t.Fatal(e)
		}
	}
	p, e := requestlog.ProcessLegacyLogBatch(ctx, db, time.Now().Add(-time.Hour), 0, 10, true)
	if e != nil || p.Cleared != 2 || p.UnchangedDiagnostics != 1 {
		t.Fatal("编码改变了原始清理边界", p, e)
	}
	for i := 0; i < 2; i++ {
		var status, value string
		if e = db.QueryRow("SELECT body_capture_status,upstream_response FROM request_logs WHERE request_id=$1", fmt.Sprint(i)).Scan(&status, &value); e != nil {
			t.Fatal(e)
		}
		got, tooLarge, e := logframe.Read(ctx, value, 1<<20)
		if e != nil || tooLarge || status != "expired" || !strings.Contains(got, "capacity") || strings.Contains(got, "remove-private-value") != (i == 1) {
			t.Fatal("清理后诊断或正文期限不正确", i, e)
		}
	}
}
