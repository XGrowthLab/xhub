package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
	_ "time/tzdata"

	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/gateway"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/store"
	"github.com/sunqirui1987/xhub/internal/util"
)

// defaultGatewayIdleConns 供启动配置处理空闲上限缺省值：未设置连接上限时保持 Go 原有的 2 条，
// 明确限定容量的池按该容量保留、最多 25 条。只配置连接回收，不立即创建连接。
func defaultGatewayIdleConns(maxOpen int) int {
	if maxOpen > 0 {
		return min(25, maxOpen)
	}
	return 2
}

// databasePoolLimit 解析启动时的非负每池上限；空串采用调用方提供的默认值，显式零保留其配置语义。
// 非整数、负数或溢出只报告参数名，不回显可能被误填的连接串。
func databasePoolLimit(name, raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a nonnegative integer", name)
	}
	return n, nil
}

// gatewayIdlePolicy 根据每池实际最大连接数约束空闲保留数；0 表示没有打开连接数上限。
// 保留数超过 Go 默认 2 时，空闲 5 分钟后归还数据库连接；显式 2 恢复原有保留和空闲寿命。
func gatewayIdlePolicy(maxOpen, maxIdle int) (int, time.Duration) {
	if maxOpen > 0 {
		maxIdle = min(maxIdle, maxOpen)
	}
	if maxIdle <= 2 {
		return maxIdle, 0
	}
	return maxIdle, 5 * time.Minute
}

// limitGatewayPools 只配置网关持有的 IAM 与框架池，在服务接受请求前执行，不预建连接。
// maxOpen 为零时保留各池原有上限，maxIdle 为零时禁用闲置连接复用；容量和寿命分别应用到每个池。
func limitGatewayPools(maxOpen, maxIdle int, identity, framework *sql.DB) {
	for _, pool := range []*sql.DB{identity, framework} {
		if maxOpen > 0 {
			pool.SetMaxOpenConns(maxOpen)
		}
		idle, ttl := gatewayIdlePolicy(pool.Stats().MaxOpenConnections, maxIdle)
		pool.SetMaxIdleConns(idle)
		pool.SetConnMaxIdleTime(ttl)
	}
}

// main 加载网关配置、数据库与连接上限，再启动管理和数据面；启动失败记录阶段并以非零状态退出。
func main() {
	cfgPath := flag.String("config", "configs/config.yaml", "LiteLLM-shaped YAML config")
	addr := flag.String("addr", ":4000", "listen address")
	flag.Parse()

	limit, err := databasePoolLimit("XHUB_DATABASE_MAX_OPEN_CONNS", os.Getenv("XHUB_DATABASE_MAX_OPEN_CONNS"), 0)
	if err != nil {
		util.Error("config: %v", err)
		os.Exit(1)
	}
	idle, err := databasePoolLimit("XHUB_DATABASE_MAX_IDLE_CONNS", os.Getenv("XHUB_DATABASE_MAX_IDLE_CONNS"), defaultGatewayIdleConns(limit))
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
	defer st.Engine.Close()
	limitGatewayPools(limit, idle, db.Engine.DB().DB, st.DB)
	srv := gateway.New(cfg, st, db)
	util.Info("xhub listening on %s", *addr)
	// 数据面和管理面都挂在这一个 Gin 引擎上。只留数据面会让 /key/generate 变成 404。
	if err := srv.Run(*addr); err != nil {
		util.Error("gateway: %v", err)
		os.Exit(1)
	}
}
