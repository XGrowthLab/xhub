# Version updates and rollback

A platform administrator can open **Administrator settings → Version updates** to check for releases. Standard installations let administrators choose the stable or beta channel on the same page. Check again after switching to see releases for that channel. After confirmation, a separate host service verifies and switches the gateway and console images. Reopen the page to resume monitoring.

**Current version** shows the running release when it has a verified rollback image pair. Images with a shared build revision show that commit, for example `main · e107582`. You can check the latest release in the channel and install a matching release pair on the host before using in-console updates.

## Stable and beta releases

Tag a reviewed commit that has passed checks. Stable tags use three components, such as v0.1.0. The first test release is v0.1.0-beta.1, with beta.2 and later tags available for follow-up testing.

A tag invokes the existing release gates and image builds. GitHub Releases attach docker-images.txt and xhub-release.json. The manifest binds the commit, both image digests, and a conservative database compatibility identifier. Beta tags do not advance latest. Publish a new version if a manifest needs to change.

## Enable the host service

The standard Linux installation enables the systemd service automatically. Source builds can check for releases. A matching published image pair and access to GHCR are required for the console to switch images and retain a verified rollback point. See the [installation guide](../../getting-started.md#install-on-linux-with-version-checks).

For an existing or customized deployment, connect the executor to the running project:

1. Install deploy/update_agent.py and deploy/update_manifest.py together in a host administrator controlled directory such as /opt/xhub-updater.
2. Adapt deploy/xhub-updater.service.example to your Compose path, project name and health URLs. The updater reads Releases from XGrowthLab/xhub. A protected EnvironmentFile may provide a read-only XHUB_RELEASE_TOKEN for a private repository. Registry credentials belong to host Docker.
3. Set XHUB_UPDATE_SOCKET=/run/xhub-updater/agent.sock in the gateway. Mount the directory /run/xhub-updater read-only at the same path and allow the gateway process to connect to the socket. The directory mount reconnects to the socket after a service restart.
4. Start the host service and recreate the gateway once. Stable is the default. Use `--allow-beta` to start a new instance on the beta channel. On an existing host, add `--enable-channel-switch` to ExecStart, reload systemd and restart the updater to let administrators save the channel from the console. Standard installations include this argument. Removing the argument fixes the channel to the host settings. Host administrators can supply --namespace and --manifest-file for an isolated or offline image mirror.

The default health URLs are http://127.0.0.1:4000/health/readiness and http://127.0.0.1:3000/login. Pass two --health-url arguments when the local ports or private IPv4 bindings differ. Only the host service controls Docker. This workflow supports a single gateway and console in the configured project.

## Switch and recover

The updater downloads and verifies both images before the switch. Gateway shutdown waits up to one minute for active HTTP requests and streams. A single gateway can still be briefly unavailable. Compose starts only the two application services with --no-deps. If startup or health checks fail, the updater attempts to restore the previous images. After a restart it checks any recorded switch against the actual image digests and service health. A healthy target can complete as planned, while an unhealthy or incomplete switch triggers recovery. If recovery still needs attention, the updater retains the transaction and blocks new operations until it can reconcile the deployment. The service journal records failures and operation IDs.

The standard installation uses /opt/xhub/docker-compose.yml. For a source deployment update, rebuild both images and recreate the application services with this file. The service manages compose.override.json in its state directory after the first in-console image switch. Include this file after the base Compose file in subsequent manual Compose operations. Retain the previous images while their rollback point is in use. Keep the state.json record restricted to deployment administrators.

Console updates require matching database compatibility identifiers. Release CI fingerprints table and migration SQL, Go migrations, ORM table definitions, startup code, storage formats, and dependency locks. Ordinary changes to log queries keep the same identifier. Put new migrations in a `migrations` directory or name them `*_migration.go`. Increment `DATABASE_FORMAT_VERSION` in `deploy/update_manifest.py` when introducing an incompatible stored format outside the fingerprinted sources. Use the host release procedure when the identifier changes. See [deployment backup inventory and rotation](deployment-backup-rotation.md).

### Updating from beta.3 or beta.4

The fixed release keeps the `beta.3` compatibility identifier. On `beta.3`, check the beta channel and select **Update now** when the fixed release appears. Installations running `beta.4` with an older host updater need a one-time updater refresh. While the updater is idle, run these commands from the fixed release source directory. With a custom installation, use the directory from the systemd unit's `ExecStart`.

```sh
updater_dir=/opt/xhub/updater
sudo install -m 0644 deploy/update_manifest.py deploy/update_agent.py "$updater_dir/" &&
  sudo systemctl restart xhub-updater.service
```

The updater refresh preserves the deployed images, Compose configuration, and database. Check for the release again in the console after the service restarts.
