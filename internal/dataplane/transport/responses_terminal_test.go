package transport

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	providercontract "github.com/sunqirui1987/xhub/internal/provider/contract"
)

// terminalRoundTrip 提供只读内存 HTTP 响应，供转发单测隔离网络；调用闭包，不修改请求。
type terminalRoundTrip func(*http.Request) (*http.Response, error)

// RoundTrip 实现测试传输接口，参数请求交给构造时闭包，返回内存响应或测试错误，无真实外发。
func (f terminalRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// terminalReader 在合法终态之后模拟没有 EOF 的连接；继续读取会明确报错，避免用等待时间验证协议结束。
type terminalReader struct {
	*strings.Reader
	afterEnd, closed bool
}

// Read 读取给定 SSE，耗尽后记录非法续读并返回错误；仅单测调用，不阻塞。
func (r *terminalReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.afterEnd = true
		return 0, errors.New("read after semantic terminal")
	}
	return n, err
}

// Close 记录 ForwardOfficial 是否回收正文，返回 nil；仅测试状态，不关闭外部资源。
func (r *terminalReader) Close() error { r.closed = true; return nil }

// TestNativeResponsesStopsAtTerminal 验证仅完整 Responses cap/completed 可停止读取，原字节及用量不变。
// 前置内存客户端且终态后继续读会报错；失败/不完整终态不能假成功，图像协议继续读；无需服务或数据库清理。
func TestNativeResponsesStopsAtTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, event, response string
		valid                           bool
	}{
		{"cap", "openai-responses", "response.incomplete", `{"id":"resp_limit","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":8,"output_tokens":8}}`, true},
		{"completed", "openai-responses", "response.completed", `{"id":"resp_complete","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":8}}`, true},
		{"missing-usage", "openai-responses", "response.incomplete", `{"id":"resp_missing","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, true},
		{"wrong-reason", "openai-responses", "response.incomplete", `{"id":"resp_unknown","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[],"usage":{"input_tokens":8,"output_tokens":8}}`, false},
		{"missing-body", "openai-responses", "response.completed", `{}`, false},
		{"images-still-read", "openai-images", "image_generation.completed", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "event: " + tc.event + "\ndata: {\"type\":\"" + tc.event + "\",\"response\":" + tc.response + "}\n\n"
			body := &terminalReader{Reader: strings.NewReader(raw)}
			client := &http.Client{Transport: terminalRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
			})}
			w := httptest.NewRecorder()
			result, err := ForwardOfficial(client, httptest.NewRequest("POST", "/", nil), "POST", "http://fixture.invalid", "fake", "fixture-call", []byte(`{}`), http.Header{}, providercontract.Transport{Protocol: tc.protocol, Auth: providercontract.AuthConfig{Header: "Authorization"}}, w)
			if (err == nil) != tc.valid || body.afterEnd == tc.valid || !body.closed {
				t.Fatalf("协议结束/回收错误 err=%v afterEnd=%v closed=%v", err, body.afterEnd, body.closed)
			}
			if w.Body.String() != raw {
				t.Fatal("原生事件被改写")
			}
			if tc.valid {
				if tc.name == "missing-usage" {
					if result.Usage["pricing_blocked"] != "upstream_usage_missing" {
						t.Fatalf("终态缺用量未阻断 %+v", result.Usage)
					}
				} else if result.Usage["input_tokens"] != float64(8) || result.Usage["output_tokens"] != float64(8) {
					t.Fatalf("正常终态丢失用量 %+v", result.Usage)
				}
			}
		})
	}
}
