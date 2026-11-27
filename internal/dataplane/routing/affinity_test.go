// 本文件验证 routing 的相关执行契约，阅读顺序为测试宿主/辅助方法，再到各个 Test 场景。
// 具体前置、期望、边界和清理方式见每个测试方法；夹具的空实现仅用于隔离当前测试。
package routing

import (
	"github.com/sunqirui1987/xhub/internal/config"
	contract "github.com/sunqirui1987/xhub/internal/dataplane/contract"
	"github.com/sunqirui1987/xhub/internal/router"
	"testing"
)

// TestAffinityRevalidatesFallback 验证回退偏好遵守链路撤销、零权重、冷却和请求禁用，父响应只允许原归属。
// 前置内存目录与完整模板；普通失效偏好恢复正常池，无外部数据需要清理。
// 参数 t 为 Go 测试上下文；返回值无，断言失败时终止当前测试。
func TestAffinityRevalidatesFallback(t *testing.T) {
	models := []config.ModelEntry{
		{ModelName: "a", LiteLLMParams: map[string]any{"deployment_id": "a-id", "model": "a"}},
		{ModelName: "b", LiteLLMParams: map[string]any{"deployment_id": "b-id", "model": "b"}},
		{ModelName: "b", LiteLLMParams: map[string]any{"deployment_id": "b-alt", "model": "b-alt"}},
	}
	target := contract.RouteTarget{DeploymentID: "b-id", Model: "b", Pool: "b", Path: []contract.RouteStep{{From: "a", To: "b", Kind: "general"}}}
	for _, mode := range []string{"valid", "revoked", "zero", "cooldown", "disabled", "parent", "parent-cooled", "parent-revoked", "wrong-model"} {
		t.Run(mode, func(t *testing.T) {
			settings := router.BuiltinSettings()
			settings.ModelFallbacks = map[string]router.FallbackPolicy{"a": {Fallbacks: []string{"b"}}}
			state := router.State{}
			if mode == "revoked" || mode == "parent-revoked" {
				settings.ModelFallbacks = map[string]router.FallbackPolicy{}
			}
			if mode == "zero" {
				settings.ModelDefaults = map[string]any{"b": map[string]any{"allocations": []any{map[string]any{"deployment_id": "b-id", "weight": 0}, map[string]any{"deployment_id": "b-alt", "weight": 1}}}}
			}
			if mode == "cooldown" || mode == "parent-cooled" {
				state.Cooldown = map[string]bool{router.CooldownID(models[1]): true}
			}
			plan := contract.RoutePlan{Target: target, Required: mode == "parent" || mode == "parent-cooled" || mode == "parent-revoked"}
			if mode == "wrong-model" {
				plan.Target.Model = "unrelated"
			}
			cfg := settings.ForModel("a")
			pool := cfg.Schedule(models, "a", state, "")
			queue := NewFallbackQueue(settings, "a", mode == "disabled" || mode == "parent")
			got, _ := queue.SelectAffinity(&plan, pool, "a", models, state, cfg)
			if mode == "parent-revoked" {
				if len(got) != 0 {
					t.Fatal("被撤销的父响应路径仍可调用")
				}
				return
			}
			want := "a-id"
			if mode == "valid" || mode == "parent" || mode == "parent-cooled" {
				want = "b-id"
			}
			if len(got) == 0 || router.DeploymentID(got[0]) != want {
				t.Fatalf("模式 %s 目标 %v，期望 %s", mode, got, want)
			}
			if plan.Required && len(got) != 1 {
				t.Fatal("父响应出现替代候选")
			}
		})
	}
}
