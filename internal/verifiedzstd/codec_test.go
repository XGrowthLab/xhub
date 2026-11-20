package verifiedzstd

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
		if _, err = Decode(t.Context(), frame, int64(len(raw)), digest[:]); err != nil {
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
	if _, err = Decode(ctx, frame, int64(len(raw)), digest[:]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("等待解码未按请求超时取消", err)
	}
}
