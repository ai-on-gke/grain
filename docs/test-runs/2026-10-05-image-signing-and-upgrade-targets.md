# Signed images, upgrade verification and upgrade targets: walkthrough

**Date:** 2026-10-05
**Plan:** `docs/test-plans/2026-10-05.md` §6
**Branch:** `grain/task-1997`
**Commit tested:** `795d33a1a` (`Merge pull request #2940 from bwsalmon/grain/task-2063`), the tip of `origin/candidate`. `git branch -r --contains` lists `origin/candidate` (and `origin/main`) for every commit §6 names: `442515c42`, `f09ac9136`, `723a4eebe`, `7cc2474b6`, `90b68a560`, `d4506511b`, `3b4d91e31`, `a7d083ac1`, `a56434fc6`, `7bb7a2de2`, `02a1995ea`. The fixes below were re-checked live on this branch.
**Environment:**

- Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, 4 vCPU / 7 GiB.
- Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 with `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` (chromium-1234), Docker 29.8.2.
- `registry:2` on `localhost:5000`, started with `docker run -d -p 5000:5000 --name reg registry:2`.
- cosign v2.4.1 (GitCommit `9a4cfe1a…`). This is the official release binary, copied out of `ghcr.io/sigstore/cosign/cosign:v2.4.1@sha256:b03690aa…`, which is the image and digest the Dockerfile pins.
  - `github.com` release downloads hang at the TLS handshake from this sandbox.
  - A `go install …/cosign@v2.4.1` through the module proxy also worked, but slowly.
- Key pairs: `COSIGN_PASSWORD= cosign generate-key-pair` for the release key, plus a second pair, `other`, to stand in for a wrong key.
- No model was used.

**Sandbox quirk worth knowing:** `/etc/hosts` here had no `localhost` line. Docker resolves `localhost:5000` by itself; the Go-built cosign asks DNS and fails with `lookup localhost on 172.17.0.1:53: no such host`. I wrote `127.0.0.1 localhost` into `/etc/hosts`. A `::1` line breaks it again, because the registry's port is published on IPv4 only.

## Stubs

- **None of grain's APIs or React.**
- **Stand-in images.** The images I signed and verified are small `FROM scratch` / `FROM busybox` images pushed under the release's real names (`localhost:5000/bwsalmon/grain/{guest,k8s-sandbox,grain}:sha-795d33a`). The real guest build is a nested-VM build of about 25 minutes. The `push` phase of `release-images.sh` ran for real against them, from a hand-written `$RELEASE_IMAGES_STATE` that names the pinned digests `docker push` reported. Only the `build` phase was skipped.
- **The UI server for step 4.** `grain demo` wires no Upgrader and has no `-upgrade-*` flags, so `/api/upgrade/targets` cannot be walked in demo (follow-up proposed). I used a 70-line Go server kept out of the tree, modelled on `ui/e2e/defaultsserver`. It runs:
  - the real `pkg/ui` server and SQLite store;
  - the production bundle;
  - a real image-mode `upgrade.Upgrader`, with `Repository=localhost:5000/bwsalmon/grain/grain`, `VerifyCmd=cosign verify --key …` and the flag's `-upgrade-allowed-branches`.

  As in demo, password sign-in is off and identity headers are trusted.
- **Failing the targets call.** `page.route` was used only to abort, 502 or slow the browser's own `GET /api/upgrade/targets`. The registry failure itself was real: a server pointed at `127.0.0.1:5999`, where nothing listens.
- **The release cut** ran against a scratch bare repository. A `pre-receive` hook stood in for a remote that refuses a push.

## 1. `release-images.sh` signing, `setup.sh` verification, `REQUIRE_IMAGE_SIGNING`

Command (the push phase, against the local registry, using Docker's existing credentials):

```
PACKAGE_PUBLISH_REGISTRY=localhost:5000 GRAIN_CI_REPO=bwsalmon/grain GRAIN_CI_REF=candidate \
GRAIN_CI_COMMIT=795d33a1a… RELEASE_IMAGES_STATE=/tmp/s6/state.env IMAGE_SIGNING_KEY=/tmp/s6/cosign.key \
bash scripts/release-images.sh push
```

Each step and its outcome:

- **The first run published to the wrong registry: finding A.** It printed `using Docker's existing credentials for ghcr.io`. It appended `140.82.121.34 ghcr.io` to `/etc/hosts`, through `ensure_direct_registry_route`.
- **The first run killed the registry: finding C.** It restarted dockerd through `ensure_network_mtu`, and the local registry container died with it.
- **After the fix it signs every image.** Exit 0. Each image is signed by digest before anything moves (`Pushing signature to: …`, `signed …@sha256:…`). The grain sha- tag is pushed, then signed, then the three `candidate` tags are pushed. Tag lists:
  - `guest`: `sha-795d33a`, `candidate`, `sha256-5e04512a….sig`
  - `k8s-sandbox`: `sha-795d33a`, `candidate`, `sha256-1f8e6f31….sig`
  - `grain`: `sha-795d33a`, `candidate`, `sha256-c7deb3cd….sig`

  So every digest has its own `.sig` tag.
- **`setup.sh`'s verify path accepts the signed image and pins it.** I extracted `image_digest_ref`, `verify_image_signature` and `verify_grain_image` from `scripts/setup.sh`, then ran them with real docker and cosign after `docker pull …/grain:candidate`.
  - Right key: exit 0. `GRAIN_IMAGE_REF` becomes `…/grain:candidate@sha256:c7deb3cd…`, the verified digest.
  - Wrong key (`other.pub`): exit 1, `no matching signatures: invalid signature …` and `… is not signed with /tmp/s6/other.pub -- refusing to run it.`
- **`setup.sh` refuses an unsigned re-push to the same tag.** I pushed a different, unsigned image to `…/grain:candidate` (`sha256:53186438…`) and pulled it again. Result: exit 1, `Error: no signatures found` and `… refusing to run it.`
- **`REQUIRE_IMAGE_SIGNING` with no key (`IMAGE_SIGNING_KEY=/tmp/s6/nokey`): finding B.**
  - With `true`: exit 1, `REQUIRE_IMAGE_SIGNING is set and there is no signing key …`. This was only at the first signature, after the build would have run.
  - With `1`, before the fix: **exit 0**. It printed three `WARNING: no signing key … published unsigned` lines and pushed all three images and the `candidate` tags. A grain image digest moved under `candidate` (`sha256:8191c0e8…`).
  - After the fix: exit 1 with the same error, before any build or push.

## 2. `pkg/upgrade`'s signature step

I wrote a Go harness (kept out of the tree) that builds `upgrade.New(upgrade.Config{Image: &upgrade.ImageConfig{…}})`, set the way `cmd/grain/daemon.go` sets it for `-upgrade-image`:

- `Repository=localhost:5000/bwsalmon/grain/grain`;
- `VerifyCmd=[cosign verify --key=/tmp/s6/cosign.pub --insecure-ignore-tlog=true]`;
- `HealthCheckArgs=[schema-version]`;
- `RestartCmd=[true]`;
- `AllowedBranches=[**]`.

It calls `Start(branch)` and waits on `Done()`. I published two busybox images: `:unsigned` (`sha256:a5c0db30…`) and `:signed` (`sha256:54039f01…`), the second signed with the release key. While each upgrade ran, `docker events --filter type=container` recorded any container created.

- **`unsigned`:** phase `failed`.
  - The pull step is `done` (it pulled `…:unsigned@sha256:a5c0db30…`).
  - The signature step is `failed`: `cosign verify … exit status 10 … Error: no signatures found`.
  - The health check and every later step are `pending`.
  - **No container events at all.** `docker ps -a` showed the same single container (the registry) before and after.
  - The ref file was never written.
- **`signed`:** phase `ok`.
  - The signature step reports `…:signed@sha256:54039f01… is signed`.
  - The health check ran: one `create/start/die/destroy` of `…:signed@sha256:54039f01…`.
  - The ref file says `GRAIN_IMAGE=localhost:5000/bwsalmon/grain/grain:signed@sha256:54039f01…`.

Pass: an unsigned image is refused before anything runs it, and a signed one passes.

## 3. The grain image's bundled cosign

- **A full `docker build .` failed in this sandbox after 123 s.** `npm ci` inside the build fails with `SELF_SIGNED_CERT_IN_CHAIN`, because the package cache's CA is not trusted inside a plain build. `release-images.sh` copies `.grain-ca.crt` into the context for exactly this. This is an environment limit, not a fault of the Dockerfile.
- **What I checked instead.** I read the pins:
  - `ARG COSIGN_IMAGE=ghcr.io/sigstore/cosign/cosign:v2.4.1@sha256:b03690aa…`, which is the same digest as the release image I pulled.
  - The final stage has `COPY --from=cosign /ko-app/cosign /usr/local/bin/cosign` (Dockerfile:329).
- **The bundled binary runs.** I built those two stages alone: `FROM ${COSIGN_IMAGE} AS cosign` / `FROM ${RUNTIME_IMAGE}` (the pinned `debian:bookworm-slim@sha256:3783cc01…`) / that `COPY`. Then `docker run --rm --entrypoint /usr/local/bin/cosign … version` printed `GitVersion: v2.4.1`.
- **The published image was not checked.** A `docker pull ghcr.io/bwsalmon/grain/grain:candidate` did not finish within this run.

## 4. The Upgrade tab's picker in a real browser

I drove the picker with Playwright's `chromium` against the production bundle (`npm ci && npm run build`). The script is kept out of the tree. Four servers ran:

| Server | Port | `-upgrade-allowed-branches` | Registry | Signed in as |
|---|---|---|---|---|
| A | 18431 | `main,release/1.6,feature/*` | `localhost:5000` | browser-test (admin) |
| B | 18432 | `**` | `localhost:5000` | browser-test (admin) |
| C | 18433 | default (`main`) | `localhost:5000` | alice (team member) |
| D | 18434 | default (`main`) | `127.0.0.1:5999` (nothing listens) | browser-test (admin) |

The registry held `v1.4.0`, `v1.5.0`, `v1.6.0`, `v1.7.0-rc.1`, `latest`, `main`, `candidate`, `sha-…` and `.sig` tags.

Results (26 checks, all PASS on this branch):

- **Groups.** In order: Standard branches, Released versions, Allowed on this deployment.
- **Released versions.** `v1.6.0`, `v1.5.0`, `v1.4.0`, newest first. `v1.7.0-rc.1`, `sha-…` and `.sig` are not listed.
  - Before the loopback fix (finding D) this list was empty for a `localhost` registry.
- **Refused entries on A.** `latest`, `candidate`, `v1.6.0`, `v1.5.0` and `v1.4.0` have `aria-disabled="true"` and read "— not allowed on this deployment". Clicking `latest` leaves the box empty.
  - `main` ("— the head of main") and `release/1.6` (under "Allowed on this deployment") can be picked.
  - The helper text names the allowlist: "this deployment only upgrades to main, release/1.6, feature/* (-upgrade-allowed-branches)".
- **Free text inside the allowlist (A).** Typing `feature/x` and pressing Upgrade posts `{"branch":"feature/x","afterTasksFinish":false}` and gets 202. The real upgrade then fails at the pull, `…:feature..x: not found`, which is correct because there is no such tag. The pane shows that failure.
- **Free text outside the allowlist (D).** `experiment/y` gets 400: `branch is not one this deployment may upgrade to: "experiment/y" matches none of main (set with -upgrade-allowed-branches)`. The alert shows the same text.
- **`**` (B).** All six entries can be picked, with their channel notes. Picking `v1.5.0` fills the box. The helper text says "Pick one from the list or type any branch."
- **Real registry failure (D).** The helper text ends with "Released versions could not be listed: Get "https://127.0.0.1:5999/v2/…": dial tcp 127.0.0.1:5999: connect: connection refused". The box still takes typed text.
- **The targets call itself failed through `page.route` (A), finding E.**
  - Abort and 502, before the fix: an empty list, no error, and a helper text that still invited "Pick one from the list". Free text still worked.
  - Abort and 502, after the fix: "The list of branches could not be loaded (Failed to fetch); type a branch instead." (and "… (bad gateway) …" for the 502).
  - 2.5 s delay: the list arrives and is complete.
- **Non-admin (C).** `GET /api/upgrade/targets` gets 403 `user "alice" does not have administrator privileges`.

## 5. Release cut against a scratch bare remote

I ran `scripts/release-candidate-cut.sh` with `FORCE=true RELEASE_INTERVAL_DAYS=0`. The remote was `/tmp/s6/cut/origin.git`.

1. **First cut:** `v1.0.0-rc.1` and `candidate` were pushed.
2. **Conflicting back-merge.** `shared.txt` was changed on both `candidate` and `main`. The cut exited 1 with `CONFLICT (content): Merge conflict in shared.txt` and `Error: Cannot cut release candidate until conflicts between candidate and main are resolved.` `git ls-remote origin` was **identical before and after**: no tag or branch moved. Pass.
3. **A refused rc tag push: finding F.** I reconciled main, then installed a `pre-receive` hook that refuses `refs/tags/v1.1.0-rc.1`.
   - Before the fix: **exit 0** and `Successfully cut candidate branch and tagged v1.1.0-rc.1.` The remote had `v1.0.0` and `latest`, but no `v1.1.0-rc.1`.
   - After the fix: exit 1, `! [remote rejected] v1.1.0-rc.1 … (pre-receive hook declined)` and `Error: could not push v1.1.0-rc.1 to origin; candidate moved but is untagged.`

## Findings

| # | Finding | Reproduction | Outcome |
|---|---|---|---|
| A | `release-images.sh` with `PACKAGE_PUBLISH_REGISTRY` set and no token replaced the registry with the legacy `REGISTRY_PUSH_REGISTRY` (empty), so it published to `ghcr.io`. | `PACKAGE_PUBLISH_REGISTRY=localhost:5000 … bash scripts/release-images.sh push` prints `using Docker's existing credentials for ghcr.io`. | **Fixed:** the legacy registry replaces it only when it names one. Test: `TestAHandRunPublishesToTheRegistryItWasGiven`. |
| B | `REQUIRE_IMAGE_SIGNING=1` was not "on". Only the literal `true` was, so a release told to require signatures published three unsigned images, moved the branch tag and exited 0. With `true`, it failed only at the first signature, after the build. | `REQUIRE_IMAGE_SIGNING=1 IMAGE_SIGNING_KEY=/nonexistent … release-images.sh push` exits 0 with three `WARNING … published unsigned` lines. | **Fixed:** `1`, `true` and `yes` all count, and the check runs before `build`, `push` or `all` touches docker. `docs/image-signing.md` updated. Test: `TestARequiredSignatureWithNoKeyStopsBeforeTheBuild`. |
| C | `ensure_network_mtu` restarted dockerd whenever the default interface's MTU was above 1460, even when `daemon.json` already said `"mtu": 1460` and nothing in it changed. That stops every container on the host. | On a host with eth0 at MTU 1500 and `daemon.json` already at 1460, run any phase. `systemctl show docker -p ActiveEnterTimestamp` moves, and running containers exit. | **Fixed:** restart only if `daemon.json`'s checksum changed. Test: `TestTheMTUClampRestartsDockerOnlyWhenDaemonJSONChanged` (static: every restart sits behind that check). |
| D | `upgrade.ListRegistryTags` spoke only HTTPS. An image-mode deployment on a `localhost`/loopback registry can pull and `cosign verify` over plain HTTP, but its picker listed no versions. It reported `http: server gave HTTP response to HTTPS client` instead. | Run a `registry:2` on `localhost:5000` and `GET /api/upgrade/targets` with `-upgrade-image=localhost:5000/…`. | **Fixed:** retry over HTTP only when the host is loopback **and** the server answered HTTP (`http.ErrSchemeMismatch`), which is Docker's own rule. Test: `TestListRegistryTagsFallsBackToHTTPOnlyOnLoopback`. |
| E | When `GET /api/upgrade/targets` itself failed (502, dropped connection), `BranchPicker` swallowed it into an empty list, with helper text that still said "Pick one from the list". | `page.route("**/api/upgrade/targets", r => r.fulfill({status: 502, …}))`, then open the picker. | **Fixed:** a 404 (older daemon) stays silent, and anything else is said in the helper text. Vitest: two new `UpgradePanel.test.jsx` cases. Re-walked live. |
| F | The release cut's last two pushes, `candidate --force` and the new `v1.X.0-rc.1`, were still `2>/dev/null \|\| true`. A refused rc tag reported "Successfully cut" and exited 0. a56434fc6 meant refused tag pushes to fail the cut, but covered only the release tag and `latest`. | Run the cut against a bare remote with a `pre-receive` hook that refuses `refs/tags/v1.1.0-rc.1`. | **Fixed:** both pushes fail the cut and say what was refused. Test: `TestReleaseCandidateCutScript_FailsWhenTheRemoteRefusesTheCut` (rc tag and candidate). |

Every new test was run against the unfixed file and fails there.

### Noted, not changed

- **The back-merge's main push is still swallowed.** `git push origin main 2>/dev/null || echo "Main branch push handled."` lets the cut go on to promote and cut the candidate from a local main whose back-merge never reached the remote. I left it alone: the release-notes step right after it treats a refused main push as expected ("it rides on the new candidate and returns to main with the next back-merge"). Making it fatal could stop every cut on a deployment whose `main` is protected. Whether it should be fatal is a decision for whoever owns the release process.
- **`release-images.sh` changes the host it runs on, on every phase, hand runs included.**
  - `ensure_transparent_cache` rewrites `/etc/resolv.conf` and installs a CA.
  - `ensure_network_mtu` clamps the interface MTU and adds an iptables rule.
  - `ensure_direct_registry_route` appends to `/etc/hosts`.

  Finding C fixed the part that broke this walkthrough. The rest belongs to a Grain CI sandbox, not to an operator's workstation. Follow-up proposed.
- **`grain demo` cannot show the upgrade picker.** It wires no Upgrader and has no `-upgrade-*` flags, so the plan's "restart with `-upgrade-allowed-branches='**'`" is not possible in demo. Follow-up proposed.

## Fixes on this branch

- `b4fb8ec75`: `fix(release)`, findings A, B and C (`scripts/release-images.sh`, `docs/image-signing.md`, `tests/deploy/release_images_test.go`).
- `b46d1b869`: `fix(upgrade)`, findings D and E (`pkg/upgrade/targets.go`, `targets_test.go`, `ui/src/components/UpgradePanel.jsx`, `UpgradePanel.test.jsx`).
- `32c862616`: `fix(release)`, finding F (`scripts/release-candidate-cut.sh`, `tests/deploy/release_workflows_test.go`).

`go test ./tests/deploy/ ./pkg/upgrade/ ./pkg/ui/` and `npx vitest run src/components/UpgradePanel.test.jsx` pass locally. `npm run format` and `gofmt` were run.

## CI verdict

Grain CI full suite (`wait_for_checks`, no `jobs`), run `ac86bd3861907c77` at `db3b9e04a`: **success**.
Every job passed: frontend (5m10s), go (16m21s), installer, lint, package cache, postgres, terraform, ui e2e (12m6s). No known flake recurred.
This commit only adds this paragraph, and it was checked again before merge.
