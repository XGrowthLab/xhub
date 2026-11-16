package models

import (
	"slices"
	"testing"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/config"
	providerdeployment "github.com/sunqirui1987/xhub/internal/provider/deployment"
)

// TestAvailableCardEndpoints 验证有效声明公开精确路径，缺失或旧声明返回原因且没有猜测绑定。
// 参数 t 为测试上下文；前置内存部署与登记目录，返回无；不写数据，无需清理。
func TestAvailableCardEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name, transport, endpoint string
		valid                     bool
	}{
		{"chat", "bypass_openai_chat", "chat", true},
		{"gemini", "gemini_generate_content", "gemini", true},
		{"missing", "", "", false},
		{"old-image", "openai_image_generation", "image", false},
		{"mismatch", "openai_image_generation", "image_edit", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := config.ModelEntry{ModelName: tc.name, LiteLLMParams: map[string]any{"model": "real", "custom_llm_provider": "custom"}, ModelInfo: map[string]any{"transport": tc.transport, "endpoint_types": []string{tc.endpoint}}}
			card := availableCard(entry)
			bindings := card["endpoints"].([]providerdeployment.EndpointBinding)
			if (len(bindings) > 0) != tc.valid {
				t.Fatalf("模型端点状态错误: %+v", card)
			}
			if (card["unavailable_reason"] == nil) != tc.valid {
				t.Fatalf("配置错误原因错误: %+v", card)
			}
			foundGoogle := false
			for _, binding := range bindings {
				if binding.Path == "/v1beta/models/gemini:generateContent" {
					foundGoogle = true
				}
			}
			if tc.name == "gemini" && !foundGoogle {
				t.Fatalf("Google 别名路径错误: %+v", bindings)
			}
		})
	}
}

// TestAvailableCardRespectsDeploymentCapabilities 验证部署的实测能力覆盖市场通用能力，未配置时仍回退市场信息。
// 使用隔离目录和内存部署覆盖 true、false 与缺失，测试后恢复全局目录，不读取供应商或用户数据。
func TestAvailableCardRespectsDeploymentCapabilities(t *testing.T) {
	const model = "fixture-capability-override"
	catalog.SetModel(model, map[string]any{"supports_vision": true, "supports_function_calling": true})
	t.Cleanup(func() { catalog.RemoveModel(model) })
	for _, tc := range []struct {
		name  string
		value any
		want  bool
	}{
		{"inherit", nil, true}, {"enabled", true, true}, {"disabled", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := map[string]any{"transport": "bypass_openai_responses", "base_model": model}
			if tc.value != nil {
				info["supports_vision"] = tc.value
			}
			entry := config.ModelEntry{ModelName: model, LiteLLMParams: map[string]any{"model": model}, ModelInfo: info}
			capabilities := availableCard(entry)["capabilities"].([]string)
			if slices.Contains(capabilities, "vision") != tc.want || !slices.Contains(capabilities, "tools") {
				t.Fatalf("模型能力显示错误: %v", capabilities)
			}
		})
	}
}

// TestSharedCapabilities 验证同名部署只展示共同能力，空候选不会产生未验证能力。
// 使用内存切片覆盖相交、全异、空值与原输入不变，不访问数据或外部供应商。
func TestSharedCapabilities(t *testing.T) {
	first := []string{"tools", "vision"}
	next := []string{"tools", "reasoning"}
	if got := sharedCapabilities(first, next); !slices.Equal(got, []string{"tools"}) {
		t.Fatalf("共同能力错误: %v", got)
	}
	if got := sharedCapabilities(first, nil); len(got) != 0 {
		t.Fatalf("空能力不应被补全: %v", got)
	}
	if !slices.Equal(first, []string{"tools", "vision"}) || !slices.Equal(next, []string{"tools", "reasoning"}) {
		t.Fatal("修改了部署能力输入")
	}
}
