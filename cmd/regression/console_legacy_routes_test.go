package regression

import (
	"net/http"
	"testing"
)

// TestConsoleLegacyRoutesKeepGatewayContracts 固定退役页面对应 API 的真实 HTTP 契约。
// t 使用隔离 PostgreSQL 和本地供应商。测试覆盖 MCP 不可用、护栏提交能力、
// 转换输入校验及结果读回；仅写测试 schema，harness 退出时清理。
func TestConsoleLegacyRoutesKeepGatewayContracts(t *testing.T) {
	h := newHarness(t, chatDeployment("legacy-route-chat"))
	admin := h.adminSession()

	for _, path := range []string{"/v1/mcp/server", "/v1/mcp/user-credentials"} {
		out := h.do(http.MethodGet, path, admin, nil)
		if out.status != http.StatusNotFound {
			t.Fatalf("MCP 未开放接口 %s 必须保持 404：%s", path, out.describe())
		}
	}
	available := h.ok(http.MethodGet, "/guardrails/submissions", admin, nil).json()
	if available["supported"] != false || len(rowsOfFrom(available, "submissions")) != 0 {
		t.Fatalf("护栏提交能力应明确未开放：%v", available)
	}

	const transform = "/utils/transform_request"
	if denied := h.do(http.MethodPost, transform, "", map[string]any{"model": "legacy-route-chat"}); denied.status != http.StatusUnauthorized {
		t.Fatalf("匿名转换请求权限变化：%s", denied.describe())
	}
	if invalid := h.do(http.MethodPost, transform, admin, map[string]any{}); invalid.status != http.StatusBadRequest {
		t.Fatalf("缺少模型名称的转换必须失败：%s", invalid.describe())
	}
	input := map[string]any{"model": "legacy-route-chat", "name": "Original 通用参数", "messages": []any{
		map[string]any{"role": "user", "content": "Verify the saved request shape."},
	}}
	const parameterList = "/utils/supported_openai_params?model=legacy-route-chat"
	converted := h.ok(http.MethodPost, transform, admin, input).json()
	if converted["transformed"] != true || converted["model"] != "legacy-route-chat" || converted["name"] != input["name"] {
		t.Fatalf("转换结果与请求不符：%v", converted)
	}
	stored := h.ok(http.MethodGet, parameterList, admin, nil).json()
	last, _ := stored["last_transform"].(map[string]any)
	if last["name"] != input["name"] || last["transformed"] != true {
		t.Fatalf("转换结果无法从现有 API 读回：%v", stored)
	}
	if len(h.upstreamCalls()) != 0 {
		t.Fatal("配置或转换接口不应调用模型")
	}
}
