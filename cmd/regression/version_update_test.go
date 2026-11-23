package regression

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const versionUpdatePath = "/admin/version-update"

// TestVersionUpdateAdminBoundary 真实 PostgreSQL 网关和本机 Unix HTTP 夹具验证权限、输入契约及错误处理。
// 平台管理员提交才到执行器，普通成员、主密钥、非法镜像字段均被拦截。清理独立 schema、socket 和监听器。
func TestVersionUpdateAdminBoundary(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "xhub-updater-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "agent.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan map[string]string, 1)
	agent := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			_, _ = w.Write([]byte("{\"enabled\":true,\"stage\":\"idle\",\"current\":\"v0.1.0-beta.1\",\"host_path\":\"secret\"}"))
		case "/apply":
			var target map[string]string
			if err := json.NewDecoder(r.Body).Decode(&target); err != nil {
				w.WriteHeader(400)
				return
			}
			seen <- target
			w.WriteHeader(202)
			_, _ = w.Write([]byte("{\"enabled\":true,\"stage\":\"pulling\"}"))
		default:
			w.WriteHeader(409)
		}
	})}
	t.Cleanup(func() { _ = agent.Close() })
	go func() { _ = agent.Serve(ln) }()
	t.Setenv("XHUB_UPDATE_SOCKET", socket)
	h := newHarness(t)
	admin := h.adminSession()
	tenant := h.newTenant(t, admin, "updates")
	member := h.signIn(t, tenant)
	version := map[string]any{"version": "v0.1.0-beta.2", "revision": strings.Repeat("1", 40)}
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {member, 403}, {h.master, 403}} {
		r := h.do("POST", versionUpdatePath+"/apply", tc.token, version)
		if r.status != tc.status {
			t.Fatalf("permission denied request got %d, want %d", r.status, tc.status)
		}
	}
	r := h.do("GET", versionUpdatePath, admin, nil)
	if r.status != 200 || r.json()["current"] != "v0.1.0-beta.1" || r.json()["host_path"] != nil {
		t.Fatalf("unexpected version state: %+v", r)
	}
	invalid := h.do("POST", versionUpdatePath+"/apply", admin, map[string]any{"image": "external"})
	if invalid.status != 400 {
		t.Fatalf("arbitrary image got %d", invalid.status)
	}
	if len(seen) != 0 {
		t.Fatal("rejected requests reached the update agent")
	}
	applied := h.do("POST", versionUpdatePath+"/apply", admin, version)
	if applied.status != 202 || applied.json()["stage"] != "pulling" {
		t.Fatalf("apply response: %+v", applied)
	}
	select {
	case request := <-seen:
		if request["version"] != version["version"] || request["revision"] != version["revision"] || request["actor"] == "" || len(request) != 3 {
			t.Fatalf("invalid host payload: %v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("verified operation did not reach the agent")
	}
	if r := h.do("POST", versionUpdatePath+"/check", admin, map[string]any{}); r.status != 409 {
		t.Fatalf("concurrent operation got %d", r.status)
	}
	_ = agent.Close()
	if r := h.do("GET", versionUpdatePath, admin, nil); r.status != 503 {
		t.Fatalf("offline executor got %d", r.status)
	}
}
