package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCORSExposesUpstreamRequestIDs 验证跨域调用可以从 HTTP 响应读取供应商与网关查单 ID。
// 参数 t 为测试上下文；使用内存请求验证正常 Origin 和缺省 Origin，无网络或持久化状态。
func TestCORSExposesUpstreamRequestIDs(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	withoutOrigin := httptest.NewRecorder()
	setCORS(withoutOrigin, request)
	if exposed := withoutOrigin.Header().Get("Access-Control-Expose-Headers"); exposed != "" {
		t.Fatalf("无 Origin 请求不应写 CORS 头: %q", exposed)
	}

	request.Header.Set("Origin", "https://console.example.test")
	response := httptest.NewRecorder()
	setCORS(response, request)
	exposed := "," + strings.ReplaceAll(strings.ToLower(response.Header().Get("Access-Control-Expose-Headers")), " ", "") + ","
	for _, name := range []string{"x-litellm-call-id", "x-oneapi-request-id", "request-id", "x-request-id", "x-amzn-requestid"} {
		if !strings.Contains(exposed, ","+name+",") {
			t.Errorf("浏览器无法读取查单头 %s", name)
		}
	}
}
