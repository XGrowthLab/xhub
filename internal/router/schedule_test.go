package router

import (
	"testing"

	"github.com/sunqirui1987/xhub/internal/config"
)

// TestScheduleAffinityOrder 验证粘性置顶后的其余候选仍按并发排序，冷却粘性不能被使用。
// 前置三个内存部署和运行快照；不改变目录，无外部数据需要清理。
func TestScheduleAffinityOrder(t *testing.T) {
	list := []config.ModelEntry{}
	for _, id := range []string{"slow", "fast", "pin"} {
		list = append(list, config.ModelEntry{ModelName: "m", LiteLLMParams: map[string]any{"deployment_id": id, "model": id}})
	}
	pin := CooldownID(list[2])
	state := State{Busy: map[string]int{CooldownID(list[0]): 9, CooldownID(list[1]): 1, pin: 20}}
	got := Schedule(list, "m", "least-busy", state, pin)
	if len(got) != 3 || DeploymentID(got[0]) != "pin" || DeploymentID(got[1]) != "fast" || DeploymentID(got[2]) != "slow" {
		t.Fatalf("粘性剩余排序错误: %v", got)
	}
	state.Cooldown = map[string]bool{pin: true}
	got = Schedule(list, "m", "least-busy", state, pin)
	if len(got) != 2 || DeploymentID(got[0]) != "fast" {
		t.Fatalf("冷却粘性未失效: %v", got)
	}
}

// TestScheduleCooldownRequiresAlternatives 验证单部署忽略旧冷却，有真实替代部署时仍优先健康项。
// 参数 t 为测试上下文；使用内存目录、分流权重及旧冷却快照，覆盖停用、零权重与重复身份，无外部资源。
func TestScheduleCooldownRequiresAlternatives(t *testing.T) {
	main := config.ModelEntry{ModelName: "m", LiteLLMParams: map[string]any{"deployment_id": "main", "model": "main"}}
	backup := config.ModelEntry{ModelName: "m", LiteLLMParams: map[string]any{"deployment_id": "backup", "model": "backup"}}
	disabled := backup
	disabled.ModelInfo = map[string]any{"disabled": true}
	state := State{Cooldown: map[string]bool{CooldownID(main): true}}
	for _, tc := range []struct {
		name         string
		models       []config.ModelEntry
		weight       float64
		alternatives bool
		want         string
	}{
		{"single", []config.ModelEntry{main}, 1, false, "main"},
		{"disabled", []config.ModelEntry{main, disabled}, 1, false, "main"},
		{"zero weight", []config.ModelEntry{main, backup}, 0, false, "main"},
		{"duplicate identity", []config.ModelEntry{main, main}, 1, false, "main"},
		{"healthy alternative", []config.ModelEntry{main, backup}, 1, true, "backup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := BuiltinSettings().ForModel("m")
			settings.Policy.Allocations = []Allocation{{DeploymentID: "main", Weight: 1}, {DeploymentID: "backup", Weight: tc.weight}}
			if got := settings.HasCooldownAlternatives(tc.models, "m"); got != tc.alternatives {
				t.Fatalf("冷却资格错误: got=%t want=%t", got, tc.alternatives)
			}
			pool := settings.Schedule(tc.models, "m", state, "")
			if len(pool) == 0 || DeploymentID(pool[0]) != tc.want || !state.Cooldown[CooldownID(main)] {
				t.Fatalf("旧冷却改变了单部署选路或共享状态: pool=%v state=%v", pool, state.Cooldown)
			}
		})
	}
}
