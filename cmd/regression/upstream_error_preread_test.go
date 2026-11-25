package regression

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestUpstream4xxPrereadFallback 验证完整的上下文超限错误允许回退，传输截断时不应凭片段回退。
// 使用真实网关、隔离 PostgreSQL、路由配置及本机 HTTP 上游；检查返回状态、实际调用、日志和账务，harness 清理数据。
func TestUpstream4xxPrereadFallback(t *testing.T) {
	var truncated, empty atomic.Bool
	var primary, backup atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if body["model"] == "fail-context" {
			primary.Add(1)
			data := []byte(`{"error":{"code":"context_length_exceeded","message":"Input is too long"}}`)
			if truncated.Load() {
				w.Header().Set("Content-Length", fmt.Sprint(len(data)+100))
				if empty.Load() {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(data)
			return
		}
		backup.Add(1)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"chat-fallback\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Fallback works\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat-fallback\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		writeJSON(w, map[string]any{"id": "chat-fallback", "object": "chat.completion", "model": "good-backup", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "Fallback works"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 5, "total_tokens": 16}})
	}))
	t.Cleanup(up.Close)
	h := newHarness(t)
	admin := h.adminSession()
	owner := h.openScope(t, admin, "4xx-preread")
	template := routeTemplate(t, h, admin, "4xx preread", routeTemplateBody([]any{}, 3, 60, 0, 0))
	bindTemplate(t, h, admin, "key", owner.keyID, template)
	for _, m := range []struct{ name, upstream string }{{"preread-primary", "fail-context"}, {"preread-backup", "good-backup"}} {
		h.ok("POST", "/model/new", admin, map[string]any{"model_name": m.name, "litellm_params": map[string]any{"model": m.upstream, "api_base": up.URL, "api_key": "local-fixture", "custom_llm_provider": "custom", "input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002}, "model_info": map[string]any{"id": m.name, "transport": "bypass_openai_chat", "endpoint_types": []string{"chat"}, "pricing_source": "manual"}})
	}
	h.ok("PUT", "/model/fallback", admin, map[string]any{"model_name": "preread-primary", "policy": map[string]any{"fallbacks": []string{"preread-backup"}, "context_window_fallbacks": []string{"preread-backup"}}})
	t.Run("empty-stream-truncated", func(t *testing.T) {
		truncated.Store(true)
		empty.Store(true)
		t.Cleanup(func() { empty.Store(false) })
		p, b := primary.Load(), backup.Load()
		before := h.moneyOf(t, owner)
		request := chatRequest("preread-primary", "Check empty upstream error.")
		request["stream"] = true
		result := h.do("POST", "/chat/completions", owner.key, request)
		if result.status != 400 || primary.Load()-p != 1 || backup.Load()-b != 0 {
			t.Fatalf("空正文截断不能重试或回退: %s primary=%d backup=%d", result.describe(), primary.Load()-p, backup.Load()-b)
		}
		log := logDetail(t, h, admin, result.header("x-litellm-call-id"))
		diag := log["metadata"].(map[string]any)["upstream_response"].(map[string]any)
		if log["status"] != "error" || diag["read_error"] != "unexpected EOF" {
			t.Fatalf("缺少空正文截断诊断: %v", log)
		}
		if after := h.moneyOf(t, owner); !after.same(before) {
			t.Fatalf("失败响应不应增加任何预算费用: before=%v after=%v", before, after)
		}
	})
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			for _, cut := range []bool{false, true} {
				truncated.Store(cut)
				p, b := primary.Load(), backup.Load()
				before := h.moneyOf(t, owner)
				request := chatRequest("preread-primary", fmt.Sprintf("Check error boundary %v %v.", stream, cut))
				request["stream"] = stream
				result := h.do("POST", "/chat/completions", owner.key, request)
				if primary.Load()-p != 1 {
					t.Fatalf("主模型调用次数不正确: %d", primary.Load()-p)
				}
				if cut {
					if result.status != 400 || backup.Load()-b != 0 {
						t.Fatalf("截断的上游错误不应触发回退: %s backup=%d", result.describe(), backup.Load()-b)
					}
					log := logDetail(t, h, admin, result.header("x-litellm-call-id"))
					diag := log["metadata"].(map[string]any)["upstream_response"].(map[string]any)
					if log["status"] != "error" || diag["read_error"] != "unexpected EOF" {
						t.Fatalf("截断的上游错误日志不完整: %v", log)
					}
					h.flushSpend()
					if after := h.moneyOf(t, owner); after.key != before.key {
						t.Fatalf("失败请求改变密钥费用: before=%v after=%v", before, after)
					}
				} else if result.status != 200 || backup.Load()-b != 1 {
					t.Fatalf("完整的上下文超限错误应回退: %s backup=%d", result.describe(), backup.Load()-b)
				}
			}
		})
	}
}

// prereadObserver 包装真实 HTTP 正文，供回归精确等到网关读取所有已发送字节。
type prereadObserver struct {
	io.ReadCloser
	ready     chan struct{}
	remaining int
}

// Read 委托真实 HTTP 读，参数 dst 为目的缓冲区，返回原读结果；准备继续等字节时唤醒取消测试，不修改正文。
func (b *prereadObserver) Read(dst []byte) (int, error) {
	if b.remaining == 0 {
		close(b.ready)
		b.remaining = -1
	}
	n, err := b.ReadCloser.Read(dst)
	if b.remaining > 0 {
		b.remaining -= n
	}
	return n, err
}

// TestUpstreamErrorPrereadActualCancellation 验证读取真实上游 400/429/503 正文时取消，日志保留 499 和已收到的供应商证据。
// 参数 t 为测试上下文；覆盖流式/非流式和空、残片、完整 JSON，隔离 PostgreSQL、Redis 与本地上游均自动清理。
func TestUpstreamErrorPrereadActualCancellation(t *testing.T) {
	const completeError = `{ "error": {"type":"api_error","message":"Vendor rejected this call"}, "request_id":"vendor-cancel" }`
	for _, status := range []int{400, 429, 503} {
		attempts := 1
		if status == 400 {
			attempts = 3
		}
		for _, stream := range []bool{false, true} {
			for _, body := range []struct{ name, raw string }{
				{"empty", ""},
				{"partial", `{"error":`},
				{"complete", completeError},
			} {
				t.Run(fmt.Sprintf("%d/stream=%t/%s", status, stream, body.name), func(t *testing.T) {
					isolatedSpendRedis(t)
					ready := make(chan struct{})
					stopped := make(chan struct{}, 1)
					var calls atomic.Int32
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						_, _ = io.Copy(io.Discard, r.Body)
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Content-Length", "1000")
						w.WriteHeader(status)
						_, _ = io.WriteString(w, body.raw)
						w.(http.Flusher).Flush()
						select {
						case <-r.Context().Done():
						case <-time.After(5 * time.Second):
						}
						stopped <- struct{}{}
					}))
					t.Cleanup(up.Close)
					model := fmt.Sprintf("preread-cancel-%d-%t-%s", status, stream, body.name)
					dep := chatDeployment(model)
					dep.LiteLLMParams["api_base"] = up.URL
					h := newHarness(t, dep)
					client := h.gw.HTTPClient()
					transport := client.Transport
					if transport == nil {
						transport = http.DefaultTransport
					}
					client.Transport = cacheObserverRoundTripper(func(req *http.Request) (*http.Response, error) {
						resp, err := transport.RoundTrip(req)
						if err == nil && resp.StatusCode == status {
							resp.Body = &prereadObserver{ReadCloser: resp.Body, ready: ready, remaining: len(body.raw)}
						}
						return resp, err
					})
					admin := h.adminSession()
					owner := h.openScope(t, admin, model)
					before := h.moneyOf(t, owner)
					template := routeTemplate(t, h, admin, "preread cancellation", routeTemplateBody([]any{}, attempts, 10, 1, 60))
					bindTemplate(t, h, admin, "key", owner.keyID, template)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					payload := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Hello"}],"stream":%t}`, model, stream)
					req, err := http.NewRequestWithContext(ctx, "POST", h.server.URL+"/v1/chat/completions", strings.NewReader(payload))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Authorization", "Bearer "+owner.key)
					req.Header.Set("Content-Type", "application/json")
					done := make(chan error, 1)
					go func() {
						resp, e := h.server.Client().Do(req)
						if resp != nil {
							_ = resp.Body.Close()
						}
						done <- e
					}()
					select {
					case <-ready:
					case <-time.After(5 * time.Second):
						t.Fatalf("网关未开始读取上游 %d 正文", status)
					}
					cancel()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("请求取消未结束")
					}
					select {
					case <-stopped:
					case <-time.After(5 * time.Second):
						t.Fatal("取消未传至上游")
					}
					rows := cancellationRows(t, h, admin, model, 1)
					if calls.Load() != 1 || len(rows) != 1 {
						t.Fatalf("取消后不应重试或重复记账: calls=%d rows=%v", calls.Load(), rows)
					}
					log := logDetail(t, h, admin, stringField(rows[0], "request_id"))
					metadata := log["metadata"].(map[string]any)
					diagnostic := metadata["upstream_response"].(map[string]any)
					failure := diagnostic["failure"].(map[string]any)
					if log["status"] != "error" || fmt.Sprint(metadata["http_status"]) != "499" || fmt.Sprint(diagnostic["status_code"]) != fmt.Sprint(status) || failure["kind"] != "request_canceled" || failure["phase"] != "response_transfer" || failure["cancellation_source"] != "unknown" {
						t.Fatalf("没有分别记录入站取消和上游 HTTP 状态: %v", log)
					}
					var detail map[string]any
					if err := json.Unmarshal([]byte(firstString(log, "error")), &detail); err != nil {
						t.Fatalf("无法读取已落库的取消详情: %v, %v", err, log)
					}
					upstream, _ := detail["upstream_error"].(map[string]any)
					if body.name == "complete" {
						if upstream["request_id"] != "vendor-cancel" || !strings.Contains(string(mustJSON(upstream)), "Vendor rejected this call") {
							t.Fatalf("已收到的完整上游错误未保留: %v", detail)
						}
					} else if detail["upstream_error"] != nil {
						t.Fatalf("空正文或残片不能当作上游返回的完整错误: %v", detail)
					}
					if after := h.moneyOf(t, owner); !after.same(before) {
						t.Fatalf("中断请求不应扣费: before=%v after=%v", before, after)
					}
				})
			}
		}
	}
}
