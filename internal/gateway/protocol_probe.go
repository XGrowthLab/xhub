package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/gateway/models"
	pd "github.com/sunqirui1987/xhub/internal/provider/deployment"
	"github.com/sunqirui1987/xhub/internal/provider/probe"
	"github.com/sunqirui1987/xhub/internal/router"
	"github.com/sunqirui1987/xhub/internal/store"
	"github.com/sunqirui1987/xhub/internal/util"
)

// ProtocolProbe 手动探测已保存部署，只有平台管理员可以触发；忽略任何客户端地址、密钥或结果字段。
// 参数为 HTTP 请求/响应，返回安全结果或 404/409/503；探测发生供应商调用，尚未保存的编辑不参与。
func (s *Server) ProtocolProbe(w http.ResponseWriter, r *http.Request) {
	p := s.RequireManage(w, r)
	if p == nil {
		return
	}
	if !p.PlatformAdmin() {
		util.WriteError(w, 403, "forbidden", "platform administrator required")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || body.ID == "" {
		util.WriteError(w, 400, "invalid_request", "deployment id required")
		return
	}
	found := false
	for _, m := range s.Models() {
		if router.DeploymentID(m) == body.ID {
			found = true
			break
		}
	}
	if !found {
		util.WriteError(w, 404, "not_found", "deployment not found")
		return
	}
	out, err := s.runProtocolProbe(r.Context(), body.ID, false)
	if err != nil {
		status := 503
		if errors.Is(err, errProbeBusy) || errors.Is(err, errProbeChanged) {
			status = 409
		}
		util.WriteError(w, status, "protocol_probe_unavailable", err.Error())
		return
	}
	util.WriteJSON(w, 200, models.Public(out))
}

var (
	errProbeBusy        = errors.New("protocol probe already running")
	errProbeChanged     = errors.New("configuration changed; probe result was discarded")
	errProbeUnavailable = errors.New("protocol probe storage or configuration unavailable")
)

// ProbeChangedModels 检查保存配置的版本差异，仅对开启自动探测且版本发生变化的数据库部署调用上游。
// ctx 控制维护任务生命周期；生产监听循环与隔离回归共用。失败结果也保存版本，避免对同一故障反复付费调用。
func (s *Server) ProbeChangedModels(ctx context.Context) {
	for _, m := range s.Models() {
		if ctx.Err() != nil {
			return
		}
		if m.ModelInfo["auto_probe"] != true || m.ModelInfo["db_model"] != true || m.Disabled() {
			continue
		}
		if tr, ok := pd.Execution(m); !ok || tr.Family != "chat" {
			continue
		}
		if _, err := s.runProtocolProbe(ctx, router.DeploymentID(m), true); err != nil && !errors.Is(err, errProbeBusy) && !errors.Is(err, errProbeChanged) {
			util.Error("protocol probe could not complete: %v", err)
		}
	}
}

// protocolProbeLoop 轮询本地配置变化，不轮询供应商健康；每五秒比较摘要，未改变的配置不会产生上游请求。
// ctx 由服务器监听生命周期取消；顺序处理部署，每个部署至多并行三个协议，关闭时等待所有请求结束。
func (s *Server) protocolProbeLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		s.ProbeChangedModels(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runProtocolProbe 以数据库互斥锁防止多进程同时探测，释放模型锁后执行网络调用，提交前复核原始及实际连接版本。
// 参数 id 为保存部署，onlyChanged 表示自动任务；返回发布后的部署，失败不发布能力。旧成功能力只在相同连接暂时失败时保留。
func (s *Server) runProtocolProbe(ctx context.Context, id string, onlyChanged bool) (config.ModelEntry, error) {
	if s.Store == nil || s.Store.DB == nil {
		return config.ModelEntry{}, errProbeUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 125*time.Second)
	defer cancel()
	// 事务级 advisory lock 在取消/退出时自动释放，不留下“运行中”任务；schema 隔离测试与不同实例。
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return config.ModelEntry{}, errProbeUnavailable
	}
	defer tx.Rollback()
	var acquired bool
	if err = tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock(hashtext(current_schema() || ':xhub-protocol-probe'))").Scan(&acquired); err != nil {
		return config.ModelEntry{}, errProbeUnavailable
	}
	if !acquired {
		return config.ModelEntry{}, errProbeBusy
	}
	var original config.ModelEntry
	for _, m := range s.Models() {
		if router.DeploymentID(m) == id {
			original = m
			break
		}
	}
	if original.ModelName == "" || original.Disabled() || original.ModelInfo["db_model"] != true {
		return original, errProbeUnavailable
	}
	tr, ok := pd.Execution(original)
	if !ok || tr.Family != "chat" {
		return original, errProbeUnavailable
	}
	attached, err := s.AttachCredential(original)
	if err != nil {
		return original, errProbeUnavailable
	}
	signature, connection := pd.ProbeSignature(original), pd.ProbeSignature(attached)
	previous := pd.ProbeRecord(original)
	same := previous != nil && previous["config_signature"] == signature && previous["connection_signature"] == connection
	if onlyChanged && (original.ModelInfo["auto_probe"] != true || same) {
		return original, nil
	}
	// 只有手动重试或连接变化才再次调用；记录在网络操作前，避免写库失败后的五秒循环。
	s.mu.Lock()
	if s.probeAttempts == nil {
		s.probeAttempts = map[string]string{}
	}
	attempt := signature + connection
	if onlyChanged && s.probeAttempts[id] == attempt {
		s.mu.Unlock()
		return original, nil
	}
	s.probeAttempts[id] = attempt
	s.mu.Unlock()
	protocols := probe.Protocols()
	results := make([]probe.Result, len(protocols))
	var wg sync.WaitGroup
	for i, p := range protocols {
		wg.Add(1)
		go func(i int, p string) { defer wg.Done(); results[i] = probe.Run(ctx, s.HTTPClient(), attached, p) }(i, p)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return original, errProbeUnavailable
	}
	verified := map[string]any{}
	if same {
		if old, ok := previous["verified"].(map[string]any); ok {
			for k, v := range old {
				verified[k] = v
			}
		}
	}
	for _, r := range results {
		if r.Status == "verified" {
			verified[r.Protocol] = true
		} else if r.Status != "temporarily_failed" {
			delete(verified, r.Protocol)
		}
	}
	record := map[string]any{"config_signature": signature, "connection_signature": connection, "verified": verified, "results": results, "checked_at": time.Now().UTC().Format(time.RFC3339)}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, current := range s.Cfg.ModelList {
		if router.DeploymentID(current) != id {
			continue
		}
		actual, err := s.AttachCredential(current)
		if err != nil || pd.ProbeSignature(current) != signature || pd.ProbeSignature(actual) != connection || current.Disabled() || (onlyChanged && current.ModelInfo["auto_probe"] != true) {
			return original, errProbeChanged
		}
		updated := config.CloneModels([]config.ModelEntry{current})[0]
		updated.ModelInfo["protocol_probe"] = record
		directory := append([]config.ModelEntry(nil), s.Cfg.ModelList...)
		directory[i] = updated
		row := store.ProxyModel{ID: id, ModelName: current.ModelName, Params: current.LiteLLMParams, Info: current.ModelInfo}
		saved, err := s.Store.SaveProtocolProbe(ctx, row, record)
		if err != nil {
			return original, errProbeUnavailable
		}
		if !saved {
			return original, errProbeChanged
		}
		s.Cfg.ModelList = directory
		return updated, nil
	}
	return original, errProbeChanged
}
