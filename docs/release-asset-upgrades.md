# Release-asset container upgrades

Container-mode deployments (`-upgrade-image` with `-upgrade-image-ref-file`) can load candidate daemon and sandbox images directly from gzip-compressed `docker save` tarballs attached to a GitHub or GitHub Enterprise repository's own releases (`release-assets`), alongside traditional OCI registry pulls (`registry`).

Loading from repository releases avoids requiring separate cloud container-registry login helpers on the daemon host: release metadata and asset tarballs are fetched using the deployment's existing git connection for that forge host (`Settings -> Connections`).

## Default behavior and fallback

When `-upgrade-image-source` (`GRAIN_UPGRADE_IMAGE_SOURCE`) is unset:

1. Upgrades default to `release-assets`, resolving the release repository from any edition-registered track default (`upgrade.RegisterTrackDefaults`) or mapping `ghcr.io/<owner>/<repo>/...` to `github.com/<owner>/<repo>`.
2. If the target release does not exist yet or carries no `images.json` manifest asset, the upgrade automatically falls back to `docker pull` from the configured container registry (`-upgrade-image`) and records:
   `release has no image assets; pulled from <registry>`
   in the upgrade status, on [Settings -> Upgrade](/settings/upgrade), and in the post-upgrade check task.
3. Integrity failures (an invalid `images.json`, a missing `.sha256` sidecar, a SHA-256 mismatch, a Docker image ID mismatch, or a failed `-upgrade-verify-cmd`) **never** fall back silently.

Pinning `-upgrade-image-source=release-assets` (or registering `Source: upgrade.ImageSourceReleaseAssets` via `upgrade.RegisterTrackDefaults` for an edition) disables the automatic missing-manifest fallback unless `-upgrade-image-fallback=registry` is explicitly configured. Pinning `-upgrade-image-source=registry` skips release lookups and always runs `docker pull` (with optional `-upgrade-image-fallback=release-assets`).

## Daemon flags and environment variables

| Flag                             | Environment variable                   | Description                                                                                                                                                                                                                  |
| -------------------------------- | -------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-upgrade-image-source`          | `GRAIN_UPGRADE_IMAGE_SOURCE`           | `release-assets` or `registry`. Empty defaults to `release-assets` with missing-manifest fallback to `registry`. Passing either the flag or env var locks the setting read-only in [Settings -> Upgrade](/settings/upgrade). |
| `-upgrade-image-fallback`        | `GRAIN_UPGRADE_IMAGE_FALLBACK`         | Optional fallback source when the primary source cannot fetch a candidate daemon image: `none`, `registry`, or `release-assets`.                                                                                             |
| `-upgrade-release-repo`          | `GRAIN_UPGRADE_RELEASE_REPO`           | Release repository as `<host>/<owner>/<name>` (for example `github.com/ai-on-gke/grain` or `ghe.example.com/org/grain`).                                                                                                     |
| `-upgrade-release-asset`         | `GRAIN_UPGRADE_RELEASE_ASSET`          | Explicit daemon tarball asset filename when `images.json` is not used (for example `grain-image.tar.gz`).                                                                                                                    |
| `-upgrade-release-checksum`      | `GRAIN_UPGRADE_RELEASE_CHECKSUM`       | Checksum sidecar asset name, or suffix starting with `.` (defaults to `.sha256`).                                                                                                                                            |
| `-upgrade-sandbox-release-asset` | `GRAIN_UPGRADE_SANDBOX_RELEASE_ASSETS` | Repeatable `<role-or-repo>=<asset>` override for sandbox images (comma-separated in the env var), such as `guest=grain-guest.tar.gz`.                                                                                        |

When `-upgrade-image-source` / `GRAIN_UPGRADE_IMAGE_SOURCE` is not passed on the command line or environment, administrators can also inspect and switch the source live on [Settings -> Upgrade](/settings/upgrade) (`PUT /api/upgrade/source`, persisted in `<data-dir>/upgrade-image-source.json`).

## Release tag convention and branch ref ambiguity

Moving channels use `channel-<tag>` release tags (`channel-main`, `channel-candidate`, `channel-<branch-with-..>`) alongside immutable `sha-<short>` and promoted `vX.Y.Z` / `latest` releases.

Publishing a release whose git tag has the same bare name as a branch (such as `main`) creates `refs/tags/main` next to `refs/heads/main`, which is an ambiguous ref in `git` and forge CLI ref resolvers. To avoid this ambiguity:

- `pkg/upgrade` never resolves release tags via `git` refs; it queries the REST endpoint `/repos/<owner>/<name>/releases/tags/<tag>` directly.
- For branch channels, `ReleaseTagsForBranch` looks up `channel-<tag>` first and only falls back to `<tag>` on HTTP `404` for compatibility with older releases.

## `images.json` v1 contract

Each release carrying container tarballs attaches `images.json` plus `<asset>` and `<asset>.sha256` (`sha256sum` format: `<64-hex>  <filename>`):

```json
{
  "schema": 1,
  "repository": "github.com/ai-on-gke/grain",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "version": "v1.2.3",
  "createdAt": "2026-11-04T00:00:00Z",
  "images": {
    "grain": {
      "asset": "grain-image.tar.gz",
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "imageId": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
      "ref": "grain:v1.2.3"
    },
    "guest": {
      "asset": "grain-guest.tar.gz",
      "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
      "imageId": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
      "ref": "grain-guest:v1.2.3"
    }
  }
}
```

### Verification and staging pipeline

1. **Unchanged short-circuit:** Before downloading any `.tar.gz`, `AlreadyRunning` and `stageImage` compare the running build's commit, image ID, and recorded tarball SHA-256 (`<ref-file>.release.json`) against `images.json` / `.sha256`. Daily or release-triggered auto-upgrades skip downloading tarballs when nothing changed.
2. **Streaming download and disk check:** Tarballs stream directly to a staging directory on disk (`<sandbox-dir>/.upgrade-staging` or `<data-dir>/upgrade-staging`) after verifying via `statfs` that free disk space exceeds the asset size plus 64 MiB headroom. When an API redirects asset downloads to object storage on another host, `Authorization` headers are stripped across the cross-host redirect.
3. **Checksum and Image ID pinning:** Each downloaded file's SHA-256 must match both `images.json` and the `.sha256` sidecar. After `docker load -i`, the loaded image's `docker image inspect --format '{{.Id}}'` must match `imageId` in `images.json`, and the image is tagged as `<upgrade-image>:<tag>`. If `-upgrade-verify-cmd` is configured, it runs against the loaded image before the health check executes anything inside it.
4. **Sandbox image handling:** If the candidate daemon expects a sandbox image (`grain sandbox-image`), `pkg/upgrade` loads it from `images.json` (`guest` / `k8s-sandbox`) or `-upgrade-sandbox-release-asset` when present, or pulls it from its registry otherwise. If neither release assets nor registry pull can fetch the required sandbox image, the upgrade fails before cutover naming the unreachable sandbox image rather than leaving the old sandbox image behind.

## Credentials

Release lookups and asset downloads reuse the deployment's configured git connection for the release repository's host (`Settings -> Connections`):

- **`github.com`:** Uses the GitHub App installation token or PAT configured for `github.com` (or unauthenticated HTTPS for public repositories).
- **GitHub Enterprise hosts:** Uses the host's configured PAT, GitHub App, or registered ambient CLI transport running under the daemon's service account.

When authentication fails (`401`, `403`, or `404` on an unauthenticated private release), the upgrade error names the connection (or `Settings -> Connections` host entry) to fix.
