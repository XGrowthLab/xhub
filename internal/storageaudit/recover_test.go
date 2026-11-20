package storageaudit

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestExternalOriginalRecovery 模拟帧损坏、旧备份摘要错误、同键后续写入以及多次恢复。
// 使用私有 Postgres 中的真实锁、原始字节和隔离路由；测试结束删除整个 schema。
func TestExternalOriginalRecovery(t *testing.T) {
	db, s, o, keys, raw := packFixture(t)
	ctx := t.Context()
	var created string
	if err := db.QueryRow("SELECT created_at FROM kv WHERE id=$1", keys[0]).Scan(&created); err != nil {
		t.Fatal(err)
	}
	key := strings.TrimPrefix(keys[0], historyPrefix)
	manifest, err := ReadPreimages(strings.NewReader(fmt.Sprintf("%s %x", key, sha256.Sum256([]byte(raw)))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Convert(ctx, db, o, "pack"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE kv SET history_frame='broken' WHERE id=$1", keys[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadHistory(ctx, keys[0], "fixture"); err == nil {
		t.Fatal("故障注入无效")
	}
	for _, v := range []struct {
		key, created, body string
		orig               *Preimages
	}{
		{key, "bad", raw, manifest}, {"bad", created, raw, manifest}, {key, created, raw, nil}, {key, created, "{}", manifest}, {key, created, "[]", manifest},
		{strings.Repeat("f", 64), created, raw, manifest},
	} {
		if done, e := RecoverOriginal(ctx, db, v.key, v.created, []byte(v.body), v.orig); e == nil || done {
			t.Fatal("接受了缺少可信摘要的原件", e)
		}
	}
	if done, e := RecoverOriginal(ctx, db, key, time.Unix(1, 0).UTC().Format(time.RFC3339), []byte(raw), manifest); e != nil || done {
		t.Fatal("恢复覆盖了创建时间不同的版本", e)
	}
	if done, e := RecoverOriginal(ctx, db, key, created, []byte(raw), manifest); e != nil || !done {
		t.Fatal("外部备份无法恢复损坏帧", e)
	}
	if got, e := s.ReadHistory(ctx, keys[0], "fixture"); e != nil || got != raw {
		t.Fatal("恢复后权威历史不等于原文", e)
	}
	if done, e := RecoverOriginal(ctx, db, key, created, []byte(raw), manifest); e != nil || done {
		t.Fatal("重复写入了已恢复的历史", e)
	}
	if e := s.WriteHistory(ctx, keys[0], "fixture", `[{"new":true}]`, true); e != nil {
		t.Fatal(e)
	}
	if done, e := RecoverOriginal(ctx, db, key, created, []byte(raw), manifest); e != nil || done {
		t.Fatal("过期备份覆盖了后续分块写入", e)
	}
}
