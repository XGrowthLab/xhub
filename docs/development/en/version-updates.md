# Version updates and rollback

A platform administrator can open **Administrator settings → Version updates** to check for releases. The page shows the running version, release channel, target and release notes. After confirmation, a separate host service verifies and switches the gateway and console images. Reopen the page to resume monitoring.

## Stable and beta releases

Tag a reviewed commit that has passed checks. Stable tags use three components, such as v0.1.0. The first test release is v0.1.0-beta.1, with beta.2 and later tags available for follow-up testing.

A tag invokes the existing release gates and image builds. GitHub Releases attach docker-images.txt and xhub-release.json. The manifest binds the commit, both image digests, and a conservative database compatibility identifier. Beta tags do not advance latest. Publish a new version if a manifest needs to change.

## Enable the host service

Use Python 3.10+, Docker Compose v2 and a matched tagged image pair. The feature is opt-in. For an older or mixed deployment, first install a matched release manually.

1. Install deploy/update_agent.py and deploy/update_manifest.py together in a host administrator controlled directory such as /opt/xhub-updater.
2. Adapt deploy/xhub-updater.service.example to your Compose path, project name and health URLs. The updater reads the configured GitHub project. A protected EnvironmentFile may provide a read-only XHUB_RELEASE_TOKEN for a private repository. Registry credentials belong to host Docker.
3. Set XHUB_UPDATE_SOCKET=/run/xhub-updater/agent.sock in the gateway. Mount the directory /run/xhub-updater read-only at the same path and allow the gateway process to connect to the socket. The directory mount reconnects to the socket after a service restart.
4. Start the host service and recreate the gateway once. Stable is the default. Add --allow-beta to ExecStart on beta test instances. Host administrators can supply --namespace and --manifest-file for an isolated or offline image mirror.

The default health URLs are http://127.0.0.1:4000/health/readiness and http://127.0.0.1:3000/login. Pass two --health-url arguments when the local ports differ. Only the host service controls Docker. This workflow supports a single gateway and console in the configured project.

## Switch and recover

The updater downloads and verifies both images before the switch. Gateway shutdown waits up to one minute for active HTTP requests and streams. A single gateway can still be briefly unavailable. Compose starts only the two application services with --no-deps. The host keeps a recovery record and attempts to restore the previous images if startup or health checks fail or an operation is interrupted.

The service manages compose.override.json in its state directory. Include this file after the base Compose file in subsequent manual Compose operations. Retain the previous images while their rollback point is in use. Keep the state.json record restricted to deployment administrators.

This version requires matching database identifiers. The fingerprint conservatively covers startup and persistent store source files and can also block some changes that do not modify the schema. Use the ordinary host release procedure after verifying compatibility or planning a migration. See [deployment backup inventory and rotation](deployment-backup-rotation.md).
