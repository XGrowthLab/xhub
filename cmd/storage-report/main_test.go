package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/cmd/regression/testsupport"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/storageaudit"
	"github.com/sunqirui1987/xhub/internal/store"
)

// TestStorageReportCLI 在临时 PostgreSQL schema 与私有目录运行两个 CLI 模式，验证可复用的 JSON、物理差值和权限失败。
// 前置表由项目原有启动入口初始化；临时目录与 schema 均由 testing 清理，不会读宿主机其他目录。
func TestStorageReportCLI(t *testing.T) {
	ctx := t.Context()
	dsn := testsupport.Postgres(t, "capacity_cli")
	db, err := iam.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Engine.Close() })
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "private-fixture"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	snap := []string{"snapshot", "--deployment=fixture", "--days=1", "--backups-dir=" + dir}
	data, err := run(ctx, snap, dsn)
	var s storageaudit.Snapshot
	if err != nil || json.Unmarshal(data, &s) != nil || s.Physical["host.backups"] <= 0 || strings.Contains(string(data), dir) {
		t.Fatalf("容量快照无效: %v", err)
	}
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	data, err = run(ctx, append(snap, "--baseline="+path), dsn)
	if err != nil || json.Unmarshal(data, &s) != nil || s.PreviousAt == nil || s.Difference["host.backups"] != 0 {
		t.Fatalf("相同归档目录的差值错误: %v", err)
	}
	p, err := run(ctx, []string{"plan", "--before=" + time.Now().UTC().Format(time.RFC3339)}, dsn)
	var plan storageaudit.Plan
	if err != nil || json.Unmarshal(p, &plan) != nil || plan.More || plan.Scanned != 0 {
		t.Fatalf("空批次的 CLI 契约错误: %v", err)
	}
	if _, err = run(ctx, append([]string{"snapshot", "--deployment=other"}, "--baseline="+path), dsn); err == nil {
		t.Fatal("跨部署基线被接受")
	}
	if _, err = run(ctx, []string{"snapshot", "--deployment=fixture", "--backups-dir=" + path}, dsn); err == nil {
		t.Fatal("普通文件被当成归档目录")
	}
	if _, err = db.Engine.Exec("ALTER TABLE request_logs RENAME TO unavailable"); err != nil {
		t.Fatal(err)
	}
	if out, e := run(ctx, snap, dsn); e == nil || out != nil || !strings.Contains(e.Error(), "SQLSTATE") || strings.Contains(e.Error(), dsn) {
		t.Fatal("缺少表时没有返回脱敏的 SQL 错误")
	}
}

// TestStorageReportRejectsUnsafeInput 使用空基线、损坏 JSON、无连接及越界参数验证无输出失败；临时基线自动删除。
func TestStorageReportRejectsUnsafeInput(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{}{}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"other"}, {"snapshot"}, {"snapshot", "--days=100"}, {"snapshot", "--deployment=x", "--baseline=" + bad}, {"snapshot", "--deployment=x", "--baseline=" + bad + "missing"}, {"plan", "--unknown"}, {"plan", "--before=bad"}, {"plan", "--before=2026-01-01T00:00:00Z", "--rows=0"}} {
		if out, err := run(t.Context(), args, "postgres://unused"); out != nil || err == nil {
			t.Fatalf("接受错误参数 %v", args)
		}
	}
	if _, err := run(t.Context(), []string{"plan", "--before=2026-01-01T00:00:00Z", "--rows=0"}, "postgres://unused"); err == nil || err.Error() != "invalid planning bounds" {
		t.Fatal("没有给出可操作的参数错误", err)
	}
	if _, err := run(t.Context(), []string{"snapshot"}, ""); err == nil {
		t.Fatal("空连接串被接受")
	}
	c, cancel := context.WithCancel(t.Context())
	cancel()
	if out, err := run(c, []string{"snapshot", "--deployment=fixture"}, "postgres://unused"); err == nil || out != nil {
		t.Fatal("取消后返回了部分报告")
	}
}
