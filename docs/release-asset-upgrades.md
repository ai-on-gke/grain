# Release-asset container upgrades

Container-mode deployments (`-upgrade-image` with `-upgrade-image-ref-file`) can load candidate daemon and sandbox images directly from gzip-compressed `docker save` tarballs attached to a GitHub or GitHub Enterprise repository's own releases (`release-assets`), alongside traditional OCI registry pulls (`registry`).

Loading from repository releases avoids requiring separate cloud container-registry login helpers on the daemon host: release metadata and asset tarballs are fetched using the deployment's existing git connection for that forge host (`Settings -> Connections`).

## Default behavior and fallback

When `-upgrade-image-source` (`GRAIN_UPGRADE_IMAGE_SOURCE`) is unset:

1. Upgrades default to `release-assets` -- or to `registry` on a deployment with `-upgrade-verify-cmd`, whose signatures are over registry manifests (see below) -- resolving the release repository from any edition-registered track default (`upgrade.RegisterTrackDefaults`), mapping `docker.io/grainai/grain` to `github.com/ai-on-gke/grain`, or mapping `ghcr.io/<owner>/<repo>/...` to `github.com/<owner>/<repo>`.
2. If the target release does not exist yet or carries no `images.json` manifest asset, the upgrade automatically falls back to `docker pull` from the configured container registry (`-upgrade-image`) and records:
   `release has no image assets; pulled from <registry>`
   in the upgrade status, on [Settings -> Upgrade](/settings/upgrade), and in the post-upgrade check task.
   A **moving** release (`channel-<branch>`, `latest`, or a pre-channel bare branch tag) that exists but has no `images.json` is not treated this way: its publisher uploads `images.json.next` and swaps it into place when it re-points the release, so the release is mid-update. The upgrade fails before cutover with "try again later" and never falls back to the registry, even under an explicit `-upgrade-image-fallback=registry`; the next check retries. Only a release that does not exist, an immutable one (`sha-<short>`, `vX.Y.Z`) without a manifest, or a moving one whose body says `tagged, not published`, falls back.
3. Integrity failures (an invalid `images.json`, a missing `.sha256` sidecar, a SHA-256 mismatch, a Docker image ID mismatch, or a failed `-upgrade-verify-cmd`) **never** fall back silently.

Pinning `-upgrade-image-source=release-assets` (or registering `Source: upgrade.ImageSourceReleaseAssets` via `upgrade.RegisterTrackDefaults` for an edition) disables the automatic missing-manifest fallback unless `-upgrade-image-fallback=registry` is explicitly configured. Pinning `-upgrade-image-source=registry` skips release lookups and always runs `docker pull` (with optional `-upgrade-image-fallback=release-assets`).

### Tagged, not published

A publisher may write every tag (`sha-<short>`, `channel-<branch>`, `vX.Y.Z`, `latest`) while uploading no assets, leaving empty releases whose body says `tagged, not published`. A deployment pinned to `release-assets` with no registry fallback keeps working against the last release that was published, meaning a non-draft, non-pointer release carrying `images.json` (newest by `created_at`, then id, from a paginated `GET /repos/<owner>/<name>/releases?per_page=100`):

- **`main` / `channel-*` / `latest`**: when the target release is missing, or has no `images.json` and no `images.json.next` swap in flight, the upgrade resolves to the newest published release. `latest` prefers the newest published non-prerelease `vX.Y.Z` and falls back to any published release. A channel with `images.json.next` up is still mid-update and retried.
- **An explicit `vX.Y.Z` or `sha-<short>`** with no `images.json` (or missing, while the releases list answers) is refused before anything is downloaded: `release <tag> on <host>/<owner>/<repo> is not published; the newest published release is <newest>`. For a `vX.Y.Z` the release it names is the newest published non-prerelease version, if there is one. The automatic upgrade's probe treats such a target as nothing to install.
- **Released versions** listed on [Settings -> Upgrade](/settings/upgrade), and the one the release-triggered automatic upgrade picks, are only versions (and release candidates) whose release carries `images.json`.

Before the first `docker load`, staging checks that every image the manifest names (the daemon, `guest`, and `k8s-sandbox`) has its tarball and a `.sha256` sidecar agreeing with `images.json`. A missing or mismatched asset is refused before Docker holds any of the release's images. Each tarball's SHA-256 is also checked as it streams, before it is loaded.

## Daemon flags and environment variables

| Flag                             | Environment variable                   | Description                                                                                                                                                                                                                  |
| -------------------------------- | -------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-upgrade-image-source`          | `GRAIN_UPGRADE_IMAGE_SOURCE`           | `release-assets` or `registry`. Empty defaults to `release-assets` with missing-manifest fallback to `registry` (`registry` when `-upgrade-verify-cmd` is set). Passing either the flag or env var locks the setting read-only in [Settings -> Upgrade](/settings/upgrade). |
| `-upgrade-image-fallback`        | `GRAIN_UPGRADE_IMAGE_FALLBACK`         | Optional fallback source when the primary source cannot fetch a candidate daemon image: `none`, `registry`, or `release-assets`.                                                                                             |
| `-upgrade-release-repo`          | `GRAIN_UPGRADE_RELEASE_REPO`           | Release repository as `<host>/<owner>/<name>` (for example `github.com/ai-on-gke/grain` or `ghe.example.com/org/grain`).                                                                                                     |
| `-upgrade-release-asset`         | `GRAIN_UPGRADE_RELEASE_ASSET`          | Explicit daemon tarball asset filename when `images.json` is not used (for example `grain-image.tar.gz`).                                                                                                                    |
| `-upgrade-release-checksum`      | `GRAIN_UPGRADE_RELEASE_CHECKSUM`       | Checksum sidecar asset name, or suffix starting with `.` (defaults to `.sha256`).                                                                                                                                            |
| `-upgrade-sandbox-release-asset` | `GRAIN_UPGRADE_SANDBOX_RELEASE_ASSETS` | Repeatable `<role-or-repo>=<asset>` override for sandbox images (comma-separated in the env var), such as `guest=grain-guest.tar.gz`.                                                                                        |

When `-upgrade-image-source` / `GRAIN_UPGRADE_IMAGE_SOURCE` is not passed on the command line or environment, administrators can also inspect and switch the source live on [Settings -> Upgrade](/settings/upgrade) (`PUT /api/upgrade/source`, persisted in `<data-dir>/upgrade-image-source.json`). A release repository set there must be one the daemon's flags or track already name, or the release repository of an image repository the deployment may upgrade onto (`github.com/ai-on-gke/grain` for `docker.io/grainai/grain`, or `github.com/<owner>/<name>` for `ghcr.io/<owner>/<name>`, including those in `GRAIN_ALLOWED_IMAGE_REPOS`); any other takes `-upgrade-release-repo`. On a deployment with `-upgrade-verify-cmd` the pane cannot choose `release-assets` as the source or the fallback (only keep a fallback the flags already set): whether a release's tarballs are ones the verifier can vouch for (see below) is the host operator's to say with `-upgrade-image-source` or `-upgrade-image-fallback`. A field left empty there falls back to the daemon flag's value.

## Release tag convention and branch ref ambiguity

Moving channels use `channel-<branch>` release tags with `/` mapped to `-` (`channel-main`, `channel-candidate`, `channel-rc-v3.2`) alongside immutable `sha-<short>` and promoted `vX.Y.Z` / `latest` releases. Every GitHub or GitHub Enterprise release tag is backed by a git ref (`refs/tags/<tag>`), and `git-check-ref-format` forbids `..` anywhere in a ref name, so release tags map `/` to `-` while local Docker image tags keep `TagForBranch`'s injective `..` substitution (`<repo>:rc..v3.2`) so `ChannelForRef` can recover the branch name.

Publishing a release whose git tag has the same bare name as a branch (such as `main`) creates `refs/tags/main` next to `refs/heads/main`, which is an ambiguous ref in `git` and forge CLI ref resolvers. To avoid this ambiguity:

- `pkg/upgrade` never resolves release tags via `git` refs; it queries the REST endpoint `/repos/<owner>/<name>/releases/tags/<tag>` directly.
- For branch channels, `ReleaseTagsForBranch` maps `/` to `-`, looks up `channel-<rel-tag>` first, and only falls back to `<rel-tag>` on HTTP `404` for compatibility with older releases.
- The `latest` channel is GitHub's own latest-release marker: the loader asks `/repos/<owner>/<name>/releases/latest` and never reads a release or git tag named `latest`.

## `images.json` schema 2 (contract v2)

Contract v2 keeps tarballs only on immutable releases (`sha-<short>`, `vX.Y.Z`). A moving `channel-<branch>` release is a pointer that holds only a schema-2 `images.json`, in which every image entry adds `"release": "<immutable tag>"`, the release holding that image's asset and `.sha256` sidecar. The loader looks that release up by tag through the same releases API and downloads the asset from it by name; a `release` naming a moving release is refused. Schema 1 (assets on the release itself) is still accepted, and immutable releases may write either schema.

A channel release with no `images.json`, or two of them while its publisher swaps them, is mid-update: an upgrade onto it fails before cutover with "try again later", and the unchanged probe reports nothing to install so the next poll retries. On a deployment that loads release assets only, a channel with neither `images.json` nor `images.json.next` was tagged without being published, and resolves to the newest published release instead (see [Tagged, not published](#tagged-not-published)).

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
3. **Checksum and Image ID pinning:** Each downloaded file's SHA-256 must match both `images.json` and the `.sha256` sidecar. After `docker load -i`, the loaded image's `docker image inspect --format '{{.Id}}'` must match `imageId` in `images.json`, and the image is tagged as `<upgrade-image>:<tag>`. If `-upgrade-verify-cmd` is configured, it runs against the loaded image before the health check executes anything inside it, and it passes only on a signature over the loaded image's own ID: a `ref` in `images.json` pinned to `@sha256:<digest>` must be that ID (as `docker load` gives an OCI image under the containerd image store), and for a tag the verifier must print the digests it verified (cosign's JSON) with the ID among them. A signed registry digest beside a tarball of something else, or a verifier that names no digest, is refused: nothing ties a tarball's bytes to the registry, so a signature on a registry manifest alone does not vouch for them. This is why a verifying deployment defaults to `registry`; choose `release-assets` explicitly only where the loaded image IDs are the signed digests.
4. **Sandbox image handling:** If the candidate daemon expects a sandbox image (`grain sandbox-image`), `pkg/upgrade` loads it from `images.json` (`guest` / `k8s-sandbox`) or `-upgrade-sandbox-release-asset` when present, or pulls it from its registry otherwise. A ref with no registry host (the local `<namespace>/<image>:sha-<short>` names `images.json` gives its `ref`s) is never pulled -- docker would resolve it to Docker Hub -- so one the release does not carry fails the upgrade instead. If neither release assets nor registry pull can fetch the required sandbox image, the upgrade fails before cutover naming the unreachable sandbox image rather than leaving the old sandbox image behind.

## Credentials

Release lookups and asset downloads reuse the deployment's configured git connection for the release repository's host (`Settings -> Connections`):

- **`github.com`:** Uses the GitHub App installation token or PAT configured for `github.com` (or unauthenticated HTTPS for public repositories).
- **GitHub Enterprise hosts:** Uses the host's configured PAT, GitHub App, or registered ambient CLI transport running under the daemon's service account.

When authentication fails (`401`, `403`, or `404` on an unauthenticated private release), the upgrade error names the connection (or `Settings -> Connections` host entry) to fix.
