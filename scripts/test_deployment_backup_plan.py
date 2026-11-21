"""测试备份盘点和证据门禁，所有文件均在独立临时目录。"""

import hashlib
import json
import os
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from scripts import deployment_backup_plan as rotation


class BackupPlanTests(unittest.TestCase):
    """验证 TOC、保留策略、证据复核和只读执行。"""

    def setUp(self):
        """使用独立临时目录存放内容和恢复记录，测试结束自动删除。"""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.base = Path(temp.name)
        self.root = self.base / "backups"
        self.root.mkdir()
        self.proof = self.base / "proof"
        self.proof.mkdir()
        self.args = SimpleNamespace(root=self.root, proof_dir=self.proof, inspect_limit=8,
                                    verify_limit=8, pg_restore="pg_restore", keep=1, pin=[])

    def archive(self, name, stamp, filename="database.dump"):
        """以 name 和 stamp 创建合成备份，返回其路径。"""
        folder = self.root / name
        folder.mkdir(exist_ok=True)
        path = folder / filename
        path.write_bytes(("synthetic-" + name).encode())
        os.utime(path, (stamp, stamp))
        return path

    def evidence(self, archive, **values):
        """给 archive 准备格式正确的本地测试证明文件，可用 values 模拟错误证据。"""
        data = archive.read_bytes()
        checksum = hashlib.sha256(data).hexdigest()
        receipt = {"snapshot": archive.parent.name, "archive": archive.name, "bytes": len(data),
                   "sha256": checksum, "offsite_sha256": checksum, "restored_sha256": checksum,
                   "restore_scope": "full_database", "restore_validated": True,
                   "offsite_reference": "synthetic-reference", "restored_at": "2026-01-02T00:00:00+00:00"}
        receipt.update(values)
        (self.proof / f"{archive.parent.name}.{archive.name}.json").write_text(json.dumps(receipt), encoding="utf-8")

    @patch.object(rotation, "inspect_toc", return_value=("full_candidate", 5))
    def test_protects_verified_checkpoint_and_explicit_pins(self, toc):
        """准备三份同类备份并固定一份，核对有独立证据的旧备份只被列为复核候选。"""
        for name, stamp in [("new", 300), ("old", 100), ("pinned", 200)]:
            self.evidence(self.archive(name, stamp))
        self.args.pin = ["pinned"]
        report = rotation.plan(self.args)
        self.assertTrue(report["verified_retained_checkpoint"])
        self.assertEqual(report["review_candidates"], 1)
        self.assertEqual([item["snapshot"] for item in report["archives"] if item["rotation"] == "review_candidate"], ["old"])
        self.assertEqual(toc.call_count, 3)
        self.assertTrue((self.root / "old" / "database.dump").is_file())
        self.assertNotIn("synthetic-reference", json.dumps(report))

    @patch.object(rotation, "inspect_toc", return_value=("full_candidate", 5))
    def test_holds_without_verified_retained_checkpoint(self, _):
        """故意缺失最新备份的恢复证明，确保旧备份即使有证明也继续保留。"""
        self.archive("new", 200)
        self.evidence(self.archive("old", 100))
        report = rotation.plan(self.args)
        self.assertFalse(report["verified_retained_checkpoint"])
        self.assertEqual(report["review_candidates"], 0)

    @patch.object(rotation, "inspect_toc", return_value=("full_candidate", 5))
    def test_blocks_mismatch_and_limits_expensive_reads(self, _):
        """构造错误的散列与未知恢复范围，验证散列检查上限、篡改拒绝和仅结构证据无效。"""
        self.evidence(self.archive("new", 500))
        old = self.archive("changed", 400)
        self.evidence(old)
        old.write_bytes(b"x" * old.stat().st_size)
        os.utime(old, (400, 400))
        self.evidence(self.archive("only-schema", 300), restore_scope="schema_only")
        self.evidence(self.archive("later", 200))
        self.args.verify_limit = 2
        self.args.inspect_limit = 3
        report = {row["snapshot"]: row for row in rotation.plan(self.args)["archives"]}
        self.assertEqual(report["changed"]["evidence"], "checksum_mismatch")
        self.assertEqual(report["only-schema"]["evidence"], "unverified")
        self.assertEqual(report["later"]["classification"], "not_inspected")
        self.assertTrue(all(row["rotation"] == "retain" for row in report.values()))

    def test_partial_and_hardlink_inventory(self):
        """覆盖部分快照、软链接和共用 inode，确保只统计一次物理空间且默认无需打开归档。"""
        first = self.archive("one", 100)
        (self.root / "two").mkdir()
        os.link(first, self.root / "two" / "database.dump")
        schema = self.archive("metadata", 200, "schema.dump")
        os.symlink(first, self.root / "metadata" / "postgres.dump")
        os.symlink(self.root / "one", self.root / "symlink-dir", target_is_directory=True)
        self.args.inspect_limit = 0
        report = rotation.plan(self.args)
        self.assertEqual(report["archive_allocated_bytes"], (first.stat().st_blocks + schema.stat().st_blocks) * 512)
        self.assertEqual(len(report["archives"]), 3)
        self.assertEqual(report["archives"][0]["classification"], "partial")

    @patch.object(rotation, "inspect_toc", return_value=("full_candidate", 5))
    def test_unfinished_files_count_space_without_displacing_restore_points(self, toc):
        """模拟正式备份、截断文件、空文件和软硬链接并存，验证空间去重、检查配额及轮换。临时目录自动回收。"""
        current = self.archive("current", 200)
        self.evidence(current)
        self.evidence(self.archive("older", 100))
        pending = self.archive("interrupted", 300, "database.dump.partial")
        self.evidence(pending)
        linked = current.parent / "postgres.dump.partial"
        os.link(pending, linked)
        empty = self.root / "starting" / "schema.dump.partial"
        empty.parent.mkdir()
        empty.touch()
        os.utime(empty, (400, 400))
        (empty.parent / "accounts-before.dump.partial").symlink_to(pending)
        self.archive("unrecognized", 500, "unrelated.dump.partial")
        self.args.inspect_limit = 5
        self.args.verify_limit = 2

        result = rotation.plan(self.args)
        unfinished = [row for row in result["archives"] if row["classification"] == "unfinished"]
        self.assertEqual({(r["snapshot"], r["archive"]) for r in unfinished}, {
            ("interrupted", pending.name), ("current", linked.name), ("starting", empty.name),
        })
        self.assertTrue(all(row["rotation"] == "retain" and row["evidence"] == "missing" for row in unfinished))
        self.assertEqual(result["archive_allocated_bytes"], sum(
            path.stat().st_blocks * 512 for path in (pending, empty, current, self.root / "older" / "database.dump")
        ))
        self.assertEqual(toc.call_count, 2)
        self.assertEqual([r["snapshot"] for r in result["archives"] if r["rotation"] == "review_candidate"], ["older"])
        self.assertEqual(result["unclassified_directories"], 1)

    @patch.object(rotation.subprocess, "run")
    def test_unfinished_archive_skips_toc_until_published(self, run):
        """测试账号快照原子改名后从未完成恢复为部分范围，两个状态均跳过 pg_restore。临时文件自动清理。"""
        staging = self.archive("release", 100, "accounts-before.dump.partial")
        archive = rotation.discover(self.root)[0][0]
        self.assertEqual(rotation.inspect_toc(archive, "pg_restore"), ("unfinished", 0))
        run.assert_not_called()
        staging.rename(staging.with_suffix(""))
        limited = rotation.discover(self.root)[0][0]
        self.assertEqual(rotation.inspect_toc(limited, "pg_restore"), ("partial", 0))
        run.assert_not_called()

    @patch.object(rotation.subprocess, "run")
    def test_toc_anchors_must_be_in_one_schema(self, run):
        """解析合成 pg_restore 目录，验证表数据需齐全且同属一个 schema，失败如实标记。"""
        path = self.archive("one", 100)
        archive = rotation.Archive("one", path, path.stat(), False, "")
        lines = ["123; 0 146 TABLE DATA public " + name + " xhub" for name in sorted(rotation.REQUIRED_TABLES)]
        run.return_value = SimpleNamespace(returncode=0, stdout="\n".join(lines).encode())
        self.assertEqual(rotation.inspect_toc(archive, "pg_restore"), ("full_candidate", 5))
        lines[0] = lines[0].replace(" public ", " alternate ")
        run.return_value.stdout = "\n".join(lines).encode()
        self.assertEqual(rotation.inspect_toc(archive, "pg_restore"), ("partial", 5))
        run.return_value.returncode = 1
        self.assertEqual(rotation.inspect_toc(archive, "pg_restore"), ("toc_unavailable", 0))


if __name__ == "__main__":
    unittest.main()
