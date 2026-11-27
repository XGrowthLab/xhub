# routing：候选筛选、模型回退与路由诊断

[English](readme.md) · [上层架构](../readme_cn.md)

## 如何由其他模块接入

这里的“对外”指仓库内其他 Go 包。Go 的 `internal` 规则限制了仓库外的直接导入；HTTP 客户端使用已挂载的网关接口。

执行器先排除停用和能力不兼容候选，再应用 router 快照与 affinity 验证。失败时用 FallbackKind 分类，在当前模板允许的队列中继续，每个新目标重新检查权限和预算。

| 入口或资源 | 调用方式与返回结果 |
| --- | --- |
| DropDisabled(pool) | 返回有效切片与停用数 |
| FallbackKind(status, body) | 返回 general/context/content 或空串 |
| SelectAffinity / DiagnoseRoute | 验证偏好和解释无候选原因 |

### 接入 demo

将下面代码保存为仓库内的 `demo/main.go`，在仓库根目录执行 `go run ./demo`；运行后删除本次创建的 demo 目录。

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

输出 general、context、""；这里只演示错误分类，不发送重试。

### 错误、状态与副作用

固定父响应目标不能进入普通跨目标回退；未知创建结果不能盲目重放。分类允许回退不等于模板里存在可用目标。

## 职责

负责候选筛选、模型回退与路由诊断。首轮优先选择健康部署或健康的配置回退目标；健康候选用尽时仍可选择处于冷却的合规部署。冷却状态保留用于路由诊断。

## Review 顺序

serve.go: DropDisabled → fallback.go → route_diagnostic.go: DiagnoseRoute

先读消费方接口或输入契约，再沿入口阅读辅助函数及旁边的单元测试；涉及请求生命周期时检查上层调用方。

## 依赖

provider 验证能力，router 提供策略与状态；execution 和路由预览复用诊断。

## 验证

同目录测试：disabled_test.go, fallback_boundary_test.go, fallback_test.go, prefer_test.go, route_diagnostic_test.go

仓库根目录执行：go test ./internal/dataplane/routing -count=1

真实 HTTP/持久化契约放 cmd/regression；浏览器 → 网关 → 本地上游流程放 frontend/e2e。测试清理私有 schema 与临时夹具；离线测试不假设有付费供应商凭据。
