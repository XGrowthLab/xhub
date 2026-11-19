package affinity

import (
	"os"
	"testing"

	"github.com/sunqirui1987/xhub/internal/auth"
)

// TestCompactHistoryAllowed 验证开关、旧全量兼容和可信身份名单。仅修改独立测试环境变量，测试自动恢复。
func TestCompactHistoryAllowed(t *testing.T) {
	for _, tc := range []struct {
		name, enabled string
		ids           *string
		principal     *auth.Principal
		want          bool
	}{
		{name: "default-off", principal: &auth.Principal{UserID: "one"}},
		{name: "explicit-off-overrides-list", enabled: "0", ids: stringPtr("one"), principal: &auth.Principal{UserID: "one"}},
		{name: "global-compat", enabled: "1", want: true},
		{name: "missing-list", enabled: "canary", principal: &auth.Principal{UserID: "one"}},
		{name: "explicit-empty", enabled: "canary", ids: stringPtr(""), principal: &auth.Principal{UserID: "one"}},
		{name: "no-principal", enabled: "canary", ids: stringPtr("one")},
		{name: "no-user", enabled: "canary", ids: stringPtr("one"), principal: &auth.Principal{KeyID: "one"}},
		{name: "listed-user", enabled: "canary", ids: stringPtr(" other , one ,"), principal: &auth.Principal{UserID: "one"}, want: true},
		{name: "exact-id-only", enabled: "canary", ids: stringPtr("someone,one2"), principal: &auth.Principal{UserID: "one"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XHUB_COMPACT_HISTORY", tc.enabled)
			t.Setenv("XHUB_COMPACT_HISTORY_USER_IDS", "")
			if tc.ids == nil {
				if err := os.Unsetenv("XHUB_COMPACT_HISTORY_USER_IDS"); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("XHUB_COMPACT_HISTORY_USER_IDS", *tc.ids)
			}
			if got := compactHistoryAllowed(tc.principal); got != tc.want {
				t.Fatalf("写入策略=%t，预期=%t", got, tc.want)
			}
		})
	}
}

// stringPtr 为测试区分空名单和名单未配置，返回参数的地址，无副作用。
func stringPtr(s string) *string { return &s }
