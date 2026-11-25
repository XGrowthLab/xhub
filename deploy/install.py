"""在 Linux 主机部署 XHub Compose 服务，并默认启用独立的 systemd 更新执行器。"""

import argparse
import fcntl
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import time

if __package__:
    from .update_agent import Compose, Releases, save_json
    from .update_manifest import NAMESPACE, SERVICES
else:
    from update_agent import Compose, Releases, save_json
    from update_manifest import NAMESPACE, SERVICES

SOURCE = Path(__file__).resolve().parent
DEPLOYMENT = Path("/opt/xhub")
STATE = Path("/var/lib/xhub-updater")
RUNTIME = Path("/run/xhub-updater")
UNIT = Path("/etc/systemd/system/xhub-updater.service")


def first_release(beta):
    """从官方发布源挑选首装清单。正式渠道首次没有正式版本时，以已发布的预览版作为起点。"""
    found = Releases(beta=beta).latest()
    if not found and not beta:
        found = Releases(beta=True).latest()
    if not found:
        raise ValueError("No XHub release with an installable manifest is available")
    return found["manifest"]


def install_config(config, images, runtime):
    """启动已构建的本地镜像或发布清单指定的镜像，并为网关绑定只读的 IPC 目录。"""
    config.pop("name", None)
    for service in SERVICES:
        spec = config["services"][service]
        spec.pop("build", None)
        if images:
            spec["image"] = images[service]
            spec["platform"] = "linux/amd64"
    gateway = config["services"]["gateway"]
    gateway["environment"]["XHUB_UPDATE_SOCKET"] = "/run/xhub-updater/agent.sock"
    gateway["volumes"].append({
        "type": "bind", "source": str(runtime), "target": "/run/xhub-updater",
        "read_only": True, "bind": {"create_host_path": False},
    })
    for spec in config["services"].values():
        spec["restart"] = "unless-stopped"
        # 默认仅向本机开放，外部访问由运维配置反向代理。
        for port in spec.get("ports", []):
            port.setdefault("host_ip", "127.0.0.1")
    return config


def protected_directory(path, mode):
    """创建运行或配置目录；仅接受归当前安装用户所有、其他账号不可写的真实目录。"""
    path.mkdir(mode=mode, parents=True, exist_ok=True)
    entry = path.lstat()
    if not stat.S_ISDIR(entry.st_mode) or entry.st_uid != os.geteuid() or entry.st_mode & 0o022:
        raise ValueError(f"Unsafe deployment directory: {path}")


def configure(source, target, state, runtime, beta, use_release=False, project="xhub"):
    """初始化 Compose 配置与管理标记。重跑使用原配置和更新状态，外部 Compose 部署交给运维接入。"""
    compose_file = target / "docker-compose.yml"
    marker = state / "install.json"
    # 写入前拒绝接管已有部署。完整配置保存在安装标记中，进程中断后也能恢复首装。
    if marker.exists():
        saved = json.loads(marker.read_text())
        if saved["directory"] != str(target) or saved["beta"] != beta or saved["release"] != use_release:
            raise ValueError("Use the original install directory and options when retrying")
    else:
        if compose_file.exists() or (state / "compose.override.json").exists():
            raise ValueError("An existing deployment must be connected using the host updater guide")
        compose = Compose(source.parent / "docker-compose.yml", state / "compose.override.json", project, NAMESPACE, ())
        if compose.run("ps", "-aq", "--filter", f"label=com.docker.compose.project={project}"):
            raise ValueError("An existing Compose project must be connected in place")
        config = json.loads(compose.compose("config", "--format", "json"))
        images = None
        if use_release:
            manifest = first_release(beta)
            compose.prepare(manifest, pull=True)
            images = manifest["images"]
            print(f"Initial release: {manifest['version']}", flush=True)
        else:
            # 公开仓库可以独立部署，本地构建镜像不依赖发布仓库的读取权限。
            for service in SERVICES:
                image = config["services"][service]["image"]
                try:
                    compose.image(image)
                except subprocess.CalledProcessError as error:
                    raise ValueError(f"Local image {image} is missing. Run bash deploy/build.sh first.") from error
        saved = {"directory": str(target), "beta": beta, "release": use_release,
                 "config": install_config(config, images, runtime)}
        save_json(marker, saved)
    if not compose_file.exists():
        # JSON 是 Compose 支持的 YAML 子集，配置与安装标记均以原子方式写入。
        save_json(compose_file, saved["config"])
    return compose_file


def enable_service(source, target, unit, beta):
    """复制执行器到受保护的宿主机目录，注册开机启动并启动 systemd 单元。"""
    program = target / "updater"
    protected_directory(program, 0o755)
    for name in ("update_agent.py", "update_manifest.py"):
        destination = program / name
        temporary = destination.with_suffix(".tmp")
        shutil.copyfile(source / name, temporary)
        temporary.replace(destination)
    if not unit.exists():
        template = (source / "xhub-updater.service.example").read_text()
        template = template.replace("/opt/xhub-updater/", f"{program}/")
        if beta:
            template = template.replace("\nRestart=on-failure", " --allow-beta\nRestart=on-failure")
        temporary_unit = unit.with_suffix(".tmp")
        temporary_unit.write_text(template)
        temporary_unit.chmod(0o644)
        temporary_unit.replace(unit)
    subprocess.run(["systemctl", "daemon-reload"], check=True, timeout=20)
    subprocess.run(["systemctl", "enable", "--now", unit.name], check=True, timeout=30)


def main():
    """启动全新发布部署或重试同一次安装；只启用更新入口，镜像切换由平台管理员确认。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--beta", action="store_true", help="Follow prereleases as well as stable releases")
    parser.add_argument("--release", action="store_true", help="Install a release image pair from GHCR")
    args = parser.parse_args()
    if os.geteuid() != 0 or not Path("/run/systemd/system").is_dir():
        parser.error("Run as root on a Linux host with systemd and Docker Compose")
    if UNIT.exists() and not (STATE / "install.json").exists():
        parser.error("An existing updater unit is already installed")
    os.umask(0o077)
    for path, mode in ((DEPLOYMENT, 0o750), (STATE, 0o700), (RUNTIME, 0o750)):
        protected_directory(path, mode)
    with (STATE / "install-lock").open("a") as lock:
        # 安装脚本与执行器分别持有安装锁和事务锁，重跑安装时不重建正在运行的应用。
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        file = configure(SOURCE, DEPLOYMENT, STATE, RUNTIME, args.beta, args.release)
        compose = Compose(file, STATE / "compose.override.json", "xhub", NAMESPACE, ())
        updater = subprocess.run(["systemctl", "is-active", "--quiet", UNIT.name], timeout=10)
        if updater.returncode:
            compose.compose("up", "-d", "--no-build", "--wait", timeout=180)
        enable_service(SOURCE, DEPLOYMENT, UNIT, args.beta)
        for _ in range(60):
            if (RUNTIME / "agent.sock").is_socket():
                print("Version updates enabled. Console: http://localhost:3000/login")
                return
            time.sleep(0.5)
        raise ValueError("The updater did not create its socket; inspect xhub-updater.service")


if __name__ == "__main__":
    main()
