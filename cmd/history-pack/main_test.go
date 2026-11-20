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
	"github.com/sunqirui1987/xhub/internal/storageaudit"
	"github.com/sunqirui1987/xhub/internal/store"
)

// TestHistoryPackCLI 验证独立命令显式执行、单键试点、反向还原和脱敏错误。使用真实临时 schema 与合成数据，结束自动删除。
func TestHistoryPackCLI(t *testing.T) {
	dsn := testsupport.Postgres(t, "packcli")
	s, e := store.Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Engine.Close()
	ctx := t.Context()
	cutoff := "--before=" + time.Now().UTC().Format(time.RFC3339)
	key := strings.Repeat("a", 64)
	value := `["` + strings.Repeat("test", 300) + `"]`
	for _, id := range []string{key, strings.Repeat("b", 64)} {
		if _, e = s.DB.Exec(`INSERT INTO kv(kind,id,body,created_at) VALUES('conversation_state',$1,$2,'2025-01-01T00:00:00Z')`, "session_route:v1:history:"+id, value); e != nil {
			t.Fatal(e)
		}
	}
	args := []string{"pack", "--execute", cutoff, "--reserve-bytes=64", "--key-digest=" + key}
	raw, e := run(ctx, args, dsn)
	var p storageaudit.Plan
	if e != nil || json.Unmarshal(raw, &p) != nil || p.Selected != 1 || p.Scanned != 1 {
		t.Fatal("单条试点失败", string(raw), e)
	}
	args[0] = "restore"
	raw, e = run(ctx, args, dsn)
	if e != nil || json.Unmarshal(raw, &p) != nil || p.Restored != 1 {
		t.Fatal("单条还原失败", e)
	}
	for _, args := range [][]string{nil, {"bad"}, {"pack", cutoff}, {"pack", "--execute", "--before=bad"}, {"pack", "--execute", cutoff, "--rows=0"}, {"restore", "--execute", cutoff, "--key-digest=private-text"}} {
		if out, err := run(ctx, args, dsn); out != nil || err == nil || strings.Contains(err.Error(), dsn) {
			t.Fatal("非法参数有副作用或泄露凭据", args)
		}
	}
	if _, e = s.DB.Exec("ALTER TABLE kv RENAME TO unavailable"); e != nil {
		t.Fatal(e)
	}
	if out, e := run(ctx, args, dsn); out != nil || e == nil || !strings.Contains(e.Error(), "SQLSTATE") {
		t.Fatal("缺表未返回受控错误", e)
	}
}

// TestRecoveryCommand 用临时清单和合成输入验证命令的明确执行、损坏后的 SQL 恢复、空输入与错误参数。
// PostgreSQL schema 和清单均由测试夹具清理，终端输出只含实际恢复状态或固定错误。
func TestRecoveryCommand(t *testing.T) {
	dsn := testsupport.Postgres(t, "recovercli")
	s, e := store.Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Engine.Close()
	key, created := strings.Repeat("c", 64), "2025-03-01T00:00:00Z"
	raw := `["` + strings.Repeat("recovery text", 120) + `"]`
	if _, e = s.DB.Exec("INSERT INTO kv(kind,id,body,created_at) VALUES('conversation_state',$1,$2,$3)", "session_route:v1:history:"+key, raw, created); e != nil {
		t.Fatal(e)
	}
	if _, e = run(t.Context(), []string{"pack", "--execute", "--before=2026-01-01T00:00:00Z", "--reserve-bytes=64"}, dsn); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec("UPDATE kv SET history_frame='bad' WHERE history_frame IS NOT NULL"); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "verified.manifest")
	if e = os.WriteFile(file, []byte(fmt.Sprintf("%s %x\n", key, sha256.Sum256([]byte(raw)))), 0600); e != nil {
		t.Fatal(e)
	}
	args := []string{"--execute", "--key-digest=" + key, "--created-at=" + created, "--backup-manifest=" + file}
	if out, e := recoverFromBackup(t.Context(), args, dsn, strings.NewReader(raw)); e != nil || string(out) != `{"recovered":true}` {
		t.Fatal("命令未恢复备份原文", e)
	}
	if out, e := recoverFromBackup(t.Context(), args, dsn, strings.NewReader(raw)); e != nil || string(out) != `{"recovered":false}` {
		t.Fatal("重复执行修改了原行", e)
	}
	if _, e = run(t.Context(), []string{"recover"}, dsn); e == nil {
		t.Fatal("缺少显式参数仍执行恢复")
	}
	if _, e = recoverFromBackup(t.Context(), args, dsn, strings.NewReader("invalid")); e == nil {
		t.Fatal("损坏原件未被拒绝")
	}
}
