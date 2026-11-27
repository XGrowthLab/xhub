package transport

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dpstream "github.com/sunqirui1987/xhub/internal/dataplane/stream"
	providercontract "github.com/sunqirui1987/xhub/internal/provider/contract"
)

// bypassWriteProbe 模拟透传流首帧短写或写入失败，仅供内存转发测试。
type bypassWriteProbe struct {
	header http.Header
	short  bool
	calls  int
}

// Header 返回独立响应头，不访问外部资源。
func (w *bypassWriteProbe) Header() http.Header { return w.header }

// WriteHeader 接受上游响应状态，不访问外部资源。
func (w *bypassWriteProbe) WriteHeader(int) {}

// Write 首次返回短写或明确错误，记录调用次数。
func (w *bypassWriteProbe) Write(p []byte) (int, error) {
	w.calls++
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("synthetic write failure")
}

// Flush 满足流式输出接口；内存夹具无需刷新。
func (w *bypassWriteProbe) Flush() {}

// TestForwardOfficialClientWriteFailure 验证透传短写和错误会停止转发，返回客户端写入失败并关闭上游。
// 内存 HTTP 响应与故障写入器覆盖错误和短写，无外部服务或数据需清理。
func TestForwardOfficialClientWriteFailure(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "short"}[short], func(t *testing.T) {
			body := &terminalReader{Reader: strings.NewReader("data: {\"id\":\"chat-1\"}\n\n")}
			client := &http.Client{Transport: terminalRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
			})}
			w := &bypassWriteProbe{header: make(http.Header), short: short}
			result, err := ForwardOfficial(client, httptest.NewRequest("POST", "/", nil), "POST", "http://fixture.invalid", "fake", "fixture-call", []byte(`{}`), http.Header{}, providercontract.Transport{Protocol: "openai-chat", Auth: providercontract.AuthConfig{Header: "Authorization"}}, w)
			if !errors.Is(err, dpstream.ErrClientWrite) || !result.Streamed || !body.closed || w.calls != 1 {
				t.Fatalf("透传写失败处理不符: err=%v streamed=%v closed=%v writes=%d", err, result.Streamed, body.closed, w.calls)
			}
			if short && !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("未保留短写原因: %v", err)
			}
		})
	}
}

// TestForwardOfficialReadInterruptedError 用真实 HTTP 夹具验证完整错误包络与 JSON 残片的处理。
// 参数 t 为测试上下文；故意发出偏长的 Content-Length，前者保留字节、状态、读错误，后者保留 EOF 故障；服务器自动关闭。
func TestForwardOfficialReadInterruptedError(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		complete   bool
	}{
		{"complete error", "{ \"error\": { \"message\": \"Vendor busy\" } }\n", true},
		{"partial error", `{"error":{"message":"Vendor busy"`, false},
		{"normal result", `{"content":"not an error"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "128")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			tr := providercontract.Transport{Protocol: "anthropic-messages", Auth: providercontract.AuthConfig{Header: "x-api-key"}}
			got, err := ForwardOfficial(server.Client(), httptest.NewRequest("POST", "/", nil), "POST", server.URL, "test-key", "fixture-call", []byte("{}"), nil, tr, nil)
			if (err == nil) != tc.complete || (!tc.complete && !errors.Is(err, io.ErrUnexpectedEOF)) || got.StatusCode != 503 ||
				string(got.Body) != tc.body || got.Diagnostics.ReadError == "" || (got.Diagnostics.Error != nil) != tc.complete {
				t.Fatalf("完整错误与截断正文未正确区分: result=%+v diagnostics=%+v err=%v", got, got.Diagnostics, err)
			}
		})
	}
}

// TestSetUpstreamCallID 验证实际出站沿用本地可信 ID，保留独立的标准请求 ID。
// 参数 t 管理内存 HTTP 头；覆盖首次写入、同名伪造、已有标准 ID 和无 ID，没有网络或清理副作用。
func TestSetUpstreamCallID(t *testing.T) {
	header := http.Header{"X-Litellm-Call-Id": {"untrusted"}}
	SetUpstreamCallID(header, "gateway-id")
	if header.Get("X-Litellm-Call-Id") != "gateway-id" || header.Get("X-Request-Id") != "gateway-id" {
		t.Fatalf("可信 ID 未关联到出站请求: %v", header)
	}
	header.Set("X-Request-Id", "caller-id")
	SetUpstreamCallID(header, "next-gateway-id")
	SetUpstreamCallID(header, "")
	if header.Get("X-Litellm-Call-Id") != "next-gateway-id" || header.Get("X-Request-Id") != "caller-id" {
		t.Fatalf("已有标准请求 ID 被覆盖: %v", header)
	}
}
