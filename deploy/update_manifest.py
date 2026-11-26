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

# beta.3 与 beta.4 的建表、启动迁移和存储格式一致。保留 beta.3 标识才能让已安装的旧执行器
# 直接更新；只为正式发布的 beta.4 三元组登记旧标识，镜像和清单仍按原始字段核验。
BASE_DATABASE_COMPATIBILITY = "81994808d891746e8d5a64e790e5fd4dc4044eeef1a596e7ca9a0a3aed4c4612"
BASE_DATABASE_CONTRACT = "bf050ec8f9989c6246c20e904ab4298c76082e204f8440442392b7c82d0f5df1"
LEGACY_DATABASE_COMPATIBILITY = {
    ("v0.1.0-beta.4", "2a6b1ff215efcdef22ce4f6e9db6c7f3a902d3ee",
     "3893505e9d4a5959e4d51abd18808a1d40dd5aed9efca13590ad8f058061a142"): BASE_DATABASE_COMPATIBILITY,
}

# 修改无法从迁移源码识别的持久化格式时递增此版本。启动和 xorm 隐式建表代码没有独立 SQL 文件，
# 必须和显式迁移一起参与标识。普通日志查询、路由和界面代码不影响标识。
DATABASE_FORMAT_VERSION = 1
DATABASE_CONTRACT_FILES = frozenset({
    "go.mod", "go.sum",
    "internal/iam/db.go", "internal/iam/quota.go",
    "internal/store/beans.go", "internal/store/engine.go", "internal/store/store.go",
    "internal/store/compact_history.go", "internal/store/packed_history.go",
})


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


def database_contract_fingerprint(root):
    """读取 root 的已跟踪迁移与存储格式输入，返回 CI 使用的兼容指纹；缺少必要源码时失败，不连接数据库。"""
    tracked = set(subprocess.check_output(
        ["git", "ls-files", "-z", "--", "internal", "cmd/gateway", "go.mod", "go.sum"], cwd=root,
    ).decode().split("\0"))
    if not DATABASE_CONTRACT_FILES <= tracked or "internal/iam/schema.sql" not in tracked:
        raise ValueError("database source paths missing")
    sources = set(DATABASE_CONTRACT_FILES)
    for path in tracked:
        parts = Path(path).parts
        if path.endswith(".sql") and (path.startswith(("internal/iam/", "internal/store/")) or "migrations" in parts):
            sources.add(path)
        elif path.endswith(".go") and not path.endswith("_test.go") and (
            path.startswith("cmd/gateway/") or "migrations" in parts or
            parts[-1] in {"migration.go", "migrations.go", "schema.go"} or
            path.endswith(("_migration.go", "_schema.go"))
        ):
            sources.add(path)
    digest = hashlib.sha256(f"xhub-database-contract-v{DATABASE_FORMAT_VERSION}\0".encode())
    for path in sorted(sources):
        digest.update(path.encode() + b"\0" + hashlib.sha256((root / path).read_bytes()).digest())
    return digest.hexdigest()


def database_fingerprint(root):
    """生成发布清单和镜像标签使用的数据库标识；旧基线仅在 root 的兼容指纹一致时复用。"""
    contract = database_contract_fingerprint(root)
    return BASE_DATABASE_COMPATIBILITY if contract == BASE_DATABASE_CONTRACT else contract


def database_compatibility_key(manifest):
    """把已校验清单映射为可比较标识，供主机检查、升级和回退使用；不修改清单、摘要或镜像标签。"""
    identity = (manifest["version"], manifest["revision"], manifest["database_compatibility"])
    return LEGACY_DATABASE_COMPATIBILITY.get(identity, manifest["database_compatibility"])


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
