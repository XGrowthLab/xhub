# Docker and version publishing

Workflow: .github/workflows/release.yml. Pull requests run verification. Pushes to main (and manual runs on main) publish development images after verification. Version tags publish versioned images and create GitHub Releases.

Images: ghcr.io/qiniu/xhub-gateway and ghcr.io/qiniu/xhub-console, supporting Linux amd64. Main builds receive main and sha tags. Stable versions receive version, sha, and latest tags. Alpha, beta, and rc versions do not update latest. Releases have generated notes and a docker-images.txt attachment with immutable digests.

Enable Actions and allow workflows to create Releases and write Packages. Authentication uses GITHUB_TOKEN; no Docker Hub credentials are needed. For anonymous pulls, set newly created GHCR packages to public. Organization policies may restrict permissions.

After committing and pushing the workflow, tag the commit to release:

    git tag v1.0.0
    git push origin v1.0.0

Supported versions: vMAJOR.MINOR.PATCH and vMAJOR.MINOR.PATCH-alpha.N / beta.N / rc.N, without leading zeros. A tag triggers publishing; the workflow does not choose the next version. Both images must be pushed before creating the Release. Reruns update the digest attachment.

Verification runs internal Go unit tests, workflow tests, build helper tests, real PostgreSQL backend regression tests, and browser E2E for administrator login and key rotation (old key rejected, new key inference succeeds, log identity retained). E2E uses a local upstream and an isolated schema cleaned up afterward. The release-test-results artifact retains browser JSON, JUnit, HTML, and failure evidence.

For deployment, replace gateway and console image values in Compose with published images and remove build entries. Keep PostgreSQL, Redis, and the gateway service name. Console listens on 3000 and gateway on 4000. Configure XHUB_PUBLIC_ORIGIN for remote deployments so browsers can reach the public gateway:

    docker pull ghcr.io/qiniu/xhub-gateway:1.0.0
    docker pull ghcr.io/qiniu/xhub-console:1.0.0

Actual GHCR writes and Release creation must be verified on GitHub. Local fixtures do not publish resources. Existing local Dockerfile and deploy/build.sh usage is preserved; CI uses deploy/Dockerfile.release for the console to preserve the Next.js standalone directory layout.
Record rollback images and verify an independent database recovery point before scheduling backup rotation. [Deployment backup inventory and rotation](deployment-backup-rotation.md)
