package regression

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sunqirui1987/xhub/internal/config"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
)

// TestQiniuGoogleGatewayCompatibility 验证真实 Google 标准入口转发、结束原因、源错误及账单。
// 参数 t 为回归上下文；本地供应商模拟同步的 stop 和真实 SSE 形状，用隔离数据库核对成功唯一收费、失败零费用、无重试和原错误。
// 每种协议独立启动网关与本地 HTTP 上游；harness 清理网关、schema 和租户，上游由 Cleanup 关闭。
func TestQiniuGoogleGatewayCompatibility(t *testing.T) {
	for _, protocol := range []string{"gemini", "vertex"} {
		t.Run(protocol, func(t *testing.T) {
			const (
				vendorError = `{"error":{"code":503,"status":"UNAVAILABLE","message":"Provider capacity waiting","details":[{"reason":"PROVIDER_CAPACITY"}]},"request_id":"vendor-qiniu-fixture"}`
				usage       = `{"candidates":[],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2}}`
				text        = `{"candidates":[{"content":{"parts":[{"text":"Google answer"}]}}]}`
				stop        = `{"candidates":[{"finishReason":"stop"}]}`
			)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				stream := strings.HasSuffix(r.URL.Path, ":streamGenerateContent")
				if stream && r.URL.Query().Get("alt") != "sse" {
					t.Error("Google 上游 SSE 请求未指定 alt=sse")
				}
				var body struct {
					Contents []struct{ Parts []struct{ Text string } }
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Contents) != 1 || len(body.Contents[0].Parts) != 1 {
					t.Errorf("Google 请求未正确转发: %+v %v", body, err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				prompt := body.Contents[0].Parts[0].Text
				if !stream {
					w.Header().Set("Content-Type", "application/json")
					if prompt == "Upstream error" {
						fmt.Fprint(w, vendorError)
					} else {
						fmt.Fprint(w, `{"candidates":[{"finishReason":"stop","content":{"parts":[{"text":"Google answer"}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2}}`)
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch prompt {
				case "Complete stream":
					fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: %s\n\n", text, stop, usage)
				case "Missing terminal":
					fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", text, usage)
				case "Only usage":
					fmt.Fprintf(w, "data: %s\n\n", usage)
				case "Upstream error":
					fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", text, vendorError)
				default:
					t.Errorf("未知夹具输入: %q", prompt)
				}
			}))
			t.Cleanup(upstream.Close)
			model := config.ModelEntry{
				ModelName: "qiniu-google",
				LiteLLMParams: map[string]any{
					"model": "real", "api_base": upstream.URL, "api_key": "fixture-only", "custom_llm_provider": "custom",
					"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002,
				},
				ModelInfo: map[string]any{"transport": protocol + "_generate_content"},
			}
			h := newHarness(t, model)
			admin := h.adminSession()
			owner := h.openScope(t, admin, "qiniu-google-"+protocol)
			pathPrefix := "/v1beta/models/"
			if protocol == "vertex" {
				pathPrefix = "/vertex/v1/models/"
			}
			for _, tc := range []struct {
				name, prompt string
				stream       bool
				httpStatus   int
				markers      []string
				success      bool
			}{
				{"sync-stop", "Complete response", false, 200, []string{`"finishReason":"STOP"`, "Google answer"}, true},
				{"sse-stop-and-tail-usage", "Complete stream", true, 200, []string{`"finishReason":"STOP"`, "Google answer", `"totalTokenCount":10`}, true},
				{"sse-partial", "Missing terminal", true, 200, []string{"Google answer", "candidates[0].finishReason", `"reason":"upstream_invalid_response"`, `"code":502`}, false},
				{"sse-no-content", "Only usage", true, 502, []string{"candidates[0].finishReason", `"reason":"upstream_invalid_response"`, `"code":502`}, false},
				{"sync-upstream-error", "Upstream error", false, 502, []string{"Provider capacity waiting", "PROVIDER_CAPACITY", "vendor-qiniu-fixture", `"code":503`}, false},
				{"sse-upstream-error", "Upstream error", true, 200, []string{"Provider capacity waiting", "PROVIDER_CAPACITY", "vendor-qiniu-fixture", `"code":503`}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					callsBefore := calls.Load()
					before := h.moneyOf(t, owner)
					path := pathPrefix + model.ModelName + ":generateContent"
					if tc.stream {
						path = pathPrefix + model.ModelName + ":streamGenerateContent?alt=sse"
					}
					body, err := llmdialogue.EncodeDialogue(llmdialogue.Dialogue{Stream: tc.stream, Turns: []llmdialogue.Turn{{Role: "user", Text: tc.prompt}}}, protocol, model.ModelName)
					if err != nil {
						t.Fatal(err)
					}
					resp := h.do(http.MethodPost, path, owner.key, body)
					if resp.status != tc.httpStatus || calls.Load() != callsBefore+1 {
						t.Fatalf("网关响应状态或外发次数错误: %s calls=%d", resp.describe(), calls.Load()-callsBefore)
					}
					for _, marker := range tc.markers {
						if !strings.Contains(resp.text(), marker) {
							t.Fatalf("成功或失败的客户端观察缺少 %s: %s", marker, resp.describe())
						}
					}
					h.flushSpend()
					bill := h.ok(http.MethodGet, "/spend/logs/ui/"+resp.header("x-litellm-call-id"), admin, nil).json()
					if tc.success {
						if bill["status"] != "success" || !h.moneyOf(t, owner).grewBy(before, 0.000012) {
							t.Fatalf("有效 Google 终态未按实测 8 输入、2 输出计费: %v", bill)
						}
					} else if strings.Contains(resp.text(), `"finishReason":"STOP"`) || bill["status"] != "error" || !h.moneyOf(t, owner).grewBy(before, 0) {
						t.Fatalf("Google 失败流伪造终态或产生费用: resp=%s bill=%v", resp.describe(), bill)
					}
				})
			}
		})
	}
}

// TestQiniuGoogleChatReasoningGateway 验证 Gemini/Vertex 对话转换保留 Chat 的公开推理及标准终态。
// 参数 t 为回归上下文；Google 客户端走真实网关访问本地 Chat 上游，同时验证同步/SSE 正文、推理标志、终态和一次计费。
// 上游与隔离数据库由测试清理，夹具不使用供应商凭据。
func TestQiniuGoogleChatReasoningGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("未通过 Chat 协议访问上游: %s %v", r.URL.Path, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w,
				"data: {\"id\":\"fixture-chat\",\"choices\":[{\"delta\":{\"reasoning_content\":\"Public summary\"}}]}\n\n"+
					"data: {\"id\":\"fixture-chat\",\"choices\":[{\"delta\":{\"content\":\"Visible answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2}}\n\n"+
					"data: [DONE]\n\n",
			)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"fixture-chat","choices":[{"message":{"role":"assistant","content":"Visible answer","reasoning_content":"Public summary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)
	dep := chatDeployment("qiniu-google-chat")
	dep.LiteLLMParams["api_base"] = upstream.URL
	dep.LiteLLMParams["input_cost_per_token"] = 0.000001
	dep.LiteLLMParams["output_cost_per_token"] = 0.000002
	dep.ModelInfo["endpoint_types"] = []string{"chat", "gemini", "vertex"}
	h := newHarness(t, dep)
	admin := h.adminSession()
	owner := h.openScope(t, admin, "qiniu-google-chat")
	for _, protocol := range []string{"gemini", "vertex"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, stream), func(t *testing.T) {
				body, err := llmdialogue.EncodeDialogue(llmdialogue.Dialogue{Stream: stream, Turns: []llmdialogue.Turn{{Role: "user", Text: fmt.Sprintf("Ping %s %v", protocol, stream)}}}, protocol, dep.ModelName)
				if err != nil {
					t.Fatal(err)
				}
				path := "/v1beta/models/" + dep.ModelName
				if protocol == "vertex" {
					path = "/vertex/v1/models/" + dep.ModelName
				}
				if stream {
					path += ":streamGenerateContent?alt=sse"
				} else {
					path += ":generateContent"
				}
				before := h.moneyOf(t, owner)
				resp := h.ok(http.MethodPost, path, owner.key, body)
				if strings.Count(resp.text(), `"text":"Public summary","thought":true`) != 1 ||
					strings.Count(resp.text(), `"text":"Visible answer"`) != 1 ||
					strings.Count(resp.text(), `"finishReason":"STOP"`) != 1 ||
					strings.Contains(resp.text(), "thoughtSignature") {
					t.Fatalf("Google 公开推理、正文或终态丢失或重复: %s", resp.describe())
				}
				h.flushSpend()
				if !h.moneyOf(t, owner).grewBy(before, 0.000012) {
					t.Fatal("Google 兼容对话与原始 Chat 用量计费不等")
				}
			})
		}
	}
}
