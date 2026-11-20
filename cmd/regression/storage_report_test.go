package regression

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sunqirui1987/xhub/internal/storageaudit"
)

// TestStorageReportGatewayAttribution 经真实网关分别发送标准 Chat、Responses 转 Chat 和流式显式透传。
// 核对数据库统计中的两个协议维度、账单和历史行，并验证权限失败仍按现有接口返回；测试结束删除私有 schema。
func TestStorageReportGatewayAttribution(t *testing.T) {
	dep := chatDeployment("storage-audit")
	dep.ModelInfo["id"] = "storage-audit-fixture"
	dep.ModelInfo["endpoint_types"] = []string{"responses", "chat"}
	h := newHarness(t, dep)
	scope := h.openScope(t, h.adminSession(), "capacity-reporter")
	t.Setenv("XHUB_COMPACT_HISTORY", "0")
	req := chatRequest(dep.ModelName, "private-report-fixture")
	h.ok(http.MethodPost, "/v1/chat/completions", scope.key, req)
	h.ok(http.MethodPost, "/v1/responses", scope.key, map[string]any{"model": dep.ModelName, "input": "private-report-fixture"})
	req["stream"] = true
	if got := h.ok(http.MethodPost, "/bypass/openai/v1/chat/completions", scope.key, req); !strings.Contains(got.text(), "[DONE]") {
		t.Fatal("流式透传未完成")
	}
	if got := h.do(http.MethodPost, "/v1/chat/completions", "invalid-report-credential", req); got.status != 401 {
		t.Fatalf("无效密钥返回 %d", got.status)
	}
	snapshot, err := storageaudit.Capture(t.Context(), h.store.DB, "regression", 1)
	if err != nil {
		t.Fatal(err)
	}
	type route struct {
		in, out        string
		bypass, stream bool
	}
	seen := map[route]int64{}
	for _, r := range snapshot.Routes {
		if r.Streamed != nil && r.Outbound != "unrecorded" {
			seen[route{r.Inbound, r.Outbound, r.Bypass, *r.Streamed}] += r.Calls
		}
	}
	for _, expected := range []route{{"chat", "chat", false, false}, {"responses", "chat", false, false}, {"chat", "chat", true, true}} {
		if seen[expected] != 1 {
			t.Fatalf("实际请求的协议归因错误: %+v", seen)
		}
	}
	var histories int64
	for _, h := range snapshot.History {
		if h.Kind == "history" {
			histories += h.Records
		}
	}
	if histories != 1 {
		t.Fatalf("三条不同入口请求应只有一份网关续聊历史，实有 %d", histories)
	}
	var charges int
	if err = h.store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM usage_events WHERE cost>0 AND key_id=$1`, scope.keyID).Scan(&charges); err != nil || charges != 3 {
		t.Fatalf("实调账单笔数错误: %d %v", charges, err)
	}
	b, _ := json.Marshal(snapshot)
	if strings.Contains(string(b), "private-report-fixture") || strings.Contains(string(b), scope.userID) {
		t.Fatal("报告包含调用内容或账号")
	}
}
