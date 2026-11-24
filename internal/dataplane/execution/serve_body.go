package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// decodeServeBody 读取 Serve 的完整 JSON 正文。参数 raw 是请求字节，exactNumbers 表示对话需保留精确数值。
// 返回动态正文与解析错误。原始文本不参与出站，护栏和协议转换处理同一个已解码的正文。
// 非对话操作及供自定义护栏读取的 metadata 沿用原数字类型。
func decodeServeBody(raw []byte, exactNumbers bool) (map[string]any, error) {
	var body map[string]any
	if !exactNumbers {
		err := json.Unmarshal(raw, &body)
		return body, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("invalid JSON after request body")
	}
	for key, value := range body {
		// 自定义护栏按 float64 读取 metadata，出站沿用同一表示，避免脚本所见与实际发送不一致。
		normalized, err := validateServeNumbers(value, key == "metadata")
		if err != nil {
			return nil, err
		}
		body[key] = normalized
	}
	return body, nil
}

// validateServeNumbers 保留原先 JSON 解码接受的有限数值范围。参数 value 为刚解析的 JSON 子树，legacy 表示归一为 float64。
// decodeServeBody 在治理前调用。返回有效数值子树或解析错误；对象和数组原位更新，不回读请求字节。
func validateServeNumbers(value any, legacy bool) (any, error) {
	switch node := value.(type) {
	case json.Number:
		number, err := node.Float64()
		if err != nil {
			return nil, err
		}
		if legacy {
			return number, nil
		}
	case map[string]any:
		for key, entry := range node {
			normalized, err := validateServeNumbers(entry, legacy)
			if err != nil {
				return nil, err
			}
			node[key] = normalized
		}
	case []any:
		for i, entry := range node {
			normalized, err := validateServeNumbers(entry, legacy)
			if err != nil {
				return nil, err
			}
			node[i] = normalized
		}
	}
	return value, nil
}
