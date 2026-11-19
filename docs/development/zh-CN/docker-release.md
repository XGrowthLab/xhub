# Docker 与版本发布

工作流：.github/workflows/release.yml。拉取请求执行验证；推送 main（或在 main 手动运行）通过验证后发布开发镜像；推送版本标签自动发布版本镜像并创建 GitHub Release。

镜像：ghcr.io/qiniu/xhub-gateway 和 ghcr.io/qiniu/xhub-console，支持 Linux amd64。main 构建带 main、sha 标签；正式版本带版本号、sha、latest 标签；alpha、beta、rc 预发布不修改 latest。Release 自动生成变更记录，附件 docker-images.txt 保存两个镜像的不可变 digest。

启用仓库 Actions，允许工作流创建 Release、写入 Packages。使用自动提供的 GITHUB_TOKEN，无需 Docker Hub 密钥。如需匿名拉取，将首次创建的 GHCR 包设为公开；组织策略可能限制权限。

提交并推送配置后，从待发布提交创建标签：

    git tag v1.0.0
    git push origin v1.0.0

支持 vMAJOR.MINOR.PATCH 和 vMAJOR.MINOR.PATCH-alpha.N / beta.N / rc.N；数字无前导零。创建标签触发发布，工作流不自动选择下一个版本号。两个镜像推送成功才创建 Release；重跑更新 digest 附件。

发布前运行内部 Go 单元测试、发布配置测试、构建辅助测试、真实 PostgreSQL 后台 regression，以及管理员登录、密钥轮换（旧密钥拒绝、新密钥推理成功、日志身份保持）的浏览器 E2E。E2E 使用本地上游和独立 schema，结束清理数据。release-test-results 附件保存浏览器 JSON、JUnit、HTML 和失败证据。

部署时将 Compose 中 gateway、console 的 image 替换为发布镜像，去掉 build，保留 PostgreSQL、Redis 和服务名 gateway。控制台使用 3000，网关使用 4000；远程部署设置 XHUB_PUBLIC_ORIGIN，使浏览器能访问网关公开地址。

    docker pull ghcr.io/qiniu/xhub-gateway:1.0.0
    docker pull ghcr.io/qiniu/xhub-console:1.0.0

真实 GHCR 写入和 Release 创建需在 GitHub 验证；本地夹具不会发布资源。现有本地 Dockerfile 和 deploy/build.sh 保持原用法，CI 控制台使用 deploy/Dockerfile.release 保留 Next.js standalone 目录层级。
记录回退镜像并核对独立数据库恢复点，按备份恢复证据安排轮换。[部署备份盘点与轮换](deployment-backup-rotation.md)
