"""在本地一次性 PostgreSQL 16 容器生成并恢复合成部署备份。"""

import hashlib
import json
import os
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
import uuid
from datetime import datetime, timezone
from pathlib import Path


class DeploymentBackupRestoreTest(unittest.TestCase):
    """使用真 pg_dump/pg_restore、备份盘点入口和完全独立的临时数据。"""

    def docker_pg(self, *args, **options):
        """在本测试私有容器执行 PostgreSQL 命令，返回已完成进程，非零退出直接失败。"""
        return subprocess.run(
            ["docker", "exec", "-i", self.container, *args], check=True, timeout=25, **options,
        )

    def test_read_only_plan_with_completed_restore(self):
        """备份两个发布点并从合成外部镜像副本完整恢复，验证保留、范围、账单值及只读行为。"""
        self.container = "xhub-backup-test-" + uuid.uuid4().hex[:10]
        subprocess.run(["docker", "run", "--rm", "-d", "--name", self.container,
                        "-e", "POSTGRES_PASSWORD=synthetic-fixture", "postgres:16"],
                       check=True, timeout=25, capture_output=True)
        self.addCleanup(subprocess.run, ["docker", "stop", "-t", "2", self.container],
                        check=True, timeout=15, capture_output=True)
        deadline = time.monotonic() + 25
        while subprocess.run(["docker", "exec", self.container, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"],
                             capture_output=True, check=False, timeout=5).returncode:
            if time.monotonic() > deadline:
                self.fail("本地 PostgreSQL 尚未就绪")
            time.sleep(0.2)
        self.docker_pg("createdb", "-U", "postgres", "fixture")
        tables = ["api_keys", "kv", "organizations", "usage_events", "users", "extra_runtime_table"]
        sql = "".join(f"CREATE TABLE {name}(value integer); INSERT INTO {name} VALUES (7);" for name in tables)
        self.docker_pg("psql", "-U", "postgres", "-d", "fixture", "-v", "ON_ERROR_STOP=1",
                       input=sql.encode(), capture_output=True)
        with tempfile.TemporaryDirectory() as work:
            area = Path(work)
            backups = area / "backups"
            proofs = area / "proofs"
            remote = area / "synthetic-offsite"
            for directory in [backups, proofs, remote]:
                directory.mkdir()
            wrapper = area / "pg-restore-fixture"
            wrapper.write_text(f'#!/bin/sh\nexec docker exec -i {shlex.quote(self.container)} pg_restore --list < "$2"\n',
                               encoding="utf-8")
            wrapper.chmod(0o700)
            command = [sys.executable, str(Path(__file__).with_name("deployment_backup_plan.py")),
                       "--root", str(backups), "--pg-restore", str(wrapper), "--keep", "1", "--inspect-limit", "4"]
            for index, name in enumerate(["older-release", "current-release", "schema-only", "accounts-only"]):
                folder = backups / name
                folder.mkdir()
                archive = folder / "database.dump"
                options = {"schema-only": ["--schema-only"],
                           "accounts-only": ["--table=users", "--table=api_keys"]}.get(name, [])
                with archive.open("wb") as output:
                    self.docker_pg("pg_dump", "-U", "postgres", "-d", "fixture", "--format=custom",
                                   "--compress=zstd:3", *options, stdout=output)
                os.utime(archive, (index + 100, index + 100))
                if options:
                    continue
                remote_copy = remote / (name + ".dump")
                shutil.copyfile(archive, remote_copy)
                target = "restored_" + str(index)
                self.docker_pg("createdb", "-U", "postgres", target)
                with remote_copy.open("rb") as body:
                    self.docker_pg("pg_restore", "-U", "postgres", "--exit-on-error", "--dbname=" + target, stdin=body)
                verification = " UNION ALL ".join(f"SELECT value FROM {name}" for name in tables)
                actual = self.docker_pg("psql", "-U", "postgres", "-d", target, "-qAt", "-c", verification,
                                        capture_output=True).stdout.splitlines()
                self.assertEqual(actual, [b"7"] * len(tables))
                digest = hashlib.sha256(archive.read_bytes()).hexdigest()
                restored_digest = hashlib.sha256(remote_copy.read_bytes()).hexdigest()
                receipt = dict(snapshot=name, archive=archive.name, bytes=archive.stat().st_size, sha256=digest,
                               offsite_sha256=restored_digest, restored_sha256=restored_digest,
                               restore_scope="full_database", restore_validated=True,
                               restored_at=datetime.now(timezone.utc).isoformat(),
                               offsite_reference="synthetic-isolated-copy")
                (proofs / f"{name}.{archive.name}.json").write_text(json.dumps(receipt), encoding="utf-8")
            default = json.loads(subprocess.check_output(command, timeout=25))
            self.assertEqual(default["review_candidates"], 0)
            report = json.loads(subprocess.check_output(command + ["--proof-dir", str(proofs), "--verify-limit", "2"], timeout=25))
            archived = {row["snapshot"]: row for row in report["archives"]}
            self.assertEqual(archived["schema-only"]["classification"], "partial")
            self.assertEqual(archived["accounts-only"]["classification"], "partial")
            self.assertEqual(archived["current-release"]["rotation"], "retain")
            self.assertEqual(archived["older-release"]["rotation"], "review_candidate")
            self.assertTrue((backups / "older-release" / "database.dump").is_file())
            self.assertNotIn("synthetic-isolated-copy", json.dumps(report))


if __name__ == "__main__":
    unittest.main()
