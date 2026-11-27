package dialogue

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
)

// nativeThinkingEvents 返回完整原生思考/工具 SSE 样本，参数无，返回事件列表；仅供纯内存传输测试，无外部副作用。
func nativeThinkingEvents() []string {
	return []string{`{"type":"message_start","message":{"id":"msg_native","type":"message","role":"assistant","model":"private","content":[],"usage":{"input_tokens":8}}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Plan"}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"native-"}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signature"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"native-cipher"}}`, `{"type":"content_block_stop","index":1}`, `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call","name":"lookup","caller":{"type":"direct"},"input":{}}}`, `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`, `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"a\"}"}}`, `{"type":"content_block_stop","index":2}`, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`, `{"type":"message_stop"}`}
}

// TestMessagesThinkingBridgeFailure 验证合法的原厂思考块在缺少安全转换的路径中被识别为协议能力限制。
// t 为上下文；覆盖首帧前和已输出文字两条路径，均不得输出原厂思考、签名或成功终态，内存夹具无需清理。
func TestMessagesThinkingBridgeFailure(t *testing.T) {
	const messageStart = `{"type":"message_start","message":{"id":"msg_native","type":"message","role":"assistant","content":[]}}`
	const thinking = `{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":"Private thought","signature":"Synthetic signature"}}`
	const textStart = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Partial answer"}}`
	const textStop = `{"type":"content_block_stop","index":0}`
	for _, tc := range []struct {
		name, caller string
		frames       []string
		started      bool
	}{
		{"Chat首帧前", "openai-chat", []string{messageStart, fmt.Sprintf(thinking, 0)}, false},
		{"Gemini首帧前", "gemini", []string{messageStart, fmt.Sprintf(thinking, 0)}, false},
		{"Vertex首帧前", "vertex", []string{messageStart, fmt.Sprintf(thinking, 0)}, false},
		{"Chat已有正文", "openai-chat", []string{messageStart, textStart, textStop, fmt.Sprintf(thinking, 1)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "data: " + strings.Join(tc.frames, "\n\ndata: ") + "\n\n"
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(raw))}
			wrote, _, _, capture, err := PipeDialogue(httptest.NewRecorder(), resp, time.Now(), "public", "anthropic-messages", tc.caller)
			const detail = "Messages content_block.type thinking requires native Messages on this route"
			if wrote != tc.started || !errors.Is(err, dpdiagnostics.ErrUnsupportedResponseBridge) || !strings.Contains(err.Error(), detail) {
				t.Fatalf("思考块限制未报告具体字段，或提前写入了客户端：wrote=%t error=%v", wrote, err)
			}
			body := string(capture)
			if tc.started && (!strings.Contains(body, detail) || !strings.Contains(body, `"code":"unsupported_response_bridge"`)) {
				t.Fatalf("已经开流的响应缺少明确诊断：%s", body)
			}
			if strings.Contains(body, "Private thought") || strings.Contains(body, "Synthetic signature") || strings.Contains(body, "[DONE]") {
				t.Fatalf("失败流泄露签名、思考或成功终态：%s", body)
			}
		})
	}
}

// TestNativeMessagesStreamHistory 验证原生签名分片/密文/工具参数/usage保留以及坏流不能发布归属或成功终态。
// 参数 t 为上下文；完整、缺终态、缺结束原因、未闭合块、错误类型/索引/JSON均使用内存HTTP，无外部清理。
func TestNativeMessagesStreamHistory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]string) []string
		ok     bool
	}{{"valid", func(e []string) []string { return e }, true}, {"no_terminal", func(e []string) []string { return e[:len(e)-1] }, false}, {"no_reason", func(e []string) []string { return append(e[:len(e)-2], e[len(e)-1]) }, false}, {"open_block", func(e []string) []string { return append(e[:11], e[12:]...) }, false}, {"bad_signature", func(e []string) []string { e[3] = strings.Replace(e[3], `"native-"`, `42`, 1); return e }, false}, {"bad_index", func(e []string) []string { e[1] = strings.Replace(e[1], `"index":0`, `"index":1.5`, 1); return e }, false}, {"bad_args", func(e []string) []string { e[10] = strings.Replace(e[10], `\"a\"}`, `broken`, 1); return e }, false}, {"bad_caller", func(e []string) []string { e[8] = strings.Replace(e[8], `"direct"`, `"programmatic"`, 1); return e }, false}, {"delta_after_stop", func(e []string) []string { e[6] = e[4]; return e }, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var raw strings.Builder
			for _, e := range tc.change(nativeThinkingEvents()) {
				fmt.Fprintf(&raw, "data: %s\n\n", e)
			}
			w := httptest.NewRecorder()
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(raw.String()))}
			committed := false
			var result llmdialogue.DialogueResult
			wrote, usage, _, capture, err := PipeDialogue(w, resp, time.Now(), "public", "anthropic-messages", "anthropic-messages", func(r llmdialogue.DialogueResult) {
				if strings.Contains(w.Body.String(), "message_stop") {
					t.Error("归属在终态之后才提交")
				}
				committed = true
				result = r
			})
			if (err == nil) != tc.ok || committed != tc.ok {
				t.Fatalf("原生流生命周期不符: committed=%t err=%v body=%s", committed, err, capture)
			}
			if tc.ok {
				if !wrote || usage["total_tokens"] != float64(10) || result.Native == nil || !strings.Contains(string(capture), `"model":"public"`) {
					t.Fatalf("原生流事实丢失: %v %s", usage, capture)
				}
				content := result.Native["content"].([]any)
				if content[0].(map[string]any)["signature"] != "native-signature" || content[1].(map[string]any)["data"] != "native-cipher" || content[2].(map[string]any)["input"].(map[string]any)["path"] != "a" {
					t.Fatalf("快照拼接改变内容: %v", content)
				}
			} else if strings.Contains(string(capture), `"type":"message_stop"`) {
				t.Fatal("坏流仍发布成功终态")
			}
		})
	}
}

// TestNativeMessagesSparseIndices 验证已闭合块之间可跳号，标准 Messages 客户端仍按连续下标接收同一组内容。
// 参数 t 为测试上下文；复刻上游 thinking 0 → text 2 的流，并对照重复、倒序、超范围及错误匹配，纯内存无需数据清理。
func TestNativeMessagesSparseIndices(t *testing.T) {
	textStart := `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`
	textDelta := `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Complete"}}`
	textStop := `{"type":"content_block_stop","index":2}`
	for _, tc := range []struct {
		name         string
		textEvents   []string
		errorDetail  string
		omitThinking bool
	}{
		{name: "gap", textEvents: []string{textStart, textDelta, textStop}},
		{name: "first_block_not_zero", textEvents: []string{textStart}, errorDetail: "first content_block_start.index must be 0, got 2", omitThinking: true},
		{name: "duplicate", textEvents: []string{textStart, textStop, textStart}, errorDetail: "content_block_start.index 2 must be greater than previous index 2"},
		{name: "backward", textEvents: []string{textStart, textStop, strings.Replace(textStart, `"index":2`, `"index":1`, 1)}, errorDetail: "content_block_start.index 1 must be greater than previous index 2"},
		{name: "overlap", textEvents: []string{textStart, strings.Replace(textStart, `"index":2`, `"index":4`, 1)}, errorDetail: "content_block_start.index 4 before content_block_stop.index 2"},
		{name: "delta_without_start", textEvents: []string{textDelta}, errorDetail: "content_block_delta.index 2 has no open content block"},
		{name: "delta_wrong_index", textEvents: []string{textStart, strings.Replace(textDelta, `"index":2`, `"index":1`, 1)}, errorDetail: "content_block_delta.index 1 does not match open content block index 2"},
		{name: "stop_wrong_index", textEvents: []string{textStart, strings.Replace(textStop, `"index":2`, `"index":1`, 1)}, errorDetail: "content_block_stop.index 1 does not match open content block index 2"},
		{name: "outside_safe_range", textEvents: []string{strings.Replace(textStart, `"index":2`, `"index":9007199254740992`, 1)}, errorDetail: "content_block_start.index must be a nonnegative safe integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thinking := nativeThinkingEvents()[:6:6]
			if tc.omitThinking {
				thinking = thinking[:1:1]
			}
			events := append(thinking, tc.textEvents...)
			events = append(events, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`, `{"type":"message_stop"}`)
			var raw strings.Builder
			for _, event := range events {
				fmt.Fprintf(&raw, "data: %s\n\n", event)
			}
			var result llmdialogue.DialogueResult
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(raw.String()))}
			_, _, _, capture, err := PipeDialogue(httptest.NewRecorder(), resp, time.Now(), "public", "anthropic-messages", "anthropic-messages", func(r llmdialogue.DialogueResult) { result = r })
			if tc.errorDetail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errorDetail) || !strings.Contains(string(capture), tc.errorDetail) || result.Native != nil || strings.Contains(string(capture), "message_stop") {
					t.Fatalf("非法跳号未向客户端明确报错或错误提交历史：err=%v stream=%s", err, capture)
				}
				return
			}
			if err != nil || result.Text != "Complete" || !strings.Contains(string(capture), `"type":"message_stop"`) {
				t.Fatalf("有效跳号流未完成：err=%v stream=%s", err, capture)
			}
			content := result.Native["content"].([]any)
			if len(content) != 2 || streamMap(content[0])["signature"] != "native-signature" || streamMap(content[1])["text"] != "Complete" {
				t.Fatalf("跳号导致签名或最终内容丢失：%v", content)
			}
			var indices []int
			for _, event := range strings.Split(string(capture), "\n\n") {
				if event == "" {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(event, "data: ")), &payload); err != nil {
					t.Fatal(err)
				}
				if index, ok := payload["index"].(float64); ok {
					indices = append(indices, int(index))
				}
			}
			if !slices.Equal(indices, []int{0, 0, 0, 0, 0, 1, 1, 1}) {
				t.Fatalf("开始、增量、停止事件的客户端序号不一致：%v", indices)
			}
		})
	}
}
