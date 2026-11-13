package deployment

import (
	"testing"

	"github.com/sunqirui1987/xhub/internal/config"
	pc "github.com/sunqirui1987/xhub/internal/provider/contract"
)

// TestVerifiedAdditionalProtocols 验证服务端证明只增加对应原生 create，普通 transport 不变；改地址/密钥/型号立即失效，无持久化资源。
func TestVerifiedAdditionalProtocols(t *testing.T) {
	m := config.ModelEntry{ModelName: "public", LiteLLMParams: map[string]any{"model": "upstream", "api_base": "https://example.invalid/v1", "api_key": "fake"}, ModelInfo: map[string]any{"transport": "bypass_openai_responses"}}
	if err := AllowsEndpoint(m, "bypass:openai-chat"); err == nil {
		t.Fatal("未验证 Chat 被开启")
	}
	sig := ProbeSignature(m)
	m.ModelInfo["protocol_probe"] = map[string]any{"config_signature": sig, "connection_signature": sig, "verified": map[string]any{"openai-chat": true, "anthropic-messages": true}}
	for _, p := range []string{"openai-chat", "anthropic-messages"} {
		if err := AllowsEndpoint(m, "bypass:"+p); err != nil {
			t.Fatal(err)
		}
		hit, err := ResolveHit(pc.Hit{Transport: pc.Transport{EndpointID: "bypass:" + p, Protocol: p}, Action: pc.Action{Name: "create"}}, m)
		if err != nil || hit.Transport.Protocol != p {
			t.Fatalf("额外协议未选择真实执行器: %+v %v", hit, err)
		}
		if _, err := ResolveHit(pc.Hit{Transport: pc.Transport{EndpointID: "bypass:" + p, Protocol: p}, Action: pc.Action{Name: "count_tokens"}}, m); err == nil {
			t.Fatal("对话探测错误开放 count_tokens")
		}
	}
	if tr, _ := Execution(m); tr.Protocol != "openai-responses" {
		t.Fatal("普通路由发生变化")
	}
	for _, key := range []string{"model", "api_base", "api_key"} {
		copy := config.CloneModels([]config.ModelEntry{m})[0]
		copy.LiteLLMParams[key] = "changed"
		if AllowsEndpoint(copy, "bypass:openai-chat") == nil {
			t.Errorf("%s 改变后旧证明仍有效", key)
		}
	}
	copy := config.CloneModels([]config.ModelEntry{m})[0]
	copy.LiteLLMParams["input_cost_per_token"] = 0.01
	if ProbeSignature(copy) != sig {
		t.Fatal("仅价格变更触发探测")
	}
	copy.LiteLLMParams["api_key"] = "rotated"
	if view := ValidatedProbeView(m, copy); ProbeRecord(view) != nil {
		t.Fatal("凭据轮换未清除旧能力")
	}
	if len(DeploymentEndpoints(m)) < 9 {
		t.Fatal("发现接口未包含额外协议")
	}
}
