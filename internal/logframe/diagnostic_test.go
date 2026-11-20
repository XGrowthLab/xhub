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

// TestDiagnosticDecodeQueueCancellation 模拟一个大日志占用诊断额度。另一个等待者取消后立即退出，
// 已占用的槽释放后，同一份有效诊断可正常还原；通道令牌由 defer 清理。
func TestDiagnosticDecodeQueueCancellation(t *testing.T) {
	value, err := Encode("fixture")
	if err != nil {
		t.Fatal(err)
	}
	func() {
		detailSlot <- struct{}{}
		defer func() { <-detailSlot }()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, _, err := Read(ctx, value, 100); err != context.Canceled {
			t.Fatal("取消的详情读取仍等待解码资源", err)
		}
	}()
	if got, over, err := Read(t.Context(), value, 100); err != nil || over || got != "fixture" {
		t.Fatal("排队取消影响后续详情", err)
	}
}
