package execution

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// streamProbeTestHost 复用数据面宿主，仅记录准备探针的调用并模拟护栏结果；不访问真实磁盘或供应商。
type streamProbeTestHost struct {
	*logHost
	blocked bool
	calls   []string
}

// WithStreamBudgetProbe 记录当前请求标识并返回传入请求，供边界测试核对当前尝试的探针次数。
func (h *streamProbeTestHost) WithStreamBudgetProbe(r *http.Request, id string) *http.Request {
	h.calls = append(h.calls, id)
	return r
}

// GuardrailBlocks 返回本场景配置的拦截结果，便于确认被拒绝的大正文不触发磁盘操作。
func (h *streamProbeTestHost) GuardrailBlocks(string, map[string]any, ...bool) (bool, string) {
	return h.blocked, "fixture rule"
}

// TestServeStreamBudgetProbeBoundary 从统一 Chat 入口送入精确大小的请求，验证阈值两侧、同步请求及护栏拒绝。
// 空路由表确保不会访问上游，全部数据留在本次测试内；等于阈值的合规流式请求应恰好准备一次。
func TestServeStreamBudgetProbeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name            string
		size            int
		stream, blocked bool
		want            int
	}{
		{"below", streamBudgetProbeMinBytes - 1, true, false, 0}, {"at-limit", streamBudgetProbeMinBytes, true, false, 1},
		{"above", streamBudgetProbeMinBytes + 1, true, false, 1}, {"sync", streamBudgetProbeMinBytes, false, false, 0},
		{"blocked", streamBudgetProbeMinBytes, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &streamProbeTestHost{logHost: newLogHost(chatConfig(), http.DefaultClient), blocked: tc.blocked}
			prefix := `{"model":"probe-window","stream":` + strconv.FormatBool(tc.stream) + `,"messages":[{"role":"user","content":"`
			suffix := `"}]}`
			payload := prefix + strings.Repeat("A", tc.size-len(prefix)-len(suffix)) + suffix
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
			w := httptest.NewRecorder()
			Serve(h, w, req, "chat")
			if len(h.calls) != tc.want || len(h.calls) > 0 && h.calls[0] != w.Header().Get("x-litellm-call-id") {
				t.Fatalf("探针次数或请求标识错误：size=%d stream=%v blocked=%v calls=%v", len(payload), tc.stream, tc.blocked, h.calls)
			}
			if tc.blocked && w.Code != http.StatusBadRequest {
				t.Fatalf("护栏拒绝的状态码错误：%d", w.Code)
			}
		})
	}
}
