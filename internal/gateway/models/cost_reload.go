// Package models 提供市场资料同步和计划管理；同步不自动替换已部署模型的计费价格。
package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sunqirui1987/xhub/internal/util"
)

const (
	costReloadNS  = "model_cost_map"
	costReloadKey = "reload"
)

type costReloadPlan struct {
	Scheduled     bool
	IntervalHours *int
	LastRun       *string
	NextRun       *string
	LastAttempt   *string
	LastError     string
}

var logTraceOnceCostReload sync.Once

// ReloadCostMap 立即同步市场资料；s 为宿主，w/r 为管理接口响应和请求。
// 成功返回模型数，失败保留上次快照并返回 502；最长等待一分钟，不修改部署价格。
// 管理员页面调用，权限沿用 RequireManage；regression/E2E 验证同步、失败与计费隔离。
func ReloadCostMap(s Host, w http.ResponseWriter, r *http.Request) {
	logTraceOnceCostReload.Do(func() { util.Trace("enter models.ReloadCostMap") })

	if s.RequireManage(w, r) == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	n, err := refreshLocalCatalog(ctx, s)
	if err != nil {
		util.Error("price catalog reload failed: %v", err)
		util.WriteError(w, 502, "upstream_error", err.Error())
		return
	}
	util.WriteJSON(w, 200, map[string]any{
		"status":       "success",
		"models_count": n,
		"source":       priceFeedURL(),
	})
}

// ScheduleCostMapReload 保存 1～168 小时的同步计划，并确保后台循环已启动。
// s 为宿主，w/r 为管理接口响应和请求；非法间隔返回 400，写入失败返回 500。
// 管理页面调用；事务串行修改计划，避免慢来源覆盖管理员的新设置。
func ScheduleCostMapReload(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	hours, err := strconv.Atoi(r.URL.Query().Get("hours"))
	if err != nil || hours < 1 || hours > 168 {
		util.WriteError(w, 400, "invalid_request", "hours must be a whole number between 1 and 168")
		return
	}
	next := time.Now().UTC().Add(time.Duration(hours) * time.Hour).Format(time.RFC3339)
	if err := updateCostReload(r.Context(), s, func(plan *costReloadPlan) {
		plan.Scheduled = true
		plan.IntervalHours = &hours
		plan.NextRun = &next
	}); err != nil {
		util.WriteError(w, 500, "internal", err.Error())
		return
	}
	StartScheduledReload(s)
	util.WriteJSON(w, 200, map[string]any{
		"status":         "success",
		"interval_hours": hours,
		"next_run":       next,
	})
}

// CancelCostMapReload 关闭同步计划并保留最近同步状态，重启不会重新启用。
// s 为宿主，w/r 为管理接口响应和请求；成功返回 200，写入失败返回 500。
// 管理页面调用；等待正在执行的同步事务结束，防止其重新开启计划。
func CancelCostMapReload(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	if err := updateCostReload(r.Context(), s, func(plan *costReloadPlan) {
		plan.Scheduled = false
		plan.IntervalHours = nil
		plan.NextRun = nil
	}); err != nil {
		util.WriteError(w, 500, "internal", err.Error())
		return
	}
	util.WriteJSON(w, 200, map[string]any{"status": "success"})
}

// CostMapReloadStatus 返回同步开关、间隔及最近成功/失败状态；参数为宿主和 HTTP 上下文。
// 管理页面调用，无存储时关闭，数据库读取失败返回 500，不能伪装成关闭或成功。
// TestMarketStatusFailure 与真实同步回归验证失败路径和可观察状态，无写入副作用。
func CostMapReloadStatus(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	if s.RecordStore() == nil {
		util.WriteJSON(w, 200, (costReloadPlan{}).public())
		return
	}
	plan, err := readCostReload(r.Context(), s.RecordStore().DB)
	if err != nil {
		util.WriteError(w, 500, "internal", "market schedule unavailable")
		return
	}
	util.WriteJSON(w, 200, plan.public())
}

// loadCostReload reads the saved timer. A missing record is treated as off.
// 参数 s（Host）：载入费用Reload使用的数据面宿主。
// 返回 costReloadPlan（costReloadPlan）：已保存的价格重载计时。没有记录或没有库时是关闭的零值。
// 调用：仅在 cost_reload.go 内使用
// 测试：无直接单测
func loadCostReload(s Host) costReloadPlan {
	if s.RecordStore() == nil {
		return costReloadPlan{}
	}
	plan, _ := readCostReload(context.Background(), s.RecordStore().DB)
	return plan
}

// readCostReload 从事务或数据库读取计划，无记录表示未启用；坏 JSON 和数据库错误必须传播。
// 市场同步在同一锁内调用以避免多实例重复抓取；无写入副作用。
func readCostReload(ctx context.Context, db catalogReader) (costReloadPlan, error) {
	var value string
	err := db.QueryRowContext(ctx, `SELECT value_json FROM proxy_config WHERE namespace=$1 AND key=$2`, costReloadNS, costReloadKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return costReloadPlan{}, nil
	}
	if err != nil {
		return costReloadPlan{}, err
	}
	var raw struct {
		Scheduled     bool    `json:"scheduled"`
		IntervalHours *int    `json:"interval_hours"`
		LastRun       *string `json:"last_run"`
		NextRun       *string `json:"next_run"`
		LastAttempt   *string `json:"last_attempt"`
		LastError     string  `json:"last_error"`
	}
	if err = json.Unmarshal([]byte(value), &raw); err != nil {
		return costReloadPlan{}, err
	}
	return costReloadPlan{raw.Scheduled, raw.IntervalHours, raw.LastRun, raw.NextRun, raw.LastAttempt, raw.LastError}, nil
}

// saveCostReload stores the timer. The next status read uses this record.
// 参数 s（Host）：保存费用Reload使用的数据面宿主；plan（costReloadPlan）：保存费用Reload使用的costReloadPlan。
// 返回 error（error）：失败原因，nil 表示这一步成功。
// 调用：仅在 cost_reload.go 内使用
// 测试：无直接单测
func saveCostReload(s Host, plan costReloadPlan) error {
	if s.RecordStore() == nil {
		return nil
	}
	return s.RecordStore().PutConfig(costReloadNS, costReloadKey, plan.public())
}

// public is the JSON the dashboard reads for a cost reload plan. An unset time is null.
// 参数：无。
// 返回 map[string]any（map[string]any）：公开的字段表。缺键表示上游或库里没有这个字段。
// 调用：仅在 cost_reload.go 内使用
// 测试：无直接单测
func (p costReloadPlan) public() map[string]any {
	var hours, last, next any
	if p.IntervalHours != nil {
		hours = *p.IntervalHours
	}
	if p.LastRun != nil {
		last = *p.LastRun
	}
	if p.NextRun != nil {
		next = *p.NextRun
	}
	return map[string]any{
		"scheduled":      p.Scheduled,
		"interval_hours": hours,
		"last_run":       last,
		"next_run":       next,
		"last_attempt":   p.LastAttempt, "last_error": p.LastError,
	}
}

// updateCostReload 在同步共用锁内重读并修改计划，避免慢请求覆盖管理员刚保存的开关。
// ctx 为请求上下文，change 只修改计划字段；返回存储错误，无数据库时不能报告保存成功。
func updateCostReload(ctx context.Context, s Host, change func(*costReloadPlan)) error {
	if s.RecordStore() == nil {
		return errors.New("market store unavailable")
	}
	return s.RecordStore().MarketTransaction(ctx, func(tx *sql.Tx) error {
		plan, err := readCostReload(ctx, tx)
		if err != nil {
			return err
		}
		change(&plan)
		return writeCatalogJSON(ctx, tx, costReloadNS, costReloadKey, plan.public())
	})
}
