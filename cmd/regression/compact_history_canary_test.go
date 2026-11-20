package regression

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestCompactHistoryAccountCanary 通过真实 HTTP 和 PostgreSQL 验证灰度用户、未命中用户与回退后的续接。
// 覆盖个人密钥、会话、JSON、SSE、跨账号拒绝及真实存储格式；夹具自动关闭服务并移除私有 schema。
func TestCompactHistoryAccountCanary(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		id := fmt.Sprintf("chatcmpl-canary-%d", len(requests))
		mu.Unlock()
		if req["stream"] != true {
			doc := chatAnswer("compact-canary", nil)
			doc["id"] = id
			writeJSON(w, doc)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		payload := map[string]any{"id": id, "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": defaultReply.Content}, "finish_reason": "stop"}},
			"usage":   openAIUsage(nil),
		}
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", mustJSON(payload))
	}))
	t.Cleanup(up.Close)
	dep := chatDeployment("compact-canary")
	dep.ModelInfo["endpoint_types"] = []string{"chat", "responses"}
	dep.ModelInfo["id"] = "compact-canary-deployment"
	dep.LiteLLMParams["api_base"] = up.URL
	h := newHarness(t, dep)
	admin := h.adminSession()
	listed := h.openScope(t, admin, "history-listed")
	unlisted := h.openScope(t, admin, "history-unlisted")
	t.Setenv("XHUB_COMPACT_HISTORY", "canary")
	t.Setenv("XHUB_COMPACT_HISTORY_USER_IDS", listed.userID)
	const path = "/v1/responses"
	mkBody := func(input string, stream bool) map[string]any {
		return map[string]any{"model": dep.ModelName, "input": input, "stream": stream, "store": false}
	}
	counts := func(wantCompact, wantLegacy int) {
		t.Helper()
		var compact, legacy int
		err := h.store.DB.QueryRowContext(t.Context(), `SELECT
			(SELECT count(*) FROM conversation_history_manifests),
			(SELECT count(*) FROM kv WHERE kind='conversation_state' AND id LIKE 'session_route:v1:history:%')`).Scan(&compact, &legacy)
		if err != nil || compact != wantCompact || legacy != wantLegacy {
			t.Fatalf("历史格式行数错误，新=%d 旧=%d，预期新=%d 旧=%d: %v", compact, legacy, wantCompact, wantLegacy, err)
		}
	}
	first := h.ok(http.MethodPost, path, listed.key, mkBody("Remember the canary fixture.", false))
	parent := stringField(first.json(), "id")
	if parent == "" {
		t.Fatal("灰度用户未得到响应 ID")
	}
	counts(1, 0)
	// 真实 PostgreSQL 在唯一冲突判断之前触发 BEFORE INSERT。让本例的串行续聊拒绝重复候选，
	// 确保 JSON 首轮到 SSE 续轮实际复用了已有块，同时让出站错误通过网关暴露。
	if _, err := h.store.DB.ExecContext(t.Context(), `CREATE FUNCTION reject_repeated_candidate() RETURNS trigger LANGUAGE plpgsql AS $$
	    BEGIN IF EXISTS(SELECT 1 FROM conversation_history_blocks WHERE scope=NEW.scope AND digest=NEW.digest)
	    THEN RAISE EXCEPTION 'repeated history candidate'; END IF; RETURN NEW; END $$;
	    CREATE TRIGGER check_candidate BEFORE INSERT ON conversation_history_blocks FOR EACH ROW EXECUTE FUNCTION reject_repeated_candidate()`); err != nil {
		t.Fatal(err)
	}
	// 读取完整事件流，只接受成功终态中的可续接响应 ID。
	followSSE := func(input, previous string) string {
		t.Helper()
		body := mkBody(input, true)
		body["previous_response_id"] = previous
		got := h.ok(http.MethodPost, path, listed.key, body)
		for _, line := range strings.Split(got.text(), "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var event struct {
					Type     string `json:"type"`
					Response struct {
						ID string `json:"id"`
					} `json:"response"`
				}
				if json.Unmarshal([]byte(data), &event) == nil && event.Type == "response.completed" && event.Response.ID != "" {
					return event.Response.ID
				}
			}
		}
		t.Fatalf("SSE 未返回可续接的成功终态: %s", got.describe())
		return ""
	}
	second := followSSE("Continue during the canary.", parent)
	counts(2, 0)

	session := h.ok(http.MethodPost, path, listed.session, mkBody("Start a session fixture.", false))
	if stringField(session.json(), "id") == "" {
		t.Fatal("灰度用户会话未得到响应 ID")
	}
	counts(3, 0)
	spoofed := mkBody("Use the unlisted fixture.", false)
	spoofed["user"] = listed.userID
	h.ok(http.MethodPost, path, unlisted.key, spoofed)
	counts(3, 1)
	cross := mkBody("Try a foreign response.", false)
	cross["previous_response_id"] = parent
	if got := h.do(http.MethodPost, path, unlisted.key, cross); got.status != http.StatusBadRequest {
		t.Fatalf("跨调用方续接未在出站前拒绝: %s", got.describe())
	}
	mu.Lock()
	beforeRollback := len(requests)
	mu.Unlock()
	if beforeRollback != 4 {
		t.Fatalf("跨调用方续接不得外发，实际收到 %d 次，预期 4 次", beforeRollback)
	}

	t.Setenv("XHUB_COMPACT_HISTORY", "0")
	followSSE("Continue after disabling.", second)
	mu.Lock()
	seen := append([]map[string]any(nil), requests...)
	mu.Unlock()
	if len(seen) != 5 {
		t.Fatalf("回退后 SSE 续接应恰好新增一次上游请求，实际收到 %d 次", len(seen)-beforeRollback)
	}
	for _, check := range []struct {
		turn   int
		wanted string
	}{
		{1, "Remember the canary fixture."},
		{4, "Continue during the canary."},
	} {
		payload, err := json.Marshal(seen[check.turn]["messages"])
		if err != nil || !strings.Contains(string(payload), check.wanted) {
			t.Fatalf("第 %d 次上游请求未收到压缩父历史: %v", check.turn+1, err)
		}
	}
	counts(3, 2)
}
