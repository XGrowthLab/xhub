package live

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestDeliveryRecoveryArbitration 验证确认入队和未知恢复只有一个胜者，以及重试、历史队列和故障保护。
// 前置真实独占 Redis；断言队列及热费用未被恢复操作篡改，testRedis 在结束时清理进程和数据。
func TestDeliveryRecoveryArbitration(t *testing.T) {
	c := testRedis(t)
	ctx := t.Context()
	if _, err := (*Client)(nil).ReserveDeliveryRecovery(ctx, []string{"x"}); err == nil {
		t.Fatal("不可用客户端不能放行恢复")
	}
	if got, err := c.ReserveDeliveryRecovery(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("空批次: %v %v", got, err)
	}
	if _, err := c.ReserveDeliveryRecovery(ctx, []string{"valid", ""}); err == nil {
		t.Fatal("空请求 ID 必须拒绝整批")
	}
	if c.rdb.HExists(ctx, "xhub:spend:requests", "valid").Val() {
		t.Fatal("输入失败不得留下恢复权")
	}
	for _, first := range []string{"confirmation", "recovery"} {
		t.Run(first, func(t *testing.T) {
			row := SpendLog{RequestID: first, UserID: "user", Spend: 0.25, SpendValid: true, DeliverySnapshot: `{"test":true}`}
			if first == "confirmation" {
				if err := c.EnqueueConfirmedDelivery(row); err != nil {
					t.Fatal(err)
				}
			}
			got, err := c.ReserveDeliveryRecovery(ctx, []string{first})
			if err != nil {
				t.Fatal(err)
			}
			if got[first] != (first == "recovery") {
				t.Fatalf("先确认的账单不能被恢复: %v", got)
			}
			if first == "recovery" {
				if err := c.EnqueueConfirmedDelivery(row); err == nil {
					t.Fatal("恢复取得标识后迟到确认必须明确失败")
				}
				retry, err := c.ReserveDeliveryRecovery(ctx, []string{first})
				if err != nil || !retry[first] {
					t.Fatalf("数据库回滚后应可重试恢复: %v %v", retry, err)
				}
			}
		})
	}
	if got := c.HotSpend("user/user"); got != 0.25 {
		t.Fatalf("恢复不得改变确认费用: %v", got)
	}
	// 旧队列即使没有 hash 索引，也不能把已有确认恢复成零费用。
	if err := c.rdb.RPush(ctx, "xhub:spendlog", `{"request_id":"legacy","spend":1}`).Err(); err != nil {
		t.Fatal(err)
	}
	got, err := c.ReserveDeliveryRecovery(ctx, []string{"legacy", "unrelated"})
	if err != nil || got["legacy"] || !got["unrelated"] {
		t.Fatalf("历史队列/无关记录裁决错误: %v %v", got, err)
	}
	if err := c.rdb.RPush(ctx, "xhub:spendlog", `broken-json`).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReserveDeliveryRecovery(ctx, []string{"corrupt"}); err == nil {
		t.Fatal("损坏队列必须阻止猜测恢复")
	}
	if c.rdb.HExists(ctx, "xhub:spend:requests", "corrupt").Val() {
		t.Fatal("解析失败不得部分写入")
	}
	if err := c.rdb.RPop(ctx, "xhub:spendlog").Err(); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.ReserveDeliveryRecovery(cancelled, []string{"cancelled"}); err == nil {
		t.Fatal("取消操作必须报错")
	}
}

// TestDeliveryRecoveryConcurrentEnqueue 验证真实 Redis 并发确认/恢复原子互斥，已持久化确认绝不获得零费用恢复权。
// 以屏障同时启动两个客户端操作；重复覆盖两种时序，检查实际队列数量及费用，测试夹具负责清理。
func TestDeliveryRecoveryConcurrentEnqueue(t *testing.T) {
	c := testRedis(t)
	confirmed := 0
	for i := range 50 {
		id := fmt.Sprintf("race-%d", i)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var enqueueErr, recoverErr error
		var allowed map[string]bool
		go func() {
			defer wg.Done()
			<-start
			enqueueErr = c.EnqueueConfirmedDelivery(SpendLog{RequestID: id, UserID: "user", Spend: 0.25, SpendValid: true, DeliverySnapshot: `{"test":true}`})
		}()
		go func() {
			defer wg.Done()
			<-start
			allowed, recoverErr = c.ReserveDeliveryRecovery(t.Context(), []string{id})
		}()
		close(start)
		wg.Wait()
		if recoverErr != nil {
			t.Fatal(recoverErr)
		}
		if (enqueueErr == nil) == allowed[id] {
			t.Fatalf("确认与恢复必须恰有一个胜者: enqueue=%v allowed=%v", enqueueErr, allowed)
		}
		if enqueueErr == nil {
			confirmed++
		}
	}
	logs, _ := c.PeekLogs(100)
	if len(logs) != confirmed || c.HotSpend("user/user") != float64(confirmed)*0.25 {
		t.Fatalf("确认账单或费用丢失: logs=%d confirmed=%d hot=%v", len(logs), confirmed, c.HotSpend("user/user"))
	}
}
