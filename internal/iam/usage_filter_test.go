package iam

import (
	"context"
	"strings"
	"testing"

	"github.com/sunqirui1987/xhub/internal/logframe"
)

// TestUsageLogCategories 验证日志分类在数据库分页前执行且普通日志和错误日志互补。
// 参数 t 为测试上下文；前置私有数据库，覆盖任务各阶段、旧失败状态、HTTP 400 边界与空状态；
// 查询结果应互不重叠并覆盖全部记录，失败状态不能进入成功筛选；schema 随测试自动清理。
func TestUsageLogCategories(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, tc := range []struct {
		id, status string
		code       int
	}{
		{"success", "success", 200}, {"completed", "completed", 200},
		{"executing", "executing", 200}, {"polling", "polling", 200},
		{"redirect", "success", 399}, {"empty", "", 0},
		{"error", "error", 502}, {"failed", "failed", 200}, {"failure", "failure", 200},
		{"http-error", "success", 400},
	} {
		r := usageRecord(tc.id, "", 0)
		r.Status, r.HTTPStatus = tc.status, tc.code
		if err := db.RecordUsage(ctx, []UsageRecord{r}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		filter string
		count  int
	}{{"non_error", 6}, {"error", 4}, {"failed", 4}, {"success", 4}, {"polling", 1}, {"missing", 0}} {
		rows, err := db.ListUsage(ctx, UsageQuery{Status: tc.filter})
		if err != nil || len(rows) != tc.count {
			t.Fatalf("分类 %s 应有 %d 条，实际 %d，错误 %v", tc.filter, tc.count, len(rows), err)
		}
		for _, row := range rows {
			failed := row.Status == "error" || row.Status == "failed" || row.Status == "failure" || row.HTTPStatus >= 400
			if (tc.filter == "non_error" || tc.filter == "success") && failed {
				t.Fatalf("失败请求进入普通日志: %+v", row)
			}
			if (tc.filter == "error" || tc.filter == "failed") && !failed {
				t.Fatalf("非失败请求进入错误日志: %+v", row)
			}
		}
	}
	rows, err := db.ListUsage(ctx, UsageQuery{Status: "non_error", Limit: 2, Offset: 2})
	if err != nil || len(rows) != 2 {
		t.Fatalf("分类后分页应保持满页: rows=%d err=%v", len(rows), err)
	}
	for _, tc := range []struct {
		q     UsageQuery
		count int
	}{
		{UsageQuery{Status: "non_error", Search: "error"}, 0},
		{UsageQuery{Status: "error", Search: "http-error"}, 1},
		{UsageQuery{RequestID: "success"}, 1},
		{UsageQuery{Search: "%"}, 0}, {UsageQuery{Search: "_"}, 0},
		{UsageQuery{Search: "' OR true --"}, 0},
	} {
		found, err := db.ListUsage(ctx, tc.q)
		if err != nil || len(found) != tc.count {
			t.Fatalf("请求 ID 搜索 %+v 应有 %d 条，实际 %d，错误 %v", tc.q, tc.count, len(found), err)
		}
	}
}

// TestUsageSeverityFilters 验证 SQL 分级对历史状态、4xx、5xx 与无状态码失败的分类与单行诊断一致。
// 前置私有 PostgreSQL；覆盖权限交集、分页与会话计数，testDB 自动回收私有 schema。
func TestUsageSeverityFilters(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	cases := []struct {
		id, status, severity string
		code                 int
	}{
		{"legacy-failure", "failed", "error", 200},
		{"missing-http", "error", "error", 0},
		{"not-yet-failed", "polling", "success", 200},
		{"first-warning", "success", "warning", 400},
		{"last-warning", "error", "warning", 499},
		{"first-error", "success", "error", 500},
	}
	for _, tc := range cases {
		rec := usageRecord(tc.id, "", 0)
		rec.UserID, rec.SessionID, rec.Status, rec.HTTPStatus = "owner", "same-session", tc.status, tc.code
		if err := db.RecordUsage(ctx, []UsageRecord{rec}); err != nil {
			t.Fatal(err)
		}
		if got := (UsageEvent{Status: tc.status, HTTPStatus: tc.code}).LogSeverity(); got != tc.severity {
			t.Fatalf("%s 应为 %s，得到 %s", tc.id, tc.severity, got)
		}
	}
	foreign := usageRecord("foreign-warning", "", 0)
	foreign.UserID, foreign.Status, foreign.HTTPStatus = "foreign", "error", 401
	if err := db.RecordUsage(ctx, []UsageRecord{foreign}); err != nil {
		t.Fatal(err)
	}
	for level, want := range map[string]int64{"error": 3, "warning": 2, "invalid": 0} {
		q := UsageQuery{UserID: "owner", Status: "error", Severity: level, Limit: 1, Offset: 1}
		count, err := db.CountUsage(ctx, q)
		if err != nil || count != want {
			t.Fatalf("级别 %s 计数应为 %d，得到 %d %v", level, want, count, err)
		}
		list, err := db.ListUsage(ctx, q)
		if err != nil || want == 0 && len(list) != 0 ||
			want > 0 && (len(list) != 1 || list[0].LogSeverity() != level) {
			t.Fatalf("级别 %s 分页未按分类收窄: %v %v", level, list, err)
		}
		q.Offset = 0
		sessions, groups, err := db.ListUsageSessions(ctx, q)
		if err != nil || want == 0 && (groups != 0 || len(sessions) != 0) ||
			want > 0 && (groups != 1 || len(sessions) != 1 || sessions[0].SessionCount != want) {
			t.Fatalf("级别 %s 会话筛选与计数不符: %v %d %v", level, sessions, groups, err)
		}
	}
}

// TestLogSummariesByIDsBoundsStoredEvidence 验证已授权请求的批量摘要不会加载原始正文。
// 前置私有 PostgreSQL，构造良好、超限及损坏压缩记录；结果按请求 ID 收窄，坏行降级，schema 自动回收。
func TestLogSummariesByIDsBoundsStoredEvidence(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	raw := `{"status_code":502,"failure":{"phase":"response_transfer"}}`
	compressed, err := logframe.Encode(raw)
	if err != nil {
		t.Fatal(err)
	}
	oversize := strings.Repeat("x", 64<<10+1)
	oversizeCompressed, err := logframe.Encode(oversize)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, errorText, upstream string }{
		{"bounded", "summary", compressed},
		{"oversize", oversize, oversizeCompressed},
		{"broken", "error", logframe.Prefix + "invalid"},
		{"excluded", "other", raw},
	} {
		if _, err := db.Engine.Exec("INSERT INTO usage_events(request_id,owner_type) VALUES(?,'personal')", tc.id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Engine.Exec("INSERT INTO request_logs(request_id,error,upstream_response,request_body,proxy_request,response_body) VALUES(?,?,?,?,?,?)",
			tc.id, tc.errorText, tc.upstream, "private request", "private proxy", "private response"); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.LogSummariesByIDs(ctx, []string{"bounded", "oversize", "broken", "absent"})
	if err != nil || len(rows) != 3 || rows["bounded"].UpstreamResponse != raw || rows["bounded"].Error != "summary" ||
		rows["oversize"].UpstreamResponse != "" || rows["oversize"].Error != "" || rows["broken"].UpstreamResponse != "" {
		t.Fatalf("摘要读取边界不符合预期: %v %v", rows, err)
	}
	for _, row := range rows {
		if row.RequestBody != "" || row.ProxyRequest != "" || row.ResponseBody != "" {
			t.Fatal("摘要查询不得提取私有请求或响应正文")
		}
	}
	if none, err := db.LogSummariesByIDs(ctx, nil); err != nil || len(none) != 0 {
		t.Fatal("空 ID 过滤不得返回其他日志", err)
	}
}

// TestUsageErrorFilters 验证最终状态和完整错误正文在分页、计数及会话聚合前共同收窄。
// 前置私有数据库，覆盖字面通配符、非法码、缺少详情、零匹配和身份隔离；testDB清理schema。
func TestUsageErrorFilters(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, tc := range []struct {
		id, user, message string
		code              int
	}{
		{"budget-a", "owner", "budget_exceeded %_", 403}, {"budget-b", "owner", "budget_exceeded second", 403},
		{"rate", "owner", "rate_limit", 429}, {"foreign", "other", "budget_exceeded %_", 403},
		{"missing", "owner", "", 402},
	} {
		r := usageRecord(tc.id, "", 0)
		r.UserID, r.SessionID, r.Status, r.HTTPStatus, r.Error = tc.user, "shared", "error", tc.code, tc.message
		if err := db.RecordUsage(ctx, []UsageRecord{r}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Engine.Exec("DELETE FROM request_logs WHERE request_id = ?", "missing"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		code, message string
		count         int
	}{
		{"", "", 4}, {"403", "", 2}, {"429", "", 1}, {"402", "", 1}, {"403", "budget_exceeded", 2},
		{"403", "%_", 1}, {"429", "budget_exceeded", 0}, {"", "no-such-error", 0},
		{"", "' OR true --", 0}, {"invalid", "", 0}, {"99", "", 0}, {"600", "", 0}, {"403 OR true", "", 0},
	} {
		q := UsageQuery{UserID: "owner", ErrorCode: tc.code, ErrorMessage: tc.message, Limit: 1}
		count, err := db.CountUsage(ctx, q)
		if err != nil || count != int64(tc.count) {
			t.Fatalf("筛选%+v计数错误: %d %v", tc, count, err)
		}
		rows, err := db.ListUsage(ctx, q)
		want := min(tc.count, 1)
		if err != nil || len(rows) != want {
			t.Fatalf("筛选%+v分页错误: %d %v", tc, len(rows), err)
		}
		groups, total, err := db.ListUsageSessions(ctx, q)
		if err != nil || len(groups) != want || total != int64(want) {
			t.Fatalf("会话筛选%+v错误: %d %d %v", tc, len(groups), total, err)
		}
		if want > 0 && groups[0].SessionCount != int64(tc.count) {
			t.Fatalf("会话统计未收窄: %+v", groups)
		}
	}
	q := UsageQuery{UserID: "owner", ErrorCode: "403", Limit: 1, Offset: 1}
	rows, err := db.ListUsage(ctx, q)
	if err != nil || len(rows) != 1 {
		t.Fatalf("第二页遗漏匹配记录: %v %v", rows, err)
	}
}

// TestUsageEvidenceFilters 验证持久化字段的组合筛选、字面错误搜索和授权交集；前置私有数据库，测试结束自动删除 schema。
func TestUsageEvidenceFilters(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	first := usageRecord("evidence-a", "", 0)
	first.UserID, first.KeyHash, first.KeyAlias, first.Supplier = "owner", "historical-hash", "old-key", "supplier-a"
	first.HTTPStatus, first.Error, first.InboundEndpoint = 499, "UPSTREAM 100%_literal", "/v1/responses"
	second := usageRecord("evidence-b", "", 0)
	second.UserID, second.KeyHash, second.CacheHit = "other", "historical-hash", true
	if err := db.RecordUsage(ctx, []UsageRecord{first, second}); err != nil {
		t.Fatal(err)
	}
	hit, miss := true, false
	for _, tc := range []struct {
		q     UsageQuery
		count int
	}{
		{UsageQuery{KeyHash: "historical-hash"}, 2},
		{UsageQuery{UserID: "owner", KeyHash: "historical-hash", KeyAlias: "old-key", ErrorCode: "499", Supplier: "supplier-a", InboundEndpoint: "/v1/responses", CacheHit: &miss}, 1},
		{UsageQuery{CacheHit: &hit}, 1},
		{UsageQuery{ErrorMessage: "upstream 100%_literal"}, 1},
		{UsageQuery{ErrorMessage: "' OR true --"}, 0},
		{UsageQuery{UserID: "other", ErrorMessage: "100%_literal"}, 0},
	} {
		rows, err := db.ListUsage(ctx, tc.q)
		if err != nil || len(rows) != tc.count {
			t.Fatalf("证据筛选 %+v 得到 %d 条，期望 %d，错误 %v", tc.q, len(rows), tc.count, err)
		}
	}
}
