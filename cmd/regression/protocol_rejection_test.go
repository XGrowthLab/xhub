package regression

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunqirui1987/xhub/internal/config"
)

// TestProtocolRejectionDetails 通过真实网关和 PG 验证字段、目标协议、出站前阶段及调用日志。
// 测试使用授权租户，拒绝不调用上游、不扣费；harness 清理私有 schema、凭据及本地服务。
func TestProtocolRejectionDetails(t *testing.T) {
	messages := config.ModelEntry{ModelName: "to-messages",
		LiteLLMParams: map[string]any{"model": "private-model-id", "api_key": "sk-private-api-key"},
		ModelInfo:     map[string]any{"id": "private-deployment", "transport": "bypass_anthropic_messages"},
	}
	h := newHarness(t, messages, chatDeployment("to-chat"))
	admin := h.adminSession()
	owner := h.openScope(t, admin, "compatibility-errors")
	before := h.moneyOf(t, owner)
	for _, tc := range []struct{ name, path, raw, field, stage, target, hint string }{
		{"unknown field", "/v1/chat/completions", `{"model":"to-messages","messages":[{"role":"user","content":"Hello"}],"vendor_extra":true}`, "vendor_extra", "request_conversion", "anthropic-messages", "requires native openai-chat"},
		{"hosted tool", "/v1/responses", `{"model":"to-chat","input":"Hello","tools":[{"type":"mcp"}]}`, "tools[0].type", "request_conversion", "openai-chat", "use a function tool"},
		{"custom format", "/v1/responses", `{"model":"to-chat","input":"Hello","tools":[{"type":"custom","name":"run","format":{"type":"vendor"}}],"stream":true}`, "tools[0].format.type", "request_conversion", "openai-chat", "text and grammar"},
		{"thinking display", "/v1/messages", `{"model":"to-chat","messages":[{"role":"user","content":"Hello"}],"thinking":{"type":"adaptive","display":"summary"}}`, "thinking.display", "request_conversion", "openai-chat", "requires native Messages"},
		{"input validation", "/v1/chat/completions", `{"model":"to-chat","messages":[{"role":"user","content":"Hello"}],"tools":[{"type":"mcp"}]}`, "tools[0].type", "request_validation", "", "only function tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do("POST", tc.path, owner.key, json.RawMessage(tc.raw))
			if r.status != 400 || r.header("X-XHub-Error-Stage") != tc.stage {
				t.Fatalf("错误应归因到网关阶段 %s: %s", tc.stage, r.describe())
			}
			doc := r.json()["error"].(map[string]any)
			message := stringField(doc, "message")
			for _, part := range []string{tc.field, tc.hint, tc.target, "before sending it upstream"} {
				if !strings.Contains(message, part) {
					t.Fatalf("客户端缺少定位信息 %q: %s", part, r.describe())
				}
			}
			if tc.path != "/v1/messages" && doc["param"] != tc.field {
				t.Fatalf("OpenAI param 缺少精确路径: %v", doc)
			}
			for _, private := range []string{"private-model-id", "private-deployment", "sk-private-api-key", "Hello"} {
				if strings.Contains(message, private) {
					t.Fatalf("客户端错误泄露了内部信息 %q", private)
				}
			}
			detail := logDetail(t, h, owner.session, r.header("x-litellm-call-id"))
			diagnosis := detail["metadata"].(map[string]any)["upstream_response"].(map[string]any)
			protocol := diagnosis["protocol"].(map[string]any)
			first := protocol["problems"].([]any)[0].(map[string]any)
			if protocol["stage"] != tc.stage || first["field"] != tc.field ||
				stringField(first, "target_protocol") != tc.target || diagnosis["status_code"] != float64(0) {
				t.Fatalf("真实调用日志与客户端错误不一致: %v", diagnosis)
			}
		})
	}
	if len(h.upstreamCalls()) != 0 || !h.moneyOf(t, owner).grewBy(before, 0) {
		t.Fatal("协议拒绝访问了上游或产生费用")
	}
}

// TestSameProtocolRawExtension 使用真实 Chat 网关和本地上游验证同协议保留精确扩展；独立 PG 和服务由 cleanup 回收。
func TestSameProtocolRawExtension(t *testing.T) {
	seen := make(chan map[string]json.RawMessage, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			return
		}
		seen <- body
		writeChatAgentResponse(w, "same-protocol", "Hello", "", "", false)
	}))
	t.Cleanup(up.Close)
	dep := chatDeployment("native-extension")
	dep.LiteLLMParams["api_base"] = up.URL + "/v1"
	dep.LiteLLMParams["model"] = "actual-upstream-model"
	h := newHarness(t, dep)
	admin := h.adminSession()
	h.ok("POST", "/v1/chat/completions", admin, json.RawMessage(`{
	  "model":"native-extension","messages":[{"role":"user","content":"Hello"}],
	  "vendor_extra":{"number":9007199254740993},"disable_fallbacks":true
	}`))
	select {
	case body := <-seen:
		if string(body["vendor_extra"]) != `{"number":9007199254740993}` ||
			string(body["model"]) != `"actual-upstream-model"` || body["disable_fallbacks"] != nil {
			t.Fatalf("保真出站丢失扩展或重新加入网关字段: %v", body)
		}
	default:
		t.Fatal("同协议没有实际到达本地上游")
	}
}
