package dialogue

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/sunqirui1987/xhub/internal/config"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
	_ "github.com/sunqirui1987/xhub/internal/provider/all"
)

// TestBuildDialogueAnthropicBeta 验证同协议转发全部 beta 值、跨协议隔离、缺值及逐跳头边界。
// 参数 t 为测试上下文，无返回值；使用已登记的本地部署，仅构造内存中的请求，无网络调用或数据清理。
func TestBuildDialogueAnthropicBeta(t *testing.T) {
	const messages = "anthropic-messages"
	const chat = "openai-chat"
	const messagesTransport = "bypass_anthropic_messages"
	const chatTransport = "bypass_openai_chat"
	betas := []string{"context-management-2025-06-27, thinking-display-updates-2026-08-18", "another-beta"}
	inbound := http.Header{
		"Anthropic-Beta":    betas,
		"Anthropic-Version": {"caller-version"},
		"Authorization":     {"Bearer caller-key"},
		"X-Api-Key":         {"caller-api-key"},
		"Cookie":            {"session=caller"},
	}
	hopByHop := inbound.Clone()
	hopByHop["Connection"] = []string{"keep-alive", "Other, aNtHrOpIc-BeTa "}
	for _, tc := range []struct {
		name, source, transport string
		in                      http.Header
		want                    []string
	}{
		{"native", messages, messagesTransport, inbound, betas},
		{"missing", messages, messagesTransport, nil, nil},
		{"hop-by-hop", messages, messagesTransport, hopByHop, nil},
		{"to-chat", messages, chatTransport, inbound, nil},
		{"from-chat", chat, messagesTransport, inbound, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dep := config.ModelEntry{
				LiteLLMParams: map[string]any{"model": "vendor-model", "api_base": "http://127.0.0.1:39999", "api_key": "fixture-secret"},
				ModelInfo:     map[string]any{"transport": tc.transport},
			}
			d, err := llmdialogue.ParseDialogue(tc.source, map[string]any{
				"max_tokens": 64, "messages": []any{map[string]any{"role": "user", "content": "Reply with OK."}},
			})
			if err != nil {
				t.Fatal(err)
			}
			before := tc.in.Clone()
			built, err := BuildDialogue(dep, d, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := built.Header.Values("Anthropic-Beta"); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("出站 beta 值不符: got %q want %q", got, tc.want)
			}
			if tc.transport == messagesTransport {
				if built.Header.Get("Anthropic-Version") != "2023-06-01" ||
					built.Header.Get("X-Api-Key") != "fixture-secret" || built.Header.Get("Authorization") != "" {
					t.Fatal("客户端请求头影响了登记的 Anthropic 版本或出站鉴权")
				}
			} else if built.Header.Get("Authorization") != "Bearer fixture-secret" ||
				built.Header.Get("Anthropic-Version") != "" || built.Header.Get("X-Api-Key") != "" {
				t.Fatal("客户端请求头影响了另一协议的出站鉴权")
			}
			if built.Header.Get("Cookie") != "" || built.Header.Get("Connection") != "" || !reflect.DeepEqual(tc.in, before) {
				t.Fatal("转发了无关请求头或修改了原始入站头")
			}
		})
	}
}
