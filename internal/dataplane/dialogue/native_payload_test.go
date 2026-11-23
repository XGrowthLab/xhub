package dialogue

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMarshalNativePayload 前置同协议原文及治理后的正文，验证精确数字、改写和删除优先级；内存数据无需清理。
func TestMarshalNativePayload(t *testing.T) {
	const body = `{"model":"public","guardrails":["fixture"],"messages":[{"content":"original"}],"vendor_extension":{"number":9007199254740993},"store":true}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "guardrails")
	payload["model"], payload["store"] = "upstream", false
	payload["messages"] = []any{map[string]any{"content": "masked"}}
	encoded, err := marshalNativePayload(payload, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"model":"upstream"`, `"store":false`, `"number":9007199254740993`, `"content":"masked"`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("同协议原文或治理结果丢失 %s: %s", expected, encoded)
		}
	}
	if strings.Contains(string(encoded), "original") || strings.Contains(string(encoded), "guardrails") {
		t.Fatalf("原文恢复了已修改或删除的内容: %s", encoded)
	}
	if fallback, err := marshalNativePayload(payload, []byte("invalid")); err != nil || !json.Valid(fallback) {
		t.Fatalf("原文不可用时应使用治理后正文: %s %v", fallback, err)
	}
}
