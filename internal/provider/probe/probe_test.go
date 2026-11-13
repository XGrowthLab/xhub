package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunqirui1987/xhub/internal/config"
	_ "github.com/sunqirui1987/xhub/internal/provider/anthropic"
	_ "github.com/sunqirui1987/xhub/internal/provider/openai"
	"github.com/sunqirui1987/xhub/internal/testutil/protocolfixture"
)

// TestRunProtocols 在本机 HTTP 上游验证三协议四步往返，成功必须实际收到四次请求；无商业凭据，Cleanup 关闭服务器。
func TestRunProtocols(t *testing.T) {
	for _, p := range Protocols() {
		t.Run(p, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; protocolfixture.Serve(w, r) }))
			defer s.Close()
			m := config.ModelEntry{LiteLLMParams: map[string]any{"model": "test", "api_base": s.URL + "/v1", "api_key": "fixture-protocol-key"}}
			got := Run(context.Background(), s.Client(), m, p)
			if got.Status != "verified" || calls != 4 {
				t.Fatalf("协议 %s 未完成真实四步: %+v calls=%d", p, got, calls)
			}
		})
	}
}

// TestRunRejectsFalseSuccess 验证错误状态、HTML 200、错误协议、超限、断流与网络失败均不会开启入口；使用本机请求并关闭服务。
func TestRunRejectsFalseSuccess(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, kind string
	}{
		{"html", 200, "<html>OK</html>", "protocol_mismatch"}, {"empty", 200, `{}`, "protocol_mismatch"}, {"wrong_protocol", 200, `{"id":"x","object":"response","status":"completed"}`, "protocol_mismatch"}, {"upstream", 503, "private-secret", "temporarily_failed"}, {"unauthorized", 401, "private-secret", "http_error"}, {"over_limit", 200, strings.Repeat("x", (1<<20)+1), "response_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer s.Close()
			got := Run(context.Background(), s.Client(), config.ModelEntry{LiteLLMParams: map[string]any{"model": "test", "api_base": s.URL, "api_key": "fixture-protocol-key"}}, "openai-chat")
			raw, _ := json.Marshal(got)
			if got.Status != tc.kind || strings.Contains(string(raw), "private-secret") {
				t.Fatalf("误报或泄露诊断: %+v", got)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Run(ctx, http.DefaultClient, config.ModelEntry{LiteLLMParams: map[string]any{"api_base": "http://127.0.0.1:1", "api_key": "test"}}, "openai-chat"); got.Status != "temporarily_failed" {
		t.Fatal(got)
	}
	if got := Run(context.Background(), nil, config.ModelEntry{}, "unknown"); got.Status != "unverified" {
		t.Fatal(got)
	}
}

// TestStreamContractBoundaries 使用完整原生事件夹具验证完整帧/终态/文本要求及错误事件；纯内存数据，无清理资源。
func TestStreamContractBoundaries(t *testing.T) {
	for _, p := range Protocols() {
		t.Run(p, func(t *testing.T) {
			valid := protocolfixture.Stream(p, protocolfixture.Reply(p, requestBody(p, "test")))
			for name, raw := range map[string]string{"valid": valid, "crlf": strings.ReplaceAll(valid, "\n", "\r\n"), "no_terminal": valid[:strings.LastIndex(strings.TrimSuffix(valid, "\n\n"), "data:")], "unfinished_frame": strings.TrimSuffix(valid, "\n\n"), "error": "data: {\"error\":{\"message\":\"private\"}}\n\n" + valid, "only_done": "data: [DONE]\n\n", "only_json": "{}\n\n"} {
				want := name == "valid" || name == "crlf"
				if got := validStream([]byte(raw), p); got != want {
					t.Errorf("%s %s got=%v want=%v", p, name, got, want)
				}
			}
		})
	}
}

// TestToolAssociationAndReplyBoundaries 验证工具名、ID、参数及普通输出的严格边界；不允许伪成功或任意工具，无外部资源。
func TestToolAssociationAndReplyBoundaries(t *testing.T) {
	for _, p := range Protocols() {
		t.Run(p, func(t *testing.T) {
			body := requestBody(p, "test")
			addTool(body, p)
			reply := protocolfixture.Reply(p, body)
			encoded, _ := json.Marshal(reply)
			for _, bad := range []string{strings.ReplaceAll(string(encoded), "xhub_echo", "other"), strings.ReplaceAll(string(encoded), "probe-call", ""), strings.ReplaceAll(string(encoded), "PROBE_OK", "wrong")} {
				var doc map[string]any
				json.Unmarshal([]byte(bad), &doc)
				fresh := requestBody(p, "test")
				addTool(fresh, p)
				if toolResult(fresh, doc, p) {
					t.Errorf("接受了非法工具结果 %s", p)
				}
			}
			if !validReply(reply, p, true) || !toolResult(body, reply, p) {
				t.Fatalf("合法工具未能回传 %s", p)
			}
			if follow := protocolfixture.Reply(p, body); follow == nil || !validReply(follow, p, false) {
				t.Fatalf("工具结果未关联 %s", p)
			}
			plain := protocolfixture.Reply(p, requestBody(p, "test"))
			plain["error"] = map[string]any{"message": "bad"}
			if validReply(plain, p, false) {
				t.Fatal("接受了错误包络")
			}
		})
	}
}

// TestReasoningAndForcedToolVariants 在真实 HTTP 往返中验证强制工具 stop 终态，以及无服务端存储的加密推理项回传。
// 供应商变体由本地严格服务返回；四步缺任一步都失败，结束关闭服务，不保存正文。
func TestReasoningAndForcedToolVariants(t *testing.T) {
	for _, p := range []string{"openai-chat", "openai-responses"} {
		t.Run(p, func(t *testing.T) {
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				if p == "openai-responses" {
					includes := list(b["include"])
					if b["store"] != false || len(includes) != 1 || includes[0] != "reasoning.encrypted_content" {
						w.WriteHeader(400)
						return
					}
					if calls == 4 {
						found := false
						for _, v := range list(b["input"]) {
							if object(v)["type"] == "reasoning" && object(v)["encrypted_content"] == "test-opaque" {
								found = true
							}
						}
						if !found {
							w.WriteHeader(400)
							return
						}
					}
				}
				reply := protocolfixture.Reply(p, b)
				if b["tool_choice"] != nil {
					if p == "openai-chat" {
						object(list(reply["choices"])[0])["finish_reason"] = "stop"
					} else {
						reply["output"] = append([]any{map[string]any{"type": "reasoning", "id": "rs-probe", "summary": []any{}, "encrypted_content": "test-opaque"}}, list(reply["output"])...)
					}
				}
				if b["stream"] == true {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Write([]byte(protocolfixture.Stream(p, reply)))
				} else {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(reply)
				}
			}))
			defer up.Close()
			result := Run(t.Context(), up.Client(), config.ModelEntry{LiteLLMParams: map[string]any{"model": "test", "api_base": up.URL, "api_key": "fake"}}, p)
			if result.Status != "verified" || calls != 4 {
				t.Fatalf("合法工具变体失败: %+v calls=%d", result, calls)
			}
		})
	}
}

// TestOutputLimitIsInconclusive 验证三协议 JSON/SSE 输出被探测上限截断均为未确定，不能误开入口或撤销旧验证。
// 使用本地服务器和原生截断字段，无真实供应商费用；每个服务随子测试关闭。
func TestOutputLimitIsInconclusive(t *testing.T) {
	for _, p := range Protocols() {
		for _, stream := range []bool{false, true} {
			t.Run(p+map[bool]string{false: "-json", true: "-sse"}[stream], func(t *testing.T) {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var b map[string]any
					json.NewDecoder(r.Body).Decode(&b)
					d := protocolfixture.Reply(p, b)
					limit := func() {
						switch p {
						case "openai-chat":
							object(list(d["choices"])[0])["finish_reason"] = "length"
						case "openai-responses":
							d["status"] = "incomplete"
							d["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
						default:
							d["stop_reason"] = "max_tokens"
						}
					}
					if stream && b["stream"] == true {
						limit()
						event := any(d)
						if p == "openai-responses" {
							event = map[string]any{"type": "response.incomplete", "response": d}
						} else if p == "anthropic-messages" {
							event = map[string]any{"type": "message_delta", "delta": d}
						}
						raw, _ := json.Marshal(event)
						w.Header().Set("Content-Type", "text/event-stream")
						w.Write(append(append([]byte("data: "), raw...), []byte("\n\n")...))
						return
					}
					if !stream {
						limit()
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(d)
				}))
				defer up.Close()
				got := Run(t.Context(), up.Client(), config.ModelEntry{LiteLLMParams: map[string]any{"model": "test", "api_base": up.URL, "api_key": "fake"}}, p)
				if got.Status != "inconclusive" {
					t.Fatalf("截断误分类: %+v", got)
				}
			})
		}
	}
}
