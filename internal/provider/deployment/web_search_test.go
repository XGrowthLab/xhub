package deployment_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sunqirui1987/xhub/internal/config"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
	"github.com/sunqirui1987/xhub/internal/provider/deployment"
)

// TestDeploymentKeepsInternalEncodeErrorsPrivate 使用内存部署与不可跨协议的历史，验证内部编码原因保留给路由诊断。
// 参数 t 为测试上下文，部署端不能将未标记的编码错误视作客户端字段错误；无外部资源。
func TestDeploymentKeepsInternalEncodeErrorsPrivate(t *testing.T) {
	dep := config.ModelEntry{ModelName: "chat", ModelInfo: map[string]any{"transport": "bypass_openai_chat"}}
	d := llmdialogue.Dialogue{Turns: []llmdialogue.Turn{
		{Role: "user", Native: map[string]any{"role": "user", "content": "Hello"}, NativeProtocol: "anthropic-messages"},
	}}
	pool, decisions := deployment.Candidates([]config.ModelEntry{dep}, "chat", &d)
	var field *llmdialogue.RequestFieldError
	_, _, err := deployment.EncodeDialogue(dep, d)
	if err == nil || errors.As(err, &field) || len(pool) != 0 || len(decisions) != 1 ||
		decisions[0].CapabilityError != nil || !strings.Contains(decisions[0].Reason, "opaque history requires") {
		t.Fatalf("未标记的编码错误不能成为可公开字段: %v %v %v", pool, decisions, err)
	}
}

// TestDeploymentSearchPolicy 验证部署级能力校验、候选筛选与真实编码一致；前置共享模型的 Chat/Responses 配置。
// 覆盖默认拒绝、显式降级、原生支持、非法配置及候选隔离；仅内存操作，无外部调用和清理。
func TestDeploymentSearchPolicy(t *testing.T) {
	d, err := llmdialogue.ParseDialogue("openai-responses", map[string]any{"input": "Hello", "tools": []any{map[string]any{"type": "web_search"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, transport, support, policy string
		allowed, dropped                 bool
	}{
		{"legacy-chat", "bypass_openai_chat", "", "", false, false},
		{"chat-drop", "bypass_openai_chat", "unsupported", "drop_optional", true, true},
		{"legacy-native", "bypass_openai_responses", "", "", true, false},
		{"native-supported", "bypass_openai_responses", "supported", "reject", true, false},
		{"native-unsupported", "bypass_openai_responses", "unsupported", "reject", false, false},
		{"native-drop", "bypass_openai_responses", "unsupported", "drop_optional", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dep := config.ModelEntry{ModelName: "shared", LiteLLMParams: map[string]any{"model": "actual", "custom_llm_provider": "openai"}, ModelInfo: map[string]any{"transport": tc.transport, "endpoint_types": []string{"responses"}}}
			if tc.support != "" {
				dep.ModelInfo["web_search_support"] = tc.support
			}
			if tc.policy != "" {
				dep.ModelInfo["web_search_fallback"] = tc.policy
			}
			pool, decisions := deployment.Candidates([]config.ModelEntry{dep}, "responses", &d)
			payload, dropped, err := deployment.EncodeDialogue(dep, d)
			if (len(pool) == 1) != tc.allowed || (err == nil) != tc.allowed || (len(dropped) > 0) != tc.dropped {
				t.Fatalf("候选/编码结果不一致: %v %v %v", decisions, dropped, err)
			}
			if !tc.allowed && (!errors.Is(err, llmdialogue.ErrWebSearchUnsupported) || decisions[0].CapabilityError != llmdialogue.ErrWebSearchUnsupported) {
				t.Fatal("搜索错误丢失明确的能力标记")
			}
			if tools, ok := payload["tools"].([]any); tc.dropped && ok && len(tools) > 0 {
				t.Fatal("移除的搜索仍被发送到上游")
			}
		})
	}
	for _, info := range []map[string]any{{"web_search_support": true}, {"web_search_support": "auto"}, {"web_search_fallback": false}, {"web_search_fallback": "ignore_all"}, {"web_search_support": "supported", "transport": "bypass_openai_chat"}} {
		if err := deployment.ValidateWebSearch(config.ModelEntry{ModelInfo: info}); err == nil {
			t.Fatalf("非法搜索配置被接受: %v", info)
		}
	}
}
