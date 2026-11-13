# 模型广场与部署价格

模型广场每天从 Modelink 同步资料。来源不再返回的条目标记为「来源下架」，完整资料仍保留；其他供应商可能继续提供这个模型。下架不会停用已有部署，也不会删除消费记录。

## 同步与调价

- 默认每 24 小时同步一次，首次启用一分钟内执行；已保存的间隔或关闭状态保持不变。管理员可以立即同步或调整计划。
- 更新名称、说明、模态、能力、上下文、文档和完整价格规则。同步失败保留上次成功快照，记录失败状态，启用中的计划一小时后重试。
- 市场快照与已接受的计费基线分开存储。每日同步、服务重启都不自动修改部署价格。
- 「部署价格更新」逐项展示当前价和市场候选，默认不选择任何部署；可以单个核对，也可以勾选后批量核对。手工价有覆盖提示。市场报价不等于供应商合同价，管理员应自行确认。
- 提交只携带部署 ID 和预览版本，后台重新计算金额；配置或价格变化返回 409，整批回滚。成功后保存部署费率；既有账单不重算。

快捷调价支持可完整表达的单档文字 Token 费率、缓存读写、完整高峰/低谷费率和明确的搜索按次费率。多档上下文价、模态/推理条件价、未知计价项、缺价或不完整时段会显示「需人工配置」，不会取第一档、猜测条件或当成免费。来源仍完整展示这些价格。

新建可支持的目录定价部署时保存当前市场费率。已有复杂目录保持原创建契约；新来源复杂型号必须人工配置。YAML 部署由配置文件维护，不纳入数据库批量更新。当前生效机制与已有部署编辑一致：更新当前网关实例的内存目录；多副本跨实例配置广播不在本次范围内。

## 管理接口

均使用现有管理员会话权限；普通用户或虚拟密钥不能调价。

- `GET /price/catalog`：完整来源资料、下架标记、本地人工覆盖；`pricing_rules_v2` 保留来源完整规则。
- `POST /reload/model_cost_map`：立即同步资料。
- `GET /schedule/model_cost_map_reload/status`：计划、上次尝试/成功时间和失败状态。
- `POST /schedule/model_cost_map_reload?hours=24` / `DELETE /schedule/model_cost_map_reload`：保存/关闭计划。
- `GET /price/deployments`：当前价、候选、是否改变、不可应用原因和版本。
- `POST /price/deployments/apply`：确认单个或一批，最多 500 个；无效输入 400、越权 401/403、版本冲突 409、存储失败 500。

~~~json
{"items":[{"id":"deployment-id","version":"preview-version"}]}
~~~

## 存储与兼容

| 记录 | 存储键 |
| --- | --- |
| 每日市场资料 | `market_snapshot/catalog` |
| 已接受计费基线 | `price_snapshot/catalog` |
| 历史人工覆盖 | `price_model/<model ID>` |
| 本地隐藏状态 | `price_listing/<model ID>` |
| 已确认部署费率 | `proxy_models.litellm_params_json.rates` |
| 同步计划和状态 | `model_cost_map/reload` |

不迁移或删除旧表。首次成功同步固化旧基线，之后不再自动覆盖；已有人工目录、供应商和隐藏状态保留。默认源为 `https://api.modelink.ai/v1/market/models`，可用 `XHUB_PRICE_FEED_URL` 指定镜像。数据库事务锁串行同步、取消计划和调价，防止慢来源覆盖新结果。

回滚到旧程序之前应关闭同步计划：旧程序仍可能自动替换旧计费基线。已确认部署费率采用已有 `rates` 格式，账单仍保留原快照。本次不增加站内信、不更改权限、预算或覆盖率门禁。

## 验证

~~~bash
go test ./internal/catalog ./internal/gateway/models ./internal/store -count=1
go test ./cmd/regression -run '^TestModelContractMarketPriceConfirmation$' -count=1
bash scripts/e2e.sh e2e/scenarios/legacy/price-feed/sc-111.test.ts
~~~

真实 PostgreSQL、本地来源和协议上游验证：同步不调价、单个/批量确认后的实际费用、旧版本整批回滚、历史账单、下架/恢复、失败保留、重启和中英文操作。测试不使用供应商真实凭据；不会用市场价推断某个供应商的合同价格。
