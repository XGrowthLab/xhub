package affinity

import (
	"context"
	"os"
	"strings"

	"github.com/sunqirui1987/xhub/internal/auth"
)

// ConversationStore 保存不可随缓存过期的会话归属与响应历史；由PostgreSQL实现并按调用方隔离键。
// 不包含普通路由偏好；读取失败必须保留错误，比较写入必须原子执行。
type ConversationStore interface {
	ReadConversation(context.Context, string) (string, error)
	CompareConversation(context.Context, string, string, string) (string, error)
	WriteConversation(context.Context, string, string) error
}

// HistoryStore 可选扩展支持隔离范围内的压缩历史；旧存储实现仍通过 ConversationStore 工作。
// key 是已授权的响应键，scope 是调用方、模型和入口计算的隔离范围，compact 只选择新写入格式；错误必须向上返回。
type HistoryStore interface {
	ReadHistory(context.Context, string, string) (string, error)
	WriteHistory(context.Context, string, string, string, bool) error
}

// compactHistoryAllowed 为统一 Responses 的路由计划选择新写入格式，参数 p 来自网关鉴权。
// 值 1 保留原有全量模式；canary 只接受明确列出的身份，名单缺失或为空时写旧格式。
// 只按可信用户 ID 比较，个人 Key 与会话均可命中，匿名、主密钥和缺少用户归属的密钥不能命中名单。
func compactHistoryAllowed(p *auth.Principal) bool {
	switch os.Getenv("XHUB_COMPACT_HISTORY") {
	case "1":
		return true
	case "canary":
		// 灰度模式必须匹配显式名单，缺少配置不会扩大范围。
	default:
		return false
	}
	if p == nil || p.UserID == "" {
		return false
	}
	for id := range strings.SplitSeq(os.Getenv("XHUB_COMPACT_HISTORY_USER_IDS"), ",") {
		if strings.TrimSpace(id) == p.UserID {
			return true
		}
	}
	return false
}

// NewDurable 组装生产会话状态；redis保存短期路由偏好，store保存续聊必需状态。
// 返回独立状态；store为空时保留旧内存测试模式，不连接存储、不启动后台任务。
func NewDurable(redis Redis, store ConversationStore) *State {
	return &State{redis: redis, durable: store}
}

// durableKey 判断键是否属于续聊必需状态；供缓存读写分流，未知命名空间不自动持久化。
// 参数为内部生成的完整键，返回是否持久保存，不修改键、不授予权限。
func durableKey(key string) bool {
	for _, kind := range []string{"owner", "response", "history", "messages"} {
		if strings.HasPrefix(key, "session_route:v1:"+kind+":") {
			return true
		}
	}
	return false
}
