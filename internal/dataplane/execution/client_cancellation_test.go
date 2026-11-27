package execution

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/auth"
	"github.com/sunqirui1987/xhub/internal/config"
	dpcontract "github.com/sunqirui1987/xhub/internal/dataplane/contract"
	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	providerregistry "github.com/sunqirui1987/xhub/internal/provider/registry"
	"github.com/sunqirui1987/xhub/internal/router"
)

// cancellationHost 记录取消分支的供应商失败和唯一记账；复用纯内存宿主，无共享状态。
type cancellationHost struct {
	*streamAuditHost
	failures, bills int
	upstream        *dpdiagnostics.UpstreamDiagnostics
}

// AnnotateCall 记录最后一次已观测的上游响应。参数 id 和 note 沿用调用契约，无持久化副作用。
func (h *cancellationHost) AnnotateCall(id string, note dpcontract.CallNote) {
	h.streamAuditHost.AnnotateCall(id, note)
	if note.Upstream != nil {
		h.upstream = note.Upstream
	}
}

// NoteFailure 统计执行器报告的部署故障；参数为路由身份和策略，无返回或外部副作用。
func (h *cancellationHost) NoteFailure(string, router.RouteSettings) { h.failures++ }

// RecordSpend 捕获一次最终状态；参数沿用记账契约，供取消与真实超时对照，不写数据库。
func (h *cancellationHost) RecordSpend(_ http.ResponseWriter, _ *auth.Principal, _, _, _ string, _ map[string]any, _ time.Time, _ bool, status int, _ string) {
	h.status = status
	h.bills++
}

// cancellationBody 在读取已有正文后产生指定错误；可先取消入站请求，以区分调用方和上游失败。
type cancellationBody struct {
	data   string
	cancel context.CancelFunc
	err    error
}

// Read 返回剩余正文，随后执行可选取消并返回故障；参数接收字节，返回读取长度/错误，无网络副作用。
func (b *cancellationBody) Read(p []byte) (int, error) {
	if b.data != "" {
		n := copy(p, b.data)
		b.data = b.data[n:]
		return n, nil
	}
	if b.cancel != nil {
		b.cancel()
	}
	return 0, b.err
}

// TestClientCancellationClassification 验证出站前、流读取和普通正文读取的取消只记499、不重试或冷却。
// 参数t为测试上下文；真实上游超时仍记 502，测试里的唯一部署不触发冷却；内存传输隔离，context 由 Cleanup 释放。
func TestClientCancellationClassification(t *testing.T) {
	for _, phase := range []string{"dial", "stream", "body"} {
		for _, caller := range []bool{true, false} {
			name := phase + map[bool]string{true: "_caller", false: "_upstream"}[caller]
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)
				attempts := 0
				stream := phase == "stream"
				client := &http.Client{Transport: auditRoundTripper(func(*http.Request) (*http.Response, error) {
					attempts++
					failure := error(context.DeadlineExceeded)
					var stop context.CancelFunc
					if caller {
						failure = context.Canceled
						stop = cancel
					}
					if phase == "dial" {
						if stop != nil {
							stop()
						}
						return nil, failure
					}
					data := `{"choices":[`
					headers := http.Header{"Content-Type": {"application/json"}}
					if stream {
						data = "data: {\"id\":\"chatcmpl_cancel\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"
						headers.Set("Content-Type", "text/event-stream")
					}
					return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(&cancellationBody{data: data, cancel: stop, err: failure})}, nil
				})}
				cfg := chatConfig(config.ModelEntry{ModelName: "cancel", LiteLLMParams: map[string]any{"model": "cancel", "api_base": "https://example.invalid/v1", "api_key": "test"}})
				settings := router.BuiltinSettings()
				settings.Settings["retry_policy"] = map[string]any{"max_attempts": 3, "timeout_seconds": 60, "failure_threshold": 3}
				h := &cancellationHost{streamAuditHost: &streamAuditHost{logHost: newLogHost(cfg, client), settings: settings}}
				Serve(h, httptest.NewRecorder(), chatRequest(t, "cancel", stream).WithContext(ctx), "chat")
				wantAttempts, wantStatus := 3, 502
				if stream {
					wantAttempts = 1
				}
				if caller {
					wantAttempts, wantStatus = 1, 499
				}
				if attempts != wantAttempts || h.status != wantStatus || h.failures != 0 || h.bills != 1 || h.commits != 0 {
					t.Fatalf("取消/故障分类错误 attempts=%d status=%d failures=%d bills=%d commits=%d", attempts, h.status, h.failures, h.bills, h.commits)
				}
			})
		}
	}
}

// canceledNativeHost 在原生测试宿主上捕获记账状态，不改变原有鉴权或路由测试契约。
type canceledNativeHost struct {
	*fallbackNativeHost
	status, bills int
	savedResponse string
}

// RememberExchange 捕获断连前实际收到的正文，以核对成功读取和取消交错的日志；内存中仅保留本次测试内容。
func (h *canceledNativeHost) RememberExchange(_ string, _ *http.Request, _, response []byte) {
	h.savedResponse = string(response)
}

// RecordSpend 记录原生入口最终HTTP分类和次数；其余参数仅满足宿主契约，无外部结算。
func (h *canceledNativeHost) RecordSpend(_ http.ResponseWriter, _ *auth.Principal, _, _, _ string, _ map[string]any, _ time.Time, _ bool, status int, _ string) {
	h.status = status
	h.bills++
}

// TestNativeClientCancellationClassification 验证原生 Chat 区分取消与上游超时，并保留最后一次尝试的阶段和出站路径。
// 参数t为测试上下文；单部署不累积冷却，使用内存传输且无供应商调用，cleanup 释放独立上下文。
func TestNativeClientCancellationClassification(t *testing.T) {
	for _, caller := range []bool{true, false} {
		t.Run(map[bool]string{true: "caller", false: "upstream"}[caller], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			attempts := 0
			client := &http.Client{Transport: auditRoundTripper(func(*http.Request) (*http.Response, error) {
				attempts++
				if caller {
					cancel()
					return nil, context.Canceled
				}
				return nil, context.DeadlineExceeded
			})}
			models := []config.ModelEntry{Deployment("cancel", "cancel", "test", "https://example.invalid/v1", "bypass_openai_chat", nil)}
			settings := router.BuiltinSettings()
			settings.Settings["retry_policy"] = map[string]any{"max_attempts": 3, "timeout_seconds": 60, "failure_threshold": 3}
			h := &canceledNativeHost{fallbackNativeHost: &fallbackNativeHost{logicHost: &logicHost{cfg: &config.Config{ModelList: models}, client: client, models: models, pins: map[string]string{}}, policy: settings}}
			req := chatRequest(t, "cancel", false).WithContext(ctx)
			req.URL.Path = "/bypass/openai/v1/chat/completions"
			hit, ok := providerregistry.Match("POST", req.URL.Path)
			if !ok {
				t.Fatal("missing native route")
			}
			ServeBypass(h, httptest.NewRecorder(), req, hit)
			count, status := 3, 502
			if caller {
				count, status = 1, 499
			}
			if attempts != count || h.status != status || h.bills != 1 || len(h.failures) != 0 {
				t.Fatalf("native classification attempts=%d status=%d bills=%d failures=%d", attempts, h.status, h.bills, len(h.failures))
			}
			if len(h.notes) == 0 {
				t.Fatal("原生失败遗漏了调用诊断")
			}
			up := h.notes[len(h.notes)-1].Upstream
			kind := "timeout"
			if caller {
				kind = "request_canceled"
			}
			if up == nil || up.Endpoint != "/v1/chat/completions" || up.StatusCode != 0 ||
				up.Failure == nil || up.Failure.Phase != "upstream_request" || up.Failure.Kind != kind || up.Failure.TransportError == "" {
				t.Fatalf("原生失败未保存出站诊断: %+v", up)
			}
		})
	}
}

// TestNativeCancellationKeepsObservedError 验证收到完整的供应商 503 错误后发生取消，仍落一次 499 及原始错误。
// 参数 t 为测试上下文；分别覆盖 EOF 和取消读错，任何继续重试均判失败；内存树和上下文由测试回收。
func TestNativeCancellationKeepsObservedError(t *testing.T) {
	for _, terminal := range []error{io.EOF, context.Canceled} {
		t.Run(terminal.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			const raw = "{ \"error\": {\"message\": \"Supplier response retained\"} }\n"
			attempts := 0
			client := &http.Client{Transport: auditRoundTripper(func(*http.Request) (*http.Response, error) {
				attempts++
				return &http.Response{
					StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(&cancellationBody{data: raw, cancel: cancel, err: terminal}),
				}, nil
			})}
			deployment := Deployment("cancel", "cancel", "test", "https://example.invalid/v1", "bypass_openai_chat", nil)
			models := []config.ModelEntry{deployment}
			settings := router.BuiltinSettings()
			settings.Settings["retry_policy"] = map[string]any{"max_attempts": 3, "timeout_seconds": 60, "failure_threshold": 3}
			host := &canceledNativeHost{fallbackNativeHost: &fallbackNativeHost{logicHost: &logicHost{
				cfg: &config.Config{}, client: client, models: models,
			}, policy: settings}}
			req := chatRequest(t, "cancel", false).WithContext(ctx)
			req.URL.Path = "/bypass/openai/v1/chat/completions"
			hit, ok := providerregistry.Match(req.Method, req.URL.Path)
			if !ok {
				t.Fatal("未匹配测试透传路径")
			}
			rec := httptest.NewRecorder()
			ServeBypass(host, rec, req, hit)
			if attempts != 1 || host.bills != 1 || host.status != 499 || host.savedResponse != raw || rec.Body.Len() != 0 || len(host.notes) == 0 {
				t.Fatalf("已收到的错误被丢弃或错误重试: attempts=%d status=%d bills=%d", attempts, host.status, host.bills)
			}
			up := host.notes[len(host.notes)-1].Upstream
			if up == nil || up.StatusCode != 503 || up.Error == nil || up.Failure == nil ||
				up.Failure.Kind != "request_canceled" || up.Failure.Phase != "response_transfer" {
				t.Fatalf("未保留取消阶段和原始供应商错误: %+v", up)
			}
		})
	}
}

// TestUpstreamErrorPrereadClientCancellation 验证错误正文读取取消时只记一次 499，无重试或冷却。
// 输入为 400、429、503 和普通/流式请求，覆盖截断及读取完整后取消。
// 保留已收到的明确错误和原 HTTP 状态，残片不会被当作上游错误。使用内存传输，测试释放上下文。
func TestUpstreamErrorPrereadClientCancellation(t *testing.T) {
	for _, status := range []int{400, 429, 503} {
		for _, stream := range []bool{false, true} {
			for _, sample := range []struct {
				data     string
				terminal error
				complete bool
			}{
				{"", context.Canceled, false},
				{`{"error":`, context.Canceled, false},
				{`{"error":{"message":"upstream failed"},"request_id":"vendor-unit"}`, context.Canceled, true},
				{`{"error":{"message":"upstream failed"},"request_id":"vendor-unit"}`, io.EOF, true},
			} {
				// 本轮修复覆盖 429/503 在正文完整读完后取消的竞态；可重试的 400 仍只对预读中断分类。
				if status == 400 && sample.terminal == io.EOF {
					continue
				}
				t.Run(fmt.Sprintf("status=%d/stream=%t/bytes=%d/trailing=%v", status, stream, len(sample.data), sample.terminal), func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					attempts := 0
					client := &http.Client{Transport: auditRoundTripper(func(*http.Request) (*http.Response, error) {
						attempts++
						return &http.Response{StatusCode: status, Header: make(http.Header),
							Body: io.NopCloser(&cancellationBody{data: sample.data, cancel: cancel, err: sample.terminal})}, nil
					})}
					cfg := chatConfig(config.ModelEntry{ModelName: "cancel-preread", LiteLLMParams: map[string]any{"model": "cancel-preread", "api_base": "https://example.invalid/v1", "api_key": "test"}})
					settings := router.BuiltinSettings()
					settings.Settings["retry_policy"] = map[string]any{"max_attempts": 1, "timeout_seconds": 60, "failure_threshold": 1}
					h := &cancellationHost{streamAuditHost: &streamAuditHost{logHost: newLogHost(cfg, client), settings: settings}}
					Serve(h, httptest.NewRecorder(), chatRequest(t, "cancel-preread", stream).WithContext(ctx), "chat")
					if attempts != 1 || h.status != 499 || h.failures != 0 || h.bills != 1 {
						t.Fatalf("HTTP %d 预读取消分类错误: attempts=%d status=%d failures=%d bills=%d", status, attempts, h.status, h.failures, h.bills)
					}
					diagnostic := h.upstream
					if diagnostic == nil || diagnostic.StatusCode != status || diagnostic.Failure == nil || diagnostic.Failure.Phase != "response_transfer" {
						t.Fatalf("预读取消丢失上游事实: %+v", diagnostic)
					}
					if (diagnostic.Error["request_id"] == "vendor-unit") != sample.complete {
						t.Fatalf("忽略了完整错误或误用了残片: %v", diagnostic.Error)
					}
				})
			}
		}
	}
}
