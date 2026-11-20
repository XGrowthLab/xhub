package historypack

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// TestVersionedFrames 验证原始字节、版本、解压长度上界和摘要；使用含非 ASCII、工具参数与空白的内存夹具，无需清理。
func TestVersionedFrames(t *testing.T) {
	raw := bytes.Repeat([]byte("  [\"历史\",9007199254740993]\n"), 512)
	f, d, err := Encode(raw)
	if err != nil || len(f) >= len(raw) {
		t.Fatal("没有生成可压缩的测试帧", err)
	}
	v, err := Decode(t.Context(), Marker, f, int64(len(raw)), d[:])
	if err != nil || !bytes.Equal(raw, v) {
		t.Fatal("原文字节被改变", err)
	}
	for _, c := range []struct {
		name, marker string
		frame        []byte
		size         int64
		hash         []byte
	}{
		{"version", "xhub:packed-history:zstd:2", f, int64(len(raw)), d[:]}, {"zero", Marker, f, 0, d[:]},
		{"limit", Marker, f, MaxRawBytes + 1, d[:]}, {"short", Marker, f, 5, d[:]},
		{"long", Marker, f, int64(len(raw) + 1), d[:]}, {"broken", Marker, f[:4], int64(len(raw)), d[:]},
		{"digest", Marker, f, int64(len(raw)), make([]byte, 32)}, {"empty", Marker, nil, 1, d[:]},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, e := Decode(t.Context(), c.marker, c.frame, c.size, c.hash); e == nil {
				t.Fatal("损坏帧被接受")
			}
		})
	}
	if _, _, err = Encode(nil); err == nil {
		t.Fatal("空原文被接受")
	}
}

// TestDecodeQueueCancellation 模拟大记录占据解码槽。其余槽可继续还原，槽全部占用时等待会遵守请求超时。
// 使用内存帧，所有模拟的槽在测试结束后释放。
func TestDecodeQueueCancellation(t *testing.T) {
	raw := []byte("[1]")
	frame, digest, err := Encode(raw)
	if err != nil {
		t.Fatal(err)
	}
	decodeSlots <- struct{}{}
	for i := 1; i < decodeConcurrency; i++ {
		if _, err = Decode(t.Context(), Marker, frame, int64(len(raw)), digest[:]); err != nil {
			t.Fatal("有空闲槽时读取失败", err)
		}
		decodeSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < decodeConcurrency; i++ {
			<-decodeSlots
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err = Decode(ctx, Marker, frame, int64(len(raw)), digest[:]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("等待解码未按请求超时取消", err)
	}
}
