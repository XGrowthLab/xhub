package models

import (
	"errors"
	"github.com/sunqirui1987/xhub/internal/config"
	"testing"
)

// probeViewHost 仅实现视图所需的凭据水合边界；输入模式决定保留/过期/失败，不访问数据库。
type probeViewHost struct {
	Host
	mode string
}

// AttachCredential 为单元测试返回安全水合结果或失败；保持原部署不变，无网络和清理副作用。
func (h probeViewHost) AttachCredential(m config.ModelEntry) (config.ModelEntry, error) {
	m = config.CloneModels([]config.ModelEntry{m})[0]
	if h.mode == "expired" {
		delete(m.ModelInfo, "protocol_probe")
	}
	if h.mode == "error" {
		return m, errors.New("credential unavailable")
	}
	m.LiteLLMParams = map[string]any{"api_key": "resolved-secret"}
	return m, nil
}

// TestProbeViewsAndPublicRedaction 验证当前证明保留、凭据失效剔除、无水合宿主兼容，以及输出不泄露摘要/密钥或修改原记录。
// 纯内存模型及宿主，无持久数据需要清理。
func TestProbeViewsAndPublicRedaction(t *testing.T) {
	m := config.ModelEntry{ModelName: "test", LiteLLMParams: map[string]any{"api_key": "raw-secret"}, ModelInfo: map[string]any{"protocol_probe": map[string]any{"config_signature": "raw-hash", "connection_signature": "resolved-hash", "verified": map[string]any{"openai-chat": true}}}}
	for _, mode := range []string{"valid", "expired", "error"} {
		got := probeViews(probeViewHost{mode: mode}, []config.ModelEntry{m})[0]
		if (got.ModelInfo["protocol_probe"] != nil) != (mode == "valid") {
			t.Fatalf("证明有效性错误: %s", mode)
		}
		if got.LiteLLMParams["api_key"] != "raw-secret" {
			t.Fatal("水合密钥进入视图")
		}
	}
	if probeViews(nil, []config.ModelEntry{m})[0].ModelInfo["protocol_probe"] == nil {
		t.Fatal("旧宿主不兼容")
	}
	out := Public(m)
	record := out["model_info"].(map[string]any)["protocol_probe"].(map[string]any)
	if record["config_signature"] != nil || record["connection_signature"] != nil {
		t.Fatal("摘要暴露")
	}
	if out["litellm_params"].(map[string]any)["api_key"] != "*****" {
		t.Fatal("密钥暴露")
	}
	if m.ModelInfo["protocol_probe"].(map[string]any)["config_signature"] != "raw-hash" {
		t.Fatal("脱敏修改了原记录")
	}
}
