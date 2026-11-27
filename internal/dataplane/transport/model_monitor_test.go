// 本文件验证 transport 的相关执行契约，阅读顺序为测试宿主/辅助方法，再到各个 Test 场景。
// 具体前置、期望、边界和清理方式见每个测试方法；夹具的空实现仅用于隔离当前测试。
package transport

import (
	providercontract "github.com/sunqirui1987/xhub/internal/provider/contract"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestForwardMonitorTiming 使用本地服务器验证正常流、缺失终态及非流式计时，服务器由 Cleanup 关闭。
// 参数 t 为 Go 测试上下文；返回值无，断言失败时终止当前测试。
func TestForwardMonitorTiming(t *testing.T) {
	for _, tc := range []struct {
		name, content, body string
		stream, failed      bool
	}{
		{"complete", "text/event-stream", ": keepalive\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", true, false},
		{"truncated", "text/event-stream", "data: {\"choices\":[]}\n\n", true, true},
		{"empty", "text/event-stream", ": keepalive\n\n", true, true},
		{"sync", "application/json", "{}", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.content)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			start := time.Now()
			result, err := ForwardOfficial(server.Client(), httptest.NewRequest("POST", "/", nil), "POST", server.URL, "fake", "fixture-call", []byte("{}"), http.Header{}, providercontract.Transport{Protocol: "openai-chat", Auth: providercontract.AuthConfig{Header: "Authorization"}}, httptest.NewRecorder())
			if (err != nil) != tc.failed || result.Streamed != tc.stream {
				t.Fatalf("转发结果错误: %+v, %v", result, err)
			}
			measured := tc.stream && tc.name != "empty"
			if !result.FirstByteAt.IsZero() != measured || measured && (result.FirstByteAt.Before(start) || result.FirstByteAt.After(time.Now())) {
				t.Fatalf("首数据时间错误: %v", result.FirstByteAt)
			}
		})
	}
}
