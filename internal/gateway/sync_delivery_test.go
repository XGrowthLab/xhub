package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/live"
)

// TestRecordCachedDeliveryKeepsRecoveryOwner 验证绕过预算准备的缓存命中仍冻结恢复归属。
// 前置隔离 PG；有 Redis 时缺失守卫不能恢复，无 Redis 时允许未知结果恢复；存储失败不交付，schema 自动清理。
func TestRecordCachedDeliveryKeepsRecoveryOwner(t *testing.T) {
	for _, withRedis := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%t", withRedis), func(t *testing.T) {
			db := testIdentityStore(t)
			s := &Server{Cfg: &config.Config{}, IAM: db}
			if withRedis {
				// 本例仅验证归属标记，不调用 Redis；缓存与匿名记录不查询热费用。
				s.Live = &live.Client{}
			}
			w := &holdWriter{ResponseWriter: httptest.NewRecorder(), guardDelivery: true}
			s.recordSpend(w, nil, "cached-delivery", "cached-model", "chat", nil, time.Now(), true, 200, "")
			if w.deliveryErr != nil || w.delivery == nil {
				t.Fatalf("缓存成功记录未准备: record=%+v err=%v", w.delivery, w.deliveryErr)
			}
			if _, err := db.Engine.Exec(`UPDATE usage_delivery_pending SET expires_at = now() - interval '1 second'`); err != nil {
				t.Fatal(err)
			}
			want := 1
			if withRedis {
				want = 0
			}
			if n, err := db.RecoverExpiredDeliveries(t.Context(), time.Now(), 10); err != nil || n != want {
				t.Fatalf("缓存记录恢复归属错误: redis=%t n=%d want=%d err=%v", withRedis, n, want, err)
			}
		})
	}
	s := &Server{Cfg: &config.Config{}}
	w := &holdWriter{ResponseWriter: httptest.NewRecorder(), guardDelivery: true}
	s.recordSpend(w, nil, "unavailable", "cached-model", "chat", nil, time.Now(), true, 200, "")
	if w.deliveryErr == nil || w.delivery != nil {
		t.Fatal("缓存记录存储不可用时不能假装已准备交付")
	}
}

// TestSyncDialogueRoute 验证同步对话入口的保护范围及非对话/错误方法的边界。
// 不调用外部服务；该函数不改请求状态，也不需要清理数据。
func TestSyncDialogueRoute(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		expected     bool
	}{
		{"POST", "/v1/chat/completions", true}, {"POST", "/bypass/openai/v1/chat/completions", true},
		{"POST", "/v1/responses", true}, {"POST", "/bypass/openai/v1/responses", true},
		{"POST", "/v1/messages", true}, {"POST", "/bypass/anthropic/v1/messages", true},
		{"GET", "/v1/responses", false}, {"POST", "/v1/embeddings", false}, {"POST", "/other", false},
	} {
		if got := syncDialogueRoute(tc.method, tc.path); got != tc.expected {
			t.Fatalf("%s %s: got=%t", tc.method, tc.path, got)
		}
	}
}

// TestSyncDialogueGuard 验证流式、WebSocket 和空请求不走同步正文缓冲，普通同步请求仍受保护。
// 使用内存 HTTP 请求，不连接供应商、不修改外部状态，无需清理。
func TestSyncDialogueGuard(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if !syncDialogueGuard(r, false) || syncDialogueGuard(r, true) || syncDialogueGuard(nil, false) {
		t.Fatal("普通与流式入口判定错误")
	}
	r.Header.Set("Upgrade", "WebSocket")
	if syncDialogueGuard(r, false) {
		t.Fatal("WebSocket 不应被缓冲")
	}
}

// TestFinishSyncDelivery 验证写出成功按实际用量结算，写失败记录独立诊断且费用为零。
// 前置私有 PostgreSQL 和已准备标记；不访问外部供应商，数据库由测试夹具清理。
func TestFinishSyncDelivery(t *testing.T) {
	db := testIdentityStore(t)
	s := &Server{IAM: db}
	for _, tc := range []struct {
		id         string
		err        error
		wantStatus string
		wantCost   float64
	}{
		{"finish-ok", nil, "success", 0.25}, {"finish-write-failed", errors.New("test client write"), "error", 0},
	} {
		record := iam.UsageRecord{RequestID: tc.id, TS: time.Now().UTC(), Model: "test", CallType: "chat", Status: "success", HTTPStatus: 200, Cost: 0.25}
		if err := db.PrepareDelivery(t.Context(), record, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		s.finishSyncDelivery(context.Background(), record, tc.err)
		event, err := db.GetUsageEvent(t.Context(), iam.UsageQuery{}, tc.id)
		if err != nil || event.Status != tc.wantStatus || event.Cost != tc.wantCost {
			t.Fatalf("同步结果错误: event=%+v err=%v", event, err)
		}
		log, err := db.GetRequestLog(t.Context(), tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if tc.err != nil && (!strings.Contains(log.UpstreamResponse, "client_write_error") || !strings.Contains(log.Error, "client_write_error")) {
			t.Fatal("写失败缺少独立诊断")
		}
	}
}

// TestFinishSyncDeliveryDiskRecovery 验证磁盘终态在数据库可用和暂时拒绝结算时均可安全确认。
// 前置独立 PostgreSQL 与临时磁盘队列；故障由测试约束注入并清理，数据库及目录由夹具回收。
func TestFinishSyncDeliveryDiskRecovery(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("database_failure=%t", fail), func(t *testing.T) {
			t.Setenv("XHUB_USAGE_SPOOL_DIR", filepath.Join(t.TempDir(), "spool"))
			db := testIdentityStore(t)
			s := &Server{IAM: db}
			s.initializeUsageSpool()
			t.Cleanup(func() { _ = s.CloseUsageSpool() })
			rec := iam.UsageRecord{RequestID: "finish-disk", TS: time.Now().UTC(), Model: "test", CallType: "chat", Status: "success", HTTPStatus: 200, Cost: 0.25, DeliveryPending: true, DeliverySpoolOnly: true}
			if err := db.PrepareDelivery(t.Context(), rec, time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			marker, err := iam.UnconfirmedDelivery(rec)
			if err != nil {
				t.Fatal(err)
			}
			marker.DeliveryUnconfirmed = true
			if err := s.usageSpool.BeginDelivery(marker); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err := db.Engine.Exec("ALTER TABLE usage_pending ADD CONSTRAINT reject_finish CHECK (false)"); err != nil {
					t.Fatal(err)
				}
			}
			s.finishSyncDelivery(context.Background(), rec, nil)
			if fail {
				pending, err := s.HasPendingLocalUsage(t.Context())
				if err != nil || !pending {
					t.Fatalf("失败快照未留存: %t %v", pending, err)
				}
				if _, err := db.Engine.Exec("ALTER TABLE usage_pending DROP CONSTRAINT reject_finish"); err != nil {
					t.Fatal(err)
				}
				if err := s.FlushLocalUsage(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			pending, err := s.HasPendingLocalUsage(t.Context())
			if err != nil || pending {
				t.Fatalf("终态未清理: %t %v", pending, err)
			}
			event, err := db.GetUsageEvent(t.Context(), iam.UsageQuery{}, rec.RequestID)
			if err != nil || event.Status != "success" || event.Cost != 0.25 {
				t.Fatalf("磁盘恢复账单错误: %+v %v", event, err)
			}
		})
	}
}

// TestDeliveryBuffer 验证只识别已启用的请求级缓冲，兼容普通包装和空输入。
// 使用本机响应记录器，无外部服务或持久数据，测试结束自动回收内存。
func TestDeliveryBuffer(t *testing.T) {
	base := httptest.NewRecorder()
	if deliveryBuffer(nil) != nil || deliveryBuffer(base) != nil {
		t.Fatal("普通 writer 不应进入同步确认")
	}
	h := &holdWriter{ResponseWriter: base, guardDelivery: true}
	w := &statusRecorder{ResponseWriter: h}
	if deliveryBuffer(w) != h {
		t.Fatal("包装链未找到缓冲")
	}
	h.guardDelivery = false
	if deliveryBuffer(w) != nil {
		t.Fatal("未启用的缓冲不应接管账单")
	}
}

// TestSyncDeliveryWriteFailureHidesNetworkAddresses 验证客户端写入故障的日志不会泄露连接两端地址。
// 使用隔离 PostgreSQL 和带地址的模拟 TCP 错误；核对失败零费用和诊断信息，测试库自动清理。
func TestSyncDeliveryWriteFailureHidesNetworkAddresses(t *testing.T) {
	db := testIdentityStore(t)
	s := &Server{IAM: db}
	record := iam.UsageRecord{RequestID: "finish-network-privacy", TS: time.Now().UTC(), Model: "test", CallType: "chat", Status: "success", HTTPStatus: 200, Cost: 0.25}
	if err := db.PrepareDelivery(t.Context(), record, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	writeErr := &net.OpError{Op: "write", Net: "tcp", Source: &net.TCPAddr{IP: net.ParseIP("10.20.30.40"), Port: 1234}, Addr: &net.TCPAddr{IP: net.ParseIP("198.51.100.123"), Port: 5678}, Err: errors.New("broken pipe")}
	s.finishSyncDelivery(context.Background(), record, writeErr)
	event, err := db.GetUsageEvent(t.Context(), iam.UsageQuery{}, record.RequestID)
	if err != nil || event.Status != "error" || event.Cost != 0 {
		t.Fatalf("写失败不能扣费: event=%+v err=%v", event, err)
	}
	log, err := db.GetRequestLog(t.Context(), record.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{log.UpstreamResponse, log.Error} {
		if strings.Contains(value, "10.20.30.40") || strings.Contains(value, "198.51.100.123") {
			t.Fatalf("错误诊断不应包含 IP 地址: %s", value)
		}
		if !strings.Contains(value, "client_write_error") {
			t.Fatalf("缺少写入失败分类: %s", value)
		}
	}
}

// TestConfirmedPendingClearsUnknownDiskMarker 验证磁盘终态超限但 PG 已确认时，后续结算故障不遗留未知磁盘标记。
// 前置私有 PG、受限临时磁盘；使用约束阻断最终入账，确认重试只恢复原费用并清理所有资源。
func TestConfirmedPendingClearsUnknownDiskMarker(t *testing.T) {
	t.Setenv("XHUB_USAGE_SPOOL_DIR", filepath.Join(t.TempDir(), "spool"))
	t.Setenv("XHUB_USAGE_SPOOL_RECORD_BYTES", "2048")
	db := testIdentityStore(t)
	s := &Server{IAM: db}
	s.initializeUsageSpool()
	t.Cleanup(func() { _ = s.CloseUsageSpool() })
	rec := iam.UsageRecord{RequestID: "confirmed-pending-disk", TS: time.Now().UTC(), Model: "test", CallType: "chat", Status: "success", HTTPStatus: 200, Cost: 0.25, DeliveryPending: true, DeliverySpoolOnly: true}
	if err := db.PrepareDelivery(t.Context(), rec, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	marker, err := iam.UnconfirmedDelivery(rec)
	if err != nil {
		t.Fatal(err)
	}
	marker.DeliveryUnconfirmed = true
	if err := s.usageSpool.BeginDelivery(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec(`ALTER TABLE usage_events ADD CONSTRAINT reject_confirmed_event CHECK (cost=0)`); err != nil {
		t.Fatal(err)
	}
	rec.Label = strings.Repeat("x", 3000)
	s.finishSyncDelivery(t.Context(), rec, nil)
	if n, err := db.Engine.Table("usage_pending").Where("request_id=?", rec.RequestID).Count(); err != nil || n != 1 {
		t.Fatalf("PG 必须保留最终快照: %d %v", n, err)
	}
	if st, err := s.usageSpool.Status(); err != nil || st.Pending != 0 || st.Reserved != 0 {
		t.Fatalf("PG 已确认后不应残留未知磁盘标记/容量: %+v %v", st, err)
	}
	if _, err := db.Engine.Exec(`ALTER TABLE usage_events DROP CONSTRAINT reject_confirmed_event`); err != nil {
		t.Fatal(err)
	}
	s.FlushSpend()
	event, err := db.GetUsageEvent(t.Context(), iam.UsageQuery{}, rec.RequestID)
	if err != nil || event.Status != "success" || event.Cost != 0.25 {
		t.Fatalf("已确认账单重试后费用错误: %+v %v", event, err)
	}
}

// TestUnknownDiskRecoveryRequiresConfirmationSource 验证重启后的磁盘未知标记必须等待原 Redis，PG 已确认时可直接清理。
// 使用私有 PG 和临时卷；分别模拟 Redis 缺失、不可用及已入账，不调用外部服务，测试退出关闭磁盘并删 schema。
func TestUnknownDiskRecoveryRequiresConfirmationSource(t *testing.T) {
	for _, state := range []string{"missing", "unavailable", "database-confirmed"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("XHUB_USAGE_SPOOL_DIR", filepath.Join(t.TempDir(), "spool"))
			db := testIdentityStore(t)
			s := &Server{IAM: db}
			s.initializeUsageSpool()
			t.Cleanup(func() { _ = s.CloseUsageSpool() })
			final := iam.UsageRecord{RequestID: "guarded-disk", TS: time.Now().UTC(), Model: "test", CallType: "chat", Status: "success", HTTPStatus: 200, Cost: 0.25, DeliveryPending: true, DeliverySpoolOnly: true, DeliveryRedisGuard: true}
			if err := db.PrepareDelivery(t.Context(), final, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			marker, err := iam.UnconfirmedDelivery(final)
			if err != nil {
				t.Fatal(err)
			}
			marker.DeliveryUnconfirmed = true
			if err := s.usageSpool.BeginDelivery(marker); err != nil {
				t.Fatal(err)
			}
			if err := s.CloseUsageSpool(); err != nil {
				t.Fatal(err)
			}
			s.initializeUsageSpool()
			if state == "unavailable" {
				s.Live = &live.Client{}
			}
			if state == "database-confirmed" {
				if err := db.SettleConfirmedDelivery(t.Context(), final); err != nil {
					t.Fatal(err)
				}
			}
			err = s.FlushLocalUsage(t.Context())
			st, statusErr := s.usageSpool.Status()
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			if state == "database-confirmed" {
				event, readErr := db.GetUsageEvent(t.Context(), iam.UsageQuery{}, final.RequestID)
				if err != nil || st.Pending != 0 || readErr != nil || event.Cost != 0.25 {
					t.Fatalf("已入账费用应保持且旧标记应清理: status=%+v event=%+v errors=%v/%v", st, event, err, readErr)
				}
			} else {
				n, countErr := db.Engine.Table("usage_events").Count()
				if err == nil || st.Pending != 1 || countErr != nil || n != 0 {
					t.Fatalf("缺少确认来源不能恢复零费用: status=%+v n=%d errors=%v/%v", st, n, err, countErr)
				}
			}
		})
	}
}
