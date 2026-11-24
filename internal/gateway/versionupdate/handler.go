// Package versionupdate 把平台管理员的有限部署操作转给主机 Unix socket。
package versionupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/sunqirui1987/xhub/internal/auth"
	"github.com/sunqirui1987/xhub/internal/util"
)

const Path = "/admin/version-update"

var (
	versionPattern  = regexp.MustCompile("^v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\\.(0|[1-9][0-9]*))?$")
	revisionPattern = regexp.MustCompile("^[0-9a-f]{40}$")
)

// AdminGuard 返回已认证的管理主体，失败时由网关写入认证错误。
type AdminGuard func(http.ResponseWriter, *http.Request) *auth.Principal

// candidate 是主机生成的候选版本预览，说明只提供纯文本。
type candidate struct {
	Version    string `json:"version"`
	Revision   string `json:"revision"`
	Notes      string `json:"notes"`
	Prerelease bool   `json:"prerelease"`
	Compatible bool   `json:"compatible"`
}

// statusReply 只公开控制台所需字段，从不代理进程路径、配置或原始错误。
type statusReply struct {
	Enabled          bool       `json:"enabled"`
	Channel          string     `json:"channel,omitempty"`
	Stage            string     `json:"stage,omitempty"`
	Current          *string    `json:"current,omitempty"`
	Candidate        *candidate `json:"candidate,omitempty"`
	Previous         *string    `json:"previous,omitempty"`
	Error            string     `json:"error,omitempty"`
	RecoveryRequired bool       `json:"recovery_required,omitempty"`
	Checked          int64      `json:"checked,omitempty"`
	UpdatedAt        int64      `json:"updated_at,omitempty"`
}

// targetRequest 是浏览器确认过的目标。操作身份从会话取得。
type targetRequest struct {
	Version  string `json:"version,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// Mount 注册检查、升级和回退入口。socket 为空时只返回功能未启用，且不会连接网络。
// 根路径用于状态查询，操作走固定子路径；每次请求单独校验平台管理员会话。
func Mount(reg util.Registrar, guard AdminGuard, socket string) {
	var client *http.Client
	if socket != "" {
		dialer := &net.Dialer{Timeout: 2 * time.Second}
		client = &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socket)
				},
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	for _, operation := range []string{"status", "check", "apply", "rollback"} {
		pattern := "POST " + Path + "/" + operation
		if operation == "status" {
			pattern = "GET " + Path
		}
		reg.Handle(pattern, handler(guard, client, operation))
	}
}

// handler 校验用户意图并只发送有限的部署请求。HTTP 请求不决定上游地址、命令、服务或 Docker 参数。
// 网关重启不取消主机已接受的任务；连接断开时控制台重新读取状态。
func handler(guard AdminGuard, client *http.Client, operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		admin := guard(w, r)
		if admin == nil {
			return
		}
		if !admin.PlatformAdmin() {
			util.WriteError(w, http.StatusForbidden, "forbidden", "Platform administrator session required")
			return
		}
		if client == nil {
			if operation == "status" {
				util.WriteJSON(w, 200, statusReply{})
				return
			}
			util.WriteError(w, http.StatusServiceUnavailable, "update_disabled", "Version updates are disabled")
			return
		}
		var payload []byte
		if operation != "status" {
			target, err := decodeTarget(w, r, operation)
			if err != nil {
				util.WriteError(w, 400, "invalid_update_target", "Invalid version update target")
				return
			}
			payload, _ = json.Marshal(struct {
				targetRequest
				Actor string `json:"actor"`
			}{target, admin.UserID})
		}

		request, err := http.NewRequestWithContext(r.Context(), r.Method, "http://xhub-updater/"+operation, bytes.NewReader(payload))
		if err != nil {
			util.WriteError(w, 503, "update_agent_unavailable", "Version update service unavailable")
			return
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			util.WriteError(w, 503, "update_agent_unavailable", "Version update service unavailable")
			return
		}
		defer response.Body.Close()
		if response.StatusCode == http.StatusServiceUnavailable {
			util.WriteError(w, 503, "update_agent_unavailable", "Version update service unavailable")
			return
		}
		if response.StatusCode == 409 {
			util.WriteError(w, 409, "update_conflict", "Deployment is busy or the selected version changed")
			return
		}

		var state statusReply
		data, err := io.ReadAll(io.LimitReader(response.Body, 32769))
		if err != nil || len(data) > 32768 || response.StatusCode != expectedStatus(operation) ||
			json.Unmarshal(data, &state) != nil || !state.Enabled {
			util.WriteError(w, 502, "update_agent_invalid_response", "Invalid version update service response")
			return
		}
		if release := state.Candidate; release != nil &&
			(!validVersion(release.Version) || !revisionPattern.MatchString(release.Revision)) {
			util.WriteError(w, 502, "update_agent_invalid_response", "Invalid version update service response")
			return
		}
		util.WriteJSON(w, response.StatusCode, state)
	}
}

// validVersion 检查部署接口支持的正式和预发布格式，版本长度与主机协议一致。
func validVersion(version string) bool {
	return len(version) <= 48 && versionPattern.MatchString(version)
}

// expectedStatus 区分同步读状态与持久化任务已接收，拒绝其他成功码。
func expectedStatus(operation string) int {
	if operation == "status" {
		return 200
	}
	return 202
}

// decodeTarget 限制请求大小和字段，检查不同操作所需的版本、提交；非法输入不会连接主机执行器。
func decodeTarget(w http.ResponseWriter, r *http.Request, operation string) (targetRequest, error) {
	var target targetRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		return target, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return target, errors.New("unexpected trailing JSON")
	}
	switch operation {
	case "check":
		if target.Version == "" && target.Revision == "" {
			return target, nil
		}
	case "apply":
		if validVersion(target.Version) && revisionPattern.MatchString(target.Revision) {
			return target, nil
		}
	case "rollback":
		if validVersion(target.Version) && target.Revision == "" {
			return target, nil
		}
	}
	return target, errors.New("invalid operation target")
}
