#!/usr/bin/env python3
"""只读盘点部署备份并评估轮换候选。"""

import argparse
import hashlib
import json
import os
import re
import stat
import subprocess
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path


ARCHIVES = ("database.dump", "postgres.dump")
PARTIAL = ("accounts-before.dump", "schema.dump")
UNFINISHED = tuple(name + ".partial" for name in ARCHIVES + PARTIAL)
REQUIRED_TABLES = {"api_keys", "kv", "organizations", "usage_events", "users"}
TABLE_DATA = re.compile(r"^\d+;\s+\d+\s+\d+\s+TABLE DATA (\S+) (\S+)\s")
SHA256 = re.compile(r"[0-9a-f]{64}")
MAX_PROOF_BYTES = 16 * 1024


@dataclass(frozen=True)
class Archive:
    """一个已发现的本地普通备份文件，包含文件身份和安全的展示字段。"""

    snapshot: str
    path: Path
    info: os.stat_result
    partial: bool
    commit: str

    @property
    def initial_classification(self):
        """根据已盘点文件名和快照范围返回初始分类，供目录检查及轮换使用。写入中的归档也可能已有完整目录头。"""
        if self.path.name in UNFINISHED:
            return "unfinished"
        return "partial" if self.partial else "not_inspected"


def read_small_json(path):
    """读取受控小 JSON 侧车文件。path 为路径，返回字典或空字典；未知格式和软链接不会作为证据。"""
    try:
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_PROOF_BYTES:
            return {}
        with path.open("rb") as source:
            raw = source.read(MAX_PROOF_BYTES + 1)
        result = json.loads(raw) if len(raw) <= MAX_PROOF_BYTES else None
        return result if isinstance(result, dict) else {}
    except (OSError, UnicodeError, ValueError):
        return {}


def discover(root):
    """读取 root 直属备份目录中的正式及未完成文件，返回按时间排序的归档和未分类目录数。归档本身只取文件元数据。"""
    archives = []
    unclassified = 0
    for directory in root.iterdir():
        if not stat.S_ISDIR(directory.lstat().st_mode):
            continue
        marker = read_small_json(directory / "deployment-source.json")
        commit = marker.get("commit")
        commit = commit if isinstance(commit, str) and re.fullmatch(r"[0-9a-f]{40,64}", commit) else ""
        found = False
        for filename in ARCHIVES + PARTIAL + UNFINISHED:
            path = directory / filename
            try:
                info = path.lstat()
            except FileNotFoundError:
                continue
            if stat.S_ISREG(info.st_mode):
                archives.append(Archive(directory.name, path, info, filename in PARTIAL, commit))
                found = True
        unclassified += not found
    archives.sort(key=lambda entry: (entry.info.st_mtime_ns, entry.snapshot, entry.path.name), reverse=True)
    return archives, unclassified


def inspect_toc(archive, pg_restore):
    """用 pg_restore 读取正式归档的目录，返回分类和表数据数量。其余类型沿用盘点分类，无需打开内容。"""
    if archive.initial_classification != "not_inspected":
        return archive.initial_classification, 0
    try:
        result = subprocess.run(
            [pg_restore, "--list", str(archive.path)],
            capture_output=True,
            check=False,
            timeout=20,
        )
    except (OSError, subprocess.TimeoutExpired):
        return "toc_unavailable", 0
    if result.returncode != 0:
        return "toc_unavailable", 0
    tables = {}
    for raw in result.stdout.decode("utf-8", errors="replace").splitlines():
        match = TABLE_DATA.match(raw)
        if match:
            tables.setdefault(match[1], set()).add(match[2])
    count = sum(map(len, tables.values()))
    full_candidate = any(REQUIRED_TABLES <= names for names in tables.values())
    return ("full_candidate" if full_candidate else "partial"), count


def checksums(archive):
    """顺序读取一个备份计算 SHA-256，确认哈希前后文件身份和长度。输入为目录盘点时的文件，变化或读取失败返回空串。"""
    try:
        with archive.path.open("rb") as source:
            before = source.fileno()
            initial = archive.path.lstat()
            if not stat.S_ISREG(initial.st_mode) or file_version(initial) != file_version(archive.info):
                return ""
            # 保留文件描述符的身份校验，防止盘点与散列之间被替换。
            if file_version(os.fstat(before)) != file_version(initial):
                return ""
            digest = hashlib.sha256()
            for block in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(block)
            if file_version(os.fstat(before)) == file_version(initial) == file_version(archive.path.lstat()):
                return digest.hexdigest()
    except OSError:
        pass
    return ""


def file_version(info):
    """提取决定本次散列是否仍适用的一组 stat 属性，不返回路径或内容。"""
    return (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def accepted_proof(proof, archive):
    """检查外部恢复记录是否声明验证同一份完整备份。返回待本地核对的哈希，证据缺失或格式错误时为空。"""
    digest = proof.get("sha256")
    if not isinstance(digest, str) or not SHA256.fullmatch(digest):
        return ""
    if (
        proof.get("snapshot") != archive.snapshot
        or proof.get("archive") != archive.path.name
        or proof.get("bytes") != archive.info.st_size
        or proof.get("offsite_sha256") != digest
        or proof.get("restored_sha256") != digest
        or proof.get("restore_scope") != "full_database"
        or proof.get("restore_validated") is not True
        or not isinstance(proof.get("offsite_reference"), str)
        or not proof["offsite_reference"].strip()
    ):
        return ""
    try:
        checked = datetime.fromisoformat(str(proof.get("restored_at")).replace("Z", "+00:00"))
        if checked.tzinfo is None or checked > datetime.now(timezone.utc):
            return ""
    except (TypeError, ValueError):
        return ""
    return digest


def plan(args):
    """生成盘点与人工轮换建议，args 包含扫描和散列上限。只输出文件名、聚合大小及状态，不修改备份。"""
    archives, unclassified = discover(args.root)
    results = []
    physical = {}
    protected = set(args.pin)
    verified_count = 0
    remaining_toc = args.inspect_limit

    for archive in archives:
        entry = {
            "snapshot": archive.snapshot,
            "archive": archive.path.name,
            "bytes": archive.info.st_size,
            "allocated_bytes": archive.info.st_blocks * 512,
            "classification": archive.initial_classification,
            "table_data_entries": 0,
            "evidence": "missing",
            "rotation": "retain",
        }
        if archive.commit:
            entry["commit"] = archive.commit
        physical[(archive.info.st_dev, archive.info.st_ino)] = entry["allocated_bytes"]
        if entry["classification"] == "not_inspected" and remaining_toc:
            entry["classification"], entry["table_data_entries"] = inspect_toc(archive, args.pg_restore)
            remaining_toc -= 1
        if entry["classification"] == "full_candidate" and args.proof_dir:
            proof = read_small_json(args.proof_dir / (archive.snapshot + "." + archive.path.name + ".json"))
            if proof:
                digest = accepted_proof(proof, archive)
                entry["evidence"] = "unverified"
                if digest and verified_count < args.verify_limit:
                    verified_count += 1
                    entry["evidence"] = "verified" if checksums(archive) == digest else "checksum_mismatch"
        results.append(entry)

    # 保留最近的完整候选与固定备份，按部署目录去重。通过校验的远端副本仍需有一份本机通过验证的基准。
    recent = list(dict.fromkeys(row["snapshot"] for row in results if row["classification"] == "full_candidate"))
    protected.update(recent[: args.keep])
    local_verified = any(row["snapshot"] in protected and row["evidence"] == "verified" for row in results)
    if local_verified:
        for row in results:
            if row["snapshot"] not in protected and row["evidence"] == "verified":
                row["rotation"] = "review_candidate"
    return {
        "archives": results,
        "archive_allocated_bytes": sum(physical.values()),
        "unclassified_directories": unclassified,
        "verified_retained_checkpoint": local_verified,
        "review_candidates": sum(row["rotation"] == "review_candidate" for row in results),
    }


def main():
    """解析命令行，生成 JSON 盘点。无写入或删除入口；目录读取失败返回非零。"""
    parser = argparse.ArgumentParser(description="Read-only inventory and rotation review for local deployment backups")
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--inspect-limit", type=int, default=0)
    parser.add_argument("--verify-limit", type=int, default=0)
    parser.add_argument("--proof-dir", type=Path)
    parser.add_argument("--pg-restore", default="pg_restore")
    parser.add_argument("--keep", type=int, default=3)
    parser.add_argument("--pin", action="append", default=[])
    args = parser.parse_args()
    if not 0 <= args.inspect_limit <= 30 or not 0 <= args.verify_limit <= 10 or args.keep < 1:
        parser.error("inspect limit must be 0..30, verification limit 0..10, keep at least 1")
    if args.verify_limit and not args.proof_dir:
        parser.error("checksum verification requires --proof-dir")
    try:
        result = plan(args)
    except OSError:
        parser.exit(1, "Backup inventory failed while reading the backup root.\n")
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
