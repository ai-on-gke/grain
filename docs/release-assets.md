# Release assets (release-asset contract v1)

Grain's open-source images are published as **assets on this repository's
own GitHub releases** (`github.com/ai-on-gke/grain/releases`), not as
registry packages. Publishing needs nothing but the repository's own
`GITHUB_TOKEN` with `contents: write`; installing needs nothing but HTTPS
to github.com and a Docker engine — no registry, no registry credential.

The Google edition publishes the same way, with the same contract, so one
loader (`pkg/upgrade`'s `release-assets` source) reads both.

## The contract

### Releases

| Release | Moves? | Published by |
| --- | --- | --- |
| `sha-<12 hex>` | never: assets are only ever added, never replaced | `release.yml`, every build of a release branch |
| `channel-<branch>` (`/` as `-`: `channel-main`, `channel-candidate`, `channel-release-v3.1.1`) | forward only: onto a commit its current one is an ancestor of | `release.yml` on a branch push; the release train for `channel-candidate` |
| `vX.Y.Z` | never | `promote.yml` (`release-assets.sh promote`) |
| `latest` | onto exactly the commit the git tag `latest` names (so a rollback moves it back) | `promote.yml` |
| `vX.Y.Z-rc.N`, `vX.Y.Z-pending`, `vX.Y.Z-rc.N-discarded`, `-rejected`, `-retracted` | never | the release train (`release-assets.sh retag`) |

A release is **never** tagged with a bare branch name (`main`): `main`
is a branch, and a tag of the same name makes every `git` ref ambiguous.

### Assets on every release

- `images.json` — the manifest, below.
- One gzip `docker save` tarball per image, each with a `<asset>.sha256`
  sidecar in `sha256sum` format (`<64 hex>  <asset>`). The OSS edition's
  names are `grain-image.tar.gz`, `guest-image.tar.gz` and
  `k8s-sandbox-image.tar.gz`; the manifest names them, so a loader never
  hard-codes them.

GitHub refuses an asset of 2 GiB or more; `release-assets.sh package`
fails, naming the image, before anything is uploaded.

### `images.json`, schema 1

```json
{
  "schema": 1,
  "repository": "github.com/ai-on-gke/grain",
  "commit": "<40 hex>",
  "version": "v3.5.0-dev.12+abcdef0",
  "createdAt": "2026-11-04T06:00:00Z",
  "images": {
    "grain":       {"asset": "grain-image.tar.gz",       "sha256": "<64 hex of the .tar.gz>", "imageId": "sha256:<64 hex>", "ref": "grain/grain:sha-<12 hex>"},
    "guest":       {"asset": "guest-image.tar.gz",       "sha256": "...", "imageId": "sha256:...", "ref": "grain/guest:sha-<12 hex>"},
    "k8s-sandbox": {"asset": "k8s-sandbox-image.tar.gz", "sha256": "...", "imageId": "sha256:...", "ref": "grain/k8s-sandbox:sha-<12 hex>"}
  }
}
```

- `ref` is the local tag inside the tarball. It is never a registry ref
  and never carries an `@sha256:` pin; `grain/...` would resolve to
  Docker Hub, so **nothing ever `docker pull`s a manifest ref** — a
  missing one is a failure, not a pull.
- The daemon image's `grain sandbox-image` and `grain k8s-sandbox-image`
  print exactly the manifest's `guest` and `k8s-sandbox` refs
  (`release-images.sh build` checks this before anything is packaged).
- `scripts/release-assets.sh validate DIR` checks a manifest, and every
  asset and sidecar it names, against this schema;
  `scripts/release-assets_test.sh` holds it to that.

### Updating a moving release

Moving releases are updated **non-atomically**: `images.json` is deleted
first and uploaded last. A `channel-*` or `latest` release that exists
but has no `images.json` is mid-update — a loader tries again on its next
check and does **not** fall back to a registry. A release that does not
exist at all (a name only published before release assets) is what the
registry fallback is for.

### Promoted and retagged releases

`vX.Y.Z`, `latest` and the train's names reuse the commit's
`sha-<short>` tarballs byte for byte; only `version` and `createdAt`
differ. Detect "unchanged" by `commit` or `images.grain.imageId`, never by
`version`.

## Publishing

`release.yml` (on `main`, `candidate`, `rc/v*`, `release/v*`):

1. `RELEASE_IMAGES_TARGET=assets scripts/release-images.sh build` builds
   the three images as local refs — no registry push, no digest pin, the
   guest built in the job rather than fetched from a registry cache.
2. The container and installer end-to-end suites run against them.
3. `scripts/release-assets.sh package` saves, compresses and checksums
   them and writes `images.json`.
4. `scripts/release-assets.sh publish sha-<short> channel-<branch>`
   uploads them and checks every release ends up with all seven assets.

Off a release ref, or with `dry_run`, every step runs and every release
write is printed instead of made.

`promote.yml` runs `scripts/release-assets.sh promote`: the `sha-<short>`
release of the commit the git tag `latest` names, published as its
`vX.Y.Z` and as `latest`. A release promoted before release assets
existed has no `sha-<short>` release; the automatic run warns and skips
it, and asking for it by name fails.

The release train publishes with `scripts/release-assets.sh retag
sha-<short> <version> <release>...` (an rc as `vX.Y.0-rc.N` and
`channel-candidate`, a soaked release as `vX.Y.0-pending`, and the
`-discarded` / `-rejected` / `-retracted` markers).

## Installing

- `scripts/setup.sh` installs from release assets by default: it maps
  `GRAIN_IMAGE_TAG` / `GRAIN_CHANNEL` to a release (`main` →
  `channel-main`, `candidate` → `channel-candidate`, `latest`,
  `sha-<12 hex>` and `vX.Y.Z` as themselves), downloads `images.json` and
  the three tarballs, checks each against the manifest and its sidecar,
  `docker load`s it and checks the loaded image ID. Setting `GRAIN_IMAGE`
  (or `GRAIN_RELEASE_ASSETS=0`) pulls from that registry repository
  instead, as before. A name with no release at all falls back to
  `ghcr.io/bwsalmon/grain/grain`, where releases published before release
  assets still are. `GRAIN_IMAGE_VERIFY_KEY` (cosign) applies to registry
  images only.
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

`pkg/upgrade`'s `mirrorSandboxImage` already pushes a sandbox image into
a deployment's configured mirror; making the k8s upgrader load release
assets and push them there is part of the `release-assets` image source
(task 2951), not this publisher.

## Existing deployments

Every OSS install today upgrades from `ghcr.io/bwsalmon/grain/grain`. Those
images stay where they are; nothing deletes them. Once the
`release-assets` source (task 2951) ships, its registry fallback lets an
install move over — but only an install already running a build that
contains it. Since GHCR's channel tags stop moving with this change, **one
last GHCR publish of the first build containing task 2951 is needed** so
existing installs can reach the new loader. `release-images.sh` keeps its
registry target for exactly this; from a checkout at that commit, with a
`bwsalmon` classic PAT holding `write:packages`:

```sh
export RELEASE_IMAGE_REPO=bwsalmon/grain GRAIN_CI_REPO=ai-on-gke/grain \
  PACKAGE_PUBLISH_REGISTRY=ghcr.io PACKAGE_PUBLISH_USERNAME=bwsalmon \
  PACKAGE_PUBLISH_TOKEN=<pat>
GRAIN_CI_REF=main bash scripts/release-images.sh all        # sha-<7>, main
# once a release containing it is promoted (git tag latest on it):
bash scripts/release-images.sh promote vX.Y.Z               # vX.Y.Z, latest
```

Run it in a scratch clone rather than a working checkout. Deployments on
`candidate` need the same with `GRAIN_CI_REF=candidate`.
