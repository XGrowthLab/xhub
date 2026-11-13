package identity

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/sunqirui1987/xhub/internal/auth"
	"github.com/sunqirui1987/xhub/internal/authz"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/util"
)

// notificationUser 仅允许活动的登录会话访问个人消息；虚拟密钥和主密钥不能借用户 ID 冒充收件人。
// g 使用现有认证，失败时直接响应；返回会话身份及是否允许，身份库不可用返回 503。
func notificationUser(g Gate, w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p := g.RequireUser(w, r)
	if p == nil {
		return nil, false
	}
	if p.Kind != authz.KindSession || p.UserID == "" {
		util.WriteError(w, 403, "forbidden", "session required")
		return nil, false
	}
	if g.Identity() == nil {
		util.WriteError(w, 503, "unavailable", "notification store unavailable")
		return nil, false
	}
	return p, true
}

// NotificationsList 返回当前用户的分页消息；limit 为 1–100，offset 为 0–10000，默认 50/0。
// 身份只从会话解析，任何参数错误返回 400，存储失败返回 500；消息和总数不泄露其他用户。
func NotificationsList(g Gate, w http.ResponseWriter, r *http.Request) {
	p, ok := notificationUser(g, w, r)
	if !ok {
		return
	}
	limit, offset := 50, 0
	for _, field := range []struct {
		name     string
		value    *int
		min, max int
	}{{"limit", &limit, 1, 100}, {"offset", &offset, 0, 10000}} {
		if raw, exists := r.URL.Query()[field.name]; exists {
			if len(raw) != 1 {
				util.WriteError(w, 400, "invalid_request", "invalid pagination")
				return
			}
			n, err := strconv.Atoi(raw[0])
			if err != nil || n < field.min || n > field.max {
				util.WriteError(w, 400, "invalid_request", "invalid pagination")
				return
			}
			*field.value = n
		}
	}
	rows, total, err := g.Identity().ListNotifications(r.Context(), p.UserID, limit, offset)
	if err != nil {
		util.WriteError(w, 500, "internal", "notification list unavailable")
		return
	}
	util.WriteJSON(w, 200, map[string]any{"items": rows, "total": total})
}

// NotificationsUnread 返回当前会话未读数；身份与存储错误不伪装成零。
func NotificationsUnread(g Gate, w http.ResponseWriter, r *http.Request) {
	p, ok := notificationUser(g, w, r)
	if !ok {
		return
	}
	n, err := g.Identity().UnreadNotifications(r.Context(), p.UserID)
	if err != nil {
		util.WriteError(w, 500, "internal", "notification count unavailable")
		return
	}
	util.WriteJSON(w, 200, map[string]any{"count": n})
}

// NotificationsRead 将会话自己的一条消息标记已读；重复调用幂等，越权与不存在均为 404。
// JSON 只允许 id，限制请求大小；数据库错误返回 500，不修改其他用户的状态。
func NotificationsRead(g Gate, w http.ResponseWriter, r *http.Request) {
	p, ok := notificationUser(g, w, r)
	if !ok {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(&struct{}{}) != io.EOF || body.ID == "" || len(body.ID) > 128 {
		util.WriteError(w, 400, "invalid_request", "notification id required")
		return
	}
	err := g.Identity().ReadNotification(r.Context(), p.UserID, body.ID)
	if err == iam.ErrNotFound {
		util.WriteError(w, 404, "not_found", "notification not found")
		return
	}
	if err != nil {
		util.WriteError(w, 500, "internal", "notification update unavailable")
		return
	}
	util.WriteJSON(w, 200, map[string]any{"status": "success"})
}
