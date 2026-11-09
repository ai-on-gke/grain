# 2026-11-09 §6 walkthrough: Codex 504 fallback, `ghcr-prune` tag retention, Stage-1-only rc qualification

Task 3733 walks §6 of [`docs/test-plans/2026-11-09.md`](../test-plans/2026-11-09.md). The PRs under test are #982 (`8544117f`, the Codex CLI download fallback), #978 (`b50697a3`, `ghcr-prune` keeping release tags on `sandbox-base`) and #954 (`a53e4045`, `RELEASE_QUALIFICATION_APP=none`).

Harnesses, stubs and logs: [`2026-11-09-release-image-machinery/`](2026-11-09-release-image-machinery/).

## Environment

- Sandbox: Debian, Go 1.26.10, Docker Engine 29.9.3 with buildx v0.38.1 (`overlayfs`). `docker pull debian:bookworm-slim` worked, so item 1 is a **real `docker build`**, not the extracted-script fallback.
- Branch base: `origin/main` at `a59d1e4c`. `origin/release/v3.2.0` is at `cba7d44b`. Real tags fetched: `latest`→`8a740f99` (v3.1.0); `v3.2.0-rc.2` + `-discarded` on `6045a6a0`; `v3.2.0-rc.3` on `cba7d44b` (tagged 2026-11-09 01:35 UTC); `v3.2.0-pending` on `cba7d44b` (tagged 03:13 UTC).
- No real registry, GitHub API or npm was contacted.
  - Item 1's stub answers for `github.com`, `api.github.com`, `release-assets.githubusercontent.com` and `registry.npmjs.org` through `docker build --add-host … :127.0.0.1 --network host`. The build trusts a CA minted for the run.
  - Item 2's fake answers for `hub.docker.com` and `api.github.com` through a temporary `/etc/hosts`. The sandbox had no `/etc/hosts`, so the script creates one and removes it afterwards. The binary trusts the fake's CA through `SSL_CERT_FILE`.
  - Item 3 ran in scratch bare remotes under `/tmp`. No tag was pushed anywhere real, and nothing ran in `/home/debian/work`; the local git identity there is unchanged.
- `jq` is preinstalled here. Every tool I used was already in the image.

## 1. Codex fallback in a real `docker build` (PR #982)

[`run-codex-builds.sh`](2026-11-09-release-image-machinery/run-codex-builds.sh) cuts the Codex step out of `Dockerfile` and `Dockerfile.k8s-sandbox` verbatim: from `ARG INSTALL_CODEX_CLI` through the `RUN`'s closing `fi`. It puts that step on a `debian:bookworm-slim` + `curl` probe image that trusts [`codex-stub.go`](2026-11-09-release-image-machinery/codex-stub.go)'s CA. The only build args passed are the three amd64 digests, set to the stub's fake archives (the real pins name real bytes). Each successful image is then run with `--network none` to execute `codex --version` and `codex-code-mode-host --help`, and to check that `/tmp/codex-release.json` is gone.

The full `Dockerfile` (Go build stage, UI build) was not built. The Codex `RUN` has no dependency on those stages beyond `curl`, `sha256sum` and `tar`, all of which the probe image has.

Result ([`logs/codex-builds/summary.txt`](2026-11-09-release-image-machinery/logs/codex-builds/summary.txt)). The two Dockerfiles behaved identically:

| Stub mode | What the stub does | Build | API tag lookups | npm | Final layer |
|---|---|---|---|---|---|
| `healthy` | `/releases/download/` serves both archives | ✅ | **0** | 0 | release binaries, no `codex-release.json` |
| `504-download` | download 504s; API JSON + `/releases/assets/<id>` (302 → release-assets) | ✅ | 1 (shared by both assets) | 0 | release binaries, no `codex-release.json` |
| `504-all` | download and API 504 | ✅ | 4 (1 + `--retry 3`) | 1 | npm binaries, no `codex-release.json` |
| `tampered` | download serves other bytes | ❌ `sha256sum: … did NOT match` | 0 | 0 | — |
| `tampered-api` | download 504s, asset endpoint serves other bytes | ❌ digest mismatch | 1 | 0 | — |
| `tampered-npm` | GitHub 504s, npm serves other bytes | ❌ digest mismatch | 4 | 1 | — |
| `html200` | download answers `200 text/html` error page | ❌ digest mismatch | 0 | 0 | — |
| `html200-api` | download 504s, API answers `200 text/html` | ✅ via npm ("has no asset URL") | 1 | 1 | npm binaries, no `codex-release.json` |
| `html200-all` | every host answers 200 HTML | ❌ digest mismatch | 0 | 0 | — |

The request logs (`logs/codex-builds/{root,k8s-sandbox}-dockerfile-<mode>.requests.log`; build logs beside them as `.build.log`) show:
- No request carried an `Authorization` header.
- The healthy path made **no** `api.github.com` call, so it spends none of the 60/hour unauthenticated quota.
- The API path sent `Accept: application/vnd.github+json` for the release lookup and `Accept: application/octet-stream` for the asset. It followed the 302 to `release-assets.githubusercontent.com`.
- The stub's API JSON includes a decoy `.zst` asset, the release's own `url`, and uploader `url`s at other depths. The awk parser chose `assets/101` and `assets/102`.

**Finding (design note, not a defect):** a `200` that is not the archive (`html200`, `tampered`) on `/releases/download/` fails the build at the digest check. It does not fall back to the API or npm, because only a failed `curl -f` triggers the fallback. That fails closed, which is the safe direction, and matches the Dockerfile comment ("checked against their digests before anything in them is unpacked"). The cost is that a captive proxy answering 200 breaks the build where a 504 would not. I left it alone; changing it would mean editing `Dockerfile*` under the human-required trigger.

## 2. `ghcr-prune` against a registry fake (PR #978)

[`registry-fake.go`](2026-11-09-release-image-machinery/registry-fake.go) serves Docker Hub's v2 tags API, GitHub's branch list and GHCR's package-versions API. The tag mix is the one `scripts/release-images.sh` publishes today:
- `latest`+`v3.1.0`+`sha-8a740f9` and its cosign `.sig`
- `main`'s head with a `.sig`
- `release..v3.2.0`'s head
- the candidate rc (`v3.2.0-rc.2`,`candidate`,`rc`) and a discarded rc (`v3.2.0-rc.1`,`-discarded`)
- `v3.2.0-pending`
- a live task branch and a deleted one
- 35 superseded main builds, one of them signed
- an orphan `.sig`
- 8 bare-hex granule keys on `sandbox-base`/`guest-base`
- on GHCR, untagged versions

[`run-ghcr-prune.sh`](2026-11-09-release-image-machinery/run-ghcr-prune.sh) runs the real `go build ./cmd/ghcr-prune` binary in three passes: dry run, `-apply`, dry run again. It does this for Docker Hub (`grain,sandbox,sandbox-base,sandbox-k8s`) and for legacy GHCR (`grain,guest-base`).

Results with `main`'s code ([`logs/ghcr-prune-before-fix/`](2026-11-09-release-image-machinery/logs/ghcr-prune-before-fix/)):

- **PR #978 holds.**
  - `sandbox-base` and `guest-base` keep `latest`,`v3.1.0`,`sha-8a740f9`, their signatures, `main`, `release..v3.2.0` and `candidate`, exactly as `grain`, `sandbox` and `sandbox-k8s` do.
  - The `sha-` tags on base packages join the `-keep-commits 30` ranking: the same 30 are kept on every package.
  - Only the 5 newest bare-hex keys survive past the window.
- **Deleted:** stale branch builds (`grain..098073e4..task-3600-old`), `sha-` builds beyond the 30 newest, old cache keys, the discarded rc, the signatures of deleted builds and the orphan signature.
- **Untagged versions are kept**, not deleted (`untagged: left alone (it may be part of a tagged image)`). The test plan expected untagged versions to be planned for deletion. That expectation is wrong: `cmd/ghcr-prune/main.go` deliberately never deletes untagged versions, because they may be index children.
- **Delete mode matches the plan exactly.** On Docker Hub, the 56 planned versions became 64 per-tag `DELETE /v2/repositories/grainai/<pkg>/tags/<tag>/` calls. `diff` of the planned tags against `deletes.log` is empty. On GHCR, 30 planned versions became 30 `DELETE …/versions/<id>`. The second dry run plans 0 deletions on both.

**Defect found and fixed — a staged release is pruned 48 h after staging, even if nobody has approved or rejected it yet.**
- **Cause:** `vX.Y.Z-pending` matched none of `classify`'s patterns, so it was `KindBranch`. No branch has that name, so the age window was all that kept it. The `before-fix` dry run shows `delete … v3.2.0-pending 2026-11-05 -- no live tag: v3.2.0-pending (4d)` on all four Docker Hub packages.
- **Effect:** `scripts/release-images.sh pending` stages a stamped build so that `promote` "only retags it, so approving builds and tests nothing". If the approval comes more than 48 h after staging (a weekend is enough), `publish_release` no longer finds `:vX.Y.0-pending`. It then rebuilds the daemon image from `sha-<short>`, so the published release is a different build from the one that was staged.
- **Exposure today:** the real `v3.2.0-pending` was staged at 2026-11-09 03:13 UTC, so it would become deletable from 2026-11-11 03:13 UTC.
- **Fix** (`cmd/ghcr-prune/plan.go`): a new `KindPending` that stays live until the same version also carries `vX.Y.Z-rejected`. That is the tag `discard-pending` adds, and the comment there says `-pending` tags are "left for cmd/ghcr-prune". Approving retags the same version `vX.Y.Z`, which `KindRelease` keeps from then on.
- **Regression test:** `TestAStagedReleaseOutlivesTheWindowUntilItIsRejected`.
- **After the fix** ([`logs/ghcr-prune-after-fix/`](2026-11-09-release-image-machinery/logs/ghcr-prune-after-fix/)): `keep … v3.2.0-pending -- "v3.2.0-pending" is a staged release nobody has approved or rejected yet`. Docker Hub plans 52 deletions instead of 56, GHCR 28 instead of 30, and the DELETEs again match the plan.
- **Backport:** none needed. `cmd/ghcr-prune` runs from `main` (`prune-packages.yml`).

Observations, not changed:
- Older rc tags (`vX.Y.0-rc.N`, `-discarded`) and `:rc` are still held by the `candidate` branch tag (while they share its version) and the window. That is consistent with `discard-rc`'s design.

## 3. Train ticks with `RELEASE_QUALIFICATION_APP=none` (PR #954)

[`run-train-tick.sh`](2026-11-09-release-image-machinery/run-train-tick.sh) copies the real `main`, `release/v3.2.0`, `candidate` and every tag into a scratch bare remote. It then runs `scripts/release-from-trunk.sh` with `DRY_RUN=true` from a scratch clone, set up the way `rc-qualification.yml` runs it:
- `RELEASE_QUALIFICATION_APP` is set but empty, which is what `${{ vars.RELEASE_QUALIFICATION_APP }}` gives for an unset variable.
- An rc-hold listing is supplied, so the train takes the path where it holds a token.
- The CI and Stage 1 answers are local JSON: green `tests`/`release` on `release/v3.2.0`'s tip, and all 14 `nightly.yml` jobs green on `cba7d44b`.

Results ([`logs/train-main/`](2026-11-09-release-image-machinery/logs/train-main/)):

| Tick | State | Exit | What it says |
|---|---|---|---|
| `cut-none` | rewound to before rc.3; `release.conf` as `main` has it | 0 | `rc.2 … was dropped` → `rc.3 of v3.2.0 converges on release/v3.2.0` → `RELEASE_QUALIFICATION_APP=none, so no grain/rc-hold check run … is trusted` → `cutting v3.2.0-rc.3 on cba7d44bb42b` → `[dry-run] would git push origin refs/tags/v3.2.0-rc.3` |
| `cut-unset` | same, with the line removed from `main`'s `release.conf` | 1 | `RELEASE_QUALIFICATION_APP is unset, so no grain/rc-hold check run can be trusted …` |
| `qualify-none` | rc.3 cut, Stage 1 green | 0 | `SKIPPED grain/release-qualification: CHECK_QUALIFICATION_APP=none (qualified by Stage 1 nightly.yml)`, `Qualification: PASS v3.2.0-rc.3`, then stages `v3.2.0-pending` for approval (breaking changes since v3.1.1) |
| `qualify-unset` | same, line removed | 1 | `candidate-gate.sh: CHECK_QUALIFICATION_APP is unset, so no grain/release-qualification check run can be trusted …` |
| `now-none` | the remote exactly as fetched | 0 | `nothing in qualification; the next rc must be after the pending v3.2.0` → `under RELEASE_MIN_INTERVAL_MINUTES (120)` |

What this shows:
- With `none`, the train gets past phase 2 and plans rc.3 on `release/v3.2.0`'s tip.
- The qualification tick says plainly that Stage 2 is skipped by policy.
- Removing the setting restores both refusals.
- The real remote matches what the dry run plans. After #954 merged (2026-11-08 22:04 UTC), the real train cut `v3.2.0-rc.3` on `cba7d44b` at 01:35 UTC and staged `v3.2.0-pending` at 03:13 UTC.

**Defect found and fixed — the cut's summary still promised Stage 2.** With `none`, phase 2 ended with `v3.2.0-rc.3 is cut and candidate is on it; nightly.yml qualifies it (Stage 1), then grain/release-qualification (Stage 2)`. That is the opposite of what the next tick does. `scripts/release-from-trunk.sh` now says `nightly.yml qualifies it (Stage 1) alone: RELEASE_QUALIFICATION_APP=none skips grain/release-qualification (Stage 2)` when the app is `none`, and keeps the old line otherwise. `scripts/release-from-trunk_test.sh` pins both spellings in its `none`/`conf-none` loop, and the suite passes 475/475. [`logs/train-branch/`](2026-11-09-release-image-machinery/logs/train-branch/) is the same five ticks run with the branch's script (`TRAIN_SCRIPT=`). No backport is needed, because the hourly train and `rc-qualification.yml` both run `main`'s script.

**`rc-qualification.yml` on `main`:**
- I could not read its runs: this task holds neither `grain#ci` nor `api:ci-read`, and the MCP `base_branch_status` reports no repository.
- The workflow passes `RELEASE_QUALIFICATION_APP: ${{ vars.RELEASE_QUALIFICATION_APP }}`. For an unset variable that is empty, and `qualification_app="${RELEASE_QUALIFICATION_APP:-$(conf_value …)}"` then falls back to `main`'s `release.conf`, which says `none`. The `cut-none` and `now-none` ticks reproduce that path with exit 0. The `RELEASE_QUALIFICATION_APP is unset` refusal noted on 11-07 (`#3368`) should therefore have stopped with #954.
- That is inferred, not observed. The deployment memory note that it "is currently failing hourly" is very likely out of date.
- `release/v3.2.0`'s own `release.conf` has no `RELEASE_QUALIFICATION_APP` line. That is harmless, because the train reads `release.conf` "as main has it" (`git show $main_ref:scripts/release.conf`).

## 4. Docs

- `docs/release-candidate-management.md` (Stage 2 bullet) and `docs/github-actions-ci.md` (variables table) describe `none` accurately.
- The `Dockerfile`/`Dockerfile.k8s-sandbox` comments describe the 504 → Releases API → npm chain and its digest checks exactly as the builds above behaved. No other doc describes the Codex download chain; `docs/codex-surface.md` covers only the pinned version. `docs/releases/` has no v3.2.0 notes yet.
- **Fixed:** `design/playbook/releases.playbook.md` §2 ("What 'pass' means") still said every Stage 2 check must succeed "with zero required checks skipped". It now carries an amendment for `RELEASE_QUALIFICATION_APP=none`: it is an explicit opt-in, it logs `SKIPPED grain/release-qualification`, Stage 1 alone qualifies the rc, it is never `none` by default, and the train refuses when the variable is empty.

## Outcomes

| # | Item | Outcome |
|---|---|---|
| 1 | Codex fallback, real `docker build` of both Dockerfiles | Pass in all 9 modes × 2 Dockerfiles; healthy path makes no API call; `codex-release.json` never reaches the final layer. One design note (200 non-archive fails closed without fallback). |
| 2 | `ghcr-prune` dry run + delete against a fake | #978 holds; DELETEs match the plan. **Fixed:** `vX.Y.Z-pending` pruned after 48 h (`cmd/ghcr-prune`, new test). |
| 3 | Train tick with `none` / unset | Pass. **Fixed:** cut summary promised Stage 2 under `none` (`scripts/release-from-trunk.sh` + shell test). `rc-qualification.yml` most likely green now (inferred; CI unreadable from this task). |
| 4 | Docs | **Fixed:** releases playbook §2 amended for `none`. |

The edits to `scripts/release-from-trunk.sh` and `scripts/release-from-trunk_test.sh` touch `scripts/release*`, and the playbook says its own edits stay human-reviewed. These wait on the human-required release/trust-boundary triggers, so this PR is not expected to auto-merge unattended. Nothing in this PR touches `Dockerfile*` or `.github/workflows/*`.
