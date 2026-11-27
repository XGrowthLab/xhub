package regression

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/router"
)

// TestCoolingRoutesPreserveUpstreamErrors 验证单部署及多个兼容部署都处于冷却时，下一次请求仍真正到达上游。
// 参数 t 为测试上下文。隔离 HTTP、PostgreSQL 和 Redis，校验原始 529、两侧查单 ID、持久化错误及零扣费；
// 各子例用实际请求触发冷却，结束后回收本地上游、数据库 schema 与专用 Redis 进程。
func TestCoolingRoutesPreserveUpstreamErrors(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		count      int
	}{
		{"single-compatible", "/v1/messages", 1},
		{"all-compatible", "/v1/messages", 2},
		{"single-native", "/bypass/anthropic/v1/messages", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedSpendRedis(t)
			const model = "cooldown-anthropic"
			const raw = " { \"type\":\"error\", \"error\": {\"message\":\"Overloaded\",\"type\":\"overloaded_error\"} }\n"
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				calls.Add(1)
				callID := r.Header.Get("X-Litellm-Call-Id")
				if r.URL.Path != "/v1/messages" || callID == "" || r.Header.Get("X-Request-Id") != callID {
					t.Errorf("实际出站端点或关联 ID 不符: path=%s call=%q request=%q", r.URL.Path, callID, r.Header.Get("X-Request-Id"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Oneapi-Request-Id", "supplier-"+callID)
				w.Header().Set("Retry-After", "8")
				w.WriteHeader(529)
				_, _ = io.WriteString(w, raw)
			}))
			t.Cleanup(up.Close)

			var deployments []config.ModelEntry
			for i := 0; i < tc.count; i++ {
				dep := deployment(model, fmt.Sprintf("anthropic-%d", i), map[string]any{"api_base": up.URL, "custom_llm_provider": "anthropic"})
				dep.ModelInfo["transport"] = "bypass_anthropic_messages"
				dep.ModelInfo["endpoint_types"] = []string{"messages", "bypass:anthropic-messages"}
				deployments = append(deployments, dep)
			}
			h := openHarness(t, false, deployments...)
			if h.gw.Live == nil {
				t.Fatal("网关未连接专用 Redis")
			}
			var ids []string
			for _, dep := range h.gw.Cfg.ModelList {
				ids = append(ids, router.CooldownID(dep))
			}
			admin := h.adminSession()
			owner := h.openScope(t, admin, tc.name)
			template := routeTemplate(t, h, admin, tc.name, routeTemplateBody([]any{}, 1, 10, 1, 60))
			bindTemplate(t, h, admin, "team", owner.teamID, template)
			before := h.moneyOf(t, owner)

			// 参数 n 让每次请求独立，返回后校验供应商 ID 来自本次出站。这个闭包只在当前子例使用。
			send := func(n int) reply {
				body := chatRequest(model, fmt.Sprintf("Check upstream failure %d.", n))
				body["max_tokens"] = 64
				response := h.do(http.MethodPost, tc.path, owner.key, body)
				if response.status != 529 || response.text() != raw || response.header("Retry-After") != "8" {
					t.Fatalf("供应商 HTTP 错误被改写: %s", response.describe())
				}
				callID := response.header("X-Litellm-Call-Id")
				if callID == "" || response.header("X-Oneapi-Request-Id") != "supplier-"+callID {
					t.Fatalf("供应商查单 ID 未关联到网关调用 %q: %s", callID, response.describe())
				}
				return response
			}
			for n := 0; n < tc.count && len(h.gw.Live.Cooled(ids)) < tc.count; n++ {
				send(n)
			}
			if cooled := h.gw.Live.Cooled(ids); len(cooled) != tc.count {
				t.Fatalf("出站失败没有使全部预期部署进入 Redis 冷却: %v", cooled)
			}
			outboundBefore := calls.Load()
			response := send(tc.count)
			if calls.Load() <= outboundBefore || (tc.count == 1 && calls.Load() != outboundBefore+1) {
				t.Fatalf("冷却后的出站次数不符: before=%d after=%d", outboundBefore, calls.Load())
			}
			detail := logDetail(t, h, admin, response.header("X-Litellm-Call-Id"))
			metadata, _ := detail["metadata"].(map[string]any)
			diagnostic, _ := metadata["upstream_response"].(map[string]any)
			if detail["status"] != "error" || firstFloat(metadata, "http_status") != 529 || firstFloat(diagnostic, "status_code") != 529 ||
				diagnostic["endpoint"] != "/v1/messages" || !strings.Contains(fmt.Sprint(detail["error"]), "Overloaded") ||
				!strings.Contains(fmt.Sprint(diagnostic["headers"]), response.header("X-Oneapi-Request-Id")) ||
				firstFloat(detail, "spend") != 0 || h.moneyOf(t, owner) != before {
				t.Fatalf("日志缺少实际供应商失败、查单 ID 或零费用: %v", detail)
			}
		})
	}
}

// TestMalformedSuccessTripsCooldown 验证 HTTP 200 的损坏协议会回退并触发部署冷却，下一请求直接跳过故障部署。
// 前置独立 Redis、隔离 schema 和本地假上游；部署身份每次唯一，测试前后只删除本用例的失败、冷却、延迟和路由用量键。
func TestMalformedSuccessTripsCooldown(t *testing.T) {
	redisURL := os.Getenv("XHUB_REGRESSION_REDIS_URL")
	if redisURL == "" {
		t.Skip("malformed response cooldown needs XHUB_REGRESSION_REDIS_URL")
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	public := "regression-malformed-cooldown-" + suffix
	malformed := deployment(public, "malformed-a-"+suffix, map[string]any{"input_cost_per_token": 0.0000001})
	healthy := deployment(public, "healthy-b-"+suffix, map[string]any{"input_cost_per_token": 0.01})
	h := newHarness(t, malformed, healthy)
	cleanupProtocolRuntimeKeys(t, redisURL, h.gw.Cfg.ModelList)
	admin := h.adminSession()
	c := h.openScope(t, admin, "malformed-cooldown")
	h.modelRouteTemplate(t, admin, c, "malformed cooldown route", public, "cost-based-routing", nil, 1, 1, 60)
	h.scriptBody("malformed-a-"+suffix, []byte("{}"))
	first := h.ok(http.MethodPost, "/v1/chat/completions", c.key, chatRequest(public, "fallback after malformed 200"))
	if !strings.Contains(first.text(), defaultReply.Content) || strings.Join(h.upstreamSince(0), ",") != "malformed-a-"+suffix+",healthy-b-"+suffix {
		t.Fatalf("损坏 200 未回退到健康部署: %s calls=%v", first.describe(), h.upstreamSince(0))
	}

	h.scriptBody("malformed-a-"+suffix, nil)
	h.resetUpstream()
	h.ok(http.MethodPost, "/v1/chat/completions", c.key, chatRequest(public, "skip malformed deployment during cooldown"))
	if got := h.upstreamSince(0); strings.Join(got, ",") != "healthy-b-"+suffix {
		t.Fatalf("损坏协议部署未进入冷却: %v", got)
	}
}

// cleanupProtocolRuntimeKeys 精确清理本用例部署产生的 Redis 路由状态。
// 参数 t 为测试上下文，redisURL 为专用实例地址，deployments 为本用例部署；开始先清残留并注册结束清理，不执行 FLUSHDB。
func cleanupProtocolRuntimeKeys(t *testing.T, redisURL string, deployments []config.ModelEntry) {
	t.Helper()
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("解析 regression Redis 地址失败: %v", err)
	}
	client := redis.NewClient(opt)
	keys := make([]string, 0, len(deployments)*4)
	for _, dep := range deployments {
		id := router.CooldownID(dep)
		keys = append(keys, "xhub:fails:"+id, "xhub:cooldown:"+id, "xhub:latency:"+id, "xhub:routetpm:"+id)
	}
	remove := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Del(ctx, keys...).Err(); err != nil {
			t.Errorf("清理 protocol regression Redis 键失败: %v", err)
			return
		}
		remaining, err := client.Exists(ctx, keys...).Result()
		if err != nil {
			t.Errorf("确认 protocol regression Redis 清理失败: %v", err)
		} else if remaining != 0 {
			t.Errorf("protocol regression Redis 键仍有 %d 个残留", remaining)
		}
	}
	remove()
	t.Cleanup(func() {
		remove()
		_ = client.Close()
	})
}
