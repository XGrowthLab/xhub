package gateway

import (
	"context"
	"time"

	"github.com/sunqirui1987/xhub/internal/util"
)

// notificationMaintenanceLoop 在独立后台任务中扫描已记账预算并分批清理 90 天前消息。
// ctx 在服务器退出时取消；每分钟一次，扫描和清理各最多 20 秒，失败只记录错误并重试，不阻塞计费。
func (s *Server) notificationMaintenanceLoop(ctx context.Context) {
	run := func() {
		scan, cancelScan := context.WithTimeout(ctx, 20*time.Second)
		if err := s.IAM.ScanBudgetNotifications(scan); err != nil {
			util.Error("notification budget scan failed: %v", err)
		}
		cancelScan()
		if s.Store != nil {
			market, cancelMarket := context.WithTimeout(ctx, 20*time.Second)
			if err := s.IAM.ScanMarketNotifications(market); err != nil {
				util.Error("market notification scan failed: %v", err)
			}
			cancelMarket()
		}
		cleanup, cancelCleanup := context.WithTimeout(ctx, 20*time.Second)
		defer cancelCleanup()
		if err := s.IAM.CleanExpiredNotifications(cleanup, time.Now().UTC().AddDate(0, 0, -90), 1000); err != nil {
			util.Error("notification cleanup failed: %v", err)
		}
	}
	run()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
