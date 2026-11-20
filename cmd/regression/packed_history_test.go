package regression

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/storageaudit"
)

// TestPackedHistoryContinuation 经真实 HTTP 生成旧历史，转换后以同一调用方继续 SSE；核对上游真实收到此前提示，
// 另一账号不能续接、已计费条目未重写，损坏帧在出站之前失败。合成供应商和私有 schema 由夹具清理。
func TestPackedHistoryContinuation(t *testing.T) {
	t.Setenv("XHUB_COMPACT_HISTORY", "0")
	dep := chatDeployment("pack-flow")
	dep.ModelInfo["endpoint_types"] = []string{"chat", "responses"}
	dep.ModelInfo["id"] = "pack-flow-id"
	h := newHarness(t, dep)
	who := h.openScope(t, h.adminSession(), "pack-owner")
	other := h.openScope(t, h.adminSession(), "pack-other")
	const path = "/v1/responses"
	msg := strings.Repeat("Retain the original tool context. ", 30)
	first := h.ok(http.MethodPost, path, who.key, map[string]any{"model": dep.ModelName, "input": msg, "store": false})
	id := stringField(first.json(), "id")
	if id == "" {
		t.Fatal("首轮没有响应 ID")
	}
	// 测试只对这条合成记录设定过去的创建时间，使固定截止点无需墙钟等待。
	if _, err := h.store.DB.Exec("UPDATE kv SET created_at='2026-01-01T00:00:00Z' WHERE kind='conversation_state' AND id LIKE 'session_route:v1:history:%'"); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	o := storageaudit.PlanOptions{Before: time.Now().UTC().Truncate(time.Second), Rows: 5, BatchBytes: 1 << 20, RecordBytes: 1 << 20, ReserveBytes: 64}
	// 在 HTTP 生成的数据上验证备份版本筛选，另一账号仍走原来的隔离检查。
	var key, sha string
	if err := h.store.DB.QueryRow("SELECT right(id,64),encode(sha256(convert_to(body,'UTF8')),'hex') FROM kv WHERE kind='conversation_state' AND id LIKE 'session_route:v1:history:%'").Scan(&key, &sha); err != nil {
		t.Fatal(err)
	}
	var err error
	o.Preimages, err = storageaudit.ReadPreimages(strings.NewReader(fmt.Sprintf("%s %s", key, sha)))
	if err != nil {
		t.Fatal(err)
	}
	p, err := storageaudit.Convert(ctx, h.store.DB, o, "pack")
	if err != nil || p.Selected != 1 {
		t.Fatalf("网关产生的历史未能转换: %+v %v", p, err)
	}
	parent := map[string]any{"model": dep.ModelName, "input": "Continue.", "previous_response_id": id, "stream": true, "store": false}
	if got := h.do(http.MethodPost, path, other.key, parent); got.status != 400 {
		t.Fatalf("跨调用方续接返回 %d", got.status)
	}
	var originalDigest []byte
	if err = h.store.DB.QueryRow("SELECT history_sha256 FROM kv WHERE history_frame IS NOT NULL").Scan(&originalDigest); err != nil {
		t.Fatal(err)
	}
	if _, err = h.store.DB.Exec("UPDATE kv SET history_sha256='bad'::bytea WHERE history_frame IS NOT NULL"); err != nil {
		t.Fatal(err)
	}
	if got := h.do(http.MethodPost, path, who.key, parent); got.status < 400 || len(h.upstreamCalls()) != 1 {
		t.Fatalf("损坏父历史仍被用于调用: %d", got.status)
	}
	if _, err = h.store.DB.Exec("UPDATE kv SET history_sha256=$1 WHERE history_frame IS NOT NULL", originalDigest); err != nil {
		t.Fatal(err)
	}

	if got := h.ok(http.MethodPost, path, who.key, parent); !strings.Contains(got.text(), "response.completed") {
		t.Fatal("压缩父历史未能完成 SSE")
	}
	calls := h.upstreamCalls()
	if len(calls) != 2 {
		t.Fatalf("错误调用不应发给供应商，实际 %d 次", len(calls))
	}
	upstream, _ := json.Marshal(calls[1].Body["messages"])
	if !strings.Contains(string(upstream), msg) {
		t.Fatal("上游未收到原始历史")
	}
	var charges int
	if err = h.store.DB.QueryRow("SELECT count(*) FROM usage_events WHERE key_id=$1 AND cost>0", who.keyID).Scan(&charges); err != nil || charges != 2 {
		t.Fatalf("压缩前后账单异常: %d %v", charges, err)
	}
}
