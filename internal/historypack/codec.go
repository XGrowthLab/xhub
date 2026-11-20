// Package historypack 定义单条旧历史的版本、长度与校验契约。调用方始终沿用原有隔离键。
package historypack

import (
	"context"
	"errors"

	"github.com/sunqirui1987/xhub/internal/verifiedzstd"
)

const Marker = "xhub:packed-history:zstd:1"
const Prefix = "session_route:v1:history:"
const MaxRawBytes = verifiedzstd.MaxRawBytes

// Encode 对有界的完整历史生成一帧与原文字节摘要，用于存储预估和实际转换。
func Encode(raw []byte) ([]byte, [32]byte, error) { return verifiedzstd.Encode(raw) }

// Decode 校验旧历史版本后按原长和摘要还原，供网关按原键读取及维护回滚；超限或损坏均返回错误。
func Decode(ctx context.Context, marker string, frame []byte, size int64, digest []byte) ([]byte, error) {
	if marker != Marker {
		return nil, errors.New("invalid packed history version")
	}
	return verifiedzstd.Decode(ctx, frame, size, digest)
}
