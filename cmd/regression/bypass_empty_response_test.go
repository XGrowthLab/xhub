package regression

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sunqirui1987/xhub/internal/iam"
)

// TestBypassEmptyResponseAccounting 使用真实网关、隔离数据库与本地上游验证空响应、账单及恢复。
// 上游首个请求返回空 200，之后返回确认用量；测试检查不自动重试，已确认费用仍结算，夹具清理数据库和服务器。
func TestBypassEmptyResponseAccounting(t *testing.T) {
	t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"msg_verified","type":"message","role":"assistant","model":"empty-test","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":2}}`)
	}))
	t.Cleanup(up.Close)
	dep := hardBudgetDeployment("empty-test")
	dep.LiteLLMParams["api_base"] = up.URL
	dep.ModelInfo["transport"] = "bypass_anthropic_messages"
	dep.ModelInfo["endpoint_types"] = []string{"bypass:anthropic-messages"}
	h := newHarness(t, dep)
	owner := h.openScope(t, h.adminSession(), "empty-owner")
	body := map[string]any{"model": "empty-test", "messages": []any{map[string]any{"role": "user", "content": "Hello"}}, "max_tokens": 32}
	before := h.moneyOf(t, owner)
	failed := h.do("POST", "/bypass/anthropic/v1/messages", owner.key, body)
	if failed.status != 502 || calls.Load() != 1 {
		t.Fatalf("空响应应返回 502 且不得重试: status=%d calls=%d body=%s", failed.status, calls.Load(), failed.describe())
	}
	h.flushSpend()
	id := failed.header("x-litellm-call-id")
	event, err := h.db.GetUsageEvent(t.Context(), iam.UsageQuery{}, id)
	if err != nil || event.Status != "error" || event.HTTPStatus != 502 || event.Cost != 0 {
		t.Fatalf("空响应应记失败并保留零网关费用: event=%+v err=%v", event, err)
	}
	row, err := h.db.GetRequestLog(t.Context(), id)
	if err != nil || !strings.Contains(row.UpstreamResponse, `"empty_response"`) || !strings.Contains(row.UpstreamResponse, `"upstream_usage_missing"`) {
		t.Fatalf("应保留上游空响应及用量缺失诊断: row=%+v err=%v", row, err)
	}
	var diagnostic map[string]any
	if err := json.Unmarshal([]byte(row.UpstreamResponse), &diagnostic); err != nil || diagnostic["status_code"] != float64(200) {
		t.Fatalf("应保留上游实际 200 状态: diagnostic=%v err=%v", diagnostic, err)
	}
	if after := h.moneyOf(t, owner); after != before {
		t.Fatalf("空响应不应扣减预算: before=%+v after=%+v", before, after)
	}
	success := h.do("POST", "/bypass/anthropic/v1/messages", owner.key, body)
	if success.status != 200 || calls.Load() != 2 || !strings.Contains(success.text(), "msg_verified") {
		t.Fatalf("用户重新请求应成功: status=%d calls=%d body=%s", success.status, calls.Load(), success.describe())
	}
	h.flushSpend()
	confirmed, err := h.db.GetUsageEvent(t.Context(), iam.UsageQuery{}, success.header("x-litellm-call-id"))
	if err != nil || confirmed.Status != "success" || confirmed.Cost <= 0 || confirmed.PromptTokens != 8 || confirmed.CompletionTokens != 2 {
		t.Fatalf("上游确认用量必须正常结算: event=%+v err=%v", confirmed, err)
	}
}
