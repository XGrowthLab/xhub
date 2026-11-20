// Package logframe 为大诊断提供可在原有 PostgreSQL 文本列中存储的版本化无损编码。
// 异步任务的正常覆盖仍直接替换整列；只有离线维护命令会写此格式。
package logframe

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/sunqirui1987/xhub/internal/verifiedzstd"
)

const Family = "xhub:diagnostic:"
const Prefix = Family + "zstd:1:"

// Encode 把单条脱敏诊断的完整 UTF-8 字节编码成适用于 TEXT 的 ASCII 值，返回内容和错误。
// 返回值包含原始长度和 SHA-256，供维护程序在实际存储有收益时写入。
func Encode(raw string) (string, error) {
	frame, digest, err := verifiedzstd.Encode([]byte(raw))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d:%x:%s", Prefix, len(raw), digest, base64.RawStdEncoding.EncodeToString(frame)), nil
}

// Read 区分普通文本和版本化编码。limit 是调用场景允许的最大原文字节，达到上限前不分配解压缓冲。
// 返回完整正文、超限标志及错误；普通日志详情和旧正文清理各自采用原有上限，损坏帧不会冒充可用诊断。
func Read(ctx context.Context, value string, limit int64) (string, bool, error) {
	if !strings.HasPrefix(value, Family) {
		if int64(len(value)) > limit {
			return "", true, nil
		}
		return value, false, nil
	}
	header, ok := strings.CutPrefix(value, Prefix)
	sizeText, tail, hasSize := strings.Cut(header, ":")
	hash, payload, hasHash := strings.Cut(tail, ":")
	n, err := strconv.ParseInt(sizeText, 10, 64)
	if !ok || !hasSize || !hasHash || err != nil || n < 1 || n > verifiedzstd.MaxRawBytes || len(hash) != sha256.Size*2 || len(payload) == 0 || len(payload) > base64.RawStdEncoding.EncodedLen(int(n)+128) {
		return "", false, errors.New("invalid diagnostic frame header")
	}
	digest, err := hex.DecodeString(hash)
	if err != nil {
		return "", false, errors.New("invalid diagnostic checksum header")
	}
	if n > limit {
		return "", true, nil
	}
	frame, err := base64.RawStdEncoding.DecodeString(payload)
	if err != nil {
		return "", false, errors.New("invalid diagnostic frame text")
	}
	raw, err := verifiedzstd.Decode(ctx, frame, n, digest)
	return string(raw), false, err
}
