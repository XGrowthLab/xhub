"""发布恢复路径的确定性测试，模拟 GitHub 及 GHCR API 的命令边界。"""

import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

from deploy.publish_release import IMAGES_NAME, MANIFEST_NAME, Publisher
from deploy.update_manifest import COMPAT_LABEL


class PublishTests(unittest.TestCase):
    """用合成 Release、镜像和错误响应检验写入顺序，测试退出时全部恢复。"""

    def setUp(self):
        """设置唯一发布、两个 OCI 变种和替代命令，所有下载、上传及容器命令由内存夹具记录。"""
        self.publisher = Publisher("Owner/xhub", "v0.1.0-beta.1")
        self.revision = "a" * 40
        self.compatibility = "b" * 64
        self.digests = {service: "sha256:" + digest * 64 for service, digest in (("gateway", "c"), ("console", "d"))}
        self.images = {service: f"ghcr.io/owner/xhub-{service}@{digest}" for service, digest in self.digests.items()}
        self.manifest = self.publisher.manifest(self.revision, self.compatibility, self.images)
        self.image_list = ("\n".join(self.images.values()) + "\n").encode()
        self.release = []
        self.commands = []
        self.files = {}
        reader = patch("deploy.publish_release.subprocess.check_output", side_effect=self.read_command)
        writer = patch("deploy.publish_release.subprocess.run", side_effect=self.write_command)
        for replacement in (reader, writer):
            replacement.start()
            self.addCleanup(replacement.stop)

    def read_command(self, command, **_):
        """返回仓库、附件和镜像的合成读取结果；测试覆盖页面分隔、文件与 OCI 元数据。"""
        if command[:2] == ["gh", "api"]:
            if "/releases/assets/" not in command[-1]:
                return json.dumps([[], self.release]).encode()
            return {51: json.dumps(self.manifest).encode(), 52: self.image_list}[int(command[-1].split("/")[-1])]
        if command[:3] == ["docker", "image", "inspect"]:
            digest = self.images["gateway" if "-gateway" in command[-1] else "console"]
            return json.dumps([{"RepoDigests": [digest], "Config": {"Labels": {
                "org.opencontainers.image.version": self.publisher.version[1:],
                "org.opencontainers.image.revision": self.revision,
                COMPAT_LABEL: self.compatibility,
            }}}]).encode()
        self.fail(f"unexpected read command {command}")

    def write_command(self, command, **_):
        """记录发布、副本恢复或镜像拉取；同步读取临时文件用于断言，发布器负责清理。"""
        self.commands.append(command)
        if command[:3] == ["gh", "release", "upload"]:
            path = Path(command[-1])
            self.files[path.name] = path.read_bytes()

    def existing(self, assets, *, draft=False):
        """在内存加入一个指定附件状态的既有发布，可模拟等待发布的草稿。"""
        ids = {MANIFEST_NAME: 51, IMAGES_NAME: 52}
        self.release = [{"tag_name": self.publisher.version, "draft": draft, "prerelease": True,
                         "assets": [{"name": name, "id": ids[name], "state": state} for name, state in assets]}]

    def test_new_release_creates_draft_then_publishes_complete_assets(self):
        """首次发布按序创建草稿、上传两个摘要附件、公开预发布。文件及子进程均隔离。"""
        self.assertEqual(self.publisher.plan(), {"build": True, "registry": True})
        self.publisher.publish(self.revision, self.compatibility, self.digests)
        self.assertEqual([command[2] for command in self.commands], ["create", "upload", "upload", "edit"])
        self.assertIn("--draft", self.commands[0])
        self.assertIn("--verify-tag", self.commands[0])
        self.assertIn("--prerelease", self.commands[-1])
        self.assertIn("--latest=false", self.commands[-1])
        self.assertEqual(json.loads(self.files[MANIFEST_NAME]), self.manifest)
        self.assertEqual(self.files[IMAGES_NAME], self.image_list)

    def test_existing_release_does_not_rebuild_or_mutate(self):
        """两份附件一致的正式发布记录可无副作用重跑，忽略本次运行未产生的镜像摘要。"""
        self.existing([(MANIFEST_NAME, "uploaded"), (IMAGES_NAME, "uploaded")])
        self.assertEqual(self.publisher.plan(), {"build": False, "registry": False})
        self.publisher.publish(self.revision, self.compatibility, {})
        self.assertEqual(self.commands, [])

    def test_draft_completes_only_missing_attachment_after_interrupted_upload(self):
        """草稿已有清单且另一个上传中断，只清理空附件、补摘要并公开，不覆盖清单。"""
        self.existing([(MANIFEST_NAME, "uploaded"), (IMAGES_NAME, "starter")], draft=True)
        self.assertEqual(self.publisher.plan(), {"build": False, "registry": False})
        self.publisher.publish(self.revision, self.compatibility, {})
        self.assertEqual([command[2] for command in self.commands], ["--method", "upload", "edit"])
        self.assertTrue(self.commands[0][-1].endswith("/assets/52"))
        self.assertEqual(self.files, {IMAGES_NAME: self.image_list})

    def test_missing_manifest_recovers_pinned_or_existing_tagged_images(self):
        """旧流程遗留不完整发布时核实固定摘要或镜像版本标签，补附件且始终不重推。"""
        for assets in ([(IMAGES_NAME, "uploaded")], []):
            with self.subTest(assets=assets):
                self.commands.clear()
                self.files.clear()
                self.existing(assets)
                self.assertEqual(self.publisher.plan(), {"build": False, "registry": True})
                self.publisher.publish(self.revision, self.compatibility, {})
                refs = self.images.values() if assets else [f"ghcr.io/owner/xhub-{s}:0.1.0-beta.1" for s in self.images]
                self.assertEqual([command[-1] for command in self.commands if command[:2] == ["docker", "pull"]], list(refs))
                self.assertTrue(all(command[:2] in (["docker", "pull"], ["gh", "release"]) for command in self.commands))
                self.assertEqual(json.loads(self.files[MANIFEST_NAME]), self.manifest)
                self.assertEqual(IMAGES_NAME in self.files, not assets)

    def test_mismatched_release_and_api_outages_fail_before_writes(self):
        """清单提交、两个附件或镜像标识不一致均禁止覆盖；GitHub 查询失败时禁止开始构建。"""
        self.existing([(MANIFEST_NAME, "uploaded"), (IMAGES_NAME, "uploaded")])
        with self.assertRaisesRegex(ValueError, "manifest differs"):
            self.publisher.publish("e" * 40, self.compatibility, {})
        self.image_list = self.image_list.replace(b"c" * 64, b"e" * 64)
        with self.assertRaisesRegex(ValueError, "image list differs"):
            self.publisher.publish(self.revision, self.compatibility, {})
        self.assertEqual(self.commands, [])
        self.existing([])
        with self.assertRaisesRegex(ValueError, "image identity differs"):
            self.publisher.publish("e" * 40, self.compatibility, {})
        self.assertTrue(all(command[:2] == ["docker", "pull"] for command in self.commands))
        with patch.object(self.publisher, "release", side_effect=subprocess.CalledProcessError(1, "gh")), \
                self.assertRaises(subprocess.CalledProcessError):
            self.publisher.plan()

    def test_partial_upload_leaves_release_draft(self):
        """第一个附件上传被拒绝时停止，不公开草稿。替代命令与临时文件均在测试后清理。"""
        with patch.object(self.publisher, "release_command", side_effect=[None, OSError("upload failed")]) as command, \
                self.assertRaisesRegex(OSError, "upload failed"):
            self.publisher.publish(self.revision, self.compatibility, self.digests)
        self.assertEqual([call.args[0] for call in command.call_args_list], ["create", "upload"])
