package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestModelContractRequests 检查开发期请求结构的正常、缺省、扩展与错误输入；纯内存，无外部资源需清理。
func TestModelContractRequests(t *testing.T) {
	v, err := NewValidator(Document)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, body string
		valid      bool
	}{
		{"/model/new", `{"model_name":"m","litellm_params":{"model":"up","extension":true},"model_info":{"transport":"openai_chat","endpoint_types":["chat"]}}`, true},
		{"/price/deployments/apply", `{"items":[{"id":"d","version":"v"}]}`, true},
		{"/price/deployments/apply", `{"items":[]}`, false},
		{"/price/deployments/apply", `{"items":[{"id":"d"}]}`, false},
		{"/price/deployments/apply", `{"items":[{"id":"","version":"v"}]}`, false},
		{"/model/protocol_probe", `{"id":"m"}`, true},
		{"/model/protocol_probe", `{}`, false},
		{"/model/protocol_probe", `{"id":42}`, false},
		{"/model/new", `{}`, false}, {"/model/new", `{"model_name":42}`, false},
		{"/model/update", `{"id":"m","litellm_params":{"api_key":"*****"},"model_info":{"custom":null}}`, true},
		{"/model/update", `{"disabled":"false"}`, false},
		{"/model/update", `{"id":"m","litellm_params":{"input_cost_per_token":"0.001","output_cost_per_token":null}}`, true},
		{"/model/update", `{"id":"m","litellm_params":{"input_cost_per_token":false}}`, false}, {"/model/update", `null`, false}, {"/model/update", `{} {}`, false},
		{"/model/disable", `{"id":"m"}`, true}, {"/model/enable", `{}`, false},
		{"/health/test_connection", `{"model_info":{"id":"m"},"mode":"chat"}`, true},
		{"/health/test_connection", `{"model_info":{}}`, false},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			if err := v.Request(r); (err == nil) != tc.valid {
				t.Fatalf("valid=%v want=%v: %v", err == nil, tc.valid, err)
			}
		})
	}
}

// TestModelContractResponses 拒绝缺字段、错误状态码及healthy/status矛盾，保留真实200业务失败；纯内存，无数据清理。
func TestModelContractResponses(t *testing.T) {
	v, err := NewValidator(Document)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, body string
		status     int
		valid      bool
	}{
		{"/health/test_connection", `{"status":"success","healthy":true,"error":null,"model":"m","mode":"chat"}`, 200, true},
		{"/health/test_connection", `{"status":"error","healthy":false,"error":"upstream_http_error","message":"Failed","result":{"error":"Failed"},"model":"m","mode":"chat"}`, 200, true},
		{"/health/test_connection", `{"status":"success","healthy":false,"error":null,"model":"m","mode":"chat"}`, 200, false},
		{"/model/new", `{"model_name":"m","litellm_params":{},"model_info":{"id":"id","db_model":true,"disabled":false,"extension":{"retained":true}}}`, 200, true},
		{"/model/new", `{"data":{}}`, 200, false},
		{"/model/delete", `{"deleted":true}`, 200, false},
		{"/model/new", `{"error":{"code":"403","type":"forbidden","message":"Denied","param":null}}`, 403, true},
		{"/model/new", `{"error":{"code":"429","type":"forbidden","message":"Denied","param":null}}`, 403, false},
		{"/model/new", `{}`, 307, false},
	} {
		if err := v.Response(httptest.NewRequest("POST", tc.path, nil), tc.status, http.Header{"Content-Type": []string{"application/json"}}, []byte(tc.body)); (err == nil) != tc.valid {
			t.Fatalf("%s %s valid=%v: %v", tc.path, tc.body, tc.valid, err)
		}
	}
}

// TestModelContractPatchRoundtrip 证明生成补丁类型保持省略/null/0/扩展原值；只序列化，不调用生产解析器，无清理。
func TestModelContractPatchRoundtrip(t *testing.T) {
	for _, raw := range []string{`{}`, `{"id":"m","disabled":false}`, `{"id":"m","litellm_params":{"input_cost_per_token":0,"custom":null},"model_info":{"extension":[1,true]}}`} {
		var value UpdateDeployment
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var before, after any
		json.Unmarshal([]byte(raw), &before)
		json.Unmarshal(out, &after)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("补丁字段改变 %s -> %s", raw, out)
		}
	}
}

// TestModelContractResponseRoundtrip 验证生成响应保留真实协议/状态/精确价格及扩展，不仅编译通过；纯内存往返，无数据清理。
func TestModelContractResponseRoundtrip(t *testing.T) {
	for _, tc := range []struct {
		raw string
		dst any
	}{
		{`{"deployments":[{"id":"d","name":"test","base_model":"model","manual":false,"current":[{"measure":"token","side":"input","window":"all","source_key":"input","usd":0.000002,"unit_size":1000000,"variant":"uncached","label":"input","fallback":false,"extension":"keep"}],"proposed":null,"changed":false,"reason":"","version":"v","extension":true}],"generated_at":"2026-01-01","extension":"keep"}`, new(DeploymentPriceList)},
		{`{"items":[{"id":"d","version":"v","extension":"keep"}],"extension":"keep"}`, new(DeploymentPriceSelection)},
		{`{"updated":2,"extension":"keep"}`, new(DeploymentPriceResult)},
		{`{"model_name":"m","litellm_params":{"model":"up","input_cost_per_token":0.000000123456789,"extension":null},"model_info":{"id":"d","disabled":false,"db_model":true,"endpoint_types":["chat","responses"],"vendor_extension":{"big":9007199254740993}}}`, new(Deployment)},
		{`{"status":"error","healthy":false,"error":"protocol_mismatch","message":"Failed","model":"m","mode":"chat","result":{"error":"Failed"}}`, new(ConnectionFailure)},
		{`{"status":"success","healthy":true,"error":null,"model":"m","mode":"chat"}`, new(ConnectionSuccess)},
	} {
		if err := json.Unmarshal([]byte(tc.raw), tc.dst); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(tc.dst)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		da := json.NewDecoder(strings.NewReader(tc.raw))
		da.UseNumber()
		if err := da.Decode(&a); err != nil {
			t.Fatal(err)
		}
		db := json.NewDecoder(strings.NewReader(string(out)))
		db.UseNumber()
		if err := db.Decode(&b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("生成响应丢字段/精度: %s -> %s", tc.raw, out)
		}
	}
}

// TestPriceContractRejectsMalformedFields 验证生成客户端拒绝错误类型，避免调价预览被解析成错误的费用或版本。
// 输入为独立内存 JSON，覆盖价格、部署和选择字段的失败路径；测试不访问外部服务或产生持久数据。
func TestPriceContractRejectsMalformedFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func() any
		fields map[string]string
	}{
		{"list", func() any { return new(DeploymentPriceList) }, map[string]string{"deployments": `"bad"`, "generated_at": `7`}},
		{"preview", func() any { return new(DeploymentPricePreview) }, map[string]string{"base_model": `7`, "changed": `"bad"`, "current": `"bad"`, "id": `7`, "manual": `"bad"`, "name": `7`, "proposed": `"bad"`, "reason": `7`, "version": `7`}},
		{"rate", func() any { return new(DeploymentPriceRate) }, map[string]string{"fallback": `"bad"`, "label": `7`, "measure": `7`, "side": `7`, "source_key": `7`, "unit_size": `"bad"`, "usd": `"bad"`, "variant": `7`, "window": `7`}},
		{"result", func() any { return new(DeploymentPriceResult) }, map[string]string{"updated": `"bad"`}},
		{"selection", func() any { return new(DeploymentPriceSelection) }, map[string]string{"items": `"bad"`}},
		{"selection-item", func() any { return new(DeploymentPriceSelection_Items_Item) }, map[string]string{"id": `7`, "version": `7`}},
	} {
		for field, value := range tc.fields {
			t.Run(tc.name+"/"+field, func(t *testing.T) {
				if err := json.Unmarshal([]byte(`{"`+field+`":`+value+`}`), tc.target()); err == nil {
					t.Fatalf("%s 字段接受了错误类型", field)
				}
			})
		}
	}
}
