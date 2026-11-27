package regression

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestMessagesBetaHeader 验证标准 Messages 出站保留上下文编辑、思考配置和 beta 头，且缺头时仍原样返回上游 400。
// 参数 t 为测试上下文，无返回值；使用真实网关、隔离数据库和本地供应商，测试结束关闭服务并回收数据。
func TestMessagesBetaHeader(t *testing.T) {
	const beta = "context-management-2025-06-27, thinking-display-updates-2026-08-18"
	const rejection = `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"}}`
	contextManagement := map[string]any{"edits": []any{map[string]any{"type": "clear_tool_uses_20250919"}}}
	thinking := map[string]any{"type": "adaptive", "display": "updates"}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("无法读取出站请求: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/v1/messages" || body["model"] != "vendor-messages" ||
			!reflect.DeepEqual(body["context_management"], contextManagement) || !reflect.DeepEqual(body["thinking"], thinking) {
			t.Errorf("出站 Messages 路径、模型或 beta 字段改变: %s %#v", r.URL.Path, body)
		}
		if r.Header.Get("X-Api-Key") != "fixture-supplier-key" || r.Header.Get("Anthropic-Version") != "2023-06-01" ||
			r.Header.Get("Authorization") != "" || r.Header.Get("X-Client-Only") != "" {
			t.Errorf("出站凭据、固定版本或请求头白名单不符合登记配置")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Anthropic-Beta") != beta {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(rejection))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_beta","type":"message","role":"assistant","model":"vendor-messages","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)

	dep := deployment("messages-beta-test", "vendor-messages", map[string]any{
		"api_base": upstream.URL, "api_key": "fixture-supplier-key", "custom_llm_provider": "anthropic",
	})
	dep.ModelInfo["transport"] = "bypass_anthropic_messages"
	dep.ModelInfo["endpoint_types"] = []string{"messages"}
	h := openHarness(t, false, dep)
	admin := h.adminSession()
	owner := h.openScope(t, admin, dep.ModelName)
	body := map[string]any{
		"model": dep.ModelName, "max_tokens": 64, "thinking": thinking, "context_management": contextManagement,
		"messages": []any{map[string]any{"role": "user", "content": "Reply with OK."}},
	}
	headers := map[string]string{"Cache-Control": "no-store", "Anthropic-Version": "client-version", "X-Client-Only": "must-stay-local"}
	failed := h.doHeaders(http.MethodPost, "/v1/messages", owner.key, body, headers)
	if failed.status != http.StatusBadRequest || failed.text() != rejection {
		t.Fatalf("缺 beta 头时应保留上游 400 正文: %s", failed.describe())
	}
	h.flushSpend()
	failedLog := h.ok(http.MethodGet, "/spend/logs/ui/"+failed.header("X-LiteLLM-Call-Id"), admin, nil).json()
	if spend, _ := floatField(failedLog, "spend"); failedLog["status"] != "error" || spend != 0 {
		t.Fatalf("供应商 400 应记录失败且不扣费: %v", failedLog)
	}

	headers["Anthropic-Beta"] = beta
	ok := h.doHeaders(http.MethodPost, "/v1/messages", owner.key, body, headers)
	if ok.status != http.StatusOK || ok.json()["model"] != dep.ModelName {
		t.Fatalf("标准 Messages 应带 beta 头成功返回公开模型: %s", ok.describe())
	}
	h.flushSpend()
	successLog := h.ok(http.MethodGet, "/spend/logs/ui/"+ok.header("X-LiteLLM-Call-Id"), admin, nil).json()
	if spend, _ := floatField(successLog, "spend"); successLog["status"] != "success" || !nearlyEqual(spend, 3*testInputRate+2*testOutputRate) {
		t.Fatalf("成功调用应按供应商用量入账: %v", successLog)
	}
}
