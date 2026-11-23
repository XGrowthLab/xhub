"""更新清单和事务状态的确定性测试。Docker 实际替换由浏览器回归另行验证。"""

from pathlib import Path
from tempfile import TemporaryDirectory
import threading
import time
import unittest

from deploy.update_manifest import MANIFEST_NAME, NAMESPACE, validate_manifest, version_key
from deploy.update_agent import ACTIVE, Agent, Releases, save_json


def manifest(number, compatibility="a"):
    """创建有效的合成版本发布，对应两个不同镜像仓库，无网络调用。"""
    return {"schema": 1, "version": f"v0.1.0-beta.{number}", "revision": str(number) * 40,
            "database_compatibility": compatibility * 64,
            "images": {service: f"{NAMESPACE}-{service}@sha256:" + str(number) * 64
                       for service in ("gateway", "console")}}


class Runtime:
    """用独立内存镜像表示 Docker 边界，记录调用和可控失败。"""

    def __init__(self, current):
        """以当前镜像开始，准备阶段可由测试阻塞，启动失败由单个目标配置。"""
        self.running = current
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


if __name__ == "__main__":
    unittest.main()
