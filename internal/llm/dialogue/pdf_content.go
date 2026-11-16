package dialogue

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode"
)

const maxInlinePDFBytes = 20 << 20

// parsePDFPart 解析用户的内联 PDF；block、protocol 和 role 是入口协议的原始内容块。
// 返回跨协议文件项或明确错误；只接受有界 Base64 PDF，不下载地址、不解析文档，也不静默丢弃文件引用。
// 供协议桥接和严格预算准入调用；原生通道仍按其自身能力保留其他格式。
func parsePDFPart(block map[string]any, protocol, role string) (ContentPart, error) {
	part := ContentPart{Type: "file", Filename: "document.pdf"}
	if role != "user" {
		return part, fmt.Errorf("document content requires user role")
	}
	var data, filename string
	switch protocol {
	case "openai-chat":
		if block["type"] != "file" || dialogueFields(block, "type", "file") != nil {
			return part, fmt.Errorf("unsupported Chat file content")
		}
		file := dialogueMap(block["file"])
		if file == nil || dialogueFields(file, "filename", "file_data") != nil {
			return part, fmt.Errorf("inline Chat PDF required")
		}
		data, _ = file["file_data"].(string)
		if v := file["filename"]; v != nil {
			var ok bool
			filename, ok = v.(string)
			if !ok {
				return part, fmt.Errorf("invalid PDF filename")
			}
		}
	case "openai-responses":
		if block["type"] != "input_file" || dialogueFields(block, "type", "filename", "file_data") != nil {
			return part, fmt.Errorf("inline Responses PDF required")
		}
		data, _ = block["file_data"].(string)
		if v := block["filename"]; v != nil {
			var ok bool
			filename, ok = v.(string)
			if !ok {
				return part, fmt.Errorf("invalid PDF filename")
			}
		}
	case "anthropic-messages":
		if block["type"] != "document" || dialogueFields(block, "type", "source", "title") != nil {
			return part, fmt.Errorf("unsupported Messages document content")
		}
		source := dialogueMap(block["source"])
		if source == nil || dialogueFields(source, "type", "media_type", "data") != nil || source["type"] != "base64" || source["media_type"] != "application/pdf" {
			return part, fmt.Errorf("inline Messages PDF required")
		}
		b64, ok := source["data"].(string)
		if !ok {
			return part, fmt.Errorf("PDF data must be a string")
		}
		data = "data:application/pdf;base64," + b64
		if v := block["title"]; v != nil {
			var valid bool
			filename, valid = v.(string)
			if !valid {
				return part, fmt.Errorf("invalid PDF title")
			}
		}
	default:
		return part, fmt.Errorf("unsupported PDF protocol")
	}
	if filename != "" {
		if len(filename) > 255 || strings.ContainsAny(filename, "/\\") || strings.IndexFunc(filename, unicode.IsControl) >= 0 {
			return part, fmt.Errorf("invalid PDF filename")
		}
		part.Filename = filename
	}
	const prefix = "data:application/pdf;base64,"
	if !strings.HasPrefix(data, prefix) || len(data)-len(prefix) > base64.StdEncoding.EncodedLen(maxInlinePDFBytes) {
		return part, fmt.Errorf("PDF must be a bounded inline base64 data URL")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(data, prefix))
	if err != nil || len(decoded) > maxInlinePDFBytes || !bytes.HasPrefix(decoded, []byte("%PDF-")) {
		return part, fmt.Errorf("invalid PDF bytes")
	}
	part.URL = data
	return part, nil
}

// encodePDFPart 把已校验的内联 PDF 映射到目标协议；part 为共享投影，protocol 为目标协议。
// 返回协议内容块或不支持错误；保留文件字节及文件名，不获取远端资源，供 EncodeDialogue 调用。
func encodePDFPart(part ContentPart, protocol string) (map[string]any, error) {
	const prefix = "data:application/pdf;base64,"
	if !strings.HasPrefix(part.URL, prefix) || part.Filename == "" {
		return nil, fmt.Errorf("validated inline PDF required")
	}
	switch protocol {
	case "openai-chat":
		return map[string]any{"type": "file", "file": map[string]any{"filename": part.Filename, "file_data": part.URL}}, nil
	case "openai-responses":
		return map[string]any{"type": "input_file", "filename": part.Filename, "file_data": part.URL}, nil
	case "anthropic-messages":
		return map[string]any{"type": "document", "title": part.Filename, "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": strings.TrimPrefix(part.URL, prefix)}}, nil
	default:
		return nil, fmt.Errorf("PDF content requires a supported protocol")
	}
}

// ValidateBudgetInputPDF 供严格预算校验用户内联文件；block 和 protocol 为实际入站内容及协议。
// 返回格式错误；缓存提示由预算层单独检查，本函数不保存或修改文件，也不允许不透明引用。
func ValidateBudgetInputPDF(block map[string]any, protocol string) error {
	copy := make(map[string]any, len(block))
	for key, value := range block {
		if key != "cache_control" {
			copy[key] = value
		}
	}
	_, err := parsePDFPart(copy, protocol, "user")
	return err
}
