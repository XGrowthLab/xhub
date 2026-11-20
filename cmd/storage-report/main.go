// storage-report 输出数据库、协议及指定运行目录的容量快照，或只读试算一批旧会话。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sunqirui1987/xhub/internal/storageaudit"
)

// addDirectories 统计操作员明确指定的本机目录已分配字节。du 限同一文件系统，错误时放弃整个快照。
// 各类别可有包含关系，报告保留独立数字；只记录固定类别，目录路径和文件名不写入结果。
func addDirectories(ctx context.Context, s *storageaudit.Snapshot, dirs map[string]*string) error {
	for _, kind := range slices.Sorted(maps.Keys(dirs)) {
		ptr := dirs[kind]
		if *ptr == "" {
			continue
		}
		info, err := os.Lstat(*ptr)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("invalid directory for %s", kind)
		}
		path, err := filepath.Abs(*ptr)
		if err != nil {
			return fmt.Errorf("invalid directory for %s", kind)
		}
		raw, err := exec.CommandContext(ctx, "du", "-skx", path).Output()
		words := strings.Fields(string(raw))
		if err != nil || len(words) == 0 {
			return fmt.Errorf("cannot measure %s", kind)
		}
		kib, err := strconv.ParseInt(words[0], 10, 64)
		if err != nil || kib < 0 || kib > 1<<53-1 {
			return fmt.Errorf("invalid size for %s", kind)
		}
		s.Physical["host."+kind] = kib * 1024
	}
	return nil
}

// readBaseline 读取有限大小的已有聚合快照；路径由运维参数提供，损坏、超长或尾随内容使比较失败。
func readBaseline(path string) (*storageaudit.Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("baseline unavailable")
	}
	defer f.Close()
	const limit = 16 << 20
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	var s storageaudit.Snapshot
	if err != nil || len(data) > limit || json.Unmarshal(data, &s) != nil {
		return nil, errors.New("invalid baseline")
	}
	return &s, nil
}

// run 解析单次运维操作并返回 JSON。连接串只从环境传入，不调用建表代码；错误只包含受控阶段或 SQLSTATE。
func run(ctx context.Context, args []string, dsn string) ([]byte, error) {
	if len(args) == 0 || (args[0] != "snapshot" && args[0] != "plan") {
		return nil, errors.New("expected snapshot or plan")
	}
	if dsn == "" {
		return nil, errors.New("XHUB_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.New("invalid database connection")
	}
	defer db.Close()
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var result any
	if args[0] == "snapshot" {
		deployment := fs.String("deployment", "", "stable unique installation label")
		days := fs.Int("days", 7, "UTC calendar days including today (1-31)")
		baseline := fs.String("baseline", "", "earlier snapshot from this installation")
		dirs := make(map[string]*string)
		for _, kind := range []string{"backups", "container-logs", "redis", "billing-spool", "images"} {
			dirs[kind] = fs.String(kind+"-dir", "", "local directory to measure")
		}
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || strings.TrimSpace(*deployment) == "" || *days < 1 || *days > 31 {
			return nil, errors.New("invalid snapshot arguments")
		}
		var previous *storageaudit.Snapshot
		if *baseline != "" {
			previous, err = readBaseline(*baseline)
			if err != nil {
				return nil, err
			}
		}
		s, e := storageaudit.Capture(ctx, db, *deployment, *days)
		if e == nil {
			if e = addDirectories(ctx, s, dirs); e != nil {
				return nil, e
			}
			if previous != nil {
				if e = s.Compare(previous); e != nil {
					return nil, e
				}
			}
		}
		result, err = s, e
	} else {
		before := fs.String("before", "", "fixed exclusive UTC cutoff (RFC3339, whole seconds)")
		cursor := fs.String("cursor", "", "next_cursor from a completed batch")
		rows := fs.Int("rows", 100, "maximum rows to visit")
		batch := fs.Int64("batch-bytes", 32<<20, "maximum raw bytes to fetch per batch")
		record := fs.Int64("record-bytes", 16<<20, "fetch at most this many raw bytes per record")
		reserve := fs.Int64("reserve-bytes", 1024, "estimated row and index overhead per selected record")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
			return nil, errors.New("invalid planning arguments")
		}
		cutoff, e := time.Parse(time.RFC3339, *before)
		if e != nil {
			return nil, errors.New("invalid --before cutoff")
		}
		result, err = storageaudit.Estimate(ctx, db, storageaudit.PlanOptions{Before: cutoff, Cursor: *cursor, Rows: *rows, BatchBytes: *batch, RecordBytes: *record, ReserveBytes: *reserve})
	}
	if err != nil {
		var usage storageaudit.OperatorError
		if errors.As(err, &usage) {
			return nil, usage
		}
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			return nil, fmt.Errorf("%s failed (SQLSTATE %s)", args[0], pg.Code)
		}
		// 驱动错误可能含连接字段，保留操作名即可定位；校验、连接和取消均不输出部分批次。
		return nil, fmt.Errorf("%s failed; check arguments, database access and timeout", args[0])
	}
	return json.MarshalIndent(result, "", "  ")
}

// main 为单次采集设置总截止时间。只在命令完整成功后写出标准输出，便于调用方原子保存及续跑。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	data, err := run(ctx, os.Args[1:], os.Getenv("XHUB_DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}
