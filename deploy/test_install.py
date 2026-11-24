"""验证标准发布安装的默认配置、首装选择和重复运行。"""

import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch

from deploy.install import configure, first_release, install_config, protected_directory
from deploy.test_update_agent import manifest


class InstallTests(unittest.TestCase):
    def test_stable_preferred_and_prerelease_bootstrap(self):
        """正式版优先。只有预发布时可以首装；发布为空时明确拒绝。用例不访问 GitHub。"""
        preview = {"manifest": manifest(2)}
        stable = {"manifest": {**manifest(3), "version": "v0.1.0"}}
        with patch("deploy.install.Releases") as releases:
            releases.return_value.latest.return_value = stable
            self.assertEqual(first_release(False), stable["manifest"])
            releases.assert_called_once_with(beta=False)
            releases.reset_mock()
            releases.return_value.latest.side_effect = [None, preview]
            self.assertEqual(first_release(False), preview["manifest"])
            self.assertEqual([call.kwargs["beta"] for call in releases.call_args_list], [False, True])
            releases.return_value.latest.side_effect = None
            releases.return_value.latest.return_value = None
            with self.assertRaisesRegex(ValueError, "installable manifest"):
                first_release(True)

    def test_release_config(self):
        """渲染首装的发布摘要、主机回环端口和仅网关可用的只读 IPC 挂载，无容器副作用。"""
        config = {
            "name": "source",
            "services": {
                "gateway": {"build": {"context": "."}, "environment": {"EXISTING": "value"},
                            "ports": [{"target": 4000}], "volumes": []},
                "console": {"build": {"context": "."}},
                "redis": {"image": "redis:7-alpine"},
            },
        }
        images = manifest(2)["images"]
        services = install_config(config, images, Path("/run/xhub-updater"))["services"]
        gateway = services["gateway"]
        self.assertNotIn("name", config)
        self.assertEqual(gateway["image"], images["gateway"])
        self.assertEqual(services["console"]["image"], images["console"])
        self.assertEqual(gateway["environment"], {"EXISTING": "value", "XHUB_UPDATE_SOCKET": "/run/xhub-updater/agent.sock"})
        self.assertEqual(gateway["volumes"], [{"type": "bind", "source": "/run/xhub-updater", "target": "/run/xhub-updater",
                                              "read_only": True, "bind": {"create_host_path": False}}])
        self.assertEqual(gateway["ports"][0]["host_ip"], "127.0.0.1")
        self.assertNotIn("volumes", services["console"])
        for service in ("gateway", "console"):
            self.assertNotIn("build", services[service])

    def test_configure_resumes_without_overwriting_deployment(self):
        """在临时目录首装、重试和恢复缺失配置，保留运维自定义与回退状态，结束清理文件。"""
        with TemporaryDirectory() as local, patch("deploy.install.Compose") as compose, \
             patch("deploy.install.first_release", return_value=manifest(2)) as release:
            root = Path(local)
            target, state, runtime = (root / name for name in ("target", "state", "runtime"))
            for folder in (target, state, runtime):
                protected_directory(folder, 0o700)
            docker = compose.return_value
            docker.run.return_value = ""
            docker.compose.return_value = json.dumps({"services": {"gateway": {"environment": {}, "volumes": []}, "console": {}}})
            path = configure(root, target, state, runtime, False, use_release=True)
            original = path.read_bytes()
            path.write_text("# local customization")
            rollback = state / "compose.override.json"
            rollback.write_text("preserve")
            self.assertEqual(configure(root, target, state, runtime, False, use_release=True).read_text(), "# local customization")
            self.assertEqual(rollback.read_text(), "preserve")
            path.unlink()
            self.assertEqual(configure(root, target, state, runtime, False, use_release=True).read_bytes(), original)
            with self.assertRaisesRegex(ValueError, "original install directory and options"):
                configure(root, target, state, runtime, True, use_release=True)
            release.assert_called_once_with(False)

    def test_source_install_uses_local_images_without_github(self):
        """默认安装检查本地网关与控制台镜像，挂载更新 socket，不读发布源，结束删除临时配置。"""
        with TemporaryDirectory() as local, patch("deploy.install.Compose") as compose, \
             patch("deploy.install.first_release") as release:
            root = Path(local)
            docker = compose.return_value
            docker.run.return_value = ""
            docker.compose.return_value = json.dumps({"services": {
                "gateway": {"image": "xhub-gateway", "environment": {}, "volumes": []},
                "console": {"image": "xhub-console"},
            }})
            file = configure(root, root, root, root, False)
            services = json.loads(file.read_text())["services"]
            self.assertEqual(services["gateway"]["image"], "xhub-gateway")
            self.assertNotIn("platform", services["gateway"])
            self.assertEqual([call.args[0] for call in docker.image.call_args_list], ["xhub-gateway", "xhub-console"])
            release.assert_not_called()
            docker.prepare.assert_not_called()

    def test_existing_compose_and_unsafe_directory_are_refused(self):
        """已有自建部署和非受控目录被拒绝，运维配置保持原样，临时目录自动清理。"""
        with TemporaryDirectory() as local:
            target = Path(local)
            existing = target / "docker-compose.yml"
            existing.write_text("# managed by operator")
            with self.assertRaisesRegex(ValueError, "existing deployment"):
                configure(target, target, target, target, False)
            self.assertEqual(existing.read_text(), "# managed by operator")
            link = target / "link"
            link.symlink_to(target, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, "Unsafe deployment directory"):
                protected_directory(link, 0o700)
