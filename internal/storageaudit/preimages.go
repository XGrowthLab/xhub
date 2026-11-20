package storageaudit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Preimages 保存已完成恢复校验的备份清单。仅含存储键摘要和原文字节摘要，供离线批次在锁内比对。
type Preimages struct {
	id     string
	hashes map[string][sha256.Size]byte
}

// ReadPreimages 读取按键摘要严格排序的两列清单，第二列为原文字节的 SHA-256。
// 维护命令在接收端密文回读、源端解密校验后使用。空清单、重复、格式和数量超限均返回错误。
func ReadPreimages(r io.Reader) (*Preimages, error) {
	p := &Preimages{hashes: make(map[string][sha256.Size]byte)}
	fingerprint := sha256.New()
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 256), 256)
	previous := ""
	for s.Scan() {
		line := s.Text()
		key, digest, ok := strings.Cut(line, " ")
		if !ok || !digestPattern.MatchString(key) || !digestPattern.MatchString(digest) || key <= previous || len(p.hashes) >= 1_000_000 {
			return nil, errors.New("invalid verified preimage manifest")
		}
		raw, _ := hex.DecodeString(digest)
		p.hashes[key] = [sha256.Size]byte(raw)
		previous = key
		fmt.Fprintln(fingerprint, line)
	}
	if s.Err() != nil || previous == "" {
		return nil, errors.New("unreadable or empty preimage manifest")
	}
	p.id = hex.EncodeToString(fingerprint.Sum(nil))
	return p, nil
}
