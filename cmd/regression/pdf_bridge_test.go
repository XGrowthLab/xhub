package regression

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestInlinePDFRealGateway 验证三种入口经真实网关、隔离数据库与 Chat 上游保持 PDF 字节并结算。
// 使用本机可校验内容的合成上游及个人密钥，覆盖协议转换、非流式、错误输入不外发和费用；harness 清理数据库与服务。
func TestInlinePDFRealGateway(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\nsynthetic\n%%EOF"))
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("上游无法解析 JSON")
			w.WriteHeader(400)
			return
		}
		encoded, _ := json.Marshal(body["messages"])
		if !strings.Contains(string(encoded), `"file_data":"data:application/pdf;base64,`+data+`"`) || !strings.Contains(string(encoded), `"filename":"fixture.pdf"`) {
			t.Errorf("PDF 文件或名称未完整到达上游: %s", encoded)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_pdf","choices":[{"message":{"role":"assistant","content":"PDF_OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`)
	}))
	t.Cleanup(up.Close)
	dep := hardBudgetDeployment("pdf-bridge")
	dep.LiteLLMParams["api_base"] = up.URL
	h := newHarness(t, dep)
	admin := h.adminSession()
	owner := h.openScope(t, admin, "pdf-owner")
	h.setKeyBudget(t, admin, owner.key, 1)
	h.ok("PUT", "/account/log-capture", owner.session, map[string]any{"enabled": true})
	for _, tc := range []struct {
		path, field string
		part        map[string]any
	}{
		{"/v1/chat/completions", "messages", map[string]any{"type": "file", "file": map[string]any{"filename": "fixture.pdf", "file_data": "data:application/pdf;base64," + data}}},
		{"/v1/responses", "input", map[string]any{"type": "input_file", "filename": "fixture.pdf", "file_data": "data:application/pdf;base64," + data}},
		{"/v1/messages", "messages", map[string]any{"type": "document", "title": "fixture.pdf", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": data}}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body := map[string]any{"model": dep.ModelName, tc.field: []any{map[string]any{"role": "user", "content": []any{tc.part}}}}
			if tc.path == "/v1/messages" {
				body["max_tokens"] = 64
			}
			got := h.do("POST", tc.path, owner.key, body)
			if got.status != 200 || !strings.Contains(got.text(), "PDF_OK") || parseFloatOrZero(got.header("x-litellm-response-cost")) <= 0 {
				t.Fatalf("%s 文件调用或计费失败: %s", tc.path, got.describe())
			}
			detail := logDetail(t, h, owner.session, got.header("x-litellm-call-id"))
			encoded, _ := json.Marshal(detail)
			if detail["body_capture_status"] != "saved" || strings.Contains(string(encoded), data) || !strings.Contains(string(encoded), "file content omitted") {
				t.Fatalf("%s 正文记录未安全省略文件内容: %s", tc.path, encoded)
			}
		})
	}
	before := calls.Load()
	bad := map[string]any{"model": dep.ModelName, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "file", "file": map[string]any{"filename": "bad.pdf", "file_data": "data:application/pdf;base64,???"}}}}}}
	got := h.do("POST", "/v1/chat/completions", owner.key, bad)
	if got.status < 400 || calls.Load() != before {
		t.Fatal(fmt.Sprintf("无效文件未在外发前拒绝: %s", got.describe()))
	}
}
