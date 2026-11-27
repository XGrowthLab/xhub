# routing: Candidate filtering, model fallbacks and routing diagnostics

[中文](readme_cn.md) · [Parent architecture](../readme.md)

## Integration from other modules

Integration here means other Go packages in this repository. Go internal import rules restrict imports from outside the repository; HTTP clients use mounted gateway endpoints.

Filter disabled and incompatible candidates, then apply router settings and affinity validation. Classify failures with FallbackKind and continue only through the current template queue, rechecking authorization and budgets for each target.

| Entry point or resource | Usage and result |
| --- | --- |
| DropDisabled(pool) | Return an enabled slice and disabled count |
| FallbackKind(status, body) | Return general/context/content or empty |
| SelectAffinity / DiagnoseRoute | Validate preferences and explain missing candidates |

### Integration demo

Save the code as `demo/main.go` inside the repository and run `go run ./demo` from the repository root. Remove the demo directory after use.

```go
package main

import (
	"fmt"
	"github.com/sunqirui1987/xhub/internal/dataplane/routing"
)

// main 演示本页 API 的最小调用；无参数和返回值，打印上述预期结果，依赖与副作用见本页说明。
func main() {
	fmt.Println(routing.FallbackKind(429, nil))
	fmt.Println(routing.FallbackKind(400, []byte(`{"error":{"code":"context_length_exceeded"}}`)))
	fmt.Printf("%q\n", routing.FallbackKind(200, nil))
}
```

Prints general, context, and "". This demonstrates classification without issuing retries.

### Errors, state, and side effects

A required parent-response target cannot use ordinary cross-target fallback. Do not replay unknown task-creation outcomes blindly. A failure category does not guarantee a usable configured target.

## Responsibility

Candidate filtering, model fallbacks, and routing diagnostics. Initial routing prefers a healthy deployment or a healthy configured fallback. When neither is available, it can still select an eligible deployment in cooldown. Diagnostics retain the cooldown state.

## Review order

serve.go: DropDisabled → fallback.go → route_diagnostic.go: DiagnoseRoute

Read injected interfaces or input contracts first, then entry points, helpers and adjacent tests. Inspect parent callers when changing request lifecycle behavior.

## Dependencies

provider validates capabilities; router supplies policy/state; execution and previews share diagnostics.

## Verification

Adjacent tests: disabled_test.go, fallback_boundary_test.go, fallback_test.go, prefer_test.go, route_diagnostic_test.go

From repository root: go test ./internal/dataplane/routing -count=1

Real HTTP/persistence contracts belong in cmd/regression; browser → gateway → local upstream flows belong in frontend/e2e. Private schemas and temporary fixtures are cleaned up. Offline tests do not assume paid vendor credentials.
