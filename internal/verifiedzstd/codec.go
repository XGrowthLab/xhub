// Package verifiedzstd 为持久化历史和诊断提供共用的有界 Zstandard 编解码与原文字节校验。
package verifiedzstd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const MaxRawBytes = 64 << 20

// 允许四条记录同时解码，输出缓冲的在途上限为 256 MiB。等待者可由请求上下文取消。
const decodeConcurrency = 4

var decodeSlots = make(chan struct{}, decodeConcurrency)

// 编码器与解码器支持并发 EncodeAll/DecodeAll。维护任务逐条试算，网关只在读取已转换的数据时分配解压缓冲。
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
		return nil, digest, errors.New("invalid compressed record length")
	}
	e, err := encoder()
	if err != nil {
		return nil, digest, err
	}
	return e.EncodeAll(raw, nil), digest, nil
}

// Decode 按可信上限、声明长度和原始 SHA-256 还原单行。调用方先校验各自存储格式的版本，解码失败保留原记录。
func Decode(ctx context.Context, frame []byte, size int64, digest []byte) ([]byte, error) {
	if size < 1 || size > MaxRawBytes || len(digest) != sha256.Size || len(frame) == 0 {
		return nil, errors.New("invalid compressed record metadata")
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
		return nil, errors.New("invalid compressed record data")
	}
	check := sha256.Sum256(raw)
	if !bytes.Equal(check[:], digest) {
		return nil, errors.New("compressed record digest mismatch")
	}
	return raw, nil
}
