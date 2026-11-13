package regression

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/iam"
)

// TestNotificationsPersonalBudgetHTTP 通过真实 HTTP、PostgreSQL 账单与两个用户验证个人提醒和已读隔离。
// 覆盖会话/密钥鉴权、80/95%、分页错误、越权、通知写失败不影响记账及恢复；harness 自动回收 schema。
func TestNotificationsPersonalBudgetHTTP(t *testing.T) {
	h := newHarness(t)
	admin := h.adminSession()
	a := h.provision(t, admin, "notify-a")
	b := h.provision(t, admin, "notify-b")
	if _, err := h.db.Engine.Exec(`UPDATE users SET max_budget=100 WHERE id=?`, a.userID); err != nil {
		t.Fatal(err)
	}
	record := func(id string, cost float64) {
		t.Helper()
		if err := h.db.RecordUsage(t.Context(), []iam.UsageRecord{{RequestID: id, TS: time.Now(), UserID: a.userID, Status: "success", HTTPStatus: 200, Cost: cost}}); err != nil {
			t.Fatal(err)
		}
	}
	record("notification-budget-1", 81)
	if err := h.db.ScanBudgetNotifications(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := h.ok("GET", "/notifications?limit=10&offset=0", a.session, nil)
	rows := rowsOf(result, "items")
	if len(rows) != 1 || firstFloat(result.json(), "total") != 1 {
		t.Fatalf("消息不完整: %v", result)
	}
	id := firstString(rows[0], "id")
	data, ok := rows[0]["data"].(map[string]any)
	if !ok || rows[0]["type"] != "budget_threshold" || firstFloat(data, "threshold") != 80 || firstFloat(data, "spent") != 81 || firstFloat(data, "limit") != 100 || firstFloat(data, "remaining") != 19 || rows[0]["read"] != false {
		t.Fatalf("消息内容不符: %v", rows[0])
	}
	if strings.Contains(fmt.Sprint(rows[0]), a.key) {
		t.Fatal("消息泄漏密钥")
	}
	if n := firstFloat(h.ok("GET", "/notifications/unread", a.session, nil).json(), "count"); n != 1 {
		t.Fatal("未读计数错误", n)
	}
	if n := firstFloat(h.ok("GET", "/notifications/unread", b.session, nil).json(), "count"); n != 0 {
		t.Fatal("泄漏其他用户消息", n)
	}
	for _, tc := range []struct {
		method, path, token string
		body                any
		want                int
	}{
		{"GET", "/notifications", "", nil, 401}, {"GET", "/notifications", a.key, nil, 403}, {"GET", "/notifications?limit=0", a.session, nil, 400}, {"POST", "/notifications/read", b.session, map[string]any{"id": id}, 404}, {"POST", "/notifications/read", a.session, map[string]any{"id": id, "user_id": b.userID}, 400},
	} {
		if got := h.do(tc.method, tc.path, tc.token, tc.body); got.status != tc.want {
			t.Fatalf("%s %s status=%d want=%d", tc.method, tc.path, got.status, tc.want)
		}
	}
	h.ok("POST", "/notifications/read", a.session, map[string]any{"id": id})
	h.ok("POST", "/notifications/read", a.session, map[string]any{"id": id})
	if n := firstFloat(h.ok("GET", "/notifications/unread", a.session, nil).json(), "count"); n != 0 {
		t.Fatal("已读未更新", n)
	}
	// 通知写失败只影响通知扫描，账单事务仍可独立提交。
	if _, err := h.db.Engine.Exec(`ALTER TABLE user_notifications ADD CONSTRAINT reject_notification CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	record("notification-budget-2", 15)
	if err := h.db.ScanBudgetNotifications(context.Background()); err == nil {
		t.Fatal("通知写失败被当作成功")
	}
	if _, err := h.db.Engine.Exec(`ALTER TABLE user_notifications DROP CONSTRAINT reject_notification`); err != nil {
		t.Fatal(err)
	}
	if err := h.db.ScanBudgetNotifications(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows = rowsOf(h.ok("GET", "/notifications", a.session, nil), "items")
	if len(rows) != 2 {
		t.Fatalf("恢复后未产生 95%% 消息: %v", rows)
	}
	u, err := h.db.GetUser(context.Background(), a.userID)
	if err != nil || u.Spend != 96 {
		t.Fatalf("通知错误影响账单计数: %v %v", u, err)
	}
	if got := h.do(http.MethodGet, "/notifications?offset=-1", a.session, nil); got.status != 400 {
		t.Fatalf("非法分页未拒绝: %d", got.status)
	}
}
