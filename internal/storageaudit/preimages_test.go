package storageaudit

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// TestVerifiedPreimages 固定格式、上界与顺序，确保清单没有因截断或重复键而失去原始版本约束。纯内存，无需清理。
func TestVerifiedPreimages(t *testing.T) {
	key := strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	row := key + " " + digest
	p, err := ReadPreimages(strings.NewReader(row + "\n"))
	if err != nil || len(p.hashes) != 1 || len(p.id) != 64 {
		t.Fatal("合法备份清单未被识别", err)
	}
	for _, v := range []string{"", row + "\n" + row, row + "\n" + strings.Repeat("0", 64) + " " + digest, strings.ToUpper(key) + " " + digest, key + " bad", row + " " + key, strings.Repeat("x", 512)} {
		if _, err = ReadPreimages(strings.NewReader(v)); err == nil {
			t.Fatal("不完整或歧义的清单被接受")
		}
	}
}

// TestPackedHistoryVerifiedBackup 在私有 PostgreSQL 写两个候选、创建一份恢复过的原始摘要，再模拟后续同键写入。
// 检查未备份和已变化的版本均保留，匹配时转换；续跑游标绑定清单，整个 schema 在结束后删除。
func TestPackedHistoryVerifiedBackup(t *testing.T) {
	db, s, o, keys, raw := packFixture(t)
	key := strings.TrimPrefix(keys[0], historyPrefix)
	original := sha256.Sum256([]byte(raw))
	good, err := ReadPreimages(strings.NewReader(fmt.Sprintf("%s %x\n", key, original)))
	if err != nil {
		t.Fatal(err)
	}
	old, err := ReadPreimages(strings.NewReader(key + " " + strings.Repeat("0", 64)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	o.Preimages = old
	p, err := Convert(ctx, db, o, "pack")
	if err != nil || p.Selected != 0 || p.Unbacked != 2 {
		t.Fatal("没有原始恢复点的行被修改", p, err)
	}
	o.Preimages = good
	o.Rows = 1
	p, err = Convert(ctx, db, o, "pack")
	if err != nil || p.Selected != 1 || !p.More {
		t.Fatal("匹配原始恢复点的行未能转换", p, err)
	}
	o.Cursor = p.Next
	o.Preimages = old
	if _, err = Convert(ctx, db, o, "pack"); err == nil {
		t.Fatal("恢复清单改变后仍接受旧游标")
	}
	o.Cursor = ""
	o.Preimages = good
	if _, err = Convert(ctx, db, o, "restore"); err == nil {
		t.Fatal("回退操作错误接受前向清单")
	}
	if err = s.WriteHistory(ctx, keys[0], "fixture", strings.ReplaceAll(raw, "tool result", "new version"), false); err != nil {
		t.Fatal(err)
	}
	p, err = Convert(ctx, db, o, "pack")
	if err != nil || p.Unbacked != 1 || p.Selected != 0 {
		t.Fatal("备份完成后同键更新的版本被覆盖", p, err)
	}
}
