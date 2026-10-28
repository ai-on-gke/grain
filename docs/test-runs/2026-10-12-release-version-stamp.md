# Walkthrough: release version stamp in promoted images (2026-10-12)

Test plan: `docs/test-plans/2026-10-11.md` §12. Commit under test: `ecf768a07` ("stamp the release
version into promoted grain images"), on `candidate` at `b762a5be9`. Task 3148, filed by the
nightly test-plan task 2955.

## Verdict

The promotion works as described: the promoted image carries the release in its label, in
`grain version --json` and in `/api/config`, names the candidate's sandbox images, and a replay
leaves `latest`'s digest alone. One bug on the v1.8.0 path (finding 1) is fixed here, with
regression tests.

## Environment

- A grain task sandbox: Debian, 4 vCPU, 7 GiB. Docker 29.9.0 with the containerd image store,
  buildx v0.38.0 (already installed).
- `registry:2` on `127.0.0.1:5000`. `localhost` does **not** work here: the sandbox has no
  `/etc/hosts`, and buildx's resolver sends `localhost` to the DNS proxy, which fails
  (`lookup localhost on 172.17.0.1:53: no such host`). Every reference below uses `127.0.0.1:5000`.
- A scratch bare repository (`/tmp/wt/remote.git`), cloned from this checkout, served as the
  promotion's `origin`. The script ran from a clone of it (`/tmp/wt/scratch`). Nothing was pushed
  to GitHub or any real registry.
- Grain's CA was staged as `.grain-ca.crt` in the scratch clone's root for `make image` and removed
  afterwards. The promote rebuild stages it itself, in its temporary checkout.
- Invocation, with no token, so the script uses Docker's existing (empty) credentials:
  `PACKAGE_PUBLISH_REGISTRY=127.0.0.1:5000 GRAIN_CI_REPO=bwsalmon/grain GRAIN_CI_REF=candidate
  IMAGE_SIGNING_KEY=/nonexistent bash scripts/release-images.sh promote [vX.Y.Z]`

## Stubs and skipped steps

- **Network stub in the image build.** The sandbox cannot reach `dl.k8s.io` (kubectl) or
  `github.com` (codex): `curl` times out. The first `make image` died in the kubectl stage. So
  the scratch release commit (`0261a7459`, on top of `b762a5be9`) adds one more commit: it replaces
  the kubectl download with a stub script and defaults `INSTALL_{CLAUDE,AGY,CODEX}_CLI` to `0`.
  It touches nothing about `ARG RELEASE`, the label or `RUN make build`. The v1.8.0-path commit
  (`f13c2d725`, on top of `v1.8.0`) gets the same stub.
- **Sandbox images are stubs.** `guest:sha-<short>` and `k8s-sandbox:sha-<short>` are `busybox:1.36`
  pushed under those tags. Building the real guest needs granule, which is out of scope for what
  promote does with them (registry retag, digest pinned into grain). The grain candidate image was
  really built with `make image`, naming them by digest the way `release-images.sh build` does.
- **The v1.8.0 path's source image is a stand-in.** `grain:sha-f13c2d7` is a retag of the
  candidate image. Promote only asks it `sandbox-image`, `k8s-sandbox-image` and `image`. The
  rebuild itself is real: v1.8.0's own Dockerfile, Makefile and source, built from a clean
  checkout of the tag.
- **Tags are `v1.99.0-rc.1` / `v1.99.0`**, not `v9.x`. Go ignores `v2+` tags for a module path with
  no `/vN` suffix, so a `v9.9.0-rc.1` candidate reported the pseudo-version
  `v1.9.0-rc.1.0.20261012042932-0261a74595e5`. This is expected Go behaviour, not a grain bug.
- **Image signing was skipped**: no key is available. Every rebuild logged
  `WARNING: no signing key at /nonexistent, so …:v1.99.0 is published unsigned`. `sign_image` is
  covered only by `tests/deploy/image_signing_test.go`.

## Steps and results

### 1. Candidate image at an rc tag

`make image` at `0261a7459`, tagged `v1.99.0-rc.1`, was pushed as `grain:sha-0261a74`
(digest `sha256:13b964ed…`).

| Check | Result |
|---|---|
| `grain version --json` | `"release": "v1.99.0-rc.1"` |
| label `org.opencontainers.image.version` | `""` |
| `/api/config` `version.release` (daemon run with `-disable-native-sandboxes`) | `v1.99.0-rc.1` |

### 2. `promote` to v1.99.0

The scratch remote has `v1.99.0` and `latest` (both annotated) on `0261a7459`. `promote` with no
argument ran in 78 s: it rebuilt grain, pushed `grain:v1.99.0`, retagged guest and k8s-sandbox, and
pointed `grain:latest` at `grain:v1.99.0`.

| Check | Result |
|---|---|
| label on `grain:v1.99.0`, read from the registry (`imagetools inspect`) | `v1.99.0` ✅ |
| label on a pulled `grain:latest` | `v1.99.0` ✅ |
| `grain version --json` in `grain:v1.99.0` | `"release": "v1.99.0"`, no `modified` ✅ |
| `/api/config` `version.release`, running `grain:latest` as a daemon | `v1.99.0` (commit `0261a7459…`) ✅ |
| `sandbox-image` candidate vs promoted | identical: `…/guest:sha-0261a74@sha256:b7f3d86d…` ✅ |
| `k8s-sandbox-image` candidate vs promoted | identical: `…/k8s-sandbox:sha-0261a74@sha256:b7f3d86d…` ✅ |
| `image` candidate vs promoted | identical: `…/grain:sha-0261a74` (by design: the same image reference the candidate stamped) ✅ |
| `grain:latest` digest == `grain:v1.99.0` digest | both `sha256:cb27ff3d…` ✅ |
| the temporary checkout is removed | no `/tmp/tmp.*` left ✅ |

### 3. Replayed promote

`latest` before: `sha256:cb27ff3d…`. The second `promote` printed
`…/grain:v1.99.0 already reports v1.99.0; nothing to rebuild`. There was no `docker build` in its
log. `latest` after: `sha256:cb27ff3d…`, unchanged ✅. The guest and k8s-sandbox `latest` tags kept
their digest too (`sha256:25ffb45c…`).

### 4. v1.8.0 path (Dockerfile without `ARG RELEASE`)

This path is live: production's git `latest` is `v1.8.0` (`0ca8da306`), so the next real promote
takes it. A scratch tag `v1.98.0` on `f13c2d725` (v1.8.0 plus the network stub; `ARG RELEASE`
count 0, `RUN make build …` at line 203) was promoted by name:

- It printed `v1.98.0's Dockerfile predates ARG RELEASE; added it to the copy built here`, and
  `latest stays put` (git `latest` names the other commit) ✅.
- The label is `v1.98.0` ✅, `grain version --json` reports `"release": "v1.98.0"` ✅, and `latest`'s
  digest stayed `cb27ff3d…` ✅.
- **But** `grain version --json` also reported `"modified": true`, and `grain version` printed
  `v1.98.0 · f13c2d7-dirty · 2026-10-12 04:41Z` ❌. See finding 1.

After the fix, the same commit promoted as `v1.97.0` reports `v1.97.0 · f13c2d7 · 2026-10-12 04:41Z`,
with no `modified` ✅.

## Findings

### 1. A promoted pre-`ARG RELEASE` release reports a dirty tree (fixed)

`rebuild_release_grain` adds `ARG RELEASE` to an old Dockerfile with `sed -i` in its temporary
checkout. The checkout's `.git` is part of the build context (`.dockerignore` keeps it on purpose),
and `go build` stamps `vcs.modified` from `git status`. The patch is an uncommitted edit, so the
v1.8.0 binary, which would become `latest`, reported `modified: true` and `<short>-dirty`. The
`.grain-ca.crt` copy is not the cause: it is gitignored in v1.8.0 as on candidate, and the v1.99.0
rebuild with the same CA copy was clean.

Reproduction (deterministic, no docker):

```sh
git clone -q --branch v1.8.0 https://github.com/bwsalmon/grain /tmp/v180 && cd /tmp/v180
sed -i 's/^\(RUN make build .*\)$/ARG RELEASE=\n\1 RELEASE=${RELEASE}/' Dockerfile
git status --porcelain     # " M Dockerfile" -> go build stamps vcs.modified=true
```

In the suite, `TestPromoteRebuildsFromATreeGitCallsClean` without the fix fails with
`git status: [ M Dockerfile]`.

Fix (`scripts/release-images.sh`):
- After patching, mark the Dockerfile `git update-index --skip-worktree`, so `git status`, and so
  Go, sees the checkout as the tag it is. Verified for real above (`v1.97.0`).
- Before pushing, the rebuild now also refuses an image whose `grain version --json` reports
  `"modified":true`. It was built from a clean checkout, so a dirty one means this script dirtied
  it. Nothing is pushed and no tag moves. The check is only in the rebuild, not in
  `check_release_stamp`, which `build` also uses on CI's own tree.

Regression tests (`tests/deploy/promote_images_test.go`, docker stubbed, as the existing promote
tests are):
- `TestPromoteRebuildsFromATreeGitCallsClean`: the stub's `docker build` logs `git status
  --porcelain` of its context, which must be empty after the patch.
- `TestPromoteOfARebuildReportingAModifiedTreeMovesNothing`: a rebuild answering
  `"modified": true` is not pushed, and nothing is retagged.

Both fail without the script change and pass with it. `releaseRepo` gained the `/.grain-ca.crt`
`.gitignore` entry grain's own tree has, because CI hosts carry the CA the rebuild copies in.

### 2. Observation: the guest and k8s-sandbox `vX.Y.Z` / `latest` digests differ from `sha-` (not filed)

`docker buildx imagetools create --tag X sha-…` wraps a single-manifest source in a new OCI index.
So `guest:v1.99.0` is `sha256:25ffb45c…` (an index) while `guest:sha-0261a74` is `sha256:b7f3d86d…`
(the manifest inside it). This predates `ecf768a07` and changes nothing a deployment pins: the grain
image names the sandbox images by the `sha-` manifest digest, which is what `publish_images` signs.
A verifier pointed at `guest:latest` by tag would find the index unsigned, though. It is noted
here, and not filed, because nothing resolves those tags for verification today.

## Docker-free test coverage

`tests/deploy/promote_images_test.go` already covered the script logic without docker: tag
selection, rebuild arguments, the v1.8.0 Dockerfile patch, replay rebuilding nothing, a rebuild
that does not report its release, and missing images. The two tests above add the dirty-tree case.
`go test ./tests/deploy` passes locally.

## CI verdict

The grain CI full suite passed on `8654ca6` (the fix and the tests; run `9b59a353ab922e7f`, PR #3525):
frontend, go, installer, lint, package cache, postgres, relay image, terraform and ui e2e all
succeeded. GitHub's red "sandbox guest image" and "Kubernetes sandbox image" checks are not this
repository's gate. The commit adding this paragraph is documentation only, and it was re-run to
green before merge.
