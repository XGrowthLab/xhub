package logframe

import (
	"context"
	"strings"
	"testing"

	"github.com/sunqirui1987/xhub/internal/verifiedzstd"
)

// TestDiagnosticFrame 校验中英文、空白、长度、数据与摘要，确认旧日志和超限诊断的读取边界。
// 使用内存文本模拟完整请求诊断，无需清理。
func TestDiagnosticFrame(t *testing.T) {
	ctx := t.Context()
	raw := " { \"x\":\"" + strings.Repeat("中 log ", 512) + "\" }\n"
	value, err := Encode(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, large, err := Read(ctx, value, int64(len(raw)))
	if err != nil || large || got != raw {
		t.Fatal("诊断未逐字节还原", err)
	}
	if _, large, err = Read(ctx, value, 32); err != nil || !large {
		t.Fatal("旧日志清理提前展开了超大诊断", err)
	}
	if got, large, err = Read(ctx, "old", 3); err != nil || large || got != "old" {
		t.Fatal("旧明文读取异常", err)
	}
	if _, large, err = Read(ctx, "old", 2); err != nil || !large {
		t.Fatal("普通文本未遵守读取上限", err)
	}
	parts := strings.Split(value, ":")
	for _, bad := range []string{Family + "v4", Prefix + "999999999:x:bad", value + "!", Prefix + parts[4] + ":" + strings.Repeat("z", 64) + ":" + parts[6], Prefix + parts[4] + ":" + strings.Repeat("0", 64) + ":" + parts[6], Prefix + "1:" + parts[5] + ":" + strings.Repeat("a", 200)} {
		if _, _, e := Read(ctx, bad, verifiedzstd.MaxRawBytes); e == nil {
			t.Fatal("损坏或未知诊断被接受")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, e := Read(cancelled, value, verifiedzstd.MaxRawBytes); e == nil {
		t.Fatal("已取消仍解压诊断")
	}
	if _, e := Encode(""); e == nil {
		t.Fatal("空诊断进入编码")
	}
}
