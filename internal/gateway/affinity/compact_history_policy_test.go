package affinity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sunqirui1987/xhub/internal/auth"
	dpcontract "github.com/sunqirui1987/xhub/internal/dataplane/contract"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
)

// TestCompactHistoryAllowed 验证开关、旧全量兼容和可信身份名单。仅修改独立测试环境变量，测试自动恢复。
func TestCompactHistoryAllowed(t *testing.T) {
	for _, tc := range []struct {
		name, enabled string
		ids           string
		principal     *auth.Principal
		want          bool
	}{
		{name: "default-off", principal: &auth.Principal{UserID: "one"}},
		{name: "explicit-off-overrides-list", enabled: "0", ids: "one", principal: &auth.Principal{UserID: "one"}},
		{name: "global-compat-ignores-the-list", enabled: " 1 ", ids: "someone", want: true},
		{name: "empty-list", enabled: "canary", principal: &auth.Principal{UserID: "one"}},
		{name: "no-principal", enabled: "canary", ids: "one"},
		{name: "no-user", enabled: "canary", ids: "one", principal: &auth.Principal{KeyID: "one"}},
		{name: "trimmed-canary-and-list", enabled: " canary\t", ids: " other , one ,", principal: &auth.Principal{UserID: "one"}, want: true},
		{name: "exact-id-only", enabled: "canary", ids: "someone,one2", principal: &auth.Principal{UserID: "one"}},
		{name: "do-not-normalize-the-authenticated-id", enabled: "canary", ids: "one", principal: &auth.Principal{UserID: " one "}},
		{name: "unknown-true", enabled: "true", ids: "one", principal: &auth.Principal{UserID: "one"}},
		{name: "unknown-yes", enabled: "yes", ids: "one", principal: &auth.Principal{UserID: "one"}},
		{name: "unknown-capitalized", enabled: "Canary", ids: "one", principal: &auth.Principal{UserID: "one"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XHUB_COMPACT_HISTORY", tc.enabled)
			t.Setenv("XHUB_COMPACT_HISTORY_USER_IDS", tc.ids)
			if got := compactHistoryAllowed(tc.principal); got != tc.want {
				t.Fatalf("写入策略=%t，预期=%t", got, tc.want)
			}
		})
	}
}

// recordingHistoryStore 复用内存归属测试替身，并记下生产缓存调用传给历史持久层的格式。
type recordingHistoryStore struct {
	memoryConversation
	formats []bool
}

// ReadHistory 为写入传播测试实现只读空历史，参数未使用，不访问外部存储。
func (s *recordingHistoryStore) ReadHistory(context.Context, string, string) (string, error) {
	return "", nil
}

// WriteHistory 记录生产调用选中的 compact 标志，其余参数未使用，记录随测试清理。
func (s *recordingHistoryStore) WriteHistory(_ context.Context, _, _, _ string, compact bool) error {
	s.formats = append(s.formats, compact)
	return nil
}

// TestCompactHistoryPolicyReachesStore 验证可信请求的格式经过路由与 Messages 桥接提交传到持久层。
// 对比其他入口及未命中用户，使用内存历史替身，环境与记录由测试自动清理。
func TestCompactHistoryPolicyReachesStore(t *testing.T) {
	for _, tc := range []struct {
		name, path, mode, id   string
		bridge, write, compact bool
	}{
		{name: "global", path: "/v1/responses", mode: "1", id: "unlisted", write: true, compact: true},
		{name: "canary", path: "/v1/responses", mode: "canary", id: "listed", write: true, compact: true},
		{name: "unlisted-with-spoofed-body", path: "/v1/responses", mode: "canary", id: "unlisted", write: true},
		{name: "messages-bridge-canary", path: "/v1/responses", mode: "canary", id: "listed", bridge: true, write: true, compact: true},
		{name: "messages-bridge-unlisted", path: "/v1/responses", mode: "canary", id: "unlisted", bridge: true, write: true},
		{name: "chat", path: "/v1/chat/completions", mode: "1", id: "listed"},
		{name: "messages", path: "/v1/messages", mode: "1", id: "listed"},
		{name: "native-bypass", path: "/bypass/openai/v1/responses", mode: "1", id: "listed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XHUB_COMPACT_HISTORY", tc.mode)
			t.Setenv("XHUB_COMPACT_HISTORY_USER_IDS", "listed")
			store := &recordingHistoryStore{memoryConversation: memoryConversation{rows: map[string]string{}}}
			state := NewDurable(nil, store)
			body := map[string]any{"input": "Use the local fixture.", "user": "listed"}
			request := httptest.NewRequest(http.MethodPost, tc.path, nil)
			plan := state.PlanRoute(request, "fixture", body, &auth.Principal{UserID: tc.id})
			if plan.Err != nil || plan.CompactHistory != tc.compact {
				t.Fatalf("路由计划格式错误: %t %v", plan.CompactHistory, plan.Err)
			}
			history := []llmdialogue.Turn{{Role: "user", Text: "Use the local fixture."}}
			if tc.bridge {
				if plan.CommitMessagesHistory == nil {
					t.Fatal("缺少 Responses 的 Messages 桥接提交")
				}
				target := dpcontract.RouteTarget{DeploymentID: "fixture-deployment", ConnectionID: "fixture-connection"}
				if err := plan.CommitMessagesHistory(target, "fixture-response", history); err != nil {
					t.Fatal(err)
				}
			} else {
				plan.History = history
				if err := state.CommitRouteChecked(plan, "fixture-deployment", "fixture-response"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.write {
				if len(store.formats) != 1 || store.formats[0] != tc.compact {
					t.Fatalf("持久层实际格式=%v，预期单次=%t", store.formats, tc.compact)
				}
			} else if len(store.formats) != 0 {
				t.Fatal("非统一 Responses 进入了完整历史写入")
			}
		})
	}
}
