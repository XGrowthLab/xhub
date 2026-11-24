"""在主机上运行 XHub 版本更新。接口仅监听 Unix socket，特权操作只针对固定 Compose 项目。"""

import argparse
from contextlib import contextmanager
import fcntl
from http.server import BaseHTTPRequestHandler
import ipaddress
import json
import logging
import os
from pathlib import Path
import re
import socketserver
import stat
import subprocess
from tempfile import NamedTemporaryFile
import threading
import time
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener
import uuid

if __package__:
    from .update_manifest import COMPAT_LABEL, MANIFEST_NAME, NAMESPACE, REPO, SERVICES, validate_manifest, version_key
else:
    from update_manifest import COMPAT_LABEL, MANIFEST_NAME, NAMESPACE, REPO, SERVICES, validate_manifest, version_key

ACTIVE = {"checking", "pulling", "switching", "recovering"}
CHANNELS = {"stable", "beta"}
CHECK_TTL = 3600
URLS = ("http://127.0.0.1:4000/health/readiness", "http://127.0.0.1:3000/login")
HEALTH_NETWORKS = tuple(ipaddress.ip_network(value) for value in ("127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"))
LOG = logging.getLogger("xhub-updater")


class UntrackedRelease(ValueError):
    """运行镜像不是可回退的发布版本。允许读取发布，但不能以此为起点切换。"""


def valid_health_url(url):
    """检查管理员传入的 HTTP 探针地址，只接受回环或私网 IPv4 字面量及合法端口。"""
    match = re.fullmatch(r"http://([0-9.]+):(\d{1,5})/[-\w/]*", url)
    if not match or not 1 <= int(match[2]) <= 65535:
        return False
    try:
        return any(ipaddress.IPv4Address(match[1]) in network for network in HEALTH_NETWORKS)
    except ipaddress.AddressValueError:
        return False


class TrustedRedirect(HTTPRedirectHandler):
    """允许 GitHub 官方发布资产跳转，禁止协议降级或向其他站点发送原请求凭据。"""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        """验证新地址后返回普通跳转请求；令牌由未转发请求头传入，不携带到重定向。"""
        target = urlsplit(newurl)
        if target.scheme != "https" or target.netloc not in {
            "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"
        }:
            raise ValueError("untrusted release redirect")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def read_json(stream, limit):
    """读取限长 JSON，拒绝超限内容；发布来源和 IPC 均使用这个边界。"""
    data = stream.read(limit + 1)
    if len(data) > limit:
        raise ValueError("JSON exceeds limit")
    return json.loads(data)


def save_json(path, value):
    """同步写入并原子替换状态/Compose 覆盖文件，保证进程中断后可读取旧值或新值。"""
    with NamedTemporaryFile(mode="w", dir=path.parent, delete=False, encoding="utf-8") as out:
        temporary = Path(out.name)
        try:
            json.dump(value, out, ensure_ascii=False)
            out.flush()
            os.fsync(out.fileno())
            os.replace(temporary, path)
            fd = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
        finally:
            temporary.unlink(missing_ok=True)


class Releases:
    """从固定 GitHub 项目选择有效发布，或由主机管理员提供离线清单。"""

    def __init__(self, beta=False, file=None, namespace=NAMESPACE):
        """保存主机配置；客户端不能指定发布站点或镜像仓库。"""
        self.beta, self.file, self.namespace = beta, file, namespace
        self.http = build_opener(TrustedRedirect())

    def github(self, path, asset=False):
        """读取固定 GitHub API 路径，不通过业务请求收取令牌或自定义请求头。"""
        accept = "application/octet-stream" if asset else "application/vnd.github+json"
        req = Request("https://api.github.com/repos/" + REPO + path, headers={
            "Accept": accept, "User-Agent": "XHub-Update-Agent", "X-GitHub-Api-Version": "2022-11-28",
        })
        if token := os.environ.get("XHUB_RELEASE_TOKEN"):
            req.add_unredirected_header("Authorization", "Bearer " + token)
        with self.http.open(req, timeout=12) as response:
            return read_json(response, 2 << 20 if not asset else 16 << 10)

    def latest(self):
        """返回最高且渠道允许的发布及纯文本说明；空仓库/没有可安装资产返回 None。"""
        if self.file:
            with self.file.open("rb") as stream:
                manifest = validate_manifest(read_json(stream, 16 << 10), self.namespace)
            return {"manifest": manifest, "notes": ""} if self.beta or version_key(manifest["version"])[3] == 3 else None
        allowed = []
        for row in self.github("/releases?per_page=30"):
            if row.get("draft"):
                continue
            try:
                key = version_key(row.get("tag_name"))
            except ValueError:
                continue
            if row.get("prerelease") != (key[3] < 3) or (key[3] < 3 and not self.beta):
                continue
            assets = [a["id"] for a in row.get("assets", []) if a.get("name") == MANIFEST_NAME and
                      a.get("state") == "uploaded" and type(a.get("id")) is int and a["id"] > 0]
            if len(assets) == 1:
                allowed.append((key, row, assets[0]))
        if not allowed:
            return None
        _, release, asset = max(allowed, key=lambda item: item[0])
        manifest = validate_manifest(self.github(f"/releases/assets/{asset}", asset=True), self.namespace)
        if manifest["version"] != release["tag_name"]:
            raise ValueError("release tag differs from manifest")
        return {"manifest": manifest, "notes": str(release.get("body") or "")[:6000]}


class Compose:
    """只读取现有服务并切换固定的 gateway、console 两份镜像。"""

    def __init__(self, file, overlay, project, namespace, health_urls, health_timeout=65):
        """固定项目文件、覆盖文件及健康地址，后续 IPC 请求不提供部署参数。"""
        self.file, self.overlay, self.project, self.namespace = file, overlay, project, namespace
        self.health_urls = health_urls
        self.health_timeout = health_timeout

    def run(self, *args, timeout=35):
        """运行固定 Docker 命令。操作失败仅交给状态机分类，不向控制台回显 Docker 输出。"""
        environment = {k: v for k, v in os.environ.items() if k != "XHUB_RELEASE_TOKEN"}
        result = subprocess.run(["docker", *args], cwd=self.file.parent, env=environment, check=True,
                                capture_output=True, text=True, timeout=timeout)
        return result.stdout.strip()

    def compose(self, *args, timeout=35):
        """固定项目目录、名称和 Compose 文件，覆盖文件仅在已应用后加入。"""
        prefix = ["compose", "--project-directory", str(self.file.parent), "-p", self.project, "-f", str(self.file)]
        if self.overlay.exists():
            prefix.extend(["-f", str(self.overlay)])
        return self.run(*prefix, *args, timeout=timeout)

    def image(self, name):
        """读取指定本地镜像标签与摘要，缺失或有歧义时拒绝继续。"""
        images = json.loads(self.run("image", "inspect", name))
        if len(images) != 1:
            raise ValueError("ambiguous local image")
        return images[0]

    def current(self):
        """检查两个运行容器是否属于同一发布并有可恢复摘要；否则需人工完成首装。"""
        releases, digests = {}, {}
        for service in SERVICES:
            ident = self.compose("ps", "-q", service)
            if not re.fullmatch(r"[0-9a-f]{12,64}", ident):
                raise ValueError("expected one running container")
            container = json.loads(self.run("inspect", ident))[0]
            if not container["State"]["Running"]:
                raise ValueError("service not running")
            image = self.image(container["Image"])
            releases[service] = self.release_labels(image["Config"].get("Labels"))
            prefix = f"{self.namespace}-{service}@sha256:"
            match = [x for x in image.get("RepoDigests") or [] if x.startswith(prefix)]
            # Compose 已固定摘要时优先采用其明确引用；标签部署必须有唯一能回退的摘要。
            reference = container["Config"]["Image"]
            if reference in match:
                digests[service] = reference
            elif len(match) == 1:
                digests[service] = match[0]
            else:
                raise UntrackedRelease("missing or ambiguous rollback image")
        version, revision, compatibility = releases["gateway"]
        if releases["console"] != (version, revision, compatibility):
            raise ValueError("application images do not belong to the same release")
        return validate_manifest({"schema": 1, "version": version, "revision": revision,
                                  "database_compatibility": compatibility, "images": digests}, self.namespace)

    @staticmethod
    def release_labels(labels):
        """把 Docker 标签对象转为版本、提交、兼容标识三元组；缺少或无效的发布标签抛 ValueError。"""
        if not isinstance(labels, dict):
            raise UntrackedRelease("image has no release labels")
        version, revision, compatibility = (labels.get(key) for key in (
            "org.opencontainers.image.version", "org.opencontainers.image.revision", COMPAT_LABEL))
        if not all(isinstance(value, str) and value for value in (version, revision, compatibility)):
            raise UntrackedRelease("image has incomplete release labels")
        version = version if version.startswith("v") else "v" + version
        try:
            version_key(version)
        except ValueError as error:
            raise UntrackedRelease("image was built outside a versioned release") from error
        return version, revision, compatibility

    def prepare(self, manifest, pull):
        """拉取并验证两份镜像与清单相符。完整通过前不修改任何 Compose 配置。"""
        expected = (manifest["version"], manifest["revision"], manifest["database_compatibility"])
        for reference in manifest["images"].values():
            if pull:
                self.run("pull", reference, timeout=300)
            image = self.image(reference)
            if reference not in (image.get("RepoDigests") or []) or self.release_labels(image["Config"].get("Labels")) != expected:
                raise ValueError("release image identity mismatch")

    def healthy(self, manifest):
        """检查给定清单对应的两个运行摘要和健康探针；切换及重启恢复调用，无 Compose 写入。"""
        no_redirect = build_opener(NoRedirect())
        try:
            for url in self.health_urls:
                with no_redirect.open(url, timeout=2) as response:
                    if response.status != 200:
                        return False
            return self.current() == manifest
        except (OSError, ValueError, KeyError, subprocess.SubprocessError):
            return False

    def switch(self, manifest):
        """持久化指定镜像并启动固定服务。保留数据库、Redis 和外部对象，给网关停机留出时间。"""
        save_json(self.overlay, {"services": {service: {"image": manifest["images"][service]} for service in SERVICES}})
        self.compose("up", "-d", "--no-deps", "--no-build", "--pull", "never", "--timeout", "80", *SERVICES, timeout=160)
        deadline = time.monotonic() + self.health_timeout
        while time.monotonic() < deadline:
            if self.healthy(manifest):
                return
            time.sleep(1)
        raise ValueError("updated services did not become healthy")


class NoRedirect(HTTPRedirectHandler):
    """健康检查只访问预先配置的回环或私网地址。"""

    def redirect_request(self, *args):
        """不跟随服务健康检查响应中的重定向地址。"""
        return None


class Agent:
    """管理单个升级或回退事务，并在 IPC 或进程退出后从持久状态恢复。"""

    def __init__(self, directory, compose, releases, channel_switch=False):
        """加载上次事务及渠道。仅当主机允许控制台切换时，已保存的渠道覆盖启动参数。"""
        self.file = directory / "state.json"
        self.compose, self.releases = compose, releases
        self.channel_switch = channel_switch
        self.lock = threading.RLock()
        self.state = {"stage": "idle", "updated_at": 0}
        try:
            if self.file.exists():
                with self.file.open("rb") as stream:
                    persisted = read_json(stream, 128 << 10)
                if not isinstance(persisted, dict) or persisted.get("stage") not in ACTIVE | {"idle", "failed", "succeeded"}:
                    raise ValueError("invalid saved update state")
                channel = persisted.get("channel")
                if channel is not None and channel not in CHANNELS:
                    raise ValueError("invalid saved release channel")
                self.state.update(persisted)
                if self.channel_switch and channel is not None:
                    self.releases.beta = channel == "beta"
        except Exception:
            # 文件不可信时保留原始证据，禁止后续更新覆盖它。
            LOG.exception("Unable to load updater state")
            self.state.update(stage="failed", error="recovery_required")
            return
        self.recover()
        try:
            current = compose.current()
        except UntrackedRelease:
            current = None
        except Exception:
            LOG.exception("Unable to inspect the running release")
            current = None
        try:
            self.record(current=current)
        except Exception:
            LOG.exception("Unable to persist the running release")
            self.state["current"] = current

    def record(self, **fields):
        """原子更新对象和磁盘；写盘成功前不发布状态，供工作线程与 IPC 共用。"""
        with self.lock:
            latest = {**self.state, **fields, "updated_at": int(time.time())}
            save_json(self.file, latest)
            self.state = latest

    def log_failure(self, step):
        """将当前异常及操作编号记入主机日志。浏览器只通过状态接口读取稳定错误码。"""
        operation = self.state.get("operation")
        operation_id = operation.get("id", "none") if isinstance(operation, dict) else "none"
        LOG.exception("Updater %s failed (operation=%s)", step, operation_id)

    def mark_failed(self, error, **fields):
        """结束忙状态并尝试持久化错误。磁盘写入失败时保留内存错误和已有回退记录。"""
        with self.lock:
            try:
                self.record(stage="failed", error=error, **fields)
            except Exception:
                self.log_failure("record failure")
                self.state = {**self.state, **fields, "stage": "failed", "error": error, "updated_at": int(time.time())}

    def recover(self):
        """启动时对照未完成事务、运行摘要与健康探针，确认成功或恢复原镜像，旧事务仍按原方式回退。"""
        pending = self.state.get("pending")
        if pending:
            try:
                prior = validate_manifest(pending["prior"], self.compose.namespace)
                target = validate_manifest(pending["target"], self.compose.namespace)
                action = pending.get("action")
                if action in {"apply", "rollback"} and self.compose.healthy(target):
                    self.complete(prior, target, action)
                else:
                    self.restore(prior, interrupted=True)
            except Exception:
                self.log_failure("recover")
                self.mark_failed("recovery_required")
        elif self.state["stage"] in ACTIVE:
            self.mark_failed("interrupted")

    def complete(self, prior, target, action):
        """健康切换后同时保存当前版本、回退点并清除事务。写入失败保留现场，避免回退已经健康的新版本。"""
        try:
            self.record(current=target, previous=prior if action == "apply" else None,
                        pending=None, stage="succeeded", error=None)
        except Exception:
            self.log_failure("record completion")
            self.mark_failed("recovery_required", current=target)

    def restore(self, prior, interrupted=False):
        """按已保存的摘要恢复并检查两项服务。失败保留 pending，禁止启动新升级。"""
        try:
            self.record(stage="recovering")
        except Exception:
            # 切换前的 pending 已经落盘；状态写入故障不应阻止试图恢复服务。
            self.log_failure("record recovery")
        try:
            self.compose.prepare(prior, pull=False)
            self.compose.switch(prior)
        except Exception:
            self.log_failure("restore")
            self.mark_failed("recovery_required")
            return
        try:
            self.record(current=prior, pending=None, stage="failed", error="interrupted" if interrupted else "rolled_back")
        except Exception:
            self.log_failure("record restoration")
            self.mark_failed("recovery_required", current=prior)

    def status(self):
        """返回浏览器所需版本与阶段，省略镜像、目录、原始命令和管理身份。"""
        with self.lock:
            state = self.state
            current, previous = state.get("current"), state.get("previous")
            checked = state.get("checked")
            candidate = None
            checked_at = state.get("checked_at", 0)
            if time.time() - checked_at > CHECK_TTL:
                checked_at = 0
            if checked and checked_at:
                manifest = checked["manifest"]
                key = version_key(manifest["version"])
                if (self.releases.beta or key[3] == 3) and (not current or key > version_key(current["version"])):
                    candidate = {"version": manifest["version"], "revision": manifest["revision"],
                                 "prerelease": version_key(manifest["version"])[3] < 3,
                                 "notes": checked["notes"], "compatible": bool(current and
                                     current["database_compatibility"] == manifest["database_compatibility"])}
            return {"enabled": True, "channel": "beta" if self.releases.beta else "stable",
                    "channel_switch_enabled": self.channel_switch, "stage": state["stage"],
                    "current": current["version"] if current else None, "candidate": candidate,
                    "previous": previous["version"] if previous else None, "error": state.get("error"),
                    "recovery_required": state.get("error") == "recovery_required",
                    "checked": checked_at, "updated_at": state["updated_at"]}

    def submit(self, action, body):
        """串行处理控制请求。渠道切换同步完成，检查与镜像切换落盘后由后台线程执行。"""
        with self.lock:
            if self.state["stage"] in ACTIVE or self.state.get("pending") or self.state.get("error") == "recovery_required":
                return 409, "busy"
            if action == "channel":
                return self.change_channel(body["channel"], body["actor"])
            current = self.state.get("current")
            if action == "apply":
                candidate = self.status()["candidate"]
                if (not candidate or not candidate["compatible"] or
                    (body.get("version"), body.get("revision")) != (candidate["version"], candidate["revision"])):
                    return 409, "invalid_target"
                target = self.state["checked"]["manifest"]
            elif action == "rollback":
                target = self.state.get("previous")
                if not target or not current or body.get("version") != target["version"]:
                    return 409, "invalid_target"
            else:
                target = None
            stage = "checking" if action == "check" else "pulling"
            fields = {"stage": stage, "error": None, "operation": {
                "id": uuid.uuid4().hex, "action": action, "actor": body["actor"]}}
            if action == "check":
                fields.update(checked=None, checked_at=0)
            try:
                self.record(**fields)
                thread = threading.Thread(target=self.execute, args=(action, current, target), daemon=True)
                thread.start()
            except Exception:
                self.log_failure("submit")
                self.mark_failed("check_failed" if action == "check" else "prepare_failed")
                return 503, "update_agent_unavailable"
            return 202, self.status()

    def change_channel(self, channel, actor):
        """在 submit 持锁时保存平台管理员选的渠道，清除旧检查结果；写盘失败继续使用原渠道。"""
        if not self.channel_switch:
            return 403, "channel_locked"
        if channel not in CHANNELS:
            return 400, "invalid_channel"
        if self.releases.beta == (channel == "beta"):
            return 200, self.status()
        try:
            self.record(channel=channel, channel_changed_by=actor, channel_changed_at=int(time.time()),
                        checked=None, checked_at=0)
        except Exception:
            self.log_failure("save release channel")
            return 503, "update_agent_unavailable"
        self.releases.beta = channel == "beta"
        return 200, self.status()

    def execute(self, action, expected, target):
        """后台检查发布或执行切换。失败只暴露稳定错误码，磁盘日志保留操作身份。"""
        try:
            try:
                current = self.compose.current()
            except UntrackedRelease:
                # 非发布镜像也能查阅更新信息，切换仍需要经过可回退的发布验证。
                if action != "check":
                    raise
                current = None
            if action == "check":
                self.record(current=current, checked=self.releases.latest(), checked_at=int(time.time()), stage="idle")
                return
            if current != expected or target["database_compatibility"] != current["database_compatibility"]:
                self.record(stage="failed", error="invalid_target")
                return
            self.compose.prepare(target, pull=action == "apply")
            self.compose.prepare(current, pull=False)
            # 拉取期间主机管理员可能换了容器。先复核，且回退记录必须能用于异常退出恢复。
            if self.compose.current() != current:
                self.record(stage="failed", error="invalid_target")
                return
            self.record(stage="switching", pending={"prior": current, "target": target, "action": action})
            self.compose.switch(target)
        except Exception:
            self.log_failure(action)
            if pending := self.state.get("pending"):
                self.restore(pending["prior"])
            else:
                self.mark_failed("check_failed" if action == "check" else "prepare_failed")
            return
        self.complete(current, target, action)


class Handler(BaseHTTPRequestHandler):
    """暴露固定的本地 HTTP 控制协议，不提供自定义命令、路径或镜像参数。"""

    def setup(self):
        """限制本地 IPC 客户端传输时长，避免半开请求占满进程。"""
        self.request.settimeout(8)
        super().setup()

    def log_message(self, *_):
        """禁用访问日志。操作身份和状态由事务状态文件记录。"""

    def reply(self, status, data):
        """编码 JSON 并结束请求；网关可区分冲突、失败和已接收。"""
        encoded = json.dumps(data, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self):
        """状态只在 /status 返回，其他请求不访问主机。"""
        if self.path == "/status":
            self.reply(200, self.server.agent.status())
        else:
            self.reply(404, {"error": "invalid_operation"})

    def do_POST(self):
        """严格限定操作、参数和调用方标识；提交前的错误没有副作用。"""
        action = self.path.lstrip("/")
        fields = {"check": {"actor"}, "apply": {"actor", "version", "revision"},
                  "rollback": {"actor", "version"}, "channel": {"actor", "channel"}}
        try:
            size = int(self.headers.get("Content-Length", "-1"))
            if action not in fields or not 0 <= size <= 1024:
                raise ValueError("invalid operation")
            body = json.loads(self.rfile.read(size))
            if (not isinstance(body, dict) or set(body) != fields[action] or
                not all(isinstance(v, str) for v in body.values()) or not re.fullmatch(r"[\w.@-]{1,128}", body["actor"])):
                raise ValueError("invalid target")
        except (ValueError, OSError):
            self.reply(400, {"error": "invalid_operation"})
            return
        status, result = self.server.agent.submit(action, body)
        self.reply(status, {"error": result} if isinstance(result, str) else result)


@contextmanager
def operation_lock(directory):
    """用持久目录的文件锁阻止第二个执行器同时操作同一项目。"""
    with (directory / "agent.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield


def main():
    """从主机参数启动专用 Unix socket。运行目录应仅对部署服务和网关开放。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--compose", type=Path, required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--state-dir", type=Path, required=True)
    parser.add_argument("--socket", type=Path, required=True)
    parser.add_argument("--allow-beta", action="store_true")
    parser.add_argument("--enable-channel-switch", action="store_true")
    parser.add_argument("--manifest-file", type=Path)
    parser.add_argument("--namespace", default=NAMESPACE)
    parser.add_argument("--health-url", action="append")
    parser.add_argument("--health-timeout", type=int, default=65)
    args = parser.parse_args()
    os.umask(0o077)
    health = args.health_url or URLS
    if not 5 <= args.health_timeout <= 300:
        parser.error("health timeout must be between 5 and 300 seconds")
    if len(health) != 2 or any(not valid_health_url(url) for url in health):
        parser.error("expected gateway and console health URLs on loopback or private IPv4")
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]{0,63}", args.project):
        parser.error("invalid Compose project")
    if not re.fullmatch(r"[a-z0-9.:/-]+/[a-z0-9_-]+", args.namespace):
        parser.error("invalid image namespace")
    args.state_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    args.socket.parent.mkdir(mode=0o750, parents=True, exist_ok=True)
    for directory in (args.state_dir, args.socket.parent):
        metadata = directory.lstat()
        if not stat.S_ISDIR(metadata.st_mode) or metadata.st_uid != os.geteuid() or metadata.st_mode & 0o022:
            parser.error("update directories must be owned by the executor and protected from writes")
    with operation_lock(args.state_dir):
        if args.socket.exists():
            if not stat.S_ISSOCK(args.socket.lstat().st_mode):
                parser.error("socket path already in use")
            args.socket.unlink()
        compose = Compose(args.compose.resolve(), args.state_dir / "compose.override.json",
                          args.project, args.namespace, health, args.health_timeout)
        # 先恢复上次事务，再接受客户端请求，避免启动恢复期间的 IPC 连接超时积压。
        agent = Agent(args.state_dir, compose, Releases(args.allow_beta, args.manifest_file, args.namespace),
                      args.enable_channel_switch)
        with socketserver.ThreadingUnixStreamServer(str(args.socket), Handler) as server:
            server.daemon_threads = True
            os.chmod(args.socket, 0o660)
            server.agent = agent
            try:
                server.serve_forever()
            finally:
                args.socket.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
