package deployment_test

import (
	"errors"
	"testing"

	"github.com/sunqirui1987/xhub/internal/config"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
	"github.com/sunqirui1987/xhub/internal/provider/deployment"
)

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
