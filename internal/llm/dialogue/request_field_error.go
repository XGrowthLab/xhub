package dialogue

import (
	"errors"
	"strings"
)

// RequestFieldError 标识请求能力错误的字段路径及说明。只承载协议字段和受控说明，不附加客户端内容。
// 解析器或编码器产生，选路和执行器可投影为原协议错误及调用日志。
type RequestFieldError struct {
	Field   string
	Message string
	cause   error
}

// Error 返回可读字段路径及说明，无参数或副作用。选路日志和直接校验错误共用。
func (e *RequestFieldError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

// Unwrap 返回需要保留的既有错误标识，供 errors.Is 判断；普通字段错误没有下层原因。
func (e *RequestFieldError) Unwrap() error {
	return e.cause
}

// atRequestField 用调用方字段路径补全子对象的校验错误；返回新错误，不改动原错误。
// 只有已返回非空错误的分支调用，子校验器已有字段时保留其相对路径。
func atRequestField(field string, err error) *RequestFieldError {
	var nested *RequestFieldError
	if errors.As(err, &nested) {
		if !strings.HasPrefix(nested.Field, "[") {
			field += "."
		}
		return &RequestFieldError{Field: field + nested.Field, Message: nested.Message, cause: nested.cause}
	}
	return &RequestFieldError{Field: field, Message: err.Error()}
}
