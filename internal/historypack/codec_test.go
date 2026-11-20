package historypack

import (
	"bytes"
	"testing"
)

// TestVersionedFrames 验证原始字节、版本、解压长度上界和摘要；使用含非 ASCII、工具参数与空白的内存夹具，无需清理。
func TestVersionedFrames(t *testing.T) {
	raw := bytes.Repeat([]byte("  [\"历史\",9007199254740993]\n"), 512)
	f, d, err := Encode(raw)
	if err != nil || len(f) >= len(raw) {
		t.Fatal("没有生成可压缩的测试帧", err)
	}
	v, err := Decode(Marker, f, int64(len(raw)), d[:])
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
			if _, e := Decode(c.marker, c.frame, c.size, c.hash); e == nil {
				t.Fatal("损坏帧被接受")
			}
		})
	}
	if _, _, err = Encode(nil); err == nil {
		t.Fatal("空原文被接受")
	}
}
