package regression

import (
	"net/http"
	"testing"
)

// routeTemplateBody 构造一份完整的新路由模板正文。
// 参数 routes 为公开模型与策略列表，其余参数控制单部署重试、超时与冷静期。
// 返回值使用 JSON 回读的浮点数类型，可直接用于模板新增或更新接口；调用方必须显式给出全部字段，不依赖旧配置补全。
func routeTemplateBody(routes []any, attempts int, timeout float64, failures int, cooldown float64) map[string]any {
	return map[string]any{
		"model_routes": routes,
		"retry_policy": map[string]any{
			"max_attempts": float64(attempts), "timeout_seconds": timeout,
			"failure_threshold": float64(failures), "cooldown_seconds": cooldown,
		},
	}
}

// routeTemplate 新建一份严格格式的命名模板并返回 ID。
// 参数 body 必须是完整新契约；接口拒绝旧字段。测试结束时由隔离 schema 清理。
func routeTemplate(t *testing.T, h *harness, admin, name string, body map[string]any) string {
	t.Helper()
	r := h.ok(http.MethodPost, "/route_template/new", admin, map[string]any{"name": name, "body": body})
	id := firstString(r.json(), "id")
	if id == "" {
		t.Fatalf("route_template/new returned no id: %s", r.describe())
	}
	return id
}

// bindTemplate 将命名模板绑定到组织、团队或密钥；空模板 ID 清除当前层绑定。
func bindTemplate(t *testing.T, h *harness, admin, scope, scopeID, templateID string) {
	t.Helper()
	h.ok(http.MethodPost, "/route_template/binding", admin, map[string]any{
		"scope": scope, "scope_id": scopeID, "route_template_id": templateID,
	})
}

// effectiveTemplate 读取某个范围实际命中的模板与来源层级。
func effectiveTemplate(t *testing.T, h *harness, admin, scope, scopeID string) map[string]any {
	t.Helper()
	r := h.ok(http.MethodGet, "/route_template/binding?scope="+scope+"&scope_id="+scopeID, admin, nil)
	effective, _ := r.json()["effective"].(map[string]any)
	if effective == nil {
		t.Fatalf("binding response has no effective resolution: %s", r.describe())
	}
	return effective
}

// TestRouteTemplateScopePrecedence 验证密钥、团队、组织按由窄到宽选择一整份模板。
// 未绑定时来源为 builtin，表示直接使用模型管理默认分配；清除当前层后立即恢复继承。
func TestRouteTemplateScopePrecedence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminSession()
	scope := h.provision(t, admin, "route-scope")
	_, keyID := h.issueKey(t, scope.session, scope.teamID, scope.projectID, "route-scope-binding")
	body := routeTemplateBody([]any{}, 1, 60, 3, 0)
	org := routeTemplate(t, h, admin, "organization route", body)
	team := routeTemplate(t, h, admin, "team route", body)
	key := routeTemplate(t, h, admin, "key route", body)

	if got := effectiveTemplate(t, h, admin, "key", keyID); got["scope_type"] != "builtin" {
		t.Fatalf("未绑定模板时来源错误: %v", got)
	}
	bindTemplate(t, h, admin, "organization", scope.orgID, org)
	bindTemplate(t, h, admin, "team", scope.teamID, team)
	bindTemplate(t, h, admin, "key", keyID, key)
	if got := effectiveTemplate(t, h, admin, "key", keyID); got["template_id"] != key || got["scope_type"] != "key" {
		t.Fatalf("密钥模板没有优先命中: %v", got)
	}
	bindTemplate(t, h, admin, "key", keyID, "")
	if got := effectiveTemplate(t, h, admin, "key", keyID); got["template_id"] != team || got["scope_type"] != "team" {
		t.Fatalf("清除密钥模板后没有继承团队: %v", got)
	}
	bindTemplate(t, h, admin, "team", scope.teamID, "")
	if got := effectiveTemplate(t, h, admin, "key", keyID); got["template_id"] != org || got["scope_type"] != "organization" {
		t.Fatalf("清除团队模板后没有继承组织: %v", got)
	}
}

// TestRouteTemplateBindingConfirmation 用真实管理接口确认条件绑定、冲突后保留当前模板及恢复继承。
// 前置隔离网关、团队、密钥与平台模板，禁止跨组织绑定，卸载时回收本轮 schema。
func TestRouteTemplateBindingConfirmation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminSession()
	scope := h.provision(t, admin, "route-confirm")
	foreign := h.provision(t, admin, "route-confirm-other")
	_, keyID := h.issueKey(t, scope.session, scope.teamID, scope.projectID, "route-confirm-key")
	body := routeTemplateBody([]any{}, 1, 60, 3, 0)
	parent := routeTemplate(t, h, admin, "confirm inherited", body)
	applied := routeTemplate(t, h, admin, "confirm selected", body)
	bindTemplate(t, h, admin, "team", scope.teamID, parent)
	if got := effectiveTemplate(t, h, admin, "key", keyID); got["template_id"] != parent {
		t.Fatalf("提交前应继承团队模板: %v", got)
	}

	path := "/route_template/binding"
	payload := map[string]any{
		"scope": "key", "scope_id": keyID, "route_template_id": applied, "expected_route_template_id": "",
	}
	h.ok(http.MethodPost, path, admin, payload)
	conflict := h.do(http.MethodPost, path, admin, payload)
	detail, _ := conflict.json()["error"].(map[string]any)
	if conflict.status != http.StatusConflict || detail["type"] != "route_template_binding_conflict" {
		t.Fatalf("过期绑定应该返回明确冲突: %s", conflict.describe())
	}
	payload["expected_route_template_id"] = nil
	if bad := h.do(http.MethodPost, path, admin, payload); bad.status != http.StatusBadRequest {
		t.Fatalf("非字符串确认值应被拒绝: %s", bad.describe())
	}
	payload["expected_route_template_id"] = applied
	payload["route_template_id"] = ""
	if denied := h.do(http.MethodPost, path, foreign.session, payload); denied.status != http.StatusForbidden && denied.status != http.StatusNotFound {
		t.Fatalf("跨组织写入应该被拒绝: %s", denied.describe())
	}
	if got := effectiveTemplate(t, h, admin, "key", keyID); got["template_id"] != applied || got["scope_type"] != "key" {
		t.Fatalf("失败请求不应覆盖现有绑定: %v", got)
	}
	restored := h.ok(http.MethodPost, path, admin, payload)
	effective, _ := restored.json()["effective"].(map[string]any)
	if effective["template_id"] != parent || effective["scope_type"] != "team" {
		t.Fatalf("恢复继承应返回实际团队规则: %s", restored.describe())
	}
}

// TestRouteTemplateRetryPolicyApplies 验证模板 retry_policy 直接控制真实网关的单部署尝试次数。
// 本地上游固定返回 500；测试分别观察绑定与清除后的尝试次数，隔离 schema 负责清理数据。
func TestRouteTemplateRetryPolicyApplies(t *testing.T) {
	h := newHarness(t)
	admin := h.adminSession()
	scope := h.provision(t, admin, "route-retry")
	addFlatPricedModel(t, h, admin, "route-retry-model", map[string]any{
		"input_cost_per_token": testInputRate, "output_cost_per_token": testOutputRate,
	}, "chat")
	h.scriptStatus("route-retry-model", http.StatusInternalServerError)
	id := routeTemplate(t, h, admin, "three attempts", routeTemplateBody([]any{}, 3, 60, 0, 0))
	bindTemplate(t, h, admin, "team", scope.teamID, id)

	h.resetUpstream()
	h.do(http.MethodPost, "/v1/chat/completions", scope.key, chatRequest("route-retry-model", "retry"))
	if got := len(h.upstreamCalls()); got != 3 {
		t.Fatalf("模板尝试次数=%d，期望 3", got)
	}
	bindTemplate(t, h, admin, "team", scope.teamID, "")
	h.resetUpstream()
	h.do(http.MethodPost, "/v1/chat/completions", scope.key, chatRequest("route-retry-model", "builtin"))
	if got := len(h.upstreamCalls()); got != 1 {
		t.Fatalf("清除模板后尝试次数=%d，期望内置值 1", got)
	}
}

// TestRouteTemplateRejectsLegacyDocuments 验证新增接口拒绝旧字段和无效部署权重。
func TestRouteTemplateRejectsLegacyDocuments(t *testing.T) {
	h := newHarness(t)
	admin := h.adminSession()
	for _, body := range []map[string]any{
		{"num_retries": 2},
		{"model_routing": []any{}},
		{"model_overrides": []any{}},
		routeTemplateBody([]any{map[string]any{"model": "m", "strategy": "traffic-split", "allocations": nil}}, 1, 60, 3, 0),
	} {
		if r := h.do(http.MethodPost, "/route_template/new", admin, map[string]any{"name": "legacy", "body": body}); r.status != http.StatusBadRequest {
			t.Fatalf("旧模板字段未被拒绝: %s", r.describe())
		}
	}
}
