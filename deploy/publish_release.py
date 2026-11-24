"""发布并恢复已验证的 XHub 标签版本。现有发布的镜像引用只读。"""

import argparse
import json
import os
from pathlib import Path
import subprocess
from tempfile import TemporaryDirectory

if __package__:
    from .update_agent import Compose
    from .update_manifest import MANIFEST_NAME, SERVICES, validate_manifest, version_key
else:
    from update_agent import Compose
    from update_manifest import MANIFEST_NAME, SERVICES, validate_manifest, version_key

IMAGES_NAME = "docker-images.txt"


class Publisher:
    """协调 GitHub Release 和已推送的 OCI 镜像，只在发布完整时公开草稿。"""

    def __init__(self, repo, version):
        """保存工作流的仓库和标签，拒绝无效版本，派生固定镜像命名空间。"""
        self.repo, self.version = repo, version
        self.prerelease = version_key(version)[3] < 3
        self.namespace = "ghcr.io/" + repo.lower()

    def release(self):
        """列举发布以查找当前标签（含草稿）。只有成功查询无结果时才允许构建镜像。"""
        pages = json.loads(subprocess.check_output([
            "gh", "api", "--paginate", "--slurp", f"repos/{self.repo}/releases?per_page=100"
        ], timeout=90))
        matches = [release for page in pages for release in page if release["tag_name"] == self.version]
        if len(matches) > 1:
            raise ValueError("multiple releases for this tag")
        return matches[0] if matches else None

    def assets(self, release):
        """索引两个允许的附件，供计划和续跑使用。重复附件或未知上传状态会阻止发布。"""
        assets = {}
        for asset in release["assets"]:
            name = asset["name"]
            if name not in (MANIFEST_NAME, IMAGES_NAME):
                continue
            if (name in assets or asset["state"] not in ("uploaded", "starter") or
                    type(asset["id"]) is not int or asset["id"] <= 0):
                raise ValueError("unexpected release asset")
            assets[name] = asset
        return assets

    def plan(self):
        """返回是否需要构建、是否需登录镜像仓库。已有清单的版本可离线验证并补齐附件。"""
        release = self.release()
        build = release is None
        manifest = self.assets(release).get(MANIFEST_NAME) if release is not None else None
        return {"build": build, "registry": not manifest or manifest["state"] != "uploaded"}

    def asset_path(self, asset):
        """返回 GitHub API 的固定附件路径，上传错误后的删除也复用经过校验的整数 ID。"""
        return f"repos/{self.repo}/releases/assets/{asset['id']}"

    def download(self, asset):
        """下载受 GitHub 权限保护的固定附件，限制清单和摘要文本大小。"""
        if asset.get("size", 0) > 16 << 10:
            raise ValueError("release asset exceeds size limit")
        raw = subprocess.check_output([
            "gh", "api", "-H", "Accept: application/octet-stream", self.asset_path(asset)
        ], timeout=60)
        if len(raw) > 16 << 10:
            raise ValueError("release asset exceeds size limit")
        return raw

    def recover_images(self, references, revision, compatibility):
        """验证不完整发布引用的两份镜像及元数据，返回其不可变摘要，完全不重新构建或推送。"""
        images = {}
        for service in SERVICES:
            namespace = f"{self.namespace}-{service}"
            reference = references[service] if references else f"{namespace}:{self.version[1:]}"
            subprocess.run(["docker", "pull", "--platform", "linux/amd64", reference], check=True, timeout=180)
            inspected = json.loads(subprocess.check_output(["docker", "image", "inspect", reference], timeout=35))
            if len(inspected) != 1:
                raise ValueError("expected one release image")
            image = inspected[0]
            identity = Compose.release_labels(image["Config"].get("Labels"))
            if identity != (self.version, revision, compatibility):
                raise ValueError("existing release image identity differs")
            digests = [digest for digest in image.get("RepoDigests") or [] if digest.startswith(namespace + "@")]
            if references and reference in digests:
                images[service] = reference
            elif not references and len(digests) == 1:
                images[service] = digests[0]
            else:
                raise ValueError("cannot determine existing release image digest")
        return images

    def manifest(self, revision, compatibility, images):
        """构造并校验版本、提交、兼容标识以及固定命名空间中的两份镜像引用。"""
        return validate_manifest({"schema": 1, "version": self.version, "revision": revision,
                                  "database_compatibility": compatibility, "images": images}, self.namespace)

    def release_command(self, operation, *arguments):
        """对当前标签执行 GitHub Release 命令。子进程失败由 CI 报告，保留草稿及既有附件。"""
        subprocess.run(["gh", "release", operation, self.version, "--repo", self.repo, *arguments],
                       check=True, timeout=90)

    def publish(self, revision, compatibility, built):
        """发布本次镜像，或校验并补齐已有版本。已上传文件保持原样，重跑只添加所缺附件。"""
        release = self.release()
        assets = self.assets(release) if release is not None else {}
        uploaded = {name: asset for name, asset in assets.items() if asset["state"] == "uploaded"}
        images_text = self.download(uploaded[IMAGES_NAME]).decode() if IMAGES_NAME in uploaded else None
        if release is None:
            images = {service: f"{self.namespace}-{service}@{built[service]}" for service in SERVICES}
            manifest = self.manifest(revision, compatibility, images)
        else:
            if release["prerelease"] != self.prerelease:
                raise ValueError("release channel differs from tag")
            if MANIFEST_NAME in uploaded:
                manifest = validate_manifest(json.loads(self.download(uploaded[MANIFEST_NAME])), self.namespace)
                if manifest != self.manifest(revision, compatibility, manifest["images"]):
                    raise ValueError("existing manifest differs from this release")
            else:
                references = None
                if images_text is not None:
                    lines = images_text.splitlines()
                    if len(lines) != len(SERVICES):
                        raise ValueError("existing image list does not identify both services")
                    references = self.manifest(revision, compatibility, dict(zip(SERVICES, lines)))["images"]
                manifest = self.manifest(revision, compatibility,
                                         self.recover_images(references, revision, compatibility))
        expected_images = [manifest["images"][service] for service in SERVICES]
        if images_text is not None and images_text.splitlines() != expected_images:
            raise ValueError("existing image list differs from the manifest")

        prerelease = ("--prerelease", "--latest=false") if self.prerelease else ()
        if release is None:
            # 先建草稿，逐个挂载资产。只有两份附件成功才公开，进程中断可以独立续跑。
            self.release_command("create", "--draft", "--verify-tag", "--title", self.version, "--generate-notes", *prerelease)
        candidates = {MANIFEST_NAME: json.dumps(manifest, indent=2) + "\n",
                      IMAGES_NAME: "\n".join(expected_images) + "\n"}
        with TemporaryDirectory(prefix="xhub-release-") as directory:
            for name, content in candidates.items():
                if name in uploaded:
                    continue
                if name in assets:
                    # GitHub 将失败的上传保留为无内容的 starter。仅清理这种占位附件。
                    subprocess.run(["gh", "api", "--method", "DELETE", self.asset_path(assets[name])],
                                   check=True, timeout=60)
                path = Path(directory) / name
                path.write_text(content, encoding="utf-8")
                self.release_command("upload", str(path))
        if release is None or release["draft"]:
            self.release_command("edit", "--draft=false", *prerelease)


def main():
    """以工作流环境为输入打印构建计划，或以已推送摘要发布版本。参数错误使步骤失败。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("plan", "publish"))
    args = parser.parse_args()
    publisher = Publisher(os.environ["GITHUB_REPOSITORY"], os.environ["VERSION"])
    if args.operation == "plan":
        for name, enabled in publisher.plan().items():
            print(f"{name}={str(enabled).lower()}")
        return
    publisher.publish(os.environ["RELEASE_SHA"], os.environ["DATABASE_COMPATIBILITY"],
                      {service: os.environ.get(service.upper() + "_DIGEST", "") for service in SERVICES})


if __name__ == "__main__":
    main()
