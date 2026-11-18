package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strconv"
	_ "time/tzdata"

	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/gateway"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/store"
	"github.com/sunqirui1987/xhub/internal/util"
)

// databaseMaxOpenConns 解析网关进程的 PostgreSQL 每池连接上限。未配置或 0 保留驱动默认设置；
// 非整数、负数、溢出返回错误，启动方应在打开数据库前报告配置项名称。
func databaseMaxOpenConns(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("XHUB_DATABASE_MAX_OPEN_CONNS must be a nonnegative integer")
	}
	return n, nil
}

// limitGatewayPools 将正上限分别应用于当前网关持有的 IAM 和框架连接池；
// 在启动后台任务与监听 HTTP 之前调用，其他进程、实例或测试夹具的池不会被修改。
func limitGatewayPools(limit int, identity, framework *sql.DB) {
	if limit > 0 {
		identity.SetMaxOpenConns(limit)
		framework.SetMaxOpenConns(limit)
	}
}

// main 加载网关配置、数据库与连接上限，再启动管理和数据面；启动失败记录阶段并以非零状态退出。
func main() {
	cfgPath := flag.String("config", "configs/config.yaml", "LiteLLM-shaped YAML config")
	addr := flag.String("addr", ":4000", "listen address")
	flag.Parse()

	limit, err := databaseMaxOpenConns(os.Getenv("XHUB_DATABASE_MAX_OPEN_CONNS"))
	if err != nil {
		util.Error("config: %v", err)
		os.Exit(1)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		util.Error("config: %v", err)
		os.Exit(1)
	}
	ctx := context.Background()
	// iam owns every identity table: users, teams, organizations, projects,
	// keys and the audit log. store keeps the framework tables — key-value
	// settings, UI sessions, the spend queue — which carry no permissions.
	db, err := iam.Open(ctx, cfg.GeneralSettings.DatabaseURL)
	if err != nil {
		util.Error("iam: %v", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		util.Error("iam migrate: %v", err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.GeneralSettings.DatabaseURL)
	if err != nil {
		util.Error("store: %v", err)
		os.Exit(1)
	}
	limitGatewayPools(limit, db.Engine.DB().DB, st.DB)
	srv := gateway.New(cfg, st, db)
	util.Info("xhub listening on %s", *addr)
	// 数据面和管理面都挂在这一个 Gin 引擎上。只留数据面会让 /key/generate 变成 404。
	if err := srv.Run(*addr); err != nil {
		util.Error("gateway: %v", err)
		os.Exit(1)
	}
}
