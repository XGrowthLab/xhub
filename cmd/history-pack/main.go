// history-pack 对旧会话执行一个有界的转换或反向还原批次，试算由 storage-report plan 提供。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sunqirui1987/xhub/internal/storageaudit"
)

// run 从参数读取本批模式、截止时间和预算。只连接已有 schema，库未升级或未指定执行开关时失败。
// 返回 JSON 只含成功提交的汇总和游标。驱动错误不回显连接地址、键或正文。
func run(ctx context.Context, args []string, dsn string) ([]byte, error) {
	if len(args) > 0 && args[0] == "recover" {
		return recoverFromBackup(ctx, args[1:], dsn, os.Stdin)
	}
	if len(args) < 1 || (args[0] != "pack" && args[0] != "restore") {
		return nil, errors.New("expected pack or restore")
	}
	f := flag.NewFlagSet("history-pack", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var o storageaudit.PlanOptions
	before := f.String("before", "", "fixed exclusive UTC cutoff")
	manifest := f.String("backup-manifest", "", "verified original SHA-256 manifest for this batch")
	execute := f.Bool("execute", false, "commit this batch")
	f.StringVar(&o.OnlyDigest, "key-digest", "", "optional single trusted history digest for targeted recovery")
	f.StringVar(&o.Cursor, "cursor", "", "cursor from the last committed batch of this operation")
	f.Int64Var(&o.MinRawBytes, "min-raw-bytes", 0, "minimum raw bytes; use a fresh cursor for a separate size class")
	f.IntVar(&o.Rows, "rows", 50, "maximum rows per batch")
	f.Int64Var(&o.BatchBytes, "batch-bytes", 16<<20, "maximum raw bytes per batch")
	f.Int64Var(&o.RecordBytes, "record-bytes", 8<<20, "maximum raw bytes per record")
	f.Int64Var(&o.ReserveBytes, "reserve-bytes", 1024, "space allowance per selected row")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || !*execute || dsn == "" {
		return nil, errors.New("explicit mode, --execute, --before and XHUB_DATABASE_URL are required")
	}
	var err error
	if o.Before, err = time.Parse(time.RFC3339, *before); err != nil {
		return nil, errors.New("invalid cutoff")
	}
	if *manifest != "" {
		if args[0] != "pack" {
			return nil, errors.New("backup manifest is only valid for packing")
		}
		file, err := os.Open(*manifest)
		if err != nil {
			return nil, errors.New("could not open verified preimage manifest")
		}
		defer file.Close()
		o.Preimages, err = storageaudit.ReadPreimages(file)
		if err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	defer db.Close()
	p, err := storageaudit.Convert(ctx, db, o, args[0])
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			return nil, fmt.Errorf("batch failed (SQLSTATE %s); retry from last confirmed cursor", pg.Code)
		}
		return nil, errors.New("batch failed; check bounds, schema and record integrity before resuming")
	}
	return json.Marshal(p)
}

// main 限制本批总时长。成功时打印单条报告，失败使用非零退出码，调用方仅在成功后保存游标。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, err := run(ctx, os.Args[1:], os.Getenv("XHUB_DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(p))
}
