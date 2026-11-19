package dialogue

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// visionBody 生成有序的文字/图片消息；参数为协议、图片地址及精度，返回独立请求，供无网络单测使用，无外部清理。
func visionBody(protocol, uri, detail string) map[string]any {
	part := map[string]any{"type": "image_url", "image_url": map[string]any{"url": uri}}
	if detail != "" {
		part["image_url"].(map[string]any)["detail"] = detail
	}
	textType := "text"
	if protocol == "openai-responses" {
		textType = "input_text"
		part = map[string]any{"type": "input_image", "image_url": uri}
		if detail != "" {
			part["detail"] = detail
		}
	}
	if protocol == "anthropic-messages" {
		part = map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": uri}}
		if len(uri) > 5 && uri[:5] == "data:" {
			part["source"] = map[string]any{"type": "base64", "media_type": "image/png", "data": "AQID"}
		}
	}
	key := "messages"
	if protocol == "openai-responses" {
		key = "input"
	}
	return map[string]any{key: []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": textType, "text": "Before 保留原文"}, part, map[string]any{"type": textType, "text": "After"}}}}}
}

// TestVisionContentMatrix 覆盖三协议九种组合、URL/Base64、多图及图片单独输入；验证字节、顺序、正文和原生字段不变。
// 前置内存消息，返回错误即失败；不下载图片、不依赖供应商或产生持久数据。
func TestVisionContentMatrix(t *testing.T) {
	for _, source := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
		for _, uri := range []string{"https://example.com/a.png?signature=original", "data:image/png;base64,AQID"} {
			body := visionBody(source, uri, "")
			before, _ := json.Marshal(body)
			parsed, err := ParseDialogue(source, body)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Turns[0].Text != "Before 保留原文After" || len(parsed.Turns[0].Content) != 3 {
				t.Fatalf("文字或图片顺序丢失: %+v", parsed.Turns)
			}
			for _, target := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
				encoded, err := EncodeDialogue(parsed, target, "upstream")
				if err != nil {
					t.Fatalf("%s -> %s: %v", source, target, err)
				}
				again, err := ParseDialogue(target, encoded)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(again.Turns, parsed.Turns) {
					t.Fatalf("%s -> %s 图文改变: %+v", source, target, again.Turns)
				}
			}
			after, _ := json.Marshal(body)
			if string(before) != string(after) {
				t.Fatal("原始消息被修改")
			}
			only := Dialogue{Turns: []Turn{{Role: "user", Content: []ContentPart{{Type: "image", URL: uri}, {Type: "image", URL: uri}}}}}
			encoded, err := EncodeDialogue(only, source, "upstream")
			if err != nil {
				t.Fatal(err)
			}
			again, err := ParseDialogue(source, encoded)
			if err != nil || len(again.Turns) != 1 || len(again.Turns[0].Content) != 2 {
				t.Fatalf("仅图片或多图片丢失: %+v %v", again, err)
			}
		}
	}
}

// TestVisionBoundaries 校验未知字段、错误来源、MIME、Base64、URL、角色与 detail；不合法输入不得外发，内存用例无需清理。
func TestVisionBoundaries(t *testing.T) {
	for _, uri := range []string{"", "file:///etc/passwd", "ftp://example.com/x", "https://user:secret@example.com/x", "https://", "data:image/png,abc", "data:image/svg+xml;base64,AQID", "data:image/png;base64,??", "data:image/png;base64,"} {
		if _, err := ParseDialogue("openai-chat", visionBody("openai-chat", uri, "")); err == nil {
			t.Fatalf("非法图片被接受: %q", uri)
		}
	}
	for _, detail := range []string{"auto", "low", "high", "original"} {
		d, err := ParseDialogue("openai-chat", visionBody("openai-chat", "https://example.com/a.png", detail))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := EncodeDialogue(d, "openai-responses", "upstream")
		if err != nil {
			t.Fatal(err)
		}
		again, err := ParseDialogue("openai-responses", encoded)
		if err != nil || again.Turns[0].Content[1].Detail != detail {
			t.Fatal("图片精度丢失")
		}
		_, err = EncodeDialogue(d, "anthropic-messages", "upstream")
		if detail == "auto" && err != nil || detail != "auto" && !errors.Is(err, ErrMessagesImageDetailUnsupported) {
			t.Fatalf("Messages 图片精度 %s 的兼容结果不正确: %v", detail, err)
		}
		if _, err = EncodeDialogue(d, "gemini", "upstream"); err == nil {
			t.Fatal("未实现的 Google 图片桥接被静默丢弃")
		}
	}
	for _, source := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
		body := visionBody(source, "https://example.com/a.png", "")
		key := "messages"
		if source == "openai-responses" {
			key = "input"
		}
		body[key].([]any)[0].(map[string]any)["role"] = "system"
		if _, err := ParseDialogue(source, body); err == nil {
			t.Fatal("非用户图片被接受")
		}
	}
	invalidText := visionBody("anthropic-messages", "https://example.com/a.png", "")
	invalidText["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] = 42
	if _, err := ParseDialogue("anthropic-messages", invalidText); err == nil {
		t.Fatal("图文消息中的错误文本类型被吞掉")
	}
	bad := []map[string]any{
		{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": 42}},
		{"type": "image", "source": map[string]any{"type": "url", "url": "data:image/png;base64,AQID"}},
		{"type": "image", "source": map[string]any{"type": "unknown"}},
		{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.com/x", "extra": true}},
	}
	for _, part := range bad {
		if _, err := parseImagePart(part, "anthropic-messages", "user"); err == nil {
			t.Fatal("非法 Messages 图片被接受")
		}
	}
	for _, mime := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		if err := validateImageURL("data:" + mime + ";base64,AQID"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := imageDetail(42); err == nil {
		t.Fatal("错误精度类型被接受")
	}
	if _, err := imageDetail("invalid"); err == nil {
		t.Fatal("未知精度被接受")
	}
	if _, _, err := messageContent([]any{map[string]any{"type": "text", "text": 42}}, "openai-chat", "user"); err == nil {
		t.Fatal("错误文本类型被吞掉")
	}
	if _, err := encodeImageContent([]ContentPart{{Type: "audio"}}, "openai-chat"); err == nil {
		t.Fatal("未知内容被编码")
	}
}

// TestVisionFileReference 保留原生 Responses file_id，桥接明确拒绝，不假装已经读取上传文件；正常/歧义/错误类型均为纯内存用例。
func TestVisionFileReference(t *testing.T) {
	for _, image := range []map[string]any{{"type": "input_image", "file_id": "file-123"}, {"type": "input_image", "file_id": 42}, {"type": "input_image", "file_id": ""}, {"type": "input_image", "file_id": "file-123", "image_url": "https://example.com/x"}, {"type": "input_image", "image_url": 42}} {
		body := map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{image}}}}
		d, err := ParseDialogue("openai-responses", body)
		if image["file_id"] != "file-123" || image["image_url"] != nil {
			if err == nil {
				t.Fatal("非法图片引用被接受")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		out, err := EncodeDialogue(d, "openai-responses", "upstream")
		if err != nil || out["input"] == nil {
			t.Fatal("原生图片引用丢失")
		}
		for _, target := range []string{"openai-chat", "anthropic-messages"} {
			if _, err := EncodeDialogue(d, target, "upstream"); err == nil {
				t.Fatal("文件引用被静默删除")
			}
		}
	}
}
