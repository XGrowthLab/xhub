"""更新清单和事务状态的确定性测试。Docker 实际替换由浏览器回归另行验证。"""

import json
from pathlib import Path
import subprocess
from tempfile import TemporaryDirectory
import threading
import time
import unittest
from unittest.mock import patch

from deploy.update_manifest import (
    BASE_DATABASE_COMPATIBILITY, DATABASE_CONTRACT_FILES, LEGACY_DATABASE_COMPATIBILITY, MANIFEST_NAME,
    NAMESPACE, database_compatibility_key, database_contract_fingerprint, database_fingerprint, validate_manifest, version_key,
)
from deploy.update_agent import ACTIVE, Agent, Compose, Releases, UntrackedRelease, save_json, valid_health_url


def manifest(number, compatibility="a"):
    """创建有效的合成版本发布，对应两个不同镜像仓库，无网络调用。"""
    return {"schema": 1, "version": f"v0.1.0-beta.{number}", "revision": str(number) * 40,
            "database_compatibility": compatibility * 64,
            "images": {service: f"{NAMESPACE}-{service}@sha256:" + str(number) * 64
                       for service in ("gateway", "console")}}


class DatabaseCompatibilityTests(unittest.TestCase):
    def test_database_contract_keeps_read_only_changes_compatible(self):
        """在临时 Git 树验证纯查询变更沿用旧标识，迁移、ORM 表结构和存储格式变更产生新标识；退出自动清理。"""
        with TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True)
            for name in DATABASE_CONTRACT_FILES | {"internal/iam/schema.sql", "internal/iam/usage_read.go"}:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("baseline\n", encoding="utf-8")
            subprocess.run(["git", "add", "."], cwd=root, check=True)
            baseline = database_contract_fingerprint(root)
            with patch("deploy.update_manifest.BASE_DATABASE_CONTRACT", baseline):
                (root / "internal/iam/usage_read.go").write_text("updated read-only query\n", encoding="utf-8")
                self.assertEqual(database_fingerprint(root), BASE_DATABASE_COMPATIBILITY)
                for name in ("internal/iam/schema.sql", "internal/store/beans.go", "internal/store/packed_history.go"):
                    with self.subTest(name=name):
                        path = root / name
                        path.write_text("changed database contract\n", encoding="utf-8")
                        self.assertNotEqual(database_fingerprint(root), BASE_DATABASE_COMPATIBILITY)
                        path.write_text("baseline\n", encoding="utf-8")
                with patch("deploy.update_manifest.DATABASE_FORMAT_VERSION", 2):
                    self.assertNotEqual(database_fingerprint(root), BASE_DATABASE_COMPATIBILITY)
                migration = root / "internal/iam/recovery_migration.go"
                migration.write_text("new data migration\n", encoding="utf-8")
                subprocess.run(["git", "add", str(migration)], cwd=root, check=True)
                self.assertNotEqual(database_fingerprint(root), BASE_DATABASE_COMPATIBILITY)
            subprocess.run(["git", "rm", "--cached", "go.sum"], cwd=root, check=True, capture_output=True)
            with self.assertRaisesRegex(ValueError, "database source paths missing"):
                database_fingerprint(root)

    def test_legacy_database_alias_requires_the_exact_release(self):
        """以正式发布的三元组和三个错误字段变体验证旧标识映射边界；仅构造内存清单，无需清理。"""
        (version, revision, compatibility), = LEGACY_DATABASE_COMPATIBILITY
        legacy = {**manifest(4), "version": version, "revision": revision, "database_compatibility": compatibility}
        self.assertEqual(database_compatibility_key(legacy), BASE_DATABASE_COMPATIBILITY)
        self.assertEqual(legacy["database_compatibility"], compatibility)
        for change in ({"revision": "0" * 40}, {"version": "v0.1.0-beta.8"}, {"database_compatibility": "c" * 64}):
            candidate = {**legacy, **change}
            self.assertEqual(database_compatibility_key(candidate), candidate["database_compatibility"])


class Runtime:
    """用独立内存镜像表示 Docker 边界，记录调用和可控失败。"""

    def __init__(self, current):
        """以当前镜像开始，准备阶段可由测试阻塞，启动失败由单个目标配置。"""
        self.running = current
        self.namespace = NAMESPACE
        self.started = threading.Event()
        self.release = threading.Event()
        self.release.set()
        self.fail = None
        self.switched = []

    def current(self):
        """返回当前版本证据；不修改模型状态。"""
        return self.running

    def prepare(self, target, pull):
        """拉取时向并发断言报告已开始，拒绝无限等待。"""
        if pull:
            self.started.set()
            if not self.release.wait(2):
                raise ValueError("fixture pull timeout")

    def switch(self, target):
        """演练成功切换或新版本已部分生效但健康失败。"""
        self.running = target
        self.switched.append(target["version"])
        if target == self.fail:
            raise ValueError("unhealthy target")

    def healthy(self, target):
        """返回指定夹具版本是否正在运行且健康，供重启恢复分支断言，无副作用。"""
        return self.running == target and target != self.fail


class UpdateTests(unittest.TestCase):
    def setUp(self):
        """每例使用独立临时状态目录，结束关闭挡板并清理目录。"""
        self.directory = TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.source = self.root / MANIFEST_NAME
        self.base = manifest(1)
        self.next = manifest(2)
        self.compose = Runtime(self.base)
        self.addCleanup(self.compose.release.set)
        self.releases = Releases(beta=True, file=self.source)
        save_json(self.source, self.next)
        self.agent = Agent(self.root, self.compose, self.releases)

    def await_idle(self):
        """等待工作线程完成持久化，超出测试限时立即报出当前状态。"""
        deadline = time.monotonic() + 4
        while time.monotonic() < deadline:
            status = self.agent.status()
            if status["stage"] not in ACTIVE:
                return status
            time.sleep(.005)
        self.fail(f"agent never settled: {self.agent.status()}")

    def check(self):
        """用允许的身份申请一次检查，验证接受码，返回最终候选。"""
        self.assertEqual(self.agent.submit("check", {"actor": "test-admin"})[0], 202)
        return self.await_idle()

    def apply(self):
        """对已检查过的版本发送一次升级动作并断言已持久接受。"""
        result = self.agent.submit("apply", {"actor": "test-admin", "version": self.next["version"],
                                             "revision": self.next["revision"]})
        self.assertEqual(result[0], 202)

    def test_manifest_and_channel(self):
        """合法测试版按语义排序、正式渠道忽略测试版、外部镜像和畸形标签拒绝；清理合成文件。"""
        self.assertLess(version_key("v0.1.0-beta.9"), version_key("v0.1.0-beta.10"))
        self.assertLess(version_key("v0.1.0-rc.1"), version_key("v0.1.0"))
        self.assertIsNone(Releases(file=self.source).latest())
        self.assertEqual(self.releases.latest()["manifest"], self.next)
        for bad in ("0.01", "v0.1", "v0.1.0-beta.01", "v0.1.0\n", "v0.1.0; pwd"):
            with self.assertRaises(ValueError):
                version_key(bad)
        images = {**self.next["images"], "console": "docker.io/elsewhere/console:latest"}
        with self.assertRaises(ValueError):
            validate_manifest({**self.next, "images": images})
        with self.assertRaises(ValueError):
            validate_manifest({**self.next, "schema": True})

    def test_release_lookup(self):
        """外部 GitHub 读边界改用合成响应，校验选最新、跳过草稿和不一致资产，无真实请求。"""
        rows = [
            {"tag_name": "v0.1.0-beta.8", "prerelease": True, "draft": True, "assets": []},
            {"tag_name": self.next["version"], "prerelease": True, "draft": False, "body": "Release notes",
             "assets": [{"name": MANIFEST_NAME, "id": 13, "state": "uploaded"}]},
        ]
        source = Releases(beta=True)
        source.github = lambda path, asset=False: self.next if asset else rows
        self.assertEqual(source.latest(), {"manifest": self.next, "notes": "Release notes"})
        source.github = lambda path, asset=False: self.base if asset else rows
        with self.assertRaises(ValueError):
            source.latest()

    def test_running_main_image_can_check_but_cannot_apply(self):
        """核对两份运行镜像的 main 提交，允许查询且拒绝切换；混用构建时不展示单一提交。"""
        labels = {
            "org.opencontainers.image.version": "main",
            "org.opencontainers.image.revision": self.base["revision"],
            "io.xhub.database-compatibility": self.base["database_compatibility"],
        }
        ids = {"gateway": "1" * 12, "console": "2" * 12}
        images = {identifier: {"Config": {"Labels": dict(labels)}} for identifier in ids.values()}
        compose = Compose(self.root / "compose.yml", self.root / "override.json", "test", NAMESPACE, ())
        with patch.object(compose, "compose", side_effect=lambda *args: ids[args[-1]]), \
                patch.object(compose, "run", side_effect=lambda _, image: json.dumps([{
                    "State": {"Running": True}, "Image": image, "Config": {"Image": "test:main"},
                }])), patch.object(compose, "image", side_effect=images.__getitem__), patch.object(compose, "switch") as switch:
            self.agent = Agent(self.root, compose, self.releases)
            expected = {"revision": self.base["revision"], "main": True}
            self.assertEqual(self.agent.status()["current_build"], expected)
            status = self.check()
            self.assertIsNone(status["current"])
            self.assertEqual(status["current_build"], expected)
            self.assertEqual(status["candidate"]["version"], self.next["version"])
            self.assertFalse(status["candidate"]["compatible"])
            code, _ = self.agent.submit("apply", {"actor": "admin", "version": self.next["version"],
                                                  "revision": self.next["revision"]})
            self.assertEqual(code, 409)
            switch.assert_not_called()
            images[ids["console"]]["Config"]["Labels"]["org.opencontainers.image.revision"] = "a" * 40
            self.assertIsNone(self.check()["current_build"])
        with self.assertRaises(UntrackedRelease):
            Compose.release_labels({})

    def test_build_identity_ignores_missing_or_untrusted_labels(self):
        """空标签、畸形提交或未知标签不伪装成发布；合法本地构建只返回受限的标识。"""
        key = "org.opencontainers.image.revision"
        labels = {"gateway": {}, "console": None}
        self.assertIsNone(Compose.build_identity(labels))
        labels = {"gateway": {key: "invalid"}, "console": {key: "invalid"}}
        self.assertIsNone(Compose.build_identity(labels))
        self.assertEqual(Compose.build_identity({"gateway": {key: "a" * 40}, "console": {key: "a" * 40}}),
                         {"revision": "a" * 40, "main": False})

    def test_health_targets_stay_on_local_network(self):
        """验证回环与私网地址可用，同时拒绝 DNS、公共 IP、非法端口和重定向参数，无外部请求。"""
        for url in ("http://127.0.0.1:4000/health/readiness", "http://192.168.1.2:3000/login",
                    "http://10.0.0.1:65535/login", "http://172.31.1.8:1/login"):
            self.assertTrue(valid_health_url(url), url)
        for url in ("http://169.254.169.254:80/latest", "http://8.8.8.8:4000/login", "http://localhost:3000/login",
                    "http://192.168.1.999:3000/login", "http://127.0.0.1:65536/login",
                    "http://127.0.0.1:0/login", "http://127.0.0.1:3000/login?a=1"):
            self.assertFalse(valid_health_url(url), url)

    def test_channel_change_resets_preview_and_survives_restart(self):
        """在临时状态目录测试切换、缓存失效、重启和关闭主机开关，结束清理状态。"""
        def new_agent(allow_switch=True):
            """以正式渠道启动执行器，返回复用临时状态的实例，不修改运行镜像。"""
            return Agent(self.root, self.compose, Releases(file=self.source), allow_switch)

        self.agent = new_agent()
        self.assertIsNone(self.check()["candidate"])
        code, beta = self.agent.submit("channel", {"channel": "beta", "actor": "platform-admin"})
        self.assertEqual((code, beta["channel"], beta["checked"]), (200, "beta", 0))
        self.agent = new_agent()
        self.assertEqual(self.agent.status()["channel"], "beta")
        self.assertEqual(self.agent.state["channel_changed_by"], "platform-admin")
        self.assertEqual(self.check()["candidate"]["version"], self.next["version"])
        code, stable = self.agent.submit("channel", {"channel": "stable", "actor": "platform-admin"})
        self.assertEqual((code, stable["channel"], stable["checked"]), (200, "stable", 0))
        self.assertIsNone(stable["candidate"])
        self.assertEqual(self.agent.submit("apply", {"actor": "admin", "version": self.next["version"],
                                                    "revision": self.next["revision"]}), (409, "invalid_target"))
        locked = new_agent(False)
        self.assertFalse(locked.status()["channel_switch_enabled"])
        self.assertEqual(locked.submit("channel", {"channel": "beta", "actor": "admin"}), (403, "channel_locked"))
        self.assertEqual(self.compose.switched, [])

    def test_channel_change_rejects_busy_invalid_and_failed_write(self):
        """无效输入、操作中或写盘失败都保留原渠道；临时状态与并发线程在用例结束清理。"""
        self.agent = Agent(self.root, self.compose, self.releases, True)
        self.assertEqual(self.agent.submit("channel", {"channel": "custom", "actor": "admin"}), (400, "invalid_channel"))
        self.check()
        self.compose.release.clear()
        self.apply()
        self.assertTrue(self.compose.started.wait(2))
        self.assertEqual(self.agent.submit("channel", {"channel": "stable", "actor": "admin"}), (409, "busy"))
        self.compose.release.set()
        self.await_idle()
        with patch.object(self.agent, "record", side_effect=OSError("read-only state")), self.assertLogs(level="ERROR"):
            self.assertEqual(self.agent.submit("channel", {"channel": "stable", "actor": "admin"}),
                             (503, "update_agent_unavailable"))
        self.assertEqual(self.agent.status()["channel"], "beta")

    def test_concurrent_upgrade_restart_and_manual_rollback(self):
        """拉取被阻塞时拒绝第二项任务。完成版本重开执行器可见，并能使用上一版本回退。"""
        self.assertTrue(self.check()["candidate"]["compatible"])
        self.compose.release.clear()
        self.apply()
        self.assertTrue(self.compose.started.wait(2))
        self.assertEqual(self.agent.submit("check", {"actor": "second"})[0], 409)
        self.compose.release.set()
        self.assertEqual(self.await_idle()["current"], self.next["version"])
        self.agent = Agent(self.root, self.compose, self.releases)
        self.assertEqual(self.agent.status()["previous"], self.base["version"])
        self.assertEqual(self.agent.submit("rollback", {"version": self.base["version"], "actor": "test-admin"})[0], 202)
        status = self.await_idle()
        self.assertEqual(status["current"], self.base["version"])
        self.assertIsNone(status["previous"])

    def test_incompatible_and_stale_target_never_switch(self):
        """不匹配的数据库标识或已改变的发布摘要均在启动前阻止切换。"""
        self.check()
        result = self.agent.submit("apply", {"actor": "test-admin", "version": self.next["version"], "revision": "0" * 40})
        self.assertEqual(result[0], 409)
        save_json(self.source, manifest(2, "b"))
        self.assertFalse(self.check()["candidate"]["compatible"])
        result = self.agent.submit("apply", {"actor": "test-admin", "version": self.next["version"], "revision": self.next["revision"]})
        self.assertEqual(result[0], 409)
        self.assertEqual(self.compose.switched, [])

    def test_legacy_beta_four_can_update_and_roll_back_without_relabeling(self):
        """以已发布 beta.4 标识验证检查、升级及回退使用同一兼容判断，并保留原始镜像身份；临时状态自动清理。"""
        (version, revision, compatibility), = LEGACY_DATABASE_COMPATIBILITY
        self.base = {**manifest(4), "version": version, "revision": revision, "database_compatibility": compatibility}
        self.next = {**manifest(5), "database_compatibility": BASE_DATABASE_COMPATIBILITY}
        self.compose.running = self.base
        save_json(self.source, self.next)
        self.assertTrue(self.check()["candidate"]["compatible"])
        self.apply()
        self.assertEqual(self.await_idle()["current"], self.next["version"])
        self.assertEqual(self.agent.state["previous"], self.base)
        self.assertEqual(self.agent.submit("rollback", {"version": self.base["version"], "actor": "test-admin"})[0], 202)
        self.assertEqual(self.await_idle()["current"], self.base["version"])
        self.assertEqual(self.compose.running, self.base)

    def test_health_failure_and_crash_recovery(self):
        """新镜像启动但不健康即恢复两项服务；模拟进程中断的持久事务也可复原。"""
        self.check()
        self.compose.fail = self.next
        self.apply()
        self.assertEqual(self.await_idle()["error"], "rolled_back")
        self.assertEqual(self.compose.switched, [self.next["version"], self.base["version"]])
        self.agent.record(stage="switching", pending={"prior": self.base, "target": self.next})
        self.compose.running = self.next
        restored = Agent(self.root, self.compose, self.releases).status()
        self.assertEqual(restored["current"], self.base["version"])
        self.assertEqual(restored["error"], "interrupted")

    def test_unrecoverable_disables_later_submissions(self):
        """恢复失败保留现场事务，防止一次后续操作覆盖恢复依据。"""
        self.agent.record(stage="switching", pending={"prior": self.base, "target": self.next})
        self.compose.fail = self.base
        agent = Agent(self.root, self.compose, self.releases)
        self.assertTrue(agent.status()["recovery_required"])
        self.assertEqual(agent.submit("check", {"actor": "test-admin"})[0], 409)

    def test_missing_docker_labels_fail_as_validation(self):
        """验证 Docker 缺失、空或不完整标签返回可处理异常。夹具不运行 Docker，不修改状态。"""
        for labels in (None, {}, [], {"org.opencontainers.image.version": None}):
            with self.subTest(labels=labels), self.assertRaisesRegex(ValueError, "release labels"):
                Compose.release_labels(labels)
        self.assertEqual(Compose.release_labels({
            "org.opencontainers.image.version": self.base["version"][1:],
            "org.opencontainers.image.revision": self.base["revision"],
            "io.xhub.database-compatibility": self.base["database_compatibility"],
        }), (self.base["version"], self.base["revision"], self.base["database_compatibility"]))

    def test_unexpected_failure_releases_operation_even_when_status_write_fails(self):
        """活动任务抛出未预期异常且失败状态暂时无法写盘时，日志可定位、状态可见且允许下一次检查。"""
        def record_failure(path, state):
            """只阻止失败阶段写盘，其他阶段委托真实原子持久化，退出夹具后撤销。"""
            if state.get("stage") == "failed":
                raise OSError("fixture status storage temporarily full")
            save_json(path, state)

        with self.assertLogs("xhub-updater", level="ERROR") as logs:
            with patch.object(self.releases, "latest", side_effect=RuntimeError("fixture lookup failure")), \
                    patch("deploy.update_agent.save_json", side_effect=record_failure):
                result = self.check()
                self.assertEqual((result["stage"], result["error"]), ("failed", "check_failed"))
                self.assertFalse(result["recovery_required"])
        self.assertIn(self.agent.state["operation"]["id"], "\n".join(logs.output))
        self.assertIn("fixture status storage temporarily full", "\n".join(logs.output))
        self.assertEqual(self.check()["candidate"]["version"], self.next["version"])

    def test_healthy_switch_with_failed_completion_is_reconciled_without_rollback(self):
        """已通过健康检查后写盘失败，保留待完成事务。修复介质并重启时验证原摘要，不重新切换。"""
        def save_without_completion(path, state):
            """拒绝写入成功终态，其他写入使用临时目录中的真实持久化。"""
            if state.get("stage") == "succeeded":
                raise OSError("fixture completion write failed")
            save_json(path, state)

        self.check()
        with self.assertLogs("xhub-updater", level="ERROR"), \
                patch("deploy.update_agent.save_json", side_effect=save_without_completion):
            self.apply()
            status = self.await_idle()
            self.assertEqual(status["current"], self.next["version"])
            self.assertTrue(status["recovery_required"])
        self.assertEqual(self.compose.switched, [self.next["version"]])
        self.assertEqual(self.agent.submit("check", {"actor": "test-admin"})[0], 409)
        self.assertEqual(self.agent.state["pending"]["action"], "apply")
        self.agent = Agent(self.root, self.compose, self.releases)
        status = self.agent.status()
        self.assertEqual((status["stage"], status["current"], status["previous"]),
                         ("succeeded", self.next["version"], self.base["version"]))
        self.assertFalse(status["recovery_required"])
        self.assertEqual(self.compose.switched, [self.next["version"]])

    def test_bad_state_is_visible_without_replacing_transaction_evidence(self):
        """状态文件无法解析时执行器只读地显示待恢复并拒绝新操作，原文件由临时目录清理。"""
        state_file = self.root / "state.json"
        state_file.write_text("{invalid")
        with self.assertLogs("xhub-updater", level="ERROR"):
            agent = Agent(self.root, self.compose, self.releases)
        self.assertTrue(agent.status()["recovery_required"])
        self.assertEqual(agent.submit("check", {"actor": "test-admin"})[0], 409)
        self.assertEqual(state_file.read_text(), "{invalid")
        broken = {"stage": "switching", "pending": {"prior": False}, "operation": "invalid"}
        save_json(state_file, broken)
        with self.assertLogs("xhub-updater", level="ERROR") as logs:
            agent = Agent(self.root, self.compose, self.releases)
        self.assertIn("operation=none", "\n".join(logs.output))
        self.assertTrue(agent.status()["recovery_required"])
        self.assertEqual(agent.state["pending"], broken["pending"])


if __name__ == "__main__":
    unittest.main()
