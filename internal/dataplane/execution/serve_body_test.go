package execution

import (
	"encoding/json"
	"testing"
)

// TestDecodeServeBody 验证对话在进入护栏前保留精确数字，非对话兼容既有类型，并拒绝尾部数据。
// 参数 t 为测试上下文，仅使用内存中的普通请求示例，不需清理。
func TestDecodeServeBody(t *testing.T) {
	const number = "9007199254740993"
	for _, tc := range []struct {
		name, raw string
		exact     bool
		want      any
	}{
		{"dialogue", `{"value":` + number + `} `, true, json.Number(number)},
		{"ordinary", `{"value":17}`, false, float64(17)},
		{"invalid_json", `{"value":`, true, nil},
		{"extra_document", `{"value":17} {}`, true, nil},
		{"extra_bytes", `{"value":17} hello`, true, nil},
		{"out_of_range_object", `{"value":{"number":1e400}}`, true, nil},
		{"out_of_range_array", `{"value":[1e400]}`, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := decodeServeBody([]byte(tc.raw), tc.exact)
			if tc.want == nil {
				if err == nil || body != nil {
					t.Fatalf("应拒绝不完整或包含尾部数据的请求正文: %#v %v", body, err)
				}
			} else if err != nil || body["value"] != tc.want {
				t.Fatalf("请求数字的类型或精度错误: %#v %v", body, err)
			}
		})
	}
	body, err := decodeServeBody([]byte(`{"metadata":{"values":[17,9007199254740993]},"vendor_options":{"number":9007199254740993}}`), true)
	if err != nil {
		t.Fatal(err)
	}
	seenByGuard := body["metadata"].(map[string]any)["values"].([]any)
	if seenByGuard[0] != float64(17) || seenByGuard[1] != float64(9007199254740992) ||
		body["vendor_options"].(map[string]any)["number"] != json.Number(number) {
		t.Fatalf("护栏的 metadata 类型与出站精确扩展不匹配: %#v", body)
	}
}
