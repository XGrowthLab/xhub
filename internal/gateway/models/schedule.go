// Package models 每日同步市场资料；手动和定时同步均不替换部署计费价格。
package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/util"
)

var reloadLoops sync.Map

// StartScheduledReload 为首次启动创建每日计划，并启动已启用计划的后台循环。
// s 为配置宿主；无返回值，无存储或初始化失败时不启动，已关闭计划保持关闭。
// 网关启动和管理端启用计划时调用；按宿主去重，数据库关闭或进程退出时结束。
func StartScheduledReload(s Host) {
	if err := ensureDailyReload(context.Background(), s); err != nil {
		util.Error("initialize market schedule: %v", err)
		return
	}
	plan := loadCostReload(s)
	if !plan.Scheduled || plan.IntervalHours == nil {
		return
	}
	if s.RecordStore() == nil {
		return
	}
	if _, running := reloadLoops.LoadOrStore(s.RecordStore(), true); running {
		return
	}
	go func() { defer reloadLoops.Delete(s.RecordStore()); scheduledReloadLoop(s) }()
}

// reloadNow 从 Modelink 或运维配置的镜像读取完整价格；失败时保留当前目录。
// 参数 ctx（context.Context）：取消和超时。
// 返回模型条数与抓取错误；成功或失败均不修改当前计费目录。
// 调用：来源可达性测试；生产同步使用 refreshCatalog，不修改计费目录。
// 测试：schedule_test.go 的缺失源、失败源与目录保留断言。
func reloadNow(ctx context.Context) (int, error) {
	doc, err := catalog.FetchMarket(ctx, priceFeedURL())
	return len(doc.Models), err
}

// priceFeedURL 解析运行时价格来源；参数无，返回显式镜像或 Modelink 默认地址。
// 调用：手动和定时重载；只解析配置，不发起网络请求。
func priceFeedURL() string {
	if url := strings.TrimSpace(os.Getenv("XHUB_PRICE_FEED_URL")); url != "" {
		return url
	}
	return catalog.MarketURL
}

// scheduledReloadLoop 每分钟检查同步计划，到期只更新市场资料；修改开关与间隔无需重启。
// 参数 s（Host）：能读和保存重载计划的宿主。
// 返回：无。这个循环一直跑到进程退出。失败保留快照和部署价，下一次重试安排在一小时后。
// 调用：StartScheduledReload 在计划已启用时。
// 测试：deployment_prices_test.go 验证计划持久化、并发同步和失败后的重试时间。
func scheduledReloadLoop(s Host) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		// 数据库关闭表示宿主已结束；短暂断连继续等待，避免丢失已保存计划。
		if err := s.RecordStore().DB.Ping(); err != nil {
			if strings.Contains(err.Error(), "database is closed") {
				return
			}
			continue
		}
		plan := loadCostReload(s)
		if !plan.Scheduled {
			continue
		}
		if plan.NextRun == nil {
			continue
		}
		due, err := time.Parse(time.RFC3339, *plan.NextRun)
		if err != nil || time.Now().UTC().Before(due) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		n, err := refreshCatalog(ctx, s, true)
		cancel()
		if err != nil {
			util.Error("scheduled market refresh failed: %v", err)
			continue
		}

		util.Info("scheduled price reload applied %d models", n)
	}
}

// refreshLocalCatalog 拉取并保存完整本地快照；参数为上下文和宿主，返回模型数与错误。
// 手动及定时刷新调用；远程移除条目保留但标记不可售，失败不覆盖目录，上下架状态独立持久化。
func refreshLocalCatalog(ctx context.Context, s Host) (int, error) {
	return refreshCatalog(ctx, s, false)
}

// refreshCatalog 在数据库锁内复核到期时间、抓取来源并保存市场快照和同步状态。
// scheduled 为真时跳过其他实例已完成的轮次；失败保留目录并记录可见错误，一小时后重试。
func refreshCatalog(ctx context.Context, s Host, scheduled bool) (int, error) {
	if s.RecordStore() == nil {
		return 0, fmt.Errorf("local catalog store unavailable")
	}
	var count int
	var fetchErr error
	err := s.RecordStore().MarketTransaction(ctx, func(tx *sql.Tx) error {
		plan, err := readCostReload(ctx, tx)
		if err != nil {
			return err
		}
		if scheduled {
			if !plan.Scheduled || plan.NextRun == nil {
				return nil
			}
			due, err := time.Parse(time.RFC3339, *plan.NextRun)
			if err != nil || time.Now().Before(due) {
				return nil
			}
		}
		// 在数据库锁内拉取，避免较慢的旧响应覆盖新快照；超时由调用方限制。
		var doc catalog.PriceDocument
		fetchCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		doc, fetchErr = catalog.FetchMarket(fetchCtx, priceFeedURL())
		cancel()
		stamp := time.Now().UTC().Format(time.RFC3339)
		plan.LastAttempt = &stamp
		if fetchErr != nil {
			plan.LastError = "market_fetch_failed"
			if plan.Scheduled {
				next := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
				plan.NextRun = &next
			}
			return writeCatalogJSON(ctx, tx, costReloadNS, costReloadKey, plan.public())
		}
		plan.LastError = ""
		plan.LastRun = &stamp
		if plan.Scheduled && plan.IntervalHours != nil {
			next := time.Now().UTC().Add(time.Duration(*plan.IntervalHours) * time.Hour).Format(time.RFC3339)
			plan.NextRun = &next
		}
		if err := writeCatalogJSON(ctx, tx, costReloadNS, costReloadKey, plan.public()); err != nil {
			return err
		}
		count = len(doc.Models)
		prior, err := marketDocument(ctx, tx)
		if err != nil {
			return err
		}
		// 首次同步固化旧计费目录；后续来源更新、离线重启均不会替换它。
		baseline := catalog.PriceDocument{Version: 1, Source: catalog.PriceSource(), GeneratedAt: catalog.PriceGeneratedAt(), Models: map[string]map[string]any{}, Providers: catalog.Providers()}
		for id := range catalog.CostMap() {
			if row, ok := catalog.BaselineModel(id); ok {
				baseline.Models[id] = row
			}
		}
		raw, err := json.Marshal(baseline)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO proxy_config(namespace,key,value_json) VALUES($1,'catalog',$2) ON CONFLICT(namespace,key) DO NOTHING`, priceSnapshotNS, string(raw)); err != nil {
			return err
		}
		return writeCatalogJSON(ctx, tx, marketSnapshotNS, "catalog", retainDelisted(doc, prior))
	})
	if err == nil {
		err = fetchErr
	}
	return count, err
}
