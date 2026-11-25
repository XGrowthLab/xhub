package util

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWriteTypedErrorUsesProviderEnvelope 验证 HTTP 错误与失败日志共用协议包络，状态、限流响应头和字段也保持一致。
// 参数 t 为测试上下文；使用内存响应与三种入站协议，无外部资源和清理步骤。
func TestWriteTypedErrorUsesProviderEnvelope(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		check func(*testing.T, map[string]any)
	}{
		{name: "openai", path: "/v1/chat/completions", check: func(t *testing.T, body map[string]any) {
			err := nestedMap(t, body, "error")
			if err["type"] != "rate_limit" || err["code"] != "429" {
				t.Fatalf("OpenAI error = %#v", err)
			}
		}},
		{name: "anthropic", path: "/v1/messages", check: func(t *testing.T, body map[string]any) {
			if body["type"] != "error" || nestedMap(t, body, "error")["type"] != "rate_limit" {
				t.Fatalf("Anthropic error = %#v", body)
			}
		}},
		{name: "gemini", path: "/v1beta/models/gemini:generateContent", check: func(t *testing.T, body map[string]any) {
			err := nestedMap(t, body, "error")
			if err["status"] != "RESOURCE_EXHAUSTED" || err["code"] != float64(429) {
				t.Fatalf("Gemini error = %#v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			WriteTypedError(rr, tt.path, http.StatusTooManyRequests, "rate_limit", "slow down")
			if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") != "1" {
				t.Fatalf("status/header = %d/%q", rr.Code, rr.Header().Get("Retry-After"))
			}
			if len(rr.Header().Get("x-litellm-call-id")) != 32 || rr.Header().Get("x-litellm-version") == "" {
				t.Fatalf("missing gateway headers: %#v", rr.Header())
			}
			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			tt.check(t, body)
			if serialized := TypedErrorBody(tt.path, http.StatusTooManyRequests, "rate_limit", "slow down", ""); !bytes.Equal(serialized, rr.Body.Bytes()) || serialized[len(serialized)-1] != '\n' {
				t.Fatalf("错误日志包络与 HTTP 响应不一致: %q, want %q", serialized, rr.Body.Bytes())
			}
		})
	}
}

func TestWriteJSONPreservesExistingHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	rr.Header().Set("Content-Type", "application/problem+json")
	rr.Header().Set("x-litellm-call-id", "existing")
	rr.Header().Set("x-litellm-version", "existing-version")
	WriteJSON(rr, http.StatusAccepted, map[string]bool{"ok": true})
	if rr.Code != http.StatusAccepted || rr.Header().Get("Content-Type") != "application/problem+json" ||
		rr.Header().Get("x-litellm-call-id") != "existing" || rr.Header().Get("x-litellm-version") != "existing-version" {
		t.Fatalf("response = status %d headers %#v", rr.Code, rr.Header())
	}
}

func nestedMap(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	got, ok := body[key].(map[string]any)
	if !ok {
		t.Fatalf("%q = %#v, want object", key, body[key])
	}
	return got
}

// TestTypedErrorBodyField 验证字段路径和包含引号、换行的原消息可无损解码。
// 参数 t 为测试上下文；使用内存 JSON，验证 OpenAI 错误对象，无清理。
func TestTypedErrorBodyField(t *testing.T) {
	const detail = "Invalid \"image\".\nUse a URL."
	var doc map[string]any
	if err := json.Unmarshal(TypedErrorBody("/v1/responses", 400, "invalid_request_error", detail, "input[0].image_url"), &doc); err != nil {
		t.Fatal(err)
	}
	if fields := nestedMap(t, doc, "error"); fields["message"] != detail || fields["param"] != "input[0].image_url" {
		t.Fatalf("错误消息或字段路径丢失: %v", fields)
	}
}
