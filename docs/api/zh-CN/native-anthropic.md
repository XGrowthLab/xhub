# Bypass Anthropic 消息

## 原厂协议与认证

使用 XHub 公开模型名及虚拟密钥；部署必须启用对应 Bypass 协议。网关替换上游模型名并注入部署凭据，保留原厂扩展字段，同时执行权限和计费；Bypass 不执行标准入口的调用前文本护栏链。客户端发送 Authorization: Bearer $XHUB_API_KEY。模型支持由部署决定，不表示所有原厂模型自动可用。 也支持 x-api-key: $XHUB_API_KEY；发送 anthropic-version: 2023-06-01。不要传入供应商密钥或不同值的认证头。

## 接口概述

Anthropic Messages

### Anthropic Messages

## 接口行为
使用 Messages 部署。原生客户端发送 anthropic-version: 2023-06-01，XHub 也接受 x-api-key 携带密钥。工具调用与结果是内容块。

## 接口与认证

~~~http
POST /bypass/anthropic/v1/messages
~~~

使用 XHub API 密钥：Authorization: Bearer <XHUB_API_KEY>。不要把供应商密钥当作网关密钥。

Base URL 使用调试台显示的网关根地址，不额外追加 /v1；实际请求使用上面的完整接口路径。

## 请求示例

~~~shell
curl "$XHUB_BASE_URL/bypass/anthropic/v1/messages" \
  -H "Authorization: Bearer $XHUB_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"YOUR_MODEL_NAME","max_tokens":256,"messages":[{"role":"user","content":"Hello, introduce yourself."}]}'
~~~

## 请求参数

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| model | string · required | 公开模型名，部署必须支持此协议。 |
| max_tokens | integer · required | 最大输出 Token。 |
| messages | object[] · required | user/assistant 内容块。 |
| system | string or array | 顶层系统指令。 |
| tools / stream | array / boolean | Anthropic 工具定义 / 事件流。 |

## 成功响应

~~~json
{
  "id": "msg-example",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "text",
      "text": "Hello!"
    }
  ],
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 10,
    "output_tokens": 5
  }
}
~~~

| 字段 | 类型 |
| --- | --- |
| id | string |
| type | string |
| role | string |
| content | object[] |
| content[].type | string |
| content[].text | string |
| stop_reason | string |
| usage | object |
| usage.input_tokens | number |
| usage.output_tokens | number |

## 错误与排查

如果上游因 thinking 签名无效返回 `400`，XHub 可在同一部署补发一次，省略已结束轮次的 thinking 块，并保留正文和工具记录。正在回传工具结果时，只有上游能明确指出出错块属于已结束轮次才会补发，当前工具轮次的签名会完整保留。

| HTTP | 说明 |
| --- | --- |
| 400 | 请求无效，请检查 JSON、参数和查询。 |
| 401 | XHub 凭据缺失或无效。 |
| 403 | 权限拒绝，请检查模型范围或策略。 |
| 429 | 超过限制，请检查响应详情与 Retry-After。 |
