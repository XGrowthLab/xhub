"""构造和校验 XHub 版本发布清单。CI 和主机执行器使用同一份规则。"""

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

SERVICES = ("gateway", "console")
VERSION = re.compile(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(alpha|beta|rc)\.(0|[1-9]\d*))?", re.ASCII)
HEX64 = re.compile(r"[0-9a-f]{64}", re.ASCII)
REVISION = re.compile(r"[0-9a-f]{40}", re.ASCII)
MANIFEST_NAME = "xhub-release.json"
REPO = "XGrowthLab/xhub"
NAMESPACE = "ghcr.io/xgrowthlab/xhub"
COMPAT_LABEL = "io.xhub.database-compatibility"


def version_key(tag):
    """解析已登记的正式及预发布标签，用于严格排序。非法或超长标签抛 ValueError。"""
    match = VERSION.fullmatch(tag) if isinstance(tag, str) and len(tag) <= 48 else None
    if match is None:
        raise ValueError("invalid release version")
    major, minor, patch, channel, number = match.groups()
    return int(major), int(minor), int(patch), {None: 3, "rc": 2, "beta": 1, "alpha": 0}[channel], int(number or 0)


def validate_manifest(value, namespace=NAMESPACE):
    """验证 CI 或执行器取得的清单。返回原对象；拒绝未知字段、格式及不同镜像仓库。"""
    keys = {"schema", "version", "revision", "database_compatibility", "images"}
    if not isinstance(value, dict) or set(value) != keys or type(value["schema"]) is not int or value["schema"] != 1:
        raise ValueError("unsupported release manifest")
    version_key(value["version"])
    for field, pattern in (("revision", REVISION), ("database_compatibility", HEX64)):
        if not isinstance(value[field], str) or not pattern.fullmatch(value[field]):
            raise ValueError("invalid release " + field)
    images = value["images"]
    if not isinstance(images, dict) or set(images) != set(SERVICES):
        raise ValueError("release must identify both images")
    for service in SERVICES:
        prefix = f"{namespace}-{service}@sha256:"
        image = images[service]
        if not isinstance(image, str) or not image.startswith(prefix) or not HEX64.fullmatch(image[len(prefix):]):
            raise ValueError("invalid release image")
    return value


def database_fingerprint(root):
    """计算启动、持久化和迁移代码的保守标识；仅 CI 使用已检出的 Git 树，不读取运行数据库。"""
    files = subprocess.check_output(
        ["git", "ls-files", "-z", "--", "internal/iam", "internal/store", "cmd/gateway", "go.mod", "go.sum"], cwd=root
    ).split(b"\0")
    sources = [p for p in files if p and p.endswith((b".go", b".sql", b".mod", b".sum")) and not p.endswith(b"_test.go")]
    if not sources:
        raise ValueError("database source paths missing")
    digest = hashlib.sha256()
    for raw_path in sorted(sources):
        path = raw_path.decode()
        content = (root / path).read_bytes()
        digest.update(raw_path + b"\0" + hashlib.sha256(content).digest())
    return digest.hexdigest()


def main():
    """处理 CI 的 fingerprint 和 write 命令。写入前校验完整清单；失败退出且不生成候选文件。"""
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("fingerprint")
    write = sub.add_parser("write")
    for argument in ("version", "revision", "compatibility", "namespace", "gateway-digest", "console-digest", "out"):
        write.add_argument("--" + argument, required=True)
    args = parser.parse_args()
    if args.command == "fingerprint":
        print(database_fingerprint(Path(__file__).resolve().parent.parent))
        return
    manifest = validate_manifest({
        "schema": 1, "version": args.version, "revision": args.revision,
        "database_compatibility": args.compatibility,
        "images": {service: f"{args.namespace}-{service}@{getattr(args, service + '_digest')}" for service in SERVICES},
    }, args.namespace)
    Path(args.out).write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
