package dialogue

import (
	"errors"
	"net/http"

	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
)

// streamFailureEvent 将明确失败包装为调用方协议事件，保留原始错误对象及请求关联字段。
// 参数为错误、调用方协议和已观察响应 ID；返回事件名和 JSON 文档，原生/转换流共用。
// HTTP 头已发送时只发布失败事件，不重写状态或提交成功历史；Google 输出数字错误码以供 SDK 识别，不修改供应商对象。
func streamFailureEvent(err error, protocol, id string) (string, map[string]any) {
	payload := dpdiagnostics.ErrorEnvelope(err)
	if protocol == "openai-responses" {
		response := map[string]any{"id": id, "status": "failed", "error": payload["error"]}
		delete(payload, "error")
		payload["type"], payload["response"] = "response.failed", response
		return "response.failed", payload
	}
	if protocol == "gemini" || protocol == "vertex" {
		var upstream *dpdiagnostics.UpstreamError
		if !errors.As(err, &upstream) {
			fields := payload["error"].(map[string]any)
			fields["reason"], fields["code"], fields["status"] = fields["code"], http.StatusBadGateway, "UNAVAILABLE"
			delete(fields, "type")
		}
		return "", payload
	}
	payload["type"] = "error"
	return "error", payload
}
