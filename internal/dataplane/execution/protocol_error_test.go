package execution

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
)

// TestProtocolValidationEnvelope 验证三种原生错误包络都带字段，且未知类型不泄露凭据；内存记录器无需清理。
func TestProtocolValidationEnvelope(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1beta/models/model:generateContent"} {
		w := httptest.NewRecorder()
		writeProtocolValidation(w, path, "fixture-source", &llmdialogue.RequestFieldError{Field: "tools[0].type", Message: "unsupported type sk-fixture-secret"})
		var body struct {
			Type  string `json:"type"`
			Error struct {
				Param   string `json:"param"`
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != 400 || w.Header().Get("X-XHub-Error-Stage") != "request_validation" ||
			w.Header().Get("x-litellm-call-id") == "" || !strings.Contains(body.Error.Message, "tools[0].type") ||
			strings.Contains(body.Error.Message, "sk-fixture-secret") {
			t.Fatalf("%s 字段、关联 ID 或脱敏不正确: %s", path, w.Body)
		}
		switch path {
		case "/v1/chat/completions":
			if body.Error.Param != "tools[0].type" {
				t.Fatal("OpenAI 错误未保留 param")
			}
		case "/v1/messages":
			if body.Type != "error" {
				t.Fatal("Anthropic 错误包络改变")
			}
		default:
			if body.Error.Status != "INVALID_ARGUMENT" {
				t.Fatal("Gemini 错误包络改变")
			}
		}
	}
}

// TestProtocolRejectionMultipleFields 验证多个目标的字段全部显示，内部部署 ID 只留日志；内存记录器无需清理。
func TestProtocolRejectionMultipleFields(t *testing.T) {
	w := httptest.NewRecorder()
	problems := []dpdiagnostics.ProtocolProblem{
		dpdiagnostics.NewProtocolProblem("tools[0].type", "openai-chat", "use a function tool"),
		dpdiagnostics.NewProtocolProblem("thinking.display", "anthropic-messages", "unsupported display option"),
	}
	writeProtocolRejection(w, "/v1/responses", 400, "model_unavailable", "request_conversion", "openai-responses", "", problems,
		map[string]any{"deployment_id": "private-id"})
	for _, expected := range []string{"tools[0].type", "thinking.display", "anthropic-messages", `"param":null`} {
		if !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("转换错误缺少 %s: %s", expected, w.Body)
		}
	}
	if strings.Contains(w.Body.String(), "private-id") {
		t.Fatal("内部目录泄露至客户端")
	}
}
