# Docker 与版本发布

工作流：.github/workflows/release.yml。拉取请求执行验证；推送 main（或在 main 手动运行）通过验证后发布开发镜像；推送版本标签自动发布版本镜像并创建 GitHub Release。

工作流发布 Linux amd64 镜像 ghcr.io/xgrowthlab/xhub-gateway 和 ghcr.io/xgrowthlab/xhub-console。main 镜像带 main 和 sha 标签，正式版本带版本号、sha 和 latest，alpha、beta、rc 预发布不推进 latest。Release 包含自动生成的变更记录、两个固定镜像引用 docker-images.txt，以及记录镜像、提交和数据库兼容标识的 xhub-release.json。两份镜像都带 `io.xhub.database-compatibility` 标签。

启用仓库 Actions，允许工作流创建 Release、写入 Packages。使用自动提供的 GITHUB_TOKEN，无需 Docker Hub 密钥。如需匿名拉取，将首次创建的 GHCR 包设为公开；组织策略可能限制权限。

提交并推送配置后，从待发布提交创建标签：

    git tag v1.0.0
    git push origin v1.0.0

支持 vMAJOR.MINOR.PATCH 和 vMAJOR.MINOR.PATCH-alpha.N / beta.N / rc.N，数字无前导零。选择并推送标签即可触发发布。工作流在构建或推送前检查是否已有 Release。首次推送两份镜像后创建草稿，等附件齐全再公开。重跑时校验已有 Release，根据已验证的镜像摘要补齐缺失附件，保留已上传的附件和镜像标签。无法确认原镜像身份时，排查发布记录或创建新版本。

发布前运行内部 Go 单元测试、发布配置测试、构建辅助测试、真实 PostgreSQL 后台 regression，以及管理员登录、密钥轮换（旧密钥拒绝、新密钥推理成功、日志身份保持）的浏览器 E2E。E2E 使用本地上游和独立 schema，结束清理数据。release-test-results 附件保存浏览器 JSON、JUnit、HTML 和失败证据。

部署时将 Compose 中 gateway、console 的 image 替换为发布镜像，去掉 build，保留 PostgreSQL、Redis 和服务名 gateway。控制台使用 3000，网关使用 4000；远程部署设置 XHUB_PUBLIC_ORIGIN，使浏览器能访问网关公开地址。

    docker pull ghcr.io/xgrowthlab/xhub-gateway:1.0.0
    docker pull ghcr.io/xgrowthlab/xhub-console:1.0.0

真实 GHCR 写入和 Release 创建需在 GitHub 验证；本地夹具不会发布资源。现有本地 Dockerfile 和 deploy/build.sh 保持原用法，CI 控制台使用 deploy/Dockerfile.release 保留 Next.js standalone 目录层级。
记录回退镜像并核对独立数据库恢复点，按备份恢复证据安排轮换。[部署备份盘点与轮换](deployment-backup-rotation.md)
