package regression

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/gateway/requestlog"
)

// TestUserContractLegacyLogBodyRetention 在独立数据库中模拟旧版请求日志，
// 验证只读试算、锁等待回滚、并发清理、保留账单和诊断，以及真实详情接口的权限。夹具自动回收 schema。
func TestUserContractLegacyLogBodyRetention(t *testing.T) {
	h := openHarness(t, false)
	admin := h.adminSession()
	member := h.provision(t, admin, "legacy-log-member")
	other := h.provision(t, admin, "legacy-log-other")
	addPricedModel(t, h, admin, "retention-model", tokenRates(0.000002, 0.00001), "chat")
	ctx := t.Context()
	db := h.db.Engine.DB().DB
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Microsecond)
	var ids []string
	for _, ts := range []time.Time{cutoff, cutoff.Add(time.Hour), cutoff.Add(-time.Hour), cutoff.Add(-time.Hour)} {
		response := h.ok(http.MethodPost, "/v1/chat/completions", member.key, chatRequest("retention-model", "Synthetic legacy request."))
		h.flushSpend()
		id := response.header("x-litellm-call-id")
		ids = append(ids, id)
		if _, err := db.ExecContext(ctx, "UPDATE usage_events SET ts = $1 WHERE request_id = $2", ts, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids[:3] {
		diag := `{"status_code":503,"usage":{"output_tokens":7},"error":{"error":{"code":"legacy_error","payload":{"text":"vendor-body-marker"}}}}`
		if id == ids[2] {
			diag = strings.Repeat("?", 65537)
		}
		if _, err := db.ExecContext(ctx, `UPDATE request_logs SET body_capture_status = '',
request_body = '[{"role":"user","content":"private-input"}]', response_body = 'private-output',
proxy_request = '{"messages":[{"content":"private-copy"}]}',
error = '{"error":{"code":"legacy_error","message":"Useful summary","payload":{"text":"private-output"}}}',
upstream_response = $1 WHERE request_id = $2`, diag, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.WriteHistory(ctx, "legacy-retention-history", "test-scope", `[{"role":"user","content":"history-marker"}]`); err != nil {
		t.Fatal(err)
	}
	before := logDetail(t, h, member.session, ids[0])
	if !strings.Contains(string(mustJSON(before["messages"])), "private-input") {
		t.Fatal("测试未读到旧正文")
	}
	first, err := requestlog.ProcessLegacyLogBatch(ctx, db, cutoff, 0, 1, false)
	if err != nil || first.Scanned != 1 || first.Eligible != 1 || first.Cleared != 0 || first.LegacyBodyBytes == 0 || first.Done {
		t.Fatalf("只读试算未准确命中一条记录: %+v %v", first, err)
	}
	next, err := requestlog.ProcessLegacyLogBatch(ctx, db, cutoff, first.Next, 10, false)
	if err != nil || next.Scanned != 3 || next.Eligible != 1 || next.UnchangedDiagnostics != 1 || !next.Done {
		t.Fatalf("游标、近期正文、独立正文或未知诊断统计错误: %+v %v", next, err)
	}
	if check := logDetail(t, h, member.session, ids[0]); check["response"] != before["response"] {
		t.Fatal("只读试算改变了正文")
	}
	// 有同一日志的并发持锁者时整批失败，重试可以从原游标开始。
	lock, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.ExecContext(ctx, "SELECT request_id FROM request_logs WHERE request_id = $1 FOR UPDATE", ids[2]); err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if blocked, err := requestlog.ProcessLegacyLogBatch(cancelCtx, db, cutoff, 0, 10, true); err == nil || blocked.Next != 0 {
		t.Fatalf("并发锁未阻止游标前移: %+v %v", blocked, err)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	cleared := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := requestlog.ProcessLegacyLogBatch(ctx, db, cutoff, 0, 10, true)
			if err != nil {
				t.Errorf("重试清理失败: %v", err)
			}
			cleared <- result.Cleared
		}()
	}
	wg.Wait()
	if sum := <-cleared + <-cleared; sum != 2 {
		t.Fatalf("并发清理遗漏或重复记录: %d", sum)
	}
	after := logDetail(t, h, member.session, ids[0])
	afterText := string(mustJSON(after))
	if after["body_capture_status"] != "expired" || after["spend"] != before["spend"] || after["total_tokens"] != before["total_tokens"] ||
		!strings.Contains(afterText, "Useful summary") || !strings.Contains(afterText, "status_code") ||
		strings.Contains(afterText, "private-") || strings.Contains(afterText, "vendor-body-marker") {
		t.Fatalf("清理影响账单或遗漏旧正文: body_capture_status=%v", after["body_capture_status"])
	}
	if recent := logDetail(t, h, member.session, ids[1]); !strings.Contains(string(mustJSON(recent["messages"])), "private-input") {
		t.Fatal("清理误删除近期正文")
	}
	if logs, err := h.db.GetRequestLog(ctx, ids[2]); err != nil || len(logs.UpstreamResponse) != 65537 {
		t.Fatalf("不能识别的诊断未保留: %v", err)
	}
	if got := h.do(http.MethodGet, "/spend/logs/ui/"+ids[0], other.session, nil); got.status != 404 && got.status != 403 {
		t.Fatalf("清理后其他用户可以读取日志: %s", got.describe())
	}
	if history, err := h.store.ReadHistory(ctx, "legacy-retention-history", "test-scope"); err != nil || !strings.Contains(history, "history-marker") {
		t.Fatalf("清理影响保存的续聊历史: %v", err)
	}
}
