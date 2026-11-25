# XHub 安装与运行指南

[English](getting-started.md) | **简体中文** · [项目首页](../README.zh-CN.md)

XHub 包含两个服务：Go API 网关默认监听 `4000`，Next.js 控制台默认监听 `3000`。PostgreSQL 保存配置、身份和用量。源码运行可不使用 Redis，Docker Compose 栈包含 Redis。

## 环境要求

| 依赖 | 版本 | 用途 |
| --- | --- | --- |
| Go | 1.25 | 构建与运行网关 |
| Node.js | ≥24.14.1 | 构建与运行控制台 |
| npm | ≥11.10.0 | 安装控制台依赖 |
| PostgreSQL | 推荐 16 | 必需的持久存储 |
| Redis | 推荐 7 | 可选的共享运行状态 |
| Docker Compose | Docker 部署时需要 | 运行本地服务栈 |

使用**全新的 PostgreSQL 数据库**。

## Docker Compose 部署

[![Docker 部署示意：控制台 3000、网关 4000、PostgreSQL 与 Redis](assets/guide-docker.zh-CN.svg)](assets/guide-docker.zh-CN.svg)

*图 1：部署入口与各服务职责。浏览器打开控制台，应用直接请求网关；连接关系以正文配置为准。*

### 在 Linux 安装并启用版本检查

使用带 systemd 的 Linux 主机和 Python 3.10 或更高版本，同时准备上表列出的构建工具与 Docker。

```bash
git clone https://github.com/qiniu/xhub.git
cd xhub
bash deploy/build.sh
sudo python3 -B deploy/install.py
```

安装命令启动本地构建的网关、控制台及 PostgreSQL 和 Redis，将部署配置保存到 `/opt/xhub`，并启动 systemd 更新执行器。发布端口默认仅监听本机地址。对外开放前请修改初始管理员密码。

打开「管理员设置 → 版本更新」即可检查发布。默认使用正式版渠道。界面一键切换要求有成套发布镜像和镜像仓库访问权限。需要从 x86-64 发布镜像起步时，在新服务器上给 root Docker 登录 GHCR，再运行 `sudo python3 -B deploy/install.py --release`。仓库只有预发布时，`--release` 使用最新预发布镜像首装。需要持续接收预发布时，在安装时加 `--beta`。已有部署与自定义环境见[版本更新指南](development/zh-CN/version-updates.md)。

### 源码构建 Compose 镜像

在 Docker Desktop 或本地开发环境中，可直接运行源码构建的 Compose 服务。Linux 上也可以用前面的安装命令同时启动更新执行器。

```bash
git clone https://github.com/qiniu/xhub.git
cd xhub
bash deploy/build.sh
docker compose up -d
```

构建脚本需要 Go、Node.js、npm、Docker，以及可读取的系统 CA bundle。脚本会检测 `/etc/ssl/certs/ca-certificates.crt`（Debian/Ubuntu）、`/etc/pki/tls/certs/ca-bundle.crt`（RHEL 系）和 `/etc/ssl/cert.pem`（macOS/Alpine）；其它位置可通过 `XHUB_CA_CERT_FILE` 指定。脚本从 npmmirror 下载 Linux Node.js，目标架构跟随主机（amd64 或 arm64）。运行镜像只包含预构建产物，不在容器内编译。控制台使用 `postgres:16` 作为提供 glibc 的运行基础镜像；Docker 需要能获取 `postgres:16` 和 `redis:7-alpine`。

| 服务 | 地址 |
| --- | --- |
| 控制台登录 | [localhost:3000/login](http://localhost:3000/login) |
| 网关 API | [localhost:4000](http://localhost:4000) |
| 主机访问 PostgreSQL | `127.0.0.1:5433` |

本地初始管理员为 `admin@xhub.local` / `admin-pass-1234`。对外开放前修改密码。仅在该邮箱尚无账号时创建初始管理员，修改配置里的密码**不会**重置已有账号；已有账号通过控制台修改密码。

## 从源码运行

### 1. 启动 PostgreSQL

可使用仓库中的 Compose 数据库：

```bash
docker compose up -d postgres
cp configs/config.example.yaml configs/config.yaml
```

把 `configs/config.yaml` 中的 `general_settings` 替换为以下配置，保留 `model_list`：

```yaml
general_settings:
  database_url: postgres://xhub:xhub_dev_password@127.0.0.1:5433/xhub?sslmode=disable
  redis_url: ""
  store_model_in_db: true
  admin_email: os.environ/XHUB_ADMIN_EMAIL
  admin_password: os.environ/XHUB_ADMIN_PASSWORD
  disable_env_credential_login: false
```

也可以连接自己的全新 PostgreSQL 数据库。示例模板使用端口 `5432`、密码 `xhub`；Compose 数据库使用主机端口 `5433`、密码 `xhub_dev_password`。

`redis_url` 为空时关闭 Redis。启用时填写网关进程可访问的地址；Compose Redis 默认没有向主机发布端口。

### 2. 启动网关

在运行网关的同一终端设置初始管理员：

```bash
export XHUB_ADMIN_EMAIL="admin@example.com"
export XHUB_ADMIN_PASSWORD="replace-with-a-strong-password"
make run
```

网关读取 `configs/config.yaml`，监听 `4000`。缺失环境变量解析为空字符串，未提供上述凭证时不会创建初始账号。账号已存在后，通过控制台修改密码。

### 3. 启动控制台

在另一终端进入仓库根目录运行：

```bash
make ui
```

打开 [localhost:3000/login](http://localhost:3000/login)，使用上面设置的账号登录。

## 接入模型并完成首次调用


新安装没有已配置的供应商或部署。

[![首次配置流程：接入模型、配置工作空间、发放虚拟密钥、调用与核对](assets/guide-setup.zh-CN.svg)](assets/guide-setup.zh-CN.svg)

*图 2：第一次配置的推荐顺序。完成团队成员关系和密钥授权后再测试调用。*

1. 打开“模型与端点”，选择供应商类型并填写对应连接与认证字段。
2. 手动或从供应商目录添加部署，设置公开模型名、上游模型及端点类型。
3. 创建组织和团队，配置团队模型范围并添加成员。独立用户也可以使用个人密钥；个人额度留空时不设金额上限；项目按需创建。
4. 创建个人或服务虚拟密钥，在 Playground 测试模型，再从应用调用。

[![模型与端点控制台：供应商标签、添加模型按钮和部署列表](assets/console-deployments.png)](assets/console-deployments.png)

*图 3：模型与端点页面。先在 Providers 标签配置凭证，再通过 Add Model 添加部署，核对公开模型名、上游模型及端点能力。点击截图可打开原图；示例中的模型和价格不代表新安装的默认配置。*

完整操作与权限说明见[客户使用与管理手册](user-guide.zh-CN.md)。通过 `pip install openai` 安装客户端，再使用虚拟密钥和公开模型名：

```python
from openai import OpenAI

client = OpenAI(
    api_key="YOUR_XHUB_VIRTUAL_KEY",
    base_url="http://localhost:4000/v1",
)
response = client.chat.completions.create(
    model="YOUR_PUBLIC_MODEL_NAME",
    messages=[{"role": "user", "content": "Hello, XHub!"}],
)
print(response.choices[0].message.content)
```

客户端连接网关地址，不能连接控制台地址。

## 部署配置

- 网关设置 `XHUB_PUBLIC_ORIGIN` 为公开 API 地址，例如 `https://api.example.com`。
- 浏览器使用其他网关地址时，在**构建控制台前**设置 `NEXT_PUBLIC_BASE_URL`。
- 控制台设置 `XHUB_GATEWAY_ORIGIN` 用于服务端访问网关，地址需要从控制台进程可达。
- 配置中的 `os.environ/变量名` 可从环境读取密钥。Docker 部署修改 `configs/config.docker.yaml` 后，为网关服务传入对应环境变量并重新构建镜像。
- 可选 `master_key` 用于初始化和应急访问，不能作为控制台登录凭证或普通应用密钥。

平台、组织、团队和成员范围见[权限参考](development/zh-CN/permissions.md)。

## 开发与验证

从[开发与 AI 编程指南](development/zh-CN/README.md)定位模块，再按修改选择[测试指南](development/zh-CN/testing.md)中的检查。Go 基础检查与模拟上游回归：

```bash
make test
make regression
```

数据库不可达时部分测试会跳过。启动本地 PostgreSQL 或设置 `XHUB_TEST_DATABASE_URL` 后检查测试输出；测试使用独立 schema。前端按受影响文件定向验证，避免无路径运行整个套件。

浏览器测试需要先安装 Chromium（`cd frontend && npx playwright install chromium`），再从根目录运行 `make e2e-offline`，使用隔离数据和本地供应商夹具。make e2e 验证已准备的共享基线，并可能调用付费供应商；端口与服务要求见测试指南。`make regression-live` 会调用真实供应商并产生费用，需要另行配置凭证并确认运行范围。

## 文档与能力边界

客户操作见[客户手册](user-guide.zh-CN.md)，实现规则见[开发参考](development/zh-CN/README.md)，模块内的 `readme.md` / `readme_cn.md` 提供双语代码说明。文档记录当前行为，临时方案与执行记录不作为使用承诺。


供应商均由管理员显式创建：选择提供商后填写对应凭据字段。手动价格刷新及已启用的定时刷新默认读取 https://api.modelink.ai/v1/market/models；XHUB_PRICE_FEED_URL 可指定其他源。刷新失败保留当前目录，人工覆盖独立管理。
