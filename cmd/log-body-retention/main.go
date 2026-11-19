// log-body-retention 检查或清理单批旧版日志正文。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sunqirui1987/xhub/internal/gateway/requestlog"
)

// run 解析单批维护参数并输出仅含计数和游标的 JSON。args 为命令行参数，dsn 为数据库地址，now 用于核验 30 天边界。
// 默认使用只读事务，--execute 才执行清理。命令结束关闭连接，不初始化或迁移数据库，也不输出连接地址或正文。
func run(args []string, dsn string, now time.Time) ([]byte, error) {
	fs := flag.NewFlagSet("log-body-retention", flag.ContinueOnError)
	before := fs.String("before", "", "fixed RFC3339 cutoff; at least 30 days before now")
	after := fs.Int64("after", 0, "resume after this usage-event sequence number")
	limit := fs.Int("limit", 100, "maximum usage-event rows to check per batch (1-500)")
	execute := fs.Bool("execute", false, "permanently clear eligible legacy body copies")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	cutoff, err := time.Parse(time.RFC3339Nano, *before)
	if err != nil || cutoff.After(now.Add(-30*24*time.Hour)) {
		return nil, fmt.Errorf("--before requires an RFC3339 cutoff at least 30 days old")
	}
	if fs.NArg() != 0 || *after < 0 || *limit < 1 || *limit > 500 {
		return nil, fmt.Errorf("invalid batch parameters")
	}
	if dsn == "" {
		return nil, fmt.Errorf("XHUB_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("database unavailable")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	batch, err := requestlog.ProcessLegacyLogBatch(ctx, db, cutoff, *after, *limit, *execute)
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) {
			return nil, fmt.Errorf("retention batch failed (SQLSTATE %s); retry from the same cursor", databaseError.Code)
		}
		return nil, fmt.Errorf("retention batch failed; retry from the same cursor")
	}
	return json.Marshal(batch)
}

// main 把标准输入环境和命令行参数交给 run；失败以非零退出码退出，便于维护脚本暂停后续批次。
func main() {
	out, err := run(os.Args[1:], os.Getenv("XHUB_DATABASE_URL"), time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}
