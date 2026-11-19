package main

import (
	"strings"
	"testing"
	"time"
)

// TestRunRejectsUnboundedRetention 验证日期、游标和批次参数在连接数据库前校验。
// 参数使用固定时钟，空连接或无效参数失败；不创建数据库和临时文件。
func TestRunRejectsUnboundedRetention(t *testing.T) {
	now := time.Date(2026, time.November, 19, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "--before"},
		{[]string{"--before=2026-11-01T00:00:00Z", "--execute"}, "--before"},
		{[]string{"--before=2026-10-20T00:00:00Z", "--limit=501"}, "invalid batch"},
		{[]string{"--before=2026-10-20T00:00:00Z", "--after=-1"}, "invalid batch"},
		{[]string{"--before=2026-10-20T00:00:00Z", "extra"}, "invalid batch"},
		{[]string{"--before=2026-10-20T00:00:00Z"}, "XHUB_DATABASE_URL"},
	} {
		out, err := run(tc.args, "", now)
		if len(out) != 0 || err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("参数未按预期处理: args=%v err=%v", tc.args, err)
		}
	}
}
