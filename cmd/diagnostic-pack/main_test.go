package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/cmd/regression/testsupport"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/storageaudit"
)

// TestDiagnosticCommand 通过可执行入口的解析与真实 PostgreSQL 验证只读、显式提交、清单约束、恢复和脱敏错误。
// 测试模型诊断为合成数据，schema 和摘要清单由测试框架清理。
func TestDiagnosticCommand(t *testing.T) {
	dsn := testsupport.Postgres(t, "diagcli")
	db, e := iam.Open(t.Context(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	const id = "request-fixture"
	var seed strings.Builder
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&seed, "%x", sha256.Sum256([]byte(fmt.Sprint(i))))
	}
	raw := `{"upstream":"` + strings.Repeat(seed.String(), 4) + `"}`
	if _, e = db.Engine.Exec("INSERT INTO usage_events(request_id,owner_type,ts) VALUES(?,'personal',now()-interval '1 hour')", id); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Engine.Exec("INSERT INTO request_logs(request_id,upstream_response) VALUES(?,?)", id, raw); e != nil {
		t.Fatal(e)
	}
	cutoff := "--before=" + time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	manifest := filepath.Join(t.TempDir(), "checked")
	if e = os.WriteFile(manifest, []byte(fmt.Sprintf("%x %x\n", sha256.Sum256([]byte(id)), sha256.Sum256([]byte(raw)))), 0600); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"plan", "pack", "restore"} {
		args := []string{mode, cutoff}
		if mode != "plan" {
			args = append(args, "--execute")
		}
		if mode == "pack" {
			args = append(args, "--backup-manifest="+manifest)
		}
		value, e := run(t.Context(), args, dsn, strings.NewReader(""))
		var r storageaudit.DiagnosticReport
		if e != nil || json.Unmarshal(value, &r) != nil || r.Selected+r.Restored != 1 {
			t.Fatal("诊断命令执行失败", mode, r, e)
		}
	}
	if _, e = run(t.Context(), []string{"pack", "--execute", cutoff}, dsn, strings.NewReader("")); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Engine.Exec("UPDATE request_logs SET upstream_response=substring(upstream_response from 1 for 112)||'!'"); e != nil {
		t.Fatal(e)
	}
	var event int64
	if _, e = db.Engine.SQL("SELECT id FROM usage_events WHERE request_id=?", id).Get(&event); e != nil {
		t.Fatal(e)
	}
	recoverArgs := []string{"recover", "--execute", fmt.Sprintf("--event=%d", event), "--backup-manifest=" + manifest}
	if _, e = run(t.Context(), recoverArgs, dsn, strings.NewReader("{}")); e == nil {
		t.Fatal("劣化原件意外被接受")
	}
	if out, e := run(t.Context(), recoverArgs, dsn, strings.NewReader(raw)); e != nil || string(out) != `{"recovered":true}` {
		t.Fatal("无法恢复损坏诊断的原件", e)
	}
	if out, e := run(t.Context(), recoverArgs, dsn, strings.NewReader(raw)); e != nil || string(out) != `{"recovered":false}` {
		t.Fatal("重复恢复影响了新版本", e)
	}
	for _, args := range [][]string{nil, {"other", cutoff}, {"plan", cutoff, "--from-event=3", "--through=1"}, {"plan", cutoff, "--cursor=invalid"}, {"recover", "--execute", "--event=1", "--backup-manifest=" + manifest, "--cursor=invalid"}, {"plan", "--execute", cutoff}, {"pack", cutoff}, {"plan", "--before=no"}, {"plan", cutoff, "--rows=0"}, {"pack", "--execute", cutoff, "--backup-manifest=" + manifest + "missing"}} {
		if value, e := run(t.Context(), args, dsn, strings.NewReader("")); e == nil || value != nil || strings.Contains(e.Error(), dsn) {
			t.Fatal("非法运维调用被接受或泄漏连接信息", args)
		}
	}
	if _, e = db.Engine.Exec("ALTER TABLE usage_events RENAME TO usage_fixture_unavailable"); e != nil {
		t.Fatal(e)
	}
	if value, e := run(t.Context(), []string{"plan", cutoff}, dsn, strings.NewReader("")); e == nil || value != nil || !strings.Contains(e.Error(), "SQLSTATE") {
		t.Fatal("数据库故障没有返回可识别的受控错误", e)
	}
}
