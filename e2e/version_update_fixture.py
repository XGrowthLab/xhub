"""为 Playwright 创建独立真实 Compose 工程、本地镜像仓库和主机执行器。"""

from contextlib import ExitStack
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
from tempfile import TemporaryDirectory
import time
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parent.parent
REGISTRY = os.environ.get("E2E_UPDATE_REGISTRY", "ghcr.io/distribution/distribution:3.0.0")
NGINX = "nginx:1.28-alpine"


def run(*args, timeout=180):
    """执行测试 Docker 操作，失败保留脱敏标准错误供本地排查。"""
    result = subprocess.run(args, text=True, capture_output=True, timeout=timeout, cwd=ROOT)
    if result.returncode:
        raise RuntimeError(f"{' '.join(args[:3])}: {result.stderr[-1800:]}")
    return result.stdout.strip()


def image(name):
    """预取公开基础镜像，已有本机依赖可复用，无需 GitHub Release 凭据。"""
    if subprocess.run(["docker", "image", "inspect", name], capture_output=True).returncode:
        run("docker", "pull", name)


def wait_for(operation, timeout=40):
    """等待确定性本地依赖，失败报告最后一次操作原因。"""
    until = time.monotonic() + timeout
    last = None
    while time.monotonic() < until:
        try:
            if value := operation():
                return value
        except (OSError, RuntimeError) as error:
            last = error
        time.sleep(.2)
    raise RuntimeError(f"fixture did not start: {last}")


def container_port(name, port):
    """读取当前测试容器的回环发布端口，拒绝默认路由到其他容器。"""
    value = run("docker", "port", name, f"{port}/tcp")
    if not re.fullmatch(r"127\.0\.0\.1:\d+", value):
        raise RuntimeError("fixture port is not bound to loopback")
    return value


def endpoint(url):
    """访问实际 Compose 服务，返回页面版本字符串供断言。"""
    with urlopen(url, timeout=2) as response:
        return response.read(1024).decode().strip()


def make_release(directory, registry, variant, resources):
    """创建两份带版本标签的实际测试镜像。提交至临时 Registry，转为真实已拉取摘要。"""
    release = f"v0.1.0-beta.{variant}"
    revision = str(variant) * 40
    compatibility = ("b" if variant == 4 else "a") * 64
    images = {}
    for service in ("gateway", "console"):
        config = directory / "nginx.conf"
        status = 503 if variant == 3 and service == "gateway" else 200
        config.write_text(f'server {{ listen 80; location / {{ return {status} "{release}"; }} }}\n')
        tag = f"{registry}/xhub-{service}:test-{variant}"
        with ExitStack() as stack:
            container = run("docker", "create", NGINX)
            stack.callback(run, "docker", "rm", "-v", container)
            run("docker", "cp", str(config), f"{container}:/etc/nginx/conf.d/default.conf")
            run("docker", "commit", "--change", f"LABEL org.opencontainers.image.version={release}",
                "--change", f"LABEL org.opencontainers.image.revision={revision}",
                "--change", f"LABEL io.xhub.database-compatibility={compatibility}", container, tag)
        resources.callback(run, "docker", "image", "rm", tag)
        result = run("docker", "push", tag)
        match = re.search(r"\bdigest: (sha256:[0-9a-f]{64})", result)
        if not match:
            raise RuntimeError("test release push returned no digest")
        reference = f"{registry}/xhub-{service}@{match[1]}"
        run("docker", "pull", reference)
        images[service] = reference
    return {"schema": 1, "version": release, "revision": revision, "database_compatibility": compatibility, "images": images}


def write_json(path, value):
    """把合成发布清单写入固定测试目录，浏览器在明确定义的步骤切换夹具。"""
    path.write_text(json.dumps(value) + "\n")


def stop(process):
    """关闭此次启动的执行器，必要时强制停止，确保套接字和文件句柄均已释放。"""
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def main():
    """运行真实 Compose 更新、权限隔离和双语 Playwright 流程；异常时只清理本次生成的资源。"""
    for dependency in (REGISTRY, NGINX, "postgres:16", "redis:7-alpine"):
        image(dependency)
    with TemporaryDirectory(prefix="xhub-upd-", dir="/tmp") as local, ExitStack() as resources:
        directory = Path(local)
        project = "xhub-upd-" + secrets.token_hex(4)
        registry_name = project + "-registry"
        run("docker", "run", "-d", "--name", registry_name, "-p", "127.0.0.1::5000", REGISTRY)
        resources.callback(run, "docker", "rm", "-fv", registry_name)
        registry_host = container_port(registry_name, 5000)
        wait_for(lambda: endpoint("http://" + registry_host + "/v2/") == "{}")
        registry = "localhost:" + registry_host.rsplit(":", 1)[1]
        print("Preparing versioned Compose images in a private test registry", flush=True)
        base, update, broken, incompatible = [make_release(directory, registry, v, resources) for v in range(1, 5)]
        feed = directory / "release.json"
        write_json(feed, update)
        for target, name in ((broken, "broken"), (incompatible, "incompatible")):
            write_json(directory / (name + ".json"), target)
        compose = directory / "compose.json"
        write_json(compose, {"services": {
            **{service: {"image": reference, "ports": ["127.0.0.1::80"]} for service, reference in base["images"].items()},
            "postgres": {"image": "postgres:16", "environment": {"POSTGRES_PASSWORD": "fixture-only"}},
            "redis": {"image": "redis:7-alpine"},
        }})
        compose_args = ("docker", "compose", "-p", project, "-f", str(compose))
        resources.callback(run, *compose_args, "down", "-v")
        # 明确分配固定回环端口，否则容器重建后随机端口会改变，旧健康检查地址不再有效。
        write_json(compose, {"services": publish_fixed_ports(json.loads(compose.read_text())["services"])})
        run(*compose_args, "up", "-d")
        ids = {service: run(*compose_args, "ps", "-q", service) for service in ("gateway", "console", "postgres", "redis")}
        # 初始化临时进程只监听 Unix socket；等待实际监听 TCP 的数据库，避免初始化完成前误判就绪。
        postgres = ("docker", "exec", "-e", "PGPASSWORD=fixture-only", ids["postgres"],
                    "psql", "-h", "127.0.0.1", "-qXAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres")
        redis = ("docker", "exec", ids["redis"], "redis-cli", "--raw")
        wait_for(lambda: run(*postgres, "-c", "SELECT 1;"))
        run(*postgres, "-c", "CREATE TABLE rollout_marker (label text); INSERT INTO rollout_marker VALUES ('original');")
        run(*redis, "SET", "rollout-marker", "original")
        gateway_url = "http://" + container_port(ids["gateway"], 80) + "/health/readiness"
        console_url = "http://" + container_port(ids["console"], 80) + "/login"
        wait_for(lambda: endpoint(gateway_url) == base["version"] and endpoint(console_url) == base["version"])

        ipc = directory / "ipc" / "updater.sock"
        state = directory / "state"
        out = resources.enter_context((directory / "agent.log").open("w"))
        agent_args = [
            sys.executable, "-B", str(ROOT / "deploy/update_agent.py"), "--compose", str(compose), "--project", project,
            "--state-dir", str(state), "--socket", str(ipc), "--manifest-file", str(feed),
            "--allow-beta", "--namespace", registry + "/xhub", "--health-url", gateway_url,
            "--health-url", console_url, "--health-timeout", "7",
        ]
        agent = subprocess.Popen(agent_args, stdout=out, stderr=out, cwd=ROOT)
        resources.callback(stop, agent)
        wait_for(ipc.exists)
        args = sys.argv[1:] or ["bash", "scripts/e2e.sh", "version-update.test.ts"]
        env = {
            **os.environ, "E2E_VERSION_UPDATE": "1", "XHUB_UPDATE_SOCKET": str(ipc), "E2E_VERSION_FEED": str(feed),
            "E2E_VERSION_GATEWAY_URL": gateway_url, "E2E_VERSION_CONSOLE_URL": console_url,
            "E2E_PORTABLE_CONSOLE": "1", "E2E_ENTERPRISE_FIXTURE": "0",
        }
        print("Running browser against the updater, real Compose services, PostgreSQL and Redis", flush=True)
        result = subprocess.run(args, cwd=ROOT, env=env)
        if result.returncode:
            out.flush()
            print((directory / "agent.log").read_text()[-2500:], file=sys.stderr)
            return result.returncode
        for service in ("postgres", "redis"):
            assert run(*compose_args, "ps", "-q", service) == ids[service], f"{service} was replaced"
        assert run(*postgres, "-c", "SELECT label FROM rollout_marker;") == "original"
        assert run(*redis, "GET", "rollout-marker") == "original"
        saved = json.loads((state / "state.json").read_text())
        assert saved["current"] == base and saved["error"] == "rolled_back" and not saved.get("pending")
        assert endpoint(gateway_url) == endpoint(console_url) == base["version"]
        stop(agent)
        agent = subprocess.Popen(agent_args, stdout=out, stderr=out, cwd=ROOT)
        resources.callback(stop, agent)
        status = wait_for(lambda: json.loads(run("curl", "-fsS", "--unix-socket", str(ipc), "http://localhost/status", timeout=4)))
        assert status["current"] == base["version"] and status["error"] == "rolled_back"
        print("Verified application rollback, durable state after updater restart, untouched PostgreSQL and Redis", flush=True)
        return 0


def publish_fixed_ports(services):
    """为测试项目保留两个不同回环端口，返回替换端口后的 Compose 服务定义。"""
    import socket

    with ExitStack() as stack:
        for service in ("gateway", "console"):
            sock = stack.enter_context(socket.socket())
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
            services[service]["ports"] = [f"127.0.0.1:{port}:80"]
    return services


if __name__ == "__main__":
    raise SystemExit(main())
