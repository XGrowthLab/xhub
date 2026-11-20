package regression

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/storageaudit"
)

// TestDiagnosticCompressionAcrossProtocols 通过本地 HTTP 上游返回长诊断响应头，真实调用 Chat、Messages、Responses、流式透传及故障路径。
// 比较转换前后的详情、用量与权限，并验证审计、损坏帧错误及回滚。服务、账号和数据随私有夹具清理。
func TestDiagnosticCompressionAcrossProtocols(t *testing.T) {
	var labels strings.Builder
	for i := 0; i < 160; i++ {
		fmt.Fprintf(&labels, "%x", sha256.Sum256([]byte(fmt.Sprint(i))))
	}
	diagnostic := strings.Repeat(labels.String(), 2)
	const model = "diagnostic-pack-fixture"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("X-Test-Counters", diagnostic)
		w.Header().Set("Set-Cookie", "fixture=must-stay-private")
		if strings.Contains(string(mustJSON(body)), "reject-diagnostic-probe") {
			w.WriteHeader(422)
			writeJSON(w, map[string]any{"error": map[string]string{"message": "diagnostic-input-fixture-rejected"}})
			return
		}
		if body["stream"] != true {
			writeJSON(w, chatAnswer(model, nil))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := map[string]any{"id": "chatcmpl-diag-sse", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "fixture answer"}, "finish_reason": "stop"}}, "usage": openAIUsage(nil)}
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", mustJSON(chunk))
	}))
	t.Cleanup(up.Close)
	dep := chatDeployment(model)
	dep.ModelInfo["endpoint_types"] = []string{"chat", "responses", "messages"}
	dep.LiteLLMParams["api_base"] = up.URL
	h := newHarness(t, dep)
	admin := h.adminSession()
	owner := h.openScope(t, admin, "diag-pack-owner")
	other := h.openScope(t, admin, "diag-pack-other")
	stream := chatRequest(model, "stream fixture")
	stream["stream"] = true
	messages := chatRequest(model, "messages fixture")
	messages["max_tokens"] = 32
	type record struct {
		id     string
		detail map[string]any
	}
	var records []record
	for _, r := range []struct {
		path    string
		body    map[string]any
		success bool
	}{
		{"/v1/chat/completions", chatRequest(model, "chat fixture"), true}, {"/v1/messages", messages, true}, {"/v1/responses", map[string]any{"model": model, "input": "responses fixture"}, true},
		{"/bypass/openai/v1/chat/completions", stream, true}, {"/v1/chat/completions", chatRequest(model, "reject-diagnostic-probe"), false},
	} {
		got := h.do(http.MethodPost, r.path, owner.key, r.body)
		if (got.status == 200) != r.success {
			t.Fatalf("协议请求 %s 状态 %d", r.path, got.status)
		}
		id := got.header("x-litellm-call-id")
		if id == "" {
			t.Fatal("缺少请求 ID", r.path)
		}
		h.flushSpend()
		detail := h.ok(http.MethodGet, "/spend/logs/ui/"+id, owner.session, nil).json()
		meta := string(mustJSON(detail["metadata"]))
		if !strings.Contains(meta, labels.String()[:100]) || strings.Contains(meta, "must-stay-private") {
			t.Fatal("没有真实大诊断或泄露了测试 Cookie", r.path)
		}
		records = append(records, record{id, detail})
	}
	// 只将本例事件设为截止点之前，避免用机器时钟等待决定边界。
	cut := time.Now().UTC().Truncate(time.Second)
	if _, e := h.store.DB.Exec("UPDATE usage_events SET ts=$1", cut.Add(-time.Minute)); e != nil {
		t.Fatal(e)
	}
	o := storageaudit.DiagnosticOptions{Before: cut, Rows: 100, BatchBytes: 1 << 20, RecordBytes: 256 << 10, MinBytes: 4096}
	plan, e := storageaudit.DiagnosticBatch(t.Context(), h.store.DB, o, "pack")
	if e != nil || plan.Selected != len(records) {
		t.Fatal("真实协议诊断未完整转换", plan, e)
	}
	for _, r := range records {
		url := "/spend/logs/ui/" + r.id
		got := h.ok(http.MethodGet, url, owner.session, nil).json()
		for _, field := range []string{"metadata", "spend", "prompt_tokens", "completion_tokens", "status"} {
			if !reflect.DeepEqual(r.detail[field], got[field]) {
				t.Fatal("转换影响日志字段", field)
			}
		}
		if resp := h.do(http.MethodGet, url, other.session, nil); resp.status != 404 {
			t.Fatal("其他账号能够访问诊断", resp.status)
		}
	}
	search := h.ok(http.MethodGet, "/spend/logs/ui?error_message=diagnostic-input-fixture-rejected", owner.session, nil)
	if !strings.Contains(search.text(), records[len(records)-1].id) || strings.Contains(search.text(), labels.String()[:100]) || strings.Contains(search.text(), "xhub:diagnostic:") {
		t.Fatal("错误搜索未保留记录或日志列表公开了完整诊断")
	}
	h.ok(http.MethodGet, "/spend/logs/ui/"+records[0].id, admin, nil)
	var audits int
	if e = h.store.DB.QueryRow("SELECT count(*) FROM audit_logs WHERE action='log.read' AND object_id=$1", records[0].id).Scan(&audits); e != nil || audits < 1 {
		t.Fatal("管理员读取未写审计", audits, e)
	}
	var saved string
	if e = h.store.DB.QueryRow("SELECT upstream_response FROM request_logs WHERE request_id=$1", records[0].id).Scan(&saved); e != nil {
		t.Fatal(e)
	}
	if _, e = h.store.DB.Exec("UPDATE request_logs SET upstream_response=upstream_response||'!' WHERE request_id=$1", records[0].id); e != nil {
		t.Fatal(e)
	}
	url := "/spend/logs/ui/" + records[0].id
	if got := h.do(http.MethodGet, url, owner.session, nil); got.status != 500 {
		t.Fatal("损坏诊断没有报告读取失败", got.status)
	}
	if got := h.do(http.MethodGet, url, other.session, nil); got.status != 404 {
		t.Fatal("损坏诊断泄露了其他用户日志", got.status)
	}
	if _, e = h.store.DB.Exec("UPDATE request_logs SET upstream_response=$2 WHERE request_id=$1", records[0].id, saved); e != nil {
		t.Fatal(e)
	}
	p, e := storageaudit.DiagnosticBatch(t.Context(), h.store.DB, o, "restore")
	if e != nil || p.Restored != len(records) {
		t.Fatal("协议诊断回退遗漏", p, e)
	}
}
