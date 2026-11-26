# 控制台更新与回退

平台管理员打开「管理员设置 → 版本更新」检查发布版本。标准安装可在同一页面切换正式和测试版渠道。切换后重新检查，可以看到所选渠道的候选版本。确认升级后，独立主机执行器校验并切换网关及控制台镜像。刷新页面可继续查看任务。

「当前版本」优先显示可回退的发布版本。运行镜像只有一致的构建提交时，显示该提交，例如 `main · e107582`。此时可以查看渠道最新发布，建立成套发布基线后才可通过控制台升级。

## 正式与测试发布

从已合入、完成检查的提交创建标签。正式版本以三段版本号命名，例如 v0.1.0。首个测试发布使用 v0.1.0-beta.1。后续测试版使用 beta.2、beta.3 等，供已开启测试渠道的实例验证。

标签触发既有完整验证及两份镜像构建。通过后 GitHub Release 附带 docker-images.txt 和 xhub-release.json。后者绑定提交、镜像摘要和保守的数据库兼容标识。预发布不会更新 latest。工作流重跑沿用已上传的清单，需要变更时使用新版本号。

## 主机启用

标准 Linux 安装会默认启用 systemd 执行器。源码构建的实例可以检查版本。界面切换要求成套发布镜像及 GHCR 读取权限，以便验证目标和回退镜像。参见[安装指南](../../getting-started.zh-CN.md#在-linux-安装并启用版本检查)。

已有部署或自定义 Compose 可按下列步骤接入执行器：

1. 将 deploy/update_agent.py 与 deploy/update_manifest.py 安装在同一只允许主机管理员修改的目录，例如 /opt/xhub-updater。
2. 拷贝 deploy/xhub-updater.service.example 为自己的 systemd 服务并校对 Compose 绝对路径、项目名和健康地址。默认从 XGrowthLab/xhub 读取发布。私有仓库可在受保护的 EnvironmentFile 中配置只读 XHUB_RELEASE_TOKEN。镜像仓库凭据由主机 Docker 管理。
3. 网关设置 XHUB_UPDATE_SOCKET=/run/xhub-updater/agent.sock，并将 /run/xhub-updater 目录只读挂载到容器同一路径。挂载目录可在进程重启后重新连接新的套接字。目录与套接字的访问权限需允许网关进程。
4. 启动主机服务并按一次性部署步骤重建网关。默认正式渠道，`--allow-beta` 为新实例选择测试版渠道。已有主机可在 ExecStart 加 `--enable-channel-switch`，重新加载 systemd 配置和重启执行器后允许控制台保存渠道。标准安装已包含此参数。移除此参数可让渠道固定由主机决定。私有镜像源或离线场景可由主机管理员提供 --namespace 及 --manifest-file。

默认健康地址为 http://127.0.0.1:4000/health/readiness 和 http://127.0.0.1:3000/login。本机端口或私网 IPv4 绑定不一致时，分别通过两个 --health-url 参数指定。管理接口只访问这个 Unix socket，Docker 权限留在执行器。项目下应只有一个网关和一个控制台实例。

## 切换与恢复

执行器在下载和校验两份镜像后，给网关停机时的在途 HTTP 与 SSE 留出最多一分钟。单网关切换期间可能短暂不可用。执行器使用 Compose 的 --no-deps，只替换两个应用服务。新版本启动或健康检查失败时尝试恢复原镜像。执行器重启时核验实际镜像摘要和服务健康，确认目标版本健康后会补记升级结果，其余未完成切换会进入恢复。需要运维处理时，执行器保留事务记录并拒绝新操作。可在主机服务日志中按操作编号查看失败原因。

标准安装使用 /opt/xhub/docker-compose.yml。源码构建更新时，重新构建镜像后用该文件重建应用服务。首次从界面切换镜像后，执行器会维护状态目录中的 compose.override.json。启用执行器后，人工运行 Compose 时应在原文件之后添加该覆盖文件，保持应用与指定摘要一致。不要删除上一版本镜像，直到不再需要该回退点。state.json 保留事务、操作者和最近回退依据，按实例运维权限保管。

控制台升级要求两个版本使用相同的数据库兼容标识。发布 CI 根据建表和迁移 SQL、Go 迁移源码、ORM 建表定义、启动代码、存储格式和依赖锁来计算标识。普通日志查询的修改可以沿用现有标识。新增迁移放在 `migrations` 目录或按 `*_migration.go` 命名。更改上述指纹范围以外的不兼容持久化格式时，维护者须同步递增 `deploy/update_manifest.py` 中的 `DATABASE_FORMAT_VERSION`。兼容标识实际变化时，通过主机发布流程升级。数据库备份和恢复见[部署备份盘点与轮换](deployment-backup-rotation.md)。

### 从 beta.3 或 beta.4 升级

修复版沿用 `beta.3` 的兼容标识。`beta.3` 实例在测试版渠道检查修复版后，可以直接点击「立即更新」。已经运行 `beta.4` 且仍使用旧版主机执行器的实例，需先刷新一次执行器。管理员在确认更新器空闲后，从修复版的源码目录运行以下命令。自定义安装使用 systemd 单元 `ExecStart` 中的脚本目录。

```sh
updater_dir=/opt/xhub/updater
sudo install -m 0644 deploy/update_manifest.py deploy/update_agent.py "$updater_dir/" &&
  sudo systemctl restart xhub-updater.service
```

刷新执行器保留现有镜像、Compose 配置和数据库。重启完成后，在控制台重新检查更新即可。
