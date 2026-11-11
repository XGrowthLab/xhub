# Upstream protocol detection

Administrators can enable detection after saved connection changes in a dialogue deployment editor, or run a manual check using the saved configuration. Existing deployments default to automatic detection off; upgrading does not trigger paid calls across the directory.

Verified Chat, Responses and Messages protocols add matching passthrough entry points to the same deployment. Model permissions, pricing and budget enforcement remain unchanged. Standard routes keep their selected upstream transport. An upstream gateway may convert protocols internally; the check validates its exposed API, not the underlying model implementation.

Each protocol uses up to four short requests: a text reply, a complete text stream, a fixed function call and a tool result round trip with matching IDs. HTTP 200, HTML or missing stream termination are not sufficient. Checks use fixed English input, no user history and no real tool execution. Each step allows 30 seconds and at most 1 MiB of response data. Three protocols run concurrently; deployments run sequentially.

Provider charges may apply; these administrator diagnostics are not billed as user inference. The maintenance loop compares local configuration fingerprints, without scheduled upstream health calls. Model, address or credential changes trigger a new check. Price and display-name changes do not. If saving results fails, the current process suppresses repeated automatic paid attempts; manual retry remains available.

Temporary network errors, 429 and 5xx retain previous verification for an unchanged connection. Definite protocol or HTTP rejection removes the corresponding extra proof, without rewriting the administrator's original selected transport. Changed credentials immediately invalidate old proof. Concurrent edits discard stale results; conditional metadata-only persistence never overwrites other configuration or recreates deleted deployments. Stored diagnostics contain fixed statuses, stage, HTTP code, time and private fingerprints, with no bodies or credentials. Public results exclude fingerprints; client-submitted proof is ignored.

`POST /model/protocol_probe` accepts a saved deployment ID and requires a platform administrator. HTTP 200 means checks finished: inspect each protocol result. HTTP 404 means missing deployment, 409 means busy or changed configuration, and 503 means unavailable configuration or storage. Text-stream verification does not establish support for streamed tools, images, web search, background jobs or token counting. Only dialogue create routes are added.

The Playground and My Models selectors use actual backend bindings, compact protocol labels and a separate request path. Examples stay in English while all explanatory controls support English and Chinese; user input is preserved.

Tests use real HTTP, isolated PostgreSQL and strict local protocol servers. They cover authorization, automatic and manual checks, concurrent edits, persistence failures, credential rotation, billing and tool round trips. Browser tests exercise both languages, ten executable dialogue examples, narrow layouts and Chat passthrough. Commercial providers were not exercised by these isolated tests; actual saved connections must be checked separately by their administrator.
