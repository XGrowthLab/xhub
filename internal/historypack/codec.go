// Package historypack 定义单条旧历史的版本、长度与校验契约。调用方始终沿用原有隔离键。
package historypack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const Marker = "xhub:packed-history:zstd:1"
const Prefix = "session_route:v1:history:"
const MaxRawBytes = 64 << 20

// 允许四条续聊同时解码，输出缓冲的在途上限为 256 MiB。等待者可由请求上下文取消。
const decodeConcurrency = 4

var decodeSlots = make(chan struct{}, decodeConcurrency)

// 编码器与解码器支持并发 EncodeAll/DecodeAll。维护任务逐条试算，网关只在读取已转换历史时分配解压缓冲。
var encoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
})
var decoder = sync.OnceValues(func() (*zstd.Decoder, error) {
	return zstd.NewReader(nil, zstd.WithDecoderConcurrency(decodeConcurrency), zstd.WithDecoderMaxMemory(MaxRawBytes), zstd.WithDecodeAllCapLimit(true))
})

// Encode 为非空原文产生 Zstandard 帧和原始 SHA-256，供试算、实际转换使用。超过单条硬上限时返回错误。
func Encode(raw []byte) ([]byte, [32]byte, error) {
	digest := sha256.Sum256(raw)
	if len(raw) == 0 || len(raw) > MaxRawBytes {
		return nil, digest, errors.New("invalid packed history length")
	}
	e, err := encoder()
	if err != nil {
		return nil, digest, err
	}
	return e.EncodeAll(raw, nil), digest, nil
}

// Decode 按可信上限、声明长度、版本和原始 SHA-256 还原单行。损坏会话不能被当成空历史或回退到过期数据。
func Decode(ctx context.Context, marker string, frame []byte, size int64, digest []byte) ([]byte, error) {
	if marker != Marker || size < 1 || size > MaxRawBytes || len(digest) != sha256.Size || len(frame) == 0 {
		return nil, errors.New("invalid packed history metadata")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case decodeSlots <- struct{}{}:
		defer func() { <-decodeSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	d, err := decoder()
	if err != nil {
		return nil, err
	}
	raw, err := d.DecodeAll(frame, make([]byte, 0, int(size)))
	if err != nil || int64(len(raw)) != size {
		return nil, errors.New("invalid packed history data")
	}
	check := sha256.Sum256(raw)
	if !bytes.Equal(check[:], digest) {
		return nil, errors.New("packed history digest mismatch")
	}
	return raw, nil
}
