// diagnostic-pack 对上游诊断执行按用量事件分页的试算、转换、还原或凭原件修复。
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
	"github.com/sunqirui1987/xhub/internal/verifiedzstd"
)

// run 解析单批边界，只对明确要求执行的 pack/restore/recover 开启写事务。错误只报告受控类型和 SQLSTATE。
// 成功返回该批的统计、固定上界与下一游标，供运维在核对备份及服务读兼容后运行。
func run(ctx context.Context, args []string, dsn string, input io.Reader) ([]byte, error) {
	if len(args) < 1 || dsn == "" {
		return nil, errors.New("mode and XHUB_DATABASE_URL required")
	}
	f := flag.NewFlagSet("diagnostic-pack", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var o storageaudit.DiagnosticOptions
	cutoff := f.String("before", "", "fixed whole-second UTC cutoff")
	exec := f.Bool("execute", false, "commit one maintenance batch")
	manifest := f.String("backup-manifest", "", "verified SHA-256 request identity and original diagnostic digest manifest")
	first := f.Int64("from-event", 0, "optional inclusive first event for a fresh range")
	f.StringVar(&o.Cursor, "cursor", "", "next_cursor from the last committed batch")
	f.Int64Var(&o.Through, "through", 0, "optional inclusive last event for a fresh range")
	f.IntVar(&o.Rows, "rows", 50, "maximum usage rows to visit")
	f.Int64Var(&o.MinBytes, "min-bytes", 16384, "minimum original length to try packing")
	f.Int64Var(&o.RecordBytes, "record-bytes", 8<<20, "maximum raw bytes per diagnostic")
	f.Int64Var(&o.BatchBytes, "batch-bytes", 16<<20, "maximum raw bytes per batch")
	event := f.Int64("event", 0, "single event ID for verified external recovery")
	mode := args[0]
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || *first < 0 {
		return nil, errors.New("invalid diagnostic command flags")
	}
	if mode != "plan" && mode != "pack" && mode != "restore" && mode != "recover" {
		return nil, errors.New("expected plan, pack, restore or recover")
	}
	if *first != 0 {
		if o.Cursor != "" || o.Through < *first {
			return nil, errors.New("a fresh event range requires --through and no resume cursor")
		}
		o.After = *first - 1
	}
	if *exec != (mode != "plan") {
		return nil, errors.New("plan is read-only; pack, restore and recover require --execute")
	}
	var err error
	if mode == "recover" {
		if *event < 1 || *manifest == "" || *cutoff != "" || *first != 0 || o.Through != 0 || o.Cursor != "" {
			return nil, errors.New("recover requires --event and --backup-manifest")
		}
	} else {
		if *event != 0 {
			return nil, errors.New("--event is only used for recovery")
		}
		if o.Before, err = time.Parse(time.RFC3339, *cutoff); err != nil {
			return nil, errors.New("invalid --before")
		}
	}
	if *manifest != "" {
		file, e := os.Open(*manifest)
		if e != nil {
			return nil, errors.New("could not open verified diagnostic manifest")
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
	var result any
	if mode == "recover" {
		raw, e := io.ReadAll(io.LimitReader(input, verifiedzstd.MaxRawBytes+1))
		if e != nil || len(raw) > verifiedzstd.MaxRawBytes {
			return nil, errors.New("unreadable or oversized diagnostic original")
		}
		var recovered bool
		recovered, err = storageaudit.RecoverDiagnostic(ctx, db, *event, raw, o.Preimages)
		result = struct {
			Recovered bool `json:"recovered"`
		}{recovered}
	} else {
		result, err = storageaudit.DiagnosticBatch(ctx, db, o, mode)
	}
	if err != nil {
		var input storageaudit.OperatorError
		var pg *pgconn.PgError
		if errors.As(err, &input) {
			return nil, input
		}
		if errors.As(err, &pg) {
			return nil, fmt.Errorf("diagnostic batch failed (SQLSTATE %s)", pg.Code)
		}
		return nil, errors.New("diagnostic batch failed; check database and frame integrity")
	}
	return json.Marshal(result)
}

// main 给单次运维调用设置总时限。只打印已成功完成的聚合结果，不回显正文、请求 ID 或连接信息。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	value, err := run(ctx, os.Args[1:], os.Getenv("XHUB_DATABASE_URL"), os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(value))
}
