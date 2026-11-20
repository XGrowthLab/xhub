# 旧版请求日志正文清理

管理员曾开启正文记录时，旧版可能在 `request_logs` 中同时保存请求、响应和代理请求。自个人正文功能上线后，新正文由独立的 `request_log_captures` 表按每行记录的到期时间清理。改变全局保留天数只影响之后的新正文。

## 先核对空间来源

用 PostgreSQL 的 `pg_total_relation_size` 核对大表以及各表的索引与 TOAST 开销，再单独检查 WAL、账单落盘队列、容器日志、对象存储和备份。续聊历史可能保存在旧 `kv` 或新的历史块与索引表内。清理旧请求日志不能减少续聊历史。普通 VACUUM 让 PostgreSQL 复用空间，已分配的磁盘文件可能不会缩小。备份副本仍按其既定轮换周期保留。

## 按批检查并执行

在 `XHUB_DATABASE_URL` 中配置连接到已完成升级的 PostgreSQL 的只读地址。令 `RETENTION_CUTOFF` 为固定的 RFC 3339 时间，至少早于当前时间 30 天。先在备份恢复或隔离实例测试。命令无需启动网关，也不创建表或索引。

```sh
go run ./cmd/log-body-retention --before="$RETENTION_CUTOFF" --limit=100
```

stdout 每批输出一行 JSON，包含检查及命中行数、确认提交的清理行数、旧请求、响应与代理正文未压缩字节量、需人工核对的诊断行数和进度。正文逻辑字节可用于估算，不能当作文件系统回收量。用 `--after` 传入上一批的 `next`，直到 `done: true`。命中为零时也继续走游标。完成一轮后重置游标，可以覆盖延迟写入的旧日期记录。

从零游标开始执行时，改用有更新权限的连接，沿用已检查的截止时间，在命令加 `--execute`。每批最多检查 500 条用量事件，整批在短事务中提交。遇到连接、锁或更新错误，命令失败，保留原游标供重试。大规模清理宜分段进行，观察账单写入延迟与 WAL 增量。

## 哪些字段会变

清理针对截止时间及以前且没有新正文状态的请求，只移除旧的请求、响应及代理请求副本，标记正文已到期。旧错误正文降为有界错误摘要。仅在上游诊断中保存旧错误副本时，只规范化可识别的内嵌错误并保留原正文状态。其他诊断属性、账单、用量、续聊历史与新正文记录保持原样。超过 64 KiB 或无法识别的旧诊断记录计入 `unchanged_diagnostics`，保留供独立核查。命令不输出正文、身份或连接信息。

## 完整诊断的无损压缩

维护工具以 `usage_events.id` 分页，只处理时间范围内的完整上游诊断。默认从 16 KiB 开始试压，保留原字节、原长度和 SHA-256。相同时间窗中的 Chat、Messages、Responses 和显式透传均可参与。错误文本继续用于搜索。详情读取、跨账号审计、采集有效期和异步任务的后续覆盖沿用网关原有流程。

```sh
go run ./cmd/diagnostic-pack plan --before="$CUTOFF" --rows=50
go run ./cmd/diagnostic-pack pack --execute --before="$CUTOFF" --rows=50 --backup-manifest="$VERIFIED_MANIFEST"
go run ./cmd/diagnostic-pack restore --execute --before="$CUTOFF" --rows=50
```

先让所有提供流量的实例支持编码读取，再在隔离环境验证。生产批次建立临时加密恢复点。诊断清单的第一列为请求 ID 的 SHA-256，第二列为旧诊断原始 UTF-8 字节的 SHA-256，按第一列升序。每次成功提交后用固定的 `through` 和上一批的 `next` 分别传给 `--through`、`--after`，以 `has_more=false` 结束。单批和单条默认上限为 16 MiB、8 MiB，过大的记录单列。保持截止秒和大小条件一致。转换读回数据库核验，在 `selected_stored_before` 和 `selected_stored_after` 报告已转换字段的实际存储大小。反向展开使用新的游标，恢复普通文本后才能回退到旧网关。

当帧损坏时，先认证并解密临时备份，在可信主机把单条原文通过标准输入传给 `diagnostic-pack recover --execute --event=… --backup-manifest=…`。命令锁定当前日志，核对原版本头、请求身份与独立清单。元数据冲突会保留当前记录供人工核对。
