package versionupdate

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunqirui1987/xhub/internal/auth"
	"github.com/sunqirui1987/xhub/internal/authz"
	"github.com/sunqirui1987/xhub/internal/iam"
)

// TestDecodeTarget 校验每种操作的请求字段。只用内存 HTTP 请求，覆盖合法目标、越权参数、尾随 JSON 与超限。
func TestDecodeTarget(t *testing.T) {
	revision := strings.Repeat("1", 40)
	for _, tc := range []struct {
		name, operation, body string
		valid                 bool
	}{
		{"check", "check", "{}", true},
		{"apply", "apply", "{\"version\":\"v0.1.0-beta.1\",\"revision\":\"" + revision + "\"}", true},
		{"rollback", "rollback", "{\"version\":\"v0.1.0\"}", true},
		{"unsafe version", "apply", "{\"version\":\"--help\",\"revision\":\"" + revision + "\"}", false},
		{"missing revision", "apply", "{\"version\":\"v0.1.0\"}", false},
		{"arbitrary image", "apply", "{\"image\":\"elsewhere/app\"}", false},
		{"caller spoof", "check", "{\"actor\":\"other\"}", false},
		{"trailing JSON", "check", "{}{}", false},
		{"oversize", "check", strings.Repeat(" ", 1024) + "{}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", Path, strings.NewReader(tc.body))
			_, err := decodeTarget(httptest.NewRecorder(), r, tc.operation)
			if (err == nil) != tc.valid {
				t.Fatalf("unexpected request validation result: %v", err)
			}
		})
	}
}

// TestDisabledAndAuth 在执行器未配置时验证管理员状态、非管理员拒绝和写入错误，无套接字或状态残留。
func TestDisabledAndAuth(t *testing.T) {
	guard := func(_ http.ResponseWriter, r *http.Request) *auth.Principal {
		kind := authz.KindSession
		if r.Header.Get("X-Test-Key") != "" {
			kind = authz.KindKey
		}
		return &auth.Principal{Kind: kind, Role: iam.RoleAdmin}
	}
	for _, tc := range []struct {
		method, op string
		key        bool
		want       int
	}{
		{"GET", "status", false, 200},
		{"POST", "apply", false, 503},
		{"GET", "status", true, 403},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(tc.method, Path, nil)
		if tc.key {
			r.Header.Set("X-Test-Key", "1")
		}
		handler(guard, nil, tc.op)(w, r)
		if w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s got %d body %s", tc.op, w.Code, w.Body.String())
		}
		if tc.want == 200 && strings.TrimSpace(w.Body.String()) != "{\"enabled\":false}" {
			t.Fatalf("disabled state differs: %s", w.Body.String())
		}
	}
}

// muxRegistrar 安装真实带方法路由，供内存客户端和本机套接字的接口测试使用。
type muxRegistrar struct{ mux *http.ServeMux }

// Handle 把业务模块的路由按原样注册。pattern 和处理器都由测试调用方提供，不启动服务。
func (r muxRegistrar) Handle(pattern string, handler http.HandlerFunc) {
	r.mux.HandleFunc(pattern, handler)
}

// TestUnixBridge 连到一条真实 Unix socket，验证操作身份和状态，过滤内部字段、异常状态和不合法响应。
// 临时套接字、HTTP 服务和线程随测试清理。
func TestUnixBridge(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "xhub-admin-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "a.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan map[string]string, 2)
	agent := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			_, _ = io.WriteString(w, "{\"enabled\":true,\"candidate\":{\"version\":\"v0.1.0-beta.2\",\"revision\":\""+strings.Repeat("1", 40)+"\",\"secret\":\"hidden\"}}")
		case "/check":
			w.WriteHeader(202)
			_, _ = io.WriteString(w, "{\"enabled\":true,\"stage\":\"checking\"}")
		case "/apply":
			var request map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err == nil {
				observed <- request
			}
			w.WriteHeader(409)
		case "/rollback":
			w.WriteHeader(202)
			_, _ = io.WriteString(w, strings.Repeat("0", 32770))
		default:
			w.WriteHeader(404)
		}
	})}
	t.Cleanup(func() { _ = agent.Close() })
	go func() { _ = agent.Serve(ln) }()
	mux := http.NewServeMux()
	Mount(muxRegistrar{mux}, func(http.ResponseWriter, *http.Request) *auth.Principal {
		return &auth.Principal{Kind: authz.KindSession, Role: iam.RoleAdmin, UserID: "internal-admin"}
	}, socket)
	for _, tc := range []struct {
		method, suffix, body string
		code                 int
	}{
		{"GET", "", "", 200},
		{"POST", "/check", "{}", 202},
		{"POST", "/apply", "{\"version\":\"v0.1.0-beta.2\",\"revision\":\"" + strings.Repeat("1", 40) + "\"}", 409},
		{"POST", "/rollback", "{\"version\":\"v0.1.0-beta.1\"}", 502},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(tc.method, Path+tc.suffix, strings.NewReader(tc.body)))
		if w.Code != tc.code || strings.Contains(w.Body.String(), "hidden") {
			t.Fatalf("%s: %d %s", tc.suffix, w.Code, w.Body.String())
		}
	}
	select {
	case body := <-observed:
		if body["actor"] != "internal-admin" || body["version"] != "v0.1.0-beta.2" || len(body) != 3 {
			t.Fatalf("wrong IPC payload: %v", body)
		}
	default:
		t.Fatal("valid apply did not reach the agent")
	}
	_ = agent.Close()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", Path, nil))
	if w.Code != 503 {
		t.Fatalf("agent outage: %d", w.Code)
	}
}
