# Bypass Anthropic Messages

## Native protocol and authentication

Use an XHub public model name and virtual key with the matching Bypass deployment. XHub maps the model and injects deployment credentials, preserves native fields and enforces access and billing. Bypass does not run the standard pre-call text guardrail chain. Send Authorization: Bearer $XHUB_API_KEY. Model availability depends on configured deployments. x-api-key: $XHUB_API_KEY is also accepted. Send anthropic-version: 2023-06-01. Do not send provider credentials or conflicting keys.

## Overview

Anthropic Messages

### Anthropic Messages

## Behavior
Use a Messages deployment. Native clients send anthropic-version: 2023-06-01. XHub also accepts its key in x-api-key. Tool use/results are content blocks.

## Endpoint and authentication

~~~http
POST /bypass/anthropic/v1/messages
~~~

Use an XHub API key: Authorization: Bearer <XHUB_API_KEY>. Do not use upstream provider credentials as gateway keys.

Use the gateway root Base URL shown in the playground, without appending /v1; send requests to the full endpoint path above.

## Request example

~~~shell
curl "$XHUB_BASE_URL/bypass/anthropic/v1/messages" \
  -H "Authorization: Bearer $XHUB_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"YOUR_MODEL_NAME","max_tokens":256,"messages":[{"role":"user","content":"Hello, introduce yourself."}]}'
~~~

## Request parameters

| Field | Type | Description |
| --- | --- | --- |
| model | string · required | Public model name; deployment must support this protocol. |
| max_tokens | integer · required | Maximum output tokens. |
| messages | object[] · required | User/assistant content blocks. |
| system | string or array | Top-level system instructions. |
| tools / stream | array / boolean | Anthropic tool schema / event stream. |

## Success response

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

| Field | Type |
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

## Errors and troubleshooting

If the upstream returns a `400` for an invalid thinking signature, XHub can retry once on the same deployment with thinking blocks from completed turns omitted. Text and tool records stay in the request. During tool continuation, recovery requires the error to identify an earlier completed turn. The current tool round keeps its signed blocks intact.

| HTTP | Meaning |
| --- | --- |
| 400 | Invalid request. Check JSON, parameters and query. |
| 401 | Missing or invalid XHub credential. |
| 403 | Permission denied. Check model scope or policy. |
| 429 | Limit exceeded. Inspect response details and Retry-After. |
