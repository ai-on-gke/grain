# Release assets (release-asset contract v2)

Grain's open-source images are published as **assets on this repository's
own GitHub releases** (`github.com/ai-on-gke/grain/releases`), as well as to
Docker Hub (`docker.io/grainai/*`), which is the registry fallback.
Publishing assets needs nothing but the repository's own `GITHUB_TOKEN` with
`contents: write`; installing from them needs nothing but HTTPS to
github.com and a Docker engine — no registry, no registry credential.

The Google edition publishes the same way, with the same contract, so one
loader (`pkg/upgrade`'s `release-assets` source) reads both. Contract v2
(operator-approved 2026-11-04) replaces v1 where they differ.

## The contract

### Releases

Only the immutable `sha-<short>` and `vX.Y.Z` releases carry images; a
`vX.Y.Z` holds its own **copies** of its commit's `sha-` tarballs, so
pruning `sha-` releases can never break it. Every other release is a
**pointer**: it holds `images.json` alone, and each image in it names the
release that holds its asset.

| Release | Holds | Moves? | Published by |
| --- | --- | --- | --- |
| `sha-<12 hex>` | the tarballs, their `.sha256` sidecars and `images.json` | never: assets are only ever added, never replaced | `release.yml`, every build of a release branch |
| `channel-<branch>` (`/` as `-`: `channel-main`, `channel-candidate`, `channel-release-v3.1.1`) | `images.json` | forward only: onto a commit its current one is an ancestor of; `channel-candidate` follows the newest rc | `release.yml` on a branch push; the release train for `channel-candidate` |
| `vX.Y.Z` | copies of its commit's `sha-<short>` tarballs and sidecars, `images.json` naming itself, and [the grain CLI](#the-grain-cli) | never: assets are only ever added | `promote.yml`, `promote-release.yml`, `publish-release.yml` (`release-assets.sh promote`) |
| `vX.Y.Z-rc.N`, `vX.Y.Z-pending`, `vX.Y.Z-rc.N-discarded`, `-rejected`, `-retracted` | `images.json` (and on `vX.Y.Z-rc.N`, [the grain CLI](#the-grain-cli)) | never | `release-train-assets.yml` on the train's tag pushes, and `promote-release.yml` |

- **There is no release named `latest`.** The latest channel is GitHub's own
  latest-release marker: promote creates or updates `vX.Y.Z` with
  `make_latest: true` when the git tag `latest` names its commit (so a
  rollback moves it back), and deletes a legacy release named `latest` (the
  git tag stays). Everything else is created `prerelease: true`,
  `make_latest: false`, and can never become latest. Loaders resolve the
  latest channel with `GET /repos/{o}/{r}/releases/latest` (or
  `releases/latest/download/images.json`). The git tag `latest` still
  moves for people and scripts; loaders ignore it.
- A release is **never** tagged with a bare branch name (`main`).

### Assets

- `images.json` — the manifest, below. Exactly one per release.
- On `sha-<short>` and `vX.Y.Z` releases only: one gzip `docker save` tarball per image,
  each with a `<asset>.sha256` sidecar in `sha256sum` format
  (`<64 hex>  <asset>`). The OSS edition's names are `grain-image.tar.gz`,
  `guest-image.tar.gz` and `k8s-sandbox-image.tar.gz`; the manifest names
  them, so a loader never hard-codes them.

GitHub refuses an asset of 2 GiB or more; `release-assets.sh package`
fails, naming the image, before anything is uploaded.

### The grain CLI

`vX.Y.Z` and `vX.Y.Z-rc.N` releases — the versions a daemon reports —
also carry the `grain` CLI that the daemon's `GET /cli/install.sh`
(`pkg/ui/cli_install.sh`) downloads:

- `grain-linux-amd64.tar.gz`, `grain-linux-arm64.tar.gz`,
  `grain-darwin-amd64.tar.gz`, `grain-darwin-arm64.tar.gz`, each holding one
  file, `grain`, built by `scripts/build-cli.sh` from the release's tag and
  stamped with the release (`pkg/version.Release`), as its daemon is;
- `checksums.txt`, one `sha256sum` line (`<64 hex>  <archive>`) per archive,
  naming exactly those four. The install script checks the archive it
  downloads against it before extracting.

`images.json` does not name them. `release-assets.sh cli-validate DIR`
checks a directory holds exactly these; `release-assets.sh cli-publish
<release> DIR` uploads the archives and then `checksums.txt` to a release
that already exists. A release carrying `checksums.txt` is never written
again; archives a failed run left without one are replaced; and a release
missing any of the five afterwards fails the run.

### `images.json`, schema 2

```json
{
  "schema": 2,
  "repository": "github.com/ai-on-gke/grain",
  "commit": "<40 hex>",
  "version": "v3.5.0-dev.12+abcdef0",
  "createdAt": "2026-11-04T06:00:00Z",
  "images": {
    "grain":       {"asset": "grain-image.tar.gz",       "sha256": "<64 hex of the .tar.gz>", "imageId": "sha256:<64 hex>", "ref": "grain/grain:sha-<12 hex>",       "release": "sha-<12 hex>"},
    "guest":       {"asset": "guest-image.tar.gz",       "sha256": "...", "imageId": "sha256:...", "ref": "grain/guest:sha-<12 hex>",       "release": "sha-<12 hex>"},
    "k8s-sandbox": {"asset": "k8s-sandbox-image.tar.gz", "sha256": "...", "imageId": "sha256:...", "ref": "grain/k8s-sandbox:sha-<12 hex>", "release": "sha-<12 hex>"}
  }
}
```

- `release` is the immutable release (`sha-<short>` or `vX.Y.Z`) holding
  the asset; on a `sha-` release it names itself. A loader downloads each
  asset by name from that release. Schema 1 (no `release`; assets on the
  release itself) is still accepted by loaders and by
  `release-assets.sh validate`; this publisher writes schema 2 everywhere.
- `ref` is the local tag inside the tarball. It is never a registry ref
  and never carries an `@sha256:` pin; `grain/...` would resolve to
  Docker Hub, so **nothing ever `docker pull`s a manifest ref** — a
  missing one is a failure, not a pull. `pkg/upgrade` refuses to pull a
  ref naming no registry host; the daemon refuses to mirror one that is
  not in the local image store, a Kubernetes backend refuses to start
  with one as its pod image (kubelet would pull it from Docker Hub), and
  the container backend starts sandboxes from a `grain/...` image with
  `docker run --pull=never`, so one gone from the local store fails the
  run instead of being fetched.
- The daemon image's `grain sandbox-image` and `grain k8s-sandbox-image`
  print exactly the manifest's `guest` and `k8s-sandbox` refs
  (`release-images.sh assets` checks this before anything is packaged).
- `scripts/release-assets.sh validate DIR` checks a manifest, and every
  asset and sidecar in DIR it names, against this schema;
  `scripts/release-assets_test.sh` holds it to that.
- Pointers (the train's names, channels) differ from their `sha-`
  release's manifest only in `version` and `createdAt`; a `vX.Y.Z` also
  names itself as every image's `release`. Detect "unchanged" by `commit`
  or `images.grain.imageId`, never by `version`.

### Moving a channel

Asset names are unique on a release, so moving a channel uploads the new
manifest as `images.json.next`, deletes the old `images.json`, and renames
`images.json.next` into its place — a single small swap; no tarball moves.
A loader that finds no `images.json` (or only `images.json.next`) retries
on its next poll: that is infra, never a failure, and never a reason to
fall back to a registry. A release
that does not exist at all (a name only published before release assets)
is what the registry fallback is for. Before any pointer is published,
`release-assets.sh` checks that every release it names exists and carries
the assets.

### Retention

`release.yml`'s `assets` job ends with `release-assets.sh prune`: it keeps
the newest `RELEASE_RETAIN_SHA` (default 50, the repository variable of
that name) `sha-` releases, plus every `sha-` release a channel, a train
release or GitHub's latest release points at, and deletes the rest with
their tags. `vX.Y.Z` (which holds copies) and every non-`sha-` release are
never pruned.

## Publishing

`release.yml` (on `main`, `candidate`, `rc/v*`, `release/v*`):

1. The `images` job (`contents: read`) runs `scripts/release-images.sh
   build` and the container and installer end-to-end suites, as for Docker
   Hub.
2. `scripts/release-images.sh assets` tags the tested guest and Kubernetes
   sandbox images as `grain/{guest,k8s-sandbox}:sha-<12 hex>` and rebuilds
   the daemon image as `grain/grain:sha-<12 hex>` naming them — no push,
   no digest pin.
3. `scripts/release-assets.sh package` saves, compresses and checksums
   them and writes `images.json`; the directory is handed on as the
   `release-assets` workflow artifact.
4. The `assets` job — the only one holding `contents: write`, running no
   build or test code — checks the manifest names the run's own commit,
   then `scripts/release-assets.sh publish sha-<short> channel-<branch>`
   uploads the tarballs, sidecars and `images.json` to `sha-<short>`, swaps
   `channel-<branch>`'s `images.json` to point at it, and checks each
   release ends up with everything it should carry.
5. `scripts/release-assets.sh prune` applies retention.

Off a release ref, or with `dry_run`, every step runs and every release
write is printed instead of made.

`promote.yml` (and `publish-release.yml` / `promote-release.yml` for the
release train) runs `scripts/release-assets.sh promote`: `vX.Y.Z` for the
commit the git tag `latest` names, with copies of that commit's
`sha-<short>` assets and marked GitHub's latest release. A release promoted before release assets
existed has no `sha-<short>` release; the automatic run warns and skips
it, and asking for it by name fails.

`promote.yml`, `promote-release.yml` and `publish-release.yml`, and
`release-train-assets.yml` for an rc tag, also build the release's grain
CLI in a `cli` job holding only `contents: read` (`scripts/build-cli.sh`,
`RELEASE=<release>`) and hand it to a `cli-assets` job that runs
`scripts/release-assets.sh cli-publish` once the release itself is
published, so `GET /cli/install.sh` works as soon as a promotion or rc
publish finishes. Both jobs are skipped once the release carries its
`checksums.txt`; `promote.yml`'s automatic run skips both building and
publishing when neither the GitHub Release nor its `sha-<short>` assets
exist (`RELEASE_CLI_SKIP_UNRELEASED=true`), as `promote` does. Every
`cli-assets` job queues in one concurrency group per release
(`grain-cli-assets-<release>`), so no two runs upload the same release's
CLI at once.

The release train publishes with `scripts/release-assets.sh retag
sha-<short> <version> <release>...` (an rc as `vX.Y.0-rc.N` and
`channel-candidate`, a soaked release as `vX.Y.0-pending`, and the
`-discarded` / `-rejected` / `-retracted` markers).

## Installing

- `scripts/setup.sh` installs from release assets by default: it maps
  `GRAIN_IMAGE_TAG` / `GRAIN_CHANNEL` to a release (`main` →
  `channel-main`, `candidate` → `channel-candidate`, `latest` → GitHub's
  latest release, `sha-<12 hex>` and `vX.Y.Z` as themselves), downloads
  `images.json` and the three tarballs from the releases it names, checks each against the manifest and its sidecar,
  `docker load`s it and checks the loaded image ID. Setting `GRAIN_IMAGE`
  (or `GRAIN_RELEASE_ASSETS=0`) pulls from that registry repository
  instead, as before. A name with no release at all falls back to
  `docker.io/grainai/grain`. `GRAIN_IMAGE_VERIFY_KEY` (cosign) applies to
  registry images only. The unit's `-upgrade-image` stays the registry
  repository (`docker.io/grainai/grain`), never a manifest ref: the
  daemon's `release-assets` upgrade source
  ([release-asset-upgrades.md](release-asset-upgrades.md)) maps it to this
  repository's releases and falls back to it.
- `scripts/install-grain.sh -version vX.Y.Z` installs the CLI the same
  way; `-image REPO [-key cosign.pub]` pulls a registry image instead.

## Kubernetes

A cluster cannot `docker load`, so a Kubernetes deployment needs a
registry of its own — the documented path is:

1. Configure a registry the cluster can pull from and the installer (or
   upgrader) can push to: `sandbox_image_mirror` / the
   `-k8s-sandbox-image` registry (for example an Artifact Registry
   repository in the cluster's project).
2. Load the release assets on a machine with Docker
   (`scripts/release-assets.sh fetch <release> DIR` then `load DIR`),
   retag the three manifest refs into that registry and push them.
3. Point the Deployment's `image:` and `-k8s-sandbox-image` at the
   pushed refs.

`scripts/release-assets.sh mirror <release> <registry/prefix>` does steps
2 and 3's push in one command. A Kubernetes backend refuses to start with a
`grain/...` manifest ref as its pod image, so a deployment that has not
pushed the release into its own registry fails loudly rather than letting
kubelet look the name up on Docker Hub. Without a mirror, the
`docker.io/grainai/*` images serve as before.

## Existing deployments

Existing OSS installs upgrade from a registry: `docker.io/grainai/grain`
since the Docker Hub move, `ghcr.io/bwsalmon/grain/grain` before it. Nothing
here deletes any of those images, and Docker Hub keeps receiving every
build. The core loader's `release-assets` source maps both repositories to
this one's releases and falls back to the registry when a release has no
manifest, so an install whose running build contains that loader moves
over on its next upgrade, with no configuration: it keeps its channel,
reads `latest` through GitHub's latest release and `main` through
`channel-main`, and pulls from `docker.io/grainai/grain` (never GHCR) when
it falls back, since the oss track maps the legacy repository there
(`installtrack.RepositoryForTrack`).

### The GHCR bridge

An install still running a build from before the loader and the Docker Hub
alias table -- v3.1.1 and older, every one of which was published only to
`ghcr.io/bwsalmon/grain/*` -- only ever polls
`ghcr.io/bwsalmon/grain/grain:<channel>`, whose tags stopped moving when the
GHCR pipeline broke. It reaches the loader through one last GHCR publish:
`scripts/ghcr-bridge.sh`, run once per channel by an operator holding a
`bwsalmon` classic PAT with `write:packages`. It builds nothing. It copies
images already on Docker Hub registry-side, by digest
(`docker buildx imagetools create`), and refuses before writing anything
unless the daemon contains the loader (task 2951) and knows
`-upgrade-image-source`, and its stamped guest is the `sandbox:sha-<7>` it
pins. It copies the daemon to `grain`, and the sandbox images to the names
the old flow used, `guest` and `k8s-sandbox`, under the same tags, and
checks every copy's digest on GHCR against its source. It deletes nothing.

| Channel an install follows | What it needs | Command |
| --- | --- | --- |
| `main` | `:main`, `:sha-<7>` from a current `main` build | `scripts/ghcr-bridge.sh main` (copies `docker.io/grainai/grain:main`) |
| `latest` (most installs) | `:latest`, `:vX.Y.Z` from the first **promoted** release containing the loader, never a `main` build | `scripts/ghcr-bridge.sh latest vX.Y.Z`, once the train has promoted it: refused unless git tag `vX.Y.Z` contains the loader and `docker.io/grainai/grain:vX.Y.Z` is the digest `docker.io/grainai/grain:latest` names |
| `candidate` / `rc` | the same release as `latest` | written by the `latest` step: GHCR's `:candidate` has been `v3.1.0`, the same digest as `:latest`, since 2026-11-02, so an install on it is moved no further than `latest` is. Once bridged, it reads `channel-candidate`, which the OSS publisher does not write, and falls back to `docker.io/grainai/grain:candidate` |
| a pinned `vX.Y.Z`, or auto-upgrade off | nothing pushed | re-run the installer (below) |
| Kubernetes (`deploy/kubernetes`) | the same `:main` / `:latest` tags | none beyond those: the Kubernetes path upgrades from a registry only (`pkg/upgrade/kube.go`), and once bridged it moves the Deployment onto `docker.io/grainai/grain`. A cluster that mirrors its images follows [Kubernetes](#kubernetes) instead |

From a clone of this repository with its tags fetched:

```sh
git fetch origin --tags
echo "$PAT" | docker login ghcr.io -u bwsalmon --password-stdin
DRY_RUN=1 scripts/ghcr-bridge.sh main     # every check, no writes, no login needed
scripts/ghcr-bridge.sh main
# once the train has promoted vX.Y.Z (docker.io/grainai/grain:latest is it):
DRY_RUN=1 scripts/ghcr-bridge.sh latest vX.Y.Z
scripts/ghcr-bridge.sh latest vX.Y.Z
docker logout ghcr.io
```

Each run prints a row for the record below. Once an install has taken the
bridge, it never reads GHCR again.

**Pinned installs and installs with auto-upgrade off** have nothing to
follow. Re-running the installer moves them onto release assets:
`scripts/setup.sh` on a host install (it installs from this repository's
release assets and keeps the data directory), or
`scripts/install-grain.sh -version vX.Y.Z` for the CLI alone.

**Verified, 2026-11-08,** against the `main` build `46ab9012bbc0`
(`docker.io/grainai/grain:sha-46ab901`, schema 125):

- Real GHCR `v3.1.0` (schema 87, the `latest` baseline) and `v3.0.0`
  (the floor) images upgraded onto it with their tasks and comments intact
  (`TestUpgradeFromReleasedVersions` with
  `GRAIN_UPGRADE_RELEASE_IMAGE=ghcr.io/bwsalmon/grain/grain`).
- An upgrader configured as a bridged install (`-upgrade-image`
  `ghcr.io/bwsalmon/grain/grain`, oss track, no source set) defaults to
  `release-assets` from `github.com/ai-on-gke/grain`, falling back to
  `docker.io/grainai/grain`. On `main`, it resolved `channel-main`'s schema-2
  manifest to `sha-46ab9012bbc0`, loaded the daemon and guest tarballs from
  it, and left its ref file at `docker.io/grainai/grain:main`. On `latest`,
  GitHub had no latest release yet (v3.1.x carry no `images.json`), so it
  fell back to the registry, as it will until a promoted release carries a
  manifest.
- `scripts/ghcr-bridge.sh main` copied into a stand-in registry kept every
  digest.

### Last GHCR publish

The bridge is the last thing ever published under `ghcr.io/bwsalmon/grain`.
Its rows are recorded here as the operator runs it:

| Date (UTC) | Channel | GHCR tags | Commit | `grain` digest | `guest` / `k8s-sandbox` digests |
| --- | --- | --- | --- | --- | --- |
| _not yet run_ | `main` | | | | |
| _waiting for the first promoted release with the loader_ | `latest` | | | | |
