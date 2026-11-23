package dialogue

import (
	"encoding/json"
	"reflect"
)

// marshalNativePayload 用已治理的 payload 决定发送哪些字段；original 提供同协议字段的原始 JSON。
// 只借回与最终值相同的字段，避免改写供应商扩展中的大整数。缺失、删减和护栏改写始终以最终值为准。
// 构造器只在同协议出站时调用；无法读取原文时沿用正常编码，不允许原文重新加入任何被移除的字段。
func marshalNativePayload(payload map[string]any, original []byte) ([]byte, error) {
	if len(original) == 0 {
		return json.Marshal(payload)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(original, &fields); err != nil {
		return json.Marshal(payload)
	}
	for key, originalValue := range fields {
		value, present := payload[key]
		if !present || key == "model" {
			continue
		}
		var comparable any
		if json.Unmarshal(originalValue, &comparable) == nil && reflect.DeepEqual(value, comparable) {
			payload[key] = originalValue
		}
	}
	return json.Marshal(payload)
}
