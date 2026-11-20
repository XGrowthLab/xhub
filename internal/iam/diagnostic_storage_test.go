package iam

import (
	"testing"

	"github.com/sunqirui1987/xhub/internal/logframe"
)

// TestDiagnosticDetailWithExpiredCapture 在真实 PostgreSQL 中准备已过期的正文采集和仍保留的日志诊断。
// 详情应还原原始字节并保持正文到期状态；损坏单条帧不能泄露编码内容或影响相邻日志。私有 schema 自动删除。
func TestDiagnosticDetailWithExpiredCapture(t *testing.T) {
	db := testDB(t)
	raw := " {\"result\":\"合成诊断\",\"latency\":3}\n"
	value, err := logframe.Encode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Engine.Exec("INSERT INTO usage_events(request_id,owner_type) VALUES(?,'personal'),(?,'personal')", "expired-detail", "plain-neighbor"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Engine.Exec("INSERT INTO request_logs(request_id,upstream_response,body_capture_status,body_capture_expires_at) VALUES(?,?,'saved','2000-01-01T00:00:00Z'),(?,?,'','')",
		"expired-detail", value, "plain-neighbor", "existing text"); err != nil {
		t.Fatal(err)
	}
	current, err := db.GetRequestLog(t.Context(), "expired-detail")
	if err != nil || current.UpstreamResponse != raw || current.BodyCaptureStatus != "expired" || current.RequestBody != "" {
		t.Fatal("诊断解压后原文或采集期限不正确", err)
	}
	var persisted string
	if _, err = db.Engine.SQL("SELECT upstream_response FROM request_logs WHERE request_id=?", "expired-detail").Get(&persisted); err != nil || persisted != value {
		t.Fatal("读取详情改变了已压缩字段", err)
	}
	if _, err = db.Engine.Exec("UPDATE request_logs SET upstream_response=? WHERE request_id=?", value+"!", "expired-detail"); err != nil {
		t.Fatal(err)
	}
	if row, e := db.GetRequestLog(t.Context(), "expired-detail"); e == nil || row != nil {
		t.Fatal("损坏编码被当作真实上游诊断")
	}
	if row, e := db.GetRequestLog(t.Context(), "plain-neighbor"); e != nil || row.UpstreamResponse != "existing text" {
		t.Fatal("单条故障导致相邻日志不可读", e)
	}
}
