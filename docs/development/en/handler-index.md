# HTTP handler source index

Use the method/path tables in the [source index](../zh-CN/handler-index.md) to find registration files and handlers. These identifiers are shared between languages. They are not request/response schemas or a generated OpenAPI contract.

~~~bash
rg 'GET|POST|DELETE|PATCH' internal/gateway/*/mount.go
~~~

| Area | Source |
| --- | --- |
| Models / prices | [models/mount.go](../../../internal/gateway/models/mount.go) |
| Identity | [identity/mount.go](../../../internal/gateway/identity/mount.go) |
| Keys | [keys/mount.go](../../../internal/gateway/keys/mount.go) |
| Preferences | [prefs/mount.go](../../../internal/gateway/prefs/mount.go) |
| Usage | [usage/mount.go](../../../internal/gateway/usage/mount.go) |

| Method and path | Handler | Registration source |
| --- | --- | --- |
| `GET /account/log-capture` | `LogCaptureSettings` | [mount.go](../../../internal/gateway/identity/mount.go) |
| `PUT /account/log-capture` | `LogCaptureSettings` | [mount.go](../../../internal/gateway/identity/mount.go) |
| `GET /spend/logs/ui/filter-options` | `LogFilterOptions` | [mount.go](../../../internal/gateway/usage/mount.go) |

[Consumer API guide](api-reference.md)

[SAML configuration and metadata registration](saml-login.md)

[Manual reconciliation of uncertain charges](budget-reconciliation.md)

## Upstream protocol detection

[Configuration and validation boundaries](upstream-protocol-detection.md). Requires a platform administrator and an existing saved deployment.

| Method and path | Handler | Registration |
| --- | --- | --- |
| `POST /model/protocol_probe` | `ProbeProtocol` | [mount.go](../../../internal/gateway/models/mount.go) |
