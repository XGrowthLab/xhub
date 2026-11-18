package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/cmd/regression/testsupport"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/store"
)

// TestDatabaseMaxOpenConns 检查默认值、显式零、正常上限、负数、非整数与溢出。
// 配置错误只指出参数名，不回显可能被误填的连接串；纯解析无需清理外部状态。
func TestDatabaseMaxOpenConns(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		invalid bool
	}{
		{"", 0, false}, {"0", 0, false}, {"1", 1, false}, {"25", 25, false}, {"-1", 0, true}, {"2.5", 0, true}, {"bad-private-value", 0, true}, {"99999999999999999999999", 0, true},
	} {
		got, err := databaseMaxOpenConns(tc.raw)
		if got != tc.want || (err != nil) != tc.invalid {
			t.Errorf("上限解析错误: want=%d invalid=%t got=%d err=%v", tc.want, tc.invalid, got, err)
		}
		if err != nil && (!strings.Contains(err.Error(), "XHUB_DATABASE_MAX_OPEN_CONNS") || strings.Contains(err.Error(), tc.raw)) {
			t.Errorf("配置诊断缺少字段名或泄露原值: %v", err)
		}
	}
}

// TestGatewayRejectsInvalidLimitBeforeOpeningPools 通过真实 main 入口验证非法上限独立触发明确的启动失败，
// 即使配置路径不可读也不能用后续数据库错误掩盖它。子进程自行退出，无外部数据库或持久改动。
func TestGatewayRejectsInvalidLimitBeforeOpeningPools(t *testing.T) {
	if os.Getenv("XHUB_TEST_INVALID_POOL_MAIN") == "1" {
		os.Args = []string{os.Args[0], "-config", "missing-pool-limit-fixture.yaml"}
		main()
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestGatewayRejectsInvalidLimitBeforeOpeningPools$")
	cmd.Env = append(os.Environ(), "XHUB_TEST_INVALID_POOL_MAIN=1", "XHUB_DATABASE_MAX_OPEN_CONNS=-1")
	out, err := cmd.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 1 || !strings.Contains(string(out), "XHUB_DATABASE_MAX_OPEN_CONNS") {
		t.Fatalf("非法上限未在数据库启动前触发配置错误: exit=%v", err)
	}
}

// TestGatewayPoolLimitQueuesAndRecovers 在私有 PostgreSQL schema 手动占满两个独立连接池。
// 超出每池上限的请求应在本地排队、可取消或在归还任一连接后恢复；仅启动入口显式调整的池受影响。
// 每个持有的连接、两个池和私有 schema 都交由测试夹具回收。
func TestGatewayPoolLimitQueuesAndRecovers(t *testing.T) {
	t.Setenv("XHUB_DATABASE_MAX_OPEN_CONNS", "2")
	dsn := testsupport.Postgres(t, "gateway_pool")
	db, err := iam.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal("打开隔离 IAM 失败")
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := store.Open(dsn)
	if err != nil {
		t.Fatal("打开隔离框架存储失败")
	}
	t.Cleanup(func() { _ = st.Engine.Close() })
	identity, framework := db.Engine.DB().DB, st.DB
	if identity.Stats().MaxOpenConnections != 0 || framework.Stats().MaxOpenConnections != 0 {
		t.Fatal("仅设置进程环境意外修改了测试夹具打开的池")
	}
	identity.SetMaxOpenConns(3)
	framework.SetMaxOpenConns(4)
	limitGatewayPools(0, identity, framework)
	if identity.Stats().MaxOpenConnections != 3 || framework.Stats().MaxOpenConnections != 4 {
		t.Fatal("缺省配置意外覆盖池的既有上限")
	}
	limitGatewayPools(2, identity, framework)
	for name, pool := range map[string]*sql.DB{"iam": identity, "framework": framework} {
		t.Run(name, func(t *testing.T) {
			if pool.Stats().MaxOpenConnections != 2 {
				t.Fatal("未应用每池上限")
			}
			held := make([]*sql.Conn, 0, 2)
			for range 2 {
				conn, err := pool.Conn(t.Context())
				if err != nil {
					t.Fatal("无法占用测试连接")
				}
				t.Cleanup(func() { _ = conn.Close() })
				held = append(held, conn)
			}
			block := func(ctx context.Context) <-chan error {
				result := make(chan error, 1)
				before := pool.Stats().WaitCount
				go func() {
					var one int
					err := pool.QueryRowContext(ctx, "SELECT 1").Scan(&one)
					result <- err
				}()
				deadline := time.Now().Add(3 * time.Second)
				for pool.Stats().WaitCount == before {
					if time.Now().After(deadline) {
						t.Fatal("第 3 个数据库查询没有进入当前实例连接池的等待队列")
					}
					time.Sleep(time.Millisecond)
				}
				if n := pool.Stats().OpenConnections; n != 2 {
					t.Fatalf("等待调用多开连接: %d", n)
				}
				return result
			}
			ctx, cancel := context.WithCancel(t.Context())
			wait := block(ctx)
			cancel()
			if err := <-wait; !errors.Is(err, context.Canceled) {
				t.Fatalf("阻塞调用未响应取消: %v", err)
			}
			wait = block(t.Context())
			if err := held[0].Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-wait:
				if err != nil {
					t.Fatalf("归还连接后排队的真实 SELECT 未恢复: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("归还连接后查询继续阻塞")
			}
		})
	}
}

// TestGatewayServesWithLimitedPools 使用真实 main、隔离 PostgreSQL 和两个连接的上限启动子进程，
// 检查健康、账号登录、模型列表和未授权调用的拒绝。测试仅创建临时账号/文件，退出时停止进程并清理 schema。
func TestGatewayServesWithLimitedPools(t *testing.T) {
	if os.Getenv("XHUB_TEST_POOL_HTTP_MAIN") == "1" {
		os.Args = []string{os.Args[0], "-config", os.Getenv("XHUB_TEST_POOL_CONFIG"), "-addr", os.Getenv("XHUB_TEST_POOL_ADDR")}
		main()
		return
	}
	dsn := testsupport.Postgres(t, "pool_http")
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "gateway.yaml")
	settings := map[string]any{"general_settings": map[string]any{"database_url": dsn, "admin_email": "pool-test@example.com", "admin_password": "pool-fixture-password"}}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal("临时配置编码失败")
	}
	if err := os.WriteFile(cfg, raw, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	childCtx, stop := context.WithCancel(t.Context())
	cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestGatewayServesWithLimitedPools$")
	cmd.Env = append(os.Environ(), "XHUB_TEST_POOL_HTTP_MAIN=1", "XHUB_TEST_POOL_CONFIG="+cfg, "XHUB_TEST_POOL_ADDR="+addr, "XHUB_DATABASE_MAX_OPEN_CONNS=2", "XHUB_USAGE_SPOOL_DIR="+filepath.Join(tmp, "spool"))
	if err := cmd.Start(); err != nil {
		stop()
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); _ = cmd.Wait() })
	client := &http.Client{Timeout: 3 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	base := "http://" + addr
	deadline := time.Now().Add(12 * time.Second)
	for {
		reply, err := client.Get(base + "/health/liveliness")
		if err == nil {
			reply.Body.Close()
			if reply.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("双池限额下网关没有就绪")
		}
		time.Sleep(20 * time.Millisecond)
	}
	res, err := client.Post(base+"/v2/login", "application/json", strings.NewReader(`{"username":"pool-test@example.com","password":"pool-fixture-password"}`))
	if err != nil {
		t.Fatal("登录服务不可达")
	}
	var session struct {
		Token string
		Key   string
	}
	decodeErr := json.NewDecoder(res.Body).Decode(&session)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || decodeErr != nil || session.Token == "" && session.Key == "" {
		t.Fatalf("双池限额下账号登录失败: HTTP %d", res.StatusCode)
	}
	token := session.Token
	if token == "" {
		token = session.Key
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	res, err = client.Do(request)
	if err != nil {
		t.Fatal("模型目录不可达")
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("已认证读取模型失败: HTTP %d", res.StatusCode)
	}
	res, err = client.Post(base+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"fixture","messages":[{"role":"user","content":"Hello"}]}`))
	if err != nil {
		t.Fatal("数据面不可达")
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未授权数据面请求的状态异常: HTTP %d", res.StatusCode)
	}
}
