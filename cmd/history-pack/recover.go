package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"github.com/sunqirui1987/xhub/internal/historypack"
	"github.com/sunqirui1987/xhub/internal/storageaudit"
	"io"
	"os"
)

// recoverFromBackup 只接收标准输入传来的单条可信原文。命令行参数绑定已校验清单、原键与创建时间，
// 不接受含正文的命令行参数。返回是否实际恢复，错误不向终端暴露数据库连接或客户内容。
func recoverFromBackup(ctx context.Context, args []string, dsn string, input io.Reader) ([]byte, error) {
	f := flag.NewFlagSet("recover", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	execute := f.Bool("execute", false, "restore one checked original")
	key := f.String("key-digest", "", "original trusted history key digest")
	created := f.String("created-at", "", "exact original UTC creation time")
	path := f.String("backup-manifest", "", "independently verified key and SHA-256 manifest")
	if f.Parse(args) != nil || f.NArg() != 0 || !*execute || *key == "" || *created == "" || *path == "" || dsn == "" {
		return nil, errors.New("recover requires --execute, --key-digest, --created-at and --backup-manifest")
	}
	file, err := os.Open(*path)
	if err != nil {
		return nil, errors.New("could not open verified preimage manifest")
	}
	defer file.Close()
	manifest, err := storageaudit.ReadPreimages(file)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(input, historypack.MaxRawBytes+1))
	if err != nil || len(raw) > historypack.MaxRawBytes {
		return nil, errors.New("unreadable or oversized recovery record")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	defer db.Close()
	restored, err := storageaudit.RecoverOriginal(ctx, db, *key, *created, raw, manifest)
	if err != nil {
		return nil, errors.New("recovery failed; verify original, manifest, schema and database availability")
	}
	return json.Marshal(struct {
		Recovered bool `json:"recovered"`
	}{restored})
}
