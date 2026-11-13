package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/sunqirui1987/xhub/internal/catalog"
)

const marketSnapshotNS = "market_snapshot"

// catalogReader 允许目录读取共用数据库和已加锁事务；只暴露参数化查询。
type catalogReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// marketDocument 读取独立市场快照；没有快照时返回已接受的目录，供离线首次启动使用。
// ctx 控制读取，db 为数据库或事务；错误向调用方传播，不把读取失败伪装成空市场。
func marketDocument(ctx context.Context, db catalogReader) (catalog.PriceDocument, error) {
	doc := catalog.PriceDocument{Version: 1, Source: catalog.PriceSource(), GeneratedAt: catalog.PriceGeneratedAt(), Models: catalog.CostMap(), Providers: catalog.Providers()}
	if db == nil {
		return doc, nil
	}
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value_json FROM proxy_config WHERE namespace=$1 AND key='catalog'`, marketSnapshotNS).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return doc, nil
	}
	if err != nil {
		return doc, err
	}
	err = json.Unmarshal([]byte(raw), &doc)
	return doc, err
}

// readMarket 供管理端读取市场资料，参数为请求上下文和宿主；返回完整快照或存储错误，无计费副作用。
func readMarket(ctx context.Context, s Host) (catalog.PriceDocument, error) {
	if s.RecordStore() == nil {
		return marketDocument(ctx, nil)
	}
	return marketDocument(ctx, s.RecordStore().DB)
}

// writeCatalogJSON 在调用方事务内保存目录或计划；namespace/key 指定记录，value 必须可 JSON 编码。
// 仅市场同步及计划管理调用；返回数据库或编码错误，不独立提交。
func writeCatalogJSON(ctx context.Context, tx *sql.Tx, namespace, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO proxy_config(namespace,key,value_json) VALUES($1,$2,$3) ON CONFLICT(namespace,key) DO UPDATE SET value_json=EXCLUDED.value_json`, namespace, key, string(raw))
	return err
}

// retainDelisted 保留新来源未返回的历史模型，标记来源下架；返回新快照，不修改旧行。
// 同步调用；再次返回的模型自然恢复来源状态，管理员手动隐藏状态独立保存。
func retainDelisted(doc, prior catalog.PriceDocument) catalog.PriceDocument {
	for id, row := range prior.Models {
		if _, found := doc.Models[id]; found {
			continue
		}
		// 只有来源条目适用撤回规则；人工目录继续独立维护。
		if row["market_catalog"] != true {
			continue
		}
		copy := make(map[string]any, len(row)+1)
		for key, value := range row {
			copy[key] = value
		}
		copy["feed_unavailable"] = true
		doc.Models[id] = copy
	}
	return doc
}

// ensureDailyReload 首次使用时创建每天一次的同步计划；已存在的计划（包括手动关闭）保持不变。
// 启动调用，首次运行安排在一分钟内；数据库失败返回错误，不写入虚假启用状态。
func ensureDailyReload(ctx context.Context, s Host) error {
	if s.RecordStore() == nil {
		return nil
	}
	hours := 24
	next := time.Now().UTC().Format(time.RFC3339)
	raw, _ := json.Marshal((costReloadPlan{Scheduled: true, IntervalHours: &hours, NextRun: &next}).public())
	_, err := s.RecordStore().DB.ExecContext(ctx, `INSERT INTO proxy_config(namespace,key,value_json) VALUES($1,$2,$3) ON CONFLICT(namespace,key) DO NOTHING`, costReloadNS, costReloadKey, string(raw))
	return err
}

// marketLoadedAt 返回最近成功同步时间；无同步时回退启动目录时间，供目录状态展示，无写入。
func marketLoadedAt(s Host) any {
	if plan := loadCostReload(s); plan.LastRun != nil {
		return *plan.LastRun
	}
	return catalog.LoadedAt()
}
