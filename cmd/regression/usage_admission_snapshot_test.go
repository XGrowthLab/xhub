package regression

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"xorm.io/xorm/contexts"
)

// pendingAdmissionReads 观察准入在实际私有 PostgreSQL 执行的计数/有序读取次数，不修改 SQL 或结果。
type pendingAdmissionReads struct{ counts, lists atomic.Int32 }

// BeforeProcess 记录带有待结算表的无条件 COUNT 和有序 SELECT，返回原上下文和操作。
func (o *pendingAdmissionReads) BeforeProcess(c *contexts.ContextHook) (context.Context, error) {
	sql := strings.ToLower(c.SQL)
	if strings.Contains(sql, "usage_pending") && strings.Contains(sql, "select") {
		if strings.Contains(sql, "count(") {
			o.counts.Add(1)
		}
		if strings.Contains(sql, "order by") {
			o.lists.Add(1)
		}
	}
	return c.Ctx, nil
}

// AfterProcess 保留驱动返回的错误，由原网关准入处理并体现在 HTTP 中。
func (*pendingAdmissionReads) AfterProcess(*contexts.ContextHook) error { return nil }

// TestAdmissionReadsFrozenPendingOnce 将损坏快照放在真实 PG 中，密钥和会话调用都须在单次快照读取后返回 503，保留坏单。
// 再移除该测试快照，验证空队列也只读取一次，实际调用上游且成功计费。测试结束回收凭据、临时数据库和服务。
func TestAdmissionReadsFrozenPendingOnce(t *testing.T) {
	const model = "pg-admission-snapshot"
	t.Setenv("XHUB_USAGE_SPOOL_DIR", "")
	t.Setenv("XHUB_REGRESSION_REDIS_URL", "")
	h := newHarness(t, chatDeployment(model))
	scope := h.openScope(t, h.adminSession(), model)
	if _, err := h.db.Engine.Exec("INSERT INTO usage_pending(request_id,payload) VALUES('invalid-evidence','{}'::jsonb)"); err != nil {
		t.Fatal(err)
	}
	probe := &pendingAdmissionReads{}
	h.db.Engine.AddHook(probe)
	for _, token := range []string{scope.key, scope.session} {
		before := probe.lists.Load()
		r := h.do(http.MethodPost, "/v1/chat/completions", token, chatRequest(model, "Check pending admission."))
		if r.status != http.StatusServiceUnavailable || !strings.Contains(string(r.body), "billing_storage_pending") || probe.lists.Load() != before+1 || probe.counts.Load() != 0 {
			t.Fatalf("坏单未用一次快照查询阻止访问: HTTP=%d 有序查询=%d 预先计数=%d", r.status, probe.lists.Load()-before, probe.counts.Load())
		}
	}
	if len(h.upstreamCalls()) != 0 {
		t.Fatal("原始 PG 坏单仍然存在时访问了上游")
	}
	ack, err := h.db.Engine.Exec("DELETE FROM usage_pending WHERE request_id='invalid-evidence'")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := ack.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("坏单未保留: %d %v", n, err)
	}
	money := h.moneyOf(t, scope)
	before := probe.lists.Load()
	var readsAtUpstream atomic.Int32
	h.onUpstream(model, func() { readsAtUpstream.Store(probe.lists.Load() - before) })
	h.ok(http.MethodPost, "/v1/chat/completions", scope.key, chatRequest(model, "Verify restored admission."))
	if len(h.upstreamCalls()) != 1 || readsAtUpstream.Load() != 1 || probe.counts.Load() != 0 {
		t.Fatalf("空队列未通过一次快照查询放行: 有序查询=%d 预先计数=%d", probe.lists.Load()-before, probe.counts.Load())
	}
	h.flushSpend()
	if !h.moneyOf(t, scope).grewBy(money, expectedCost()) {
		t.Fatal("准入恢复后的唯一真实 HTTP 调用未精确入账")
	}
}
