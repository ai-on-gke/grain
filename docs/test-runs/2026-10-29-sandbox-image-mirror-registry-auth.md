# 2026-10-29: Sandbox image mirror and Artifact Registry auth

Task 1802 (`grain/049f1e09/task-1802`) walked §8 of
`docs/test-plans/2026-10-29.md`: `-sandbox-image-mirror` /
`sandbox_image_mirror` / `sandboxImageMirror` (PR #132) and
`pkg/registryauth` with the `scripts/setup.sh` docker login (PR #106). It
ran a real `grain daemon` against two real `registry:2` registries, then
the Settings page in Chromium. It found three bugs in the mirror path and
fixed them on this branch, with regression tests.

## Commit and environment

- **Base tested**: `d71aa7e4` (`main`, "Merge pull request #133 from
  ai-on-gke/grain/049f1e09/task-1752"). Every fix below was checked again
  against a daemon rebuilt from this branch.
- **Toolchain**: Debian, Go 1.26.10, Docker 29.9.2 (containerd image
  store), Node v20.19.0, Playwright Chromium `chromium-1234` from
  `/var/cache/playwright`.
- **Registries** (real `registry:2`, plain HTTP on loopback, which docker
  and grain both treat as insecure without being told):
  ```
  docker run -d --name srcreg -p 127.0.0.1:5000:5000 registry:2
  docker run -d --name mirreg -e REGISTRY_STORAGE_DELETE_ENABLED=true -p 127.0.0.1:5001:5000 registry:2
  docker tag busybox 127.0.0.1:5000/src/guest:sha-aaaa111 && docker push 127.0.0.1:5000/src/guest:sha-aaaa111
  docker tag alpine  127.0.0.1:5000/src/guest:sha-bbbb222 && docker push 127.0.0.1:5000/src/guest:sha-bbbb222
  ```
  The local copies were removed after the push, so the daemon had to pull.
- **Builds**: `cd ui && npm ci && npm run build`, then two binaries. Each
  was stamped the way the Makefile's `SANDBOX_IMAGE` stamps a release:
  ```
  go build -ldflags "-X github.com/ai-on-gke/grain/pkg/daemon.DefaultSandboxImage=127.0.0.1:5000/src/guest:sha-aaaa111" -o /home/debian/bin/grain  ./cmd/grain
  go build -ldflags "-X github.com/ai-on-gke/grain/pkg/daemon.DefaultSandboxImage=127.0.0.1:5000/src/guest:sha-bbbb222" -o /home/debian/bin/grain2 ./cmd/grain
  ```
- **Daemon** (`/var/tmp/mw/start.sh`, on a fresh `-data-dir` for each scenario
  unless noted):
  ```
  grain daemon -data-dir /var/tmp/mw/data -ui-addr 127.0.0.1:18090 -sandbox-port-base=0 -as alice \
    -agy-path /bin/true -gemini-api-key-file <dummy> -github-host 127.0.0.1:9 -github-insecure-http \
    -k8s-granule-sandboxes -k8s-workspace /home/debian -k8s-git-proxy-host 127.0.0.1 \
    -k8s-kubectl /var/tmp/mw/kubectl [-sandbox-image-mirror 127.0.0.1:5001/mirror/guest]
  ```
  `grain settings` used `GRAIN_SERVER=http://127.0.0.1:18090` and
  `GRAIN_TOKEN=$(cat data/local-api-token)`.

## Stubs

| Boundary | How | Why |
|---|---|---|
| Kubernetes cluster | `-k8s-kubectl` pointed at a shell script that logs its argv and answers `{"items":[]}`. For the 403 case, a second script answers `get nodes … providerID` with `gce://cluster-proj/us-central1-a/gke-node-1` and the pod waiting-reason query with an `ErrImagePull: … 403 Forbidden` line for `us-docker.pkg.dev/source-proj/grain/guest:v1`. | There is no cluster here. The mirror runs on the daemon host's docker and needs none. The cluster is only consulted for the pull check. |
| Google token | `registryauth.TokenSource` replaced in Go tests only. No gcloud and no metadata server here, so the daemon itself ran tokenless, which is correct for loopback registries. | No GCP credential in the sandbox. |
| Artifact Registry | Not impersonated. The token-scoping proof is a Go test with separate `httptest` TLS servers routed by host name (below). | No `*-docker.pkg.dev` is reachable. |
| setup.sh | `registry_login`, `google_registry_host` and `gcp_access_token` were extracted with `awk` and run with a fake `docker` (logging argv and stdin) and a fake `gcloud` on `PATH`. | Running the installer would provision the host. |

## Outcome

| # | Check | Outcome |
|---|---|---|
| 1 | Startup with `-sandbox-image-mirror`, first start | **Bug, fixed.** Pods were created from the source image and nothing was mirrored. |
| 2 | Startup mirror round trip (pull → tag → push → RepoDigest) | Pass after fix 1 |
| 3 | Restart after a failed push to the mirror | **Bug, fixed.** The daemon pinned a digest the mirror never received. |
| 4 | Restart after the mirror repository was emptied | **Bug, fixed.** The same pin, from docker's stale RepoDigest. |
| 5 | Live config: set `sandboxImageMirror` with `grain settings` | Pass. The log line was misleading; fixed. |
| 6 | Live config: clear it on a daemon started with a mirror | **Bug, fixed.** New sandboxes stayed on the mirrored image. |
| 7 | Second build (new stamp) | Pass: the new tag was mirrored. Old tags are not cleaned at startup (see follow-ups). |
| 8 | Token scoping over real HTTP (redirect, non-Google registry) | Pass. Pinned as a test. |
| 9 | `scripts/setup.sh` docker login via stdin | Pass (security review below) |
| 10 | Settings → Sandboxes 403 suggestion, 1280 and 390 px, light and dark | Pass |

## Steps and observations

### 1. Startup with the mirror flag (bug)

On the base build, a daemon started with `-sandbox-image-mirror
127.0.0.1:5001/mirror/guest` logged:

```
grain daemon: Kubernetes granule sandboxing -- one privileged pod per run in namespace "grain" from image "127.0.0.1:5000/src/guest:sha-aaaa111", …
```

`/v2/_catalog` on the mirror stayed `{"repositories":[]}`: nothing was
mirrored. The cause: `-k8s-granule-image` defaults to
`daemon.DefaultSandboxImage`, the stamp itself. On the start that seeds the
store, `cfg.K8sGranuleImage` therefore holds the stamp, and
`EffectiveK8sGranuleImage` read any non-mirror value there as an operator's
pin that opts out of the mirror. The existing unit test only covered a
`K8sGranuleImage` already inside the mirror repository.

**Fix** (`pkg/daemon/sandbox_backends.go`): under a mirror, a
`K8sGranuleImage` equal to the stamp (or the unstamped default) counts as
unset. A pin naming an image from elsewhere still wins. Regression test:
`TestEffectiveK8sGranuleImageMirrorsWhenTheFlagDefaultIsTheStamp`.

### 2. Mirror round trip (after fix 1)

```
grain daemon: mirrored sandbox image 127.0.0.1:5000/src/guest:sha-aaaa111 -> 127.0.0.1:5001/mirror/guest:sha-aaaa111@sha256:f97baa53…dce4
grain daemon: Kubernetes granule sandboxing -- … from image "127.0.0.1:5001/mirror/guest:sha-aaaa111@sha256:f97baa53…dce4"
```

The mirror's tag list was `{"name":"mirror/guest","tags":["sha-aaaa111"]}`.
A `HEAD /v2/mirror/guest/manifests/sha-aaaa111` answered
`Docker-Content-Digest: sha256:f97baa53…dce4`, the digest the daemon pinned.
`GET /api/settings` reported `sandboxImage: ""`, `sandboxImageWarning: ""`
and `sandboxImageCheck: null`. The mirrored image is the stock image, with
no custom-image drift warning.

### 3. Restart after a failed push (bug)

Steps: the mirror registry stopped, then a fresh start. The log said:

```
WARNING: mirroring sandbox image … to 127.0.0.1:5001/mirror/guest failed (docker push 127.0.0.1:5001/mirror/guest:sha-aaaa111: exit status 1); using 127.0.0.1:5001/mirror/guest:sha-aaaa111
```

Then the registry was started again (empty) and the daemon restarted. It
logged **no** mirror line and created pods from
`127.0.0.1:5001/mirror/guest:sha-aaaa111@sha256:f97b…`. The mirror still
answered `NAME_UNKNOWN` for that repository. The cause:
`inspectMirroredRepoDigest` accepted the only RepoDigest of the locally tagged
target. That digest belonged to the *source* repository and was carried over
by `docker tag`, so a push that never happened read as done. The warning
also said only `exit status 1`.

**Fix** (`pkg/daemon/sandbox_image.go`): only a RepoDigest recorded for the
mirror repository counts. A failed docker step's error now carries docker's
last line (`denied: …`, `connection refused`). Regression test:
`TestMirrorSandboxImageDoesNotPinASourceDigestThatWasNeverPushed`.

### 4. Restart after the mirror was emptied (bug)

After fix 3 the same scenario still pinned the unpushed digest, because an
earlier successful push had left a mirror RepoDigest in local docker. Docker
keeps that record after the registry loses the image, for example when the
repository is recreated or cleaned up.

**Fix**: the fast path now asks the mirror itself
(`upgrade.RegistryManifestDigest`, a `HEAD` that sends the Google token only
to a Google host). It skips the push only when the mirror answers the same
digest. Re-run: the daemon pushed again (`mirrored sandbox image … ->
127.0.0.1:5001/mirror/guest:sha-aaaa111@sha256:f97b…`, tag list back to
`["sha-aaaa111"]`). A restart after that took the fast path with no push.
Regression test:
`TestMirrorSandboxImageChecksTheRegistryBeforeTrustingALocalRepoDigest`.

### 5–6. Live config

On a daemon started **without** a mirror,
`grain settings -sandbox-image-mirror 127.0.0.1:5001/live/guest` was applied
within one refresh, with no restart:

```
grain daemon: mirrored sandbox image 127.0.0.1:5000/src/guest:sha-aaaa111 -> 127.0.0.1:5001/live/guest:sha-aaaa111@sha256:f97b…
grain daemon: custom sandbox image cleared; new sandboxes are created from 127.0.0.1:5001/live/guest:sha-aaaa111@sha256:f97b…
grain daemon: applying changed settings without a restart: sandbox-image-mirror  -> 127.0.0.1:5001/live/guest
```

`grain settings` printed `sandbox image mirror: 127.0.0.1:5001/live/guest`.
Clearing it (`-sandbox-image-mirror ''`) returned new sandboxes to
`127.0.0.1:5000/src/guest:sha-aaaa111`. The "custom sandbox image cleared"
line was wrong both times: no custom image was ever set.

On a daemon started **with** `-sandbox-image-mirror`, clearing it logged only
`applying changed settings … mirror/guest -> ` and **no** image change. New
sandboxes stayed on the mirrored image until a restart.
`applySandboxImage` fell back to `baseStockImage`, the stock image the
process started with, which on that daemon is the mirrored image.

**Fix** (`pkg/daemon/live_config.go`): when the mirror changes, the stock
image is worked out again from the new settings. The log says "stock sandbox
image changed" unless a custom image really was cleared. Re-run on the fixed
build: clearing logged `stock sandbox image changed; new sandboxes are
created from 127.0.0.1:5000/src/guest:sha-aaaa111`. Setting
`127.0.0.1:5001/live2/guest` live pushed and switched to it. Regression test:
`TestApplySandboxImageClearingTheMirrorReturnsToTheStamp`.

### 7. Second build

The `sha-bbbb222` build was restarted on the same data directory with
`-sandbox-image-mirror 127.0.0.1:5001/mirror/guest`. It logged `loadConfig:
ignoring -sandbox-image-mirror=…, stored config already has
127.0.0.1:5001/live2/guest -- use 'grain settings' to change it`, which is
the documented store-wins rule. It then mirrored `sha-bbbb222` into
`live2/guest` and pinned its digest (`sha256:c2661275…`).

Old tags are **not** cleaned on this path. Local docker kept
`live/guest:sha-aaaa111`, `live2/guest:sha-aaaa111` and
`mirror/guest:sha-aaaa111`, and the registries kept every tag. The only
cleanup PR #132 adds is in an upgrade's `cleanOldImages`, which removes
*local* images and keeps the new and previous mirror targets
(`TestUpgraderImageMirrorsSandboxImageAndCleansOldMirroredTags`). Nothing
removes tags from the mirror registry. A full `-upgrade-image` `stageImage`
run was not driven here, because it needs a published grain image to run
`sandbox-image` inside. See the follow-ups.

### 8. Token scoping over real HTTP

`pkg/upgrade/registry_token_scope_test.go`
(`TestGoogleTokenReachesOnlyTheGoogleRegistryAcrossARedirect`) runs three
separate TLS servers, routed by host name:

- `us-docker.pkg.dev` answers a manifest `HEAD`, and redirects its tag list
  (307) to `blobs.example`.
- `blobs.example` answers the tag list.
- `registry.example` is a non-Google registry.

`RegistryManifestDigest` and `ListRegistryTags` against the Google host sent
`Bearer ya29.walkthrough` on every request there. The redirected request
reached `blobs.example` with **no** `Authorization` header. Both calls
against `registry.example` carried none, and the token source was not even
asked. Read alongside the code: `ListRegistryTags` follows `Link`
pagination only on the same host (`nextPage`), and the anonymous token
exchange (`registryToken`) never sends the Google token to a challenge's
realm. The UI's reads (`CheckImageManifest`, `FetchImageLabels`) are already
pinned by `pkg/ui/google_registry_internal_test.go`, including a blob
redirect to `storage.googleapis.com`.

### 9. `scripts/setup.sh` docker login (security review)

`registry_login`, run with a fake `gcloud` printing `ya29.from-gcloud`:

| `GRAIN_IMAGE` | docker argv | stdin |
|---|---|---|
| `us-docker.pkg.dev/p/r/grain` | `login https://us-docker.pkg.dev -u oauth2accesstoken --password-stdin` | the token |
| `gcr.io/p/grain` | `login https://gcr.io -u oauth2accesstoken --password-stdin` | the token |
| `ghcr.io/bwsalmon/grain/grain` | no login (no `GRAIN_IMAGE_PULL_TOKEN`) | — |
| `evil.test?x-docker.pkg.dev/p/grain` | no login | — |
| `EVIL-docker.pkg.dev/p/g` | no login | — |
| `us-docker.pkg.dev:443/p/g` | no login | — |

The token is written by `printf` (a builtin) into the pipe and never appears
in argv. docker's output goes to `/dev/null`. `google_registry_host` accepts
only `[a-z0-9.-]+` ending in `-docker.pkg.dev`, `gcr.io` or `.gcr.io`, so no
query, fragment, port or user-info trick passes. A non-Google host is only
ever sent the operator's own `GRAIN_IMAGE_PULL_TOKEN`. The sandbox-image
login (`ensure_granule_oci_image`) uses the same check and the same stdin
pipe. One difference from Go: `registryauth.IsGoogleRegistry` lower-cases
first, so it accepts `EVIL-docker.pkg.dev` where the script does not. That
is harmless, since DNS is case-insensitive and both answers name a Google
host. No change to `scripts/setup.sh` was needed.

### 10. Settings → Sandboxes with a forced 403

The daemon ran with the 403 kubectl and `-k8s-granule-image
us-docker.pkg.dev/source-proj/grain/guest:v1` (a non-`sha-` tag, so no
mirror is derived). It logged, and `GET /api/settings` returned as
`k8sGranuleImageWarning`:

> cluster nodes cannot pull us-docker.pkg.dev/source-proj/grain/guest:v1
> across GCP organization boundaries (403 Forbidden); configure a sandbox
> image mirror repository in the cluster's GCP project with: grain settings
> -sandbox-image-mirror us-docker.pkg.dev/cluster-proj/grain/guest

Playwright signed in with the access code and opened `/settings/sandboxes`.
The `k8s-image-warning` alert in the Kubernetes card showed the full text at
1280×860 (box x=505, w=726, h=88) and 390×844 (box x=25, w=340, h=188),
light and dark. `document.documentElement.scrollWidth` equalled the
viewport at both widths, so the long command wraps inside the alert without
horizontal overflow. No UI code changed.

## Findings, fixes and follow-ups

Fixed on this branch (all in `pkg/daemon`, with tests in
`sandboximage_test.go`):

1. A first start with `-sandbox-image-mirror` never mirrored, because the
   `-k8s-granule-image` flag default was read as a pin.
2. After a failed push, a restart pinned the source repository's digest
   under the mirror name, which the mirror never received.
3. A stale local RepoDigest was trusted without asking the mirror.
4. Clearing the mirror live left new sandboxes on the mirrored image. The
   log line claimed a custom image had been cleared.
5. Mirror warnings now carry docker's own error line.

Follow-ups proposed (not fixed here):

- **Settings computes the mirrored guest from the UI's version default, not
  the daemon's stamp.** With a build stamped by `SANDBOX_IMAGE`, the
  Kubernetes card probed `127.0.0.1:5001/mirror/guest:latest` and warned
  "did not resolve", while the daemon really ran `…:sha-aaaa111@sha256:…`.
  `CheckImagePullConnectivity` can also blame `imageRef` for any pod's
  `ErrImagePull` (`k8s.go`, the `||` in the waiting-reason loop).
- **No cleanup of old mirrored tags outside an upgrade.** Startup and live
  mirroring leave every earlier local tag, and nothing ever prunes tags from
  the mirror registry.

Not exercised: an `-upgrade-image` upgrade's `stageImage` mirror step end
to end (it needs a published grain image to run `sandbox-image` in; it is
covered by `TestUpgraderImageMirrorsSandboxImageAndCleansOldMirroredTags`).
A real GKE cluster and a real Artifact Registry token were also not
available.
