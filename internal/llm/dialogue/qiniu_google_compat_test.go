package dialogue

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestQiniuGoogleFinishReasonCompatibility 验证 Gemini 和 Vertex 共享的结束枚举转换及错误边界。
// 参数 t 为测试上下文。同步和流式夹具覆盖小写、大小写混合、限额、非终态和上游阻断，不使用外部资源。
func TestQiniuGoogleFinishReasonCompatibility(t *testing.T) {
	for _, tc := range []struct{ source, stop, want, failure string }{
		{"gemini", "stop", "stop", ""},
		{"vertex", "sToP", "stop", ""},
		{"gemini", "max_tokens", "length", ""},
		{"vertex", "MAX_TOKENS", "length", ""},
		{"gemini", "safety", "", "candidates[0].finishReason"},
		{"vertex", "provider_unknown", "", "candidates[0].finishReason"},
	} {
		t.Run(tc.source+"/"+tc.stop, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"candidates":[{"finishReason":%q,"content":{"parts":[{"text":"Hello"}]}}]}`, tc.stop))
			sync, err := ParseDialogueResult(tc.source, raw)
			frame, frameErr := ParseGoogleDialogueFrame(raw)
			if tc.failure != "" {
				if err == nil || frameErr == nil || !strings.Contains(err.Error(), tc.failure) || !strings.Contains(frameErr.Error(), tc.stop) {
					t.Fatalf("归一代码吞掉阻断或未知原因: sync=%v stream=%v", err, frameErr)
				}
				return
			}
			if err != nil || frameErr != nil || sync.Stop != tc.want || frame.Stop != tc.want {
				t.Fatalf("结束原因未归一为 %q: sync=%+v err=%v frame=%+v err=%v", tc.want, sync, err, frame, frameErr)
			}
		})
	}
	for _, raw := range []string{
		`{"candidates":[{"content":{"parts":[{"text":"Partial"}]}}]}`,
		`{"candidates":[],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2}}`,
		`{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2}}`,
	} {
		result, err := ParseGoogleDialogueFrame([]byte(raw))
		if err != nil || result.Stop != "" {
			t.Fatalf("增量或独立用量帧误标为终态: %+v error=%v", result, err)
		}
	}
	result, err := ParseGoogleDialogueFrame([]byte(`{"candidates":[{"finishReason":"max_tokens","content":{"parts":[{"functionCall":{"name":"Read","args":{}}}]}}]}`))
	if err != nil || result.Stop != "length" || len(result.Calls) != 1 {
		t.Fatalf("工具部分覆盖了最大输出限额: %+v error=%v", result, err)
	}
}

// TestQiniuGooglePublicReasoningParts 验证正文和无签名公开推理分别编码和还原。
// 参数 t 为测试上下文。跨协议编码后往返解码，含密文或非法标志应返回字段错误。纯内存测试，无清理。
func TestQiniuGooglePublicReasoningParts(t *testing.T) {
	encoded, err := EncodeDialogueResult(DialogueResult{Text: "Answer", Reasoning: "Summary", Stop: "stop"}, "vertex", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) || !strings.Contains(string(encoded), `"text":"Summary","thought":true`) || !strings.Contains(string(encoded), `{"text":"Answer"}`) {
		t.Fatalf("公开推理和正文未独立编码: %s", encoded)
	}
	for _, protocol := range []string{"gemini", "vertex"} {
		r, err := ParseDialogueResult(protocol, encoded)
		if err != nil || r.Text != "Answer" || r.Reasoning != "Summary" {
			t.Fatalf("公开推理或正文丢失: %+v %v", r, err)
		}
	}
	for _, part := range []string{`"thought":true,"thoughtSignature":"eA=="`, `"thought":"true"`, `"thought":true,"unexpected":1`} {
		raw := []byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"Summary",` + part + `}]}}]}`)
		if _, err := ParseDialogueResult("gemini", raw); err == nil || !strings.Contains(err.Error(), "part") {
			t.Fatalf("签名字段或未知字段未被拒绝: %s err=%v", part, err)
		}
	}
}
