# Release audit walkthrough — 2026-11-18 (test plan §6)

Walkthrough of `docs/test-plans/2026-11-18.md` §6: `.grain/templates/release-audit.yml`
(PR #1536) run by hand, as its audit agent would, against the real `v3.13.0` and `v3.12.0`
releases, then the release manager's filing of it against a scratch train.

## Environment

- **Commit walked:** `848f5271c` (`origin/main` at 2026-11-18 05:30 UTC); fixes in `f4d670f16`.
- **Sandbox:** grain task 7057, open network (no `request_access` needed): `api.github.com`,
  `registry-1.docker.io` and `hub.docker.com` all answered 200.
- **Tools:** `docker` 29.10.0 with `buildx` v0.39.0 (`imagetools inspect`, no daemon or login
  needed), `jq`, `git`, `faketime` (installed with apt for item 4), `go test`.
- **Grants this run held:** `api:ci-read` on `ai-on-gke/grain` (so `grain ci list/show/log` read
  GitHub Actions), but **no `tasks:read`/`self-debug`**: `grain task list` returned only this
  task, and `grain api GET /api/notifications` answered
  `task 7057 holds no api grant for scope tasks:read`. The template's §0.2, §4 and §6, which read
  other tasks and notifications, stayed **unverified** here (see item 3 for why the template's
  own grants do cover them).
- **No ref, tag, image or GitHub Release was moved, created or deleted.**
  `scripts/release-from-trunk.sh` ran only in a `mktemp -d` scratch clone against a local bare
  remote. `git config --local user.name` in `~/work` was unset before and after.

Helpers and raw output are in [`2026-11-18-release-audit/`](2026-11-18-release-audit/):
`imgcheck.sh` (every image's digests and labels), `imgcheck-v3.13.0.log`, `imgcheck-v3.12.0.log`,
`images-v3.13.0.json`, `images-v3.12.0.json` (the GitHub Release assets), and
`train-then-audit.sh` / `.log` (item 4).

## Item 1 — the audit of `v3.13.0`, check by check

`v3.13.0` = `46fdc1104ba8c6febd3abf9820dfb0055240421c` (`<short>` `46fdc11`), a **minor**.

| § | Check | Verdict | Evidence | Instructions executable as written? |
|---|---|---|---|---|
| 0.1 | version shape | pass | `v3.13.0` | yes |
| 0.2 | no duplicate audit | unverified | `grain task list --state proposed,queued,running,awaiting_reply,deferred --search 'Release audit for v3.13.0'` ran (exit 0) but this run sees only its own task | yes: the command exists and every state it names is real (`model.StateDeferred`); it needs the template's `self-debug` |
| 0.3 | fetch, patch/minor | pass | `git fetch origin --tags --force` | yes |
| 0.4 | image tooling | — | `docker buildx imagetools inspect` works unauthenticated | **no**: "labels are in the image config it prints with `--raw`" is wrong. `--raw` prints the OCI index (0 matches for `revision`). The labels come from `--format '{{json .Image}}'` → `.["linux/amd64"].config.Labels`. **Fixed** (F1). |
| 1 | tags → commits | pass | `for-each-ref`: `v3.13.0-rc.1` (09:25:19Z) and `v3.13.0` (11:28:56Z), both annotated on `46fdc1104` | yes |
| 1 | promoted rc | pass | tag message `Promoted-From: v3.13.0-rc.1`, `Qualification: PASS`. There is no `-discarded` twin, and it is the only rc. | yes |
| 1 | discarded rcs | pass (none) | no `v3.13.0-rc.*-discarded` | yes |
| 1 | release branch | pass | no `refs/heads/release/v3.13.0`, which is correct for a minor. Patch branches are `release/v3.5.1` and `release/v3.1.1`. | yes |
| 1 | no stray versions | pass | no `v3.13.0-pending` or `-retracted` | yes |
| 2 | which images | pass | `git show v3.13.0:scripts/release-images.sh` (`on_demand_images` / `publish_release`): `sandbox`, `sandbox-k8s`, `grain`, `hub`, `registry` | partly: the "`promote` path's `images=(...)`" is `on_demand_images`' `daemons` list. Readable, left as is. |
| 2 | each image at `v3.13.0` | pass | `grain` `sha256:dbd04963…` (rev `46fdc1104…`, ver `v3.13.0`). `hub` = `registry` `sha256:79307b40…` (rev `46fdc1104…`, ver `v3.13.0`). `sandbox` `sha256:70e8e047…` = `:sha-46fdc11`. `sandbox-k8s` `sha256:bb928be4…` = `:sha-46fdc11`. | yes, with F1's command |
| 2 | `latest` | pass | git `latest` → `46fdc1104`; `:latest` = `:v3.13.0` on all five images | yes, for `v3.13.0` (see F3 for `v3.12.0`) |
| 2 | publish run | pass | `grain ci list --repo ai-on-gke/grain --provider github-actions --workflow publish-release.yml --ref v3.13.0` → `gha:ai-on-gke:grain:44074400661` success, 11:29Z | yes (needs `ci-read`, which the template holds) |
| 2 | GitHub Release | pass | not a draft, body of 972 chars, published 11:36:44Z, assets include `images.json`, `checksums.txt` and the four `grain-{linux,darwin}-{amd64,arm64}.tar.gz`. `docs/releases/v3.13.0.md` is on `main`. | yes (`curl -fsSL …/releases/tags/v3.13.0`, then `browser_download_url`) |
| 2 | `images.json` = digests above | **fail** (`k8s-sandbox`) | `commit` `46fdc1104…` and `version` `v3.13.0` are correct. `guest.imageId` `sha256:70e8e047…` = `sandbox:v3.13.0`. But `k8s-sandbox.imageId` `sha256:7becfcf7…` ≠ `sandbox-k8s:v3.13.0` `sha256:bb928be4…`: it is `sandbox-k8s:main`'s digest, pushed 09:03Z, while `:sha-46fdc11` was re-pushed 09:30Z. | **no** as written: `images.json` holds tarball `sha256`s and `imageId`s, not registry digests, and `grain`'s is the unstamped build, so it can never equal `:v3.13.0`. **Fixed** (F2): compare `guest`/`k8s-sandbox` `imageId` with the index digest, and check `grain`'s `ref`. With that wording the `k8s-sandbox` mismatch is a real finding (R4). |
| 3 | `:sha-46fdc11`, `sha-46fdc1104ba8` | pass | the daemon `:sha-46fdc11` tags were moved onto the stamped builds (by design, `publish_release`). GitHub Release `sha-46fdc1104ba8` `images.json` has commit `46fdc1104ba8`. | yes |
| 3 | `channel-release-v3.13.0` | pass (absent) | 404, as expected for a minor | yes |
| 3 | rc releases / markers | pass | GitHub Release `v3.13.0-rc.1` has commit `46fdc1104ba8` and version `v3.13.0-rc.1`. `channel-candidate` names the same. | yes |
| 3 | `:candidate`/`:rc` | **fail** | `grain:candidate` is rev `cba7d44b…`, ver `v3.2.0-rc.2.14+cba7d44`. `registry:candidate` is `v3.2.0-rc.3`. `hub:candidate` and every `:rc` are missing. There are no `:vX.Y.0-rc.N` images at all (`v3.13.0-rc.1` and `v3.12.0-rc.3` are missing). | yes (R2) |
| 3 | `:main`, `channel-main` | **fail** (stale) | `grain:main` = rev `46fdc1104…` ver `v3.13.0-dev.68+46fdc11`, pushed 2026-11-17 09:03Z. `channel-main` is the same, while `main` is 271 commits ahead. Not *older* than `<sha>`, so the letter of the check passes, but `docs/release-candidate-management.md` §8 promises `main`'s head. | yes (R1) |
| 3 | older version untouched | pass | `v3.12.0`'s daemon images still carry rev `e06c555…`, ver `v3.12.0`. `sandbox`/`sandbox-k8s:v3.12.0` = `:sha-e06c555`. | yes |
| 4 | leftover tasks, managers, notifications | unverified | no `tasks:read` here | yes: `grain task list --state …` and `grain api GET /api/notifications` (`tasks:read`, `pkg/ui/route_scopes.go`) |
| 5 | upgrade path | pass | the 232 `grainai/grain` tags filtered as `releasedVersionsMatching` does give newest `v3.13.0`, with no `-retracted`. `:latest` = `:v3.13.0`. `go test ./pkg/upgrade -run 'Latest\|Released'` ok. | yes (`LatestRelease` is a method of `Upgrader` in `targets.go`) |
| 6 | downstream edition | unverified | needs `tasks:read` | yes |
| 7 | timeline | see below | | yes |

### `v3.13.0` timeline (from tags, Docker Hub `last_updated` and Actions runs)

- 09:03Z: `release.yml` on `46fdc11` (`main`) publishes `:sha-46fdc11`/`:main` for every image, and GitHub Release `sha-46fdc1104ba8` (09:04Z).
- 09:25Z: the train cuts `v3.13.0-rc.1`. `release-train-assets.yml` (`gha:…:44055430310`) publishes GitHub Release `v3.13.0-rc.1` (09:25:38Z). **No rc image is published** (R2).
- 09:25Z: the train's force-move of `candidate` onto `46fdc11` triggers `release.yml` on `candidate` (`gha:…:44055436973`). At 09:30Z that run re-pushes `sandbox-k8s:sha-46fdc11` with a different digest (`bb928be4…`) from the 09:03Z `main` build's (`7becfcf7…`, still `:main`). `sandbox` builds reproducibly and kept its digest; `sandbox-k8s` does not. The release tarball holds the 09:03Z build (R4).
- 11:28:56Z: tag `v3.13.0` (promoted from rc.1 by the train, no person). `publish-release.yml` (`gha:…:44074400661`) publishes `grain:v3.13.0`/`latest` at 11:36Z and the GitHub Release at 11:36:44Z. It logs `v3.13.0's tree publishes no registry image, so none was published for it`: **`hub`/`registry` are skipped**.
- 20:19Z: a `promote.yml` tick on `main` (`gha:…:44167283275`, `80995c6`) publishes `hub:v3.13.0`/`latest` and `registry:v3.13.0`, **8h43m after the release** (R3). Until then `hub:latest` was `v3.12.0`'s.
- From 2026-11-17 09:08Z: every `release.yml` on `main` fails at "Publish the images" with `reviewed-merge: refused`. The first is `gha:…:44052762982` on `ad8ebd4` ("pull request #1747 was merged by sidthe, with no approval from an owner, member or collaborator other than its author"); later runs name `ce6520f717ac` (PR #1758), e.g. `gha:…:44236868946` on `2c77c98`. `sandbox`/`sandbox-k8s:sha-*` keep appearing for every merge, but `grain`/`hub` `:sha-*` and `:main` stop at `46fdc11` (R1).
- The publish logs warn that `grain:v3.13.0` and `hub:v3.13.0` are **published unsigned** (no `IMAGE_SIGNING_KEY`). The template does not check signatures. Noted only.

### Verdict the template would reach for `v3.13.0` (with F1–F3 applied)

Fails: R1 (`:main` stale), R2 (`:candidate`/`:rc`), R4 (`k8s-sandbox` tarball ≠ registry). Timeline finding: R3 (`hub` late). Unverified: §0.2, §4, §6 in this run only.

## Item 2 — a release that is not clean: `v3.12.0`

`v3.12.0` = `e06c555103b8…`, promoted from `v3.12.0-rc.3` after two discarded rcs.

- **§1:** pass. `v3.12.0-rc.1-discarded`: "systemic: 3 jobs failed … (cmd-grain-stress go-long ui-e2e-long)". `v3.12.0-rc.2-discarded`: "grant-matrix-ui: failed again on its retry (failure,failure): confirmed red". The two reasons differ, so no repeated blocker.
- **§2 images:** pass. All five images at `v3.12.0` = `:sha-e06c555`, and the daemon labels read rev `e06c555…`, ver `v3.12.0`. `hub:v3.12.0` was published at 09:10Z, 1.5h after `grain:v3.12.0` (07:40Z): the same R3 lag.
- **§2 `latest`, as the template was written:** **false fail**. For a minor it required git `latest` = `<sha>` and `:latest` = `:v3.12.0`. `v3.13.0` was released 3h54m later, so `latest` correctly names `v3.13.0`. An audit at priority 7 can easily start after the next minor. The newer-release rule existed only for patches. **Fixed** (F3): `latest` must name the newest release, and a newer release passes.
- **§2 `images.json`:** `k8s-sandbox.imageId` `sha256:2a80d101…` ≠ `sandbox-k8s:v3.12.0` `sha256:5689227d…`, and `guest` matches. That is the same R4 on a second release, so the cause is systematic, not this release's.
- **§3:** `:candidate`/`:rc` and `:main` fail as for `v3.13.0` (R2, R1).

**One fix task per root cause?** The symptoms across both audits reduce to four causes: R1 (reviewed-merge refusal stops daemon publishing on `main`), R2 (nothing publishes rc images), R3 (`publish-release.yml` decides "no hub" from the tag's tree), and R4 (`k8s-sandbox` packaged and pushed from different builds). §8's "root cause, not symptom" wording and its search-before-filing step map them correctly:
- the `:main` and `channel-main` symptoms are one R1;
- `:candidate`, `:rc` and the missing `:vX.Y.0-rc.N` are one R2;
- R3 and R4 recur on every release, and §8's `grain task list --search` would find the previous audit's task instead of filing a duplicate.

The one place it would have filed a bogus task was the old `latest` rule (F3).

## Item 3 — grants are enough, and no more

- **Every call the body makes, and the scope it needs:**
  - `grain ci list …` and `grain ci show/log`: `ci:read`, held through `api#ci-read`. Verified in this run.
  - `grain task list`: `tasks:read`.
  - `grain api GET /api/notifications`: `tasks:read` (`pkg/ui/route_scopes.go`).

  `tasks:read` comes with `api#self-debug`, and `TestTheReleaseAuditOnlyReads` asserts that `tasks:read`, `ci:read`, `releases:read` and `repos:read` are held. Without `tasks:read` this run got exactly the refusal shown above, so the template's own `self-debug` grant is the one doing the work.

  `git`, `curl` to `api.github.com` and `docker buildx` need no grain grant. The network itself has to be open, though: this run's was. The template declares no network setting and relies on the deployment's default. That is not checkable from the core repository: it lives in `grain/config`.
- **Pushes and ref moves are refused:**
  - The template declares no `push:` (`repodecl.TemplatePush` is empty, as `TestTheReleaseAuditOnlyReads` asserts).
  - The git proxy allows a task only its own `grain/…` branch: `go test ./pkg/gitproxy -run 'TestAuthorizeRefUpdates|TestHandleForwardsAPushOnlyOfTheRunsOwnBranch'` passes, refusing `refs/heads/main`, another task's branch and `refs/tags/…`.
  - `declaredUpdate` refuses moving an existing tag without `force: true`.
  - I did not try a real tag push from this run, because the plan forbids moving a ref.
  - The audit can still push its *own* task branch, as any task can. "Opens no pull request" is a rule in its wording, not a structural block.

## Item 4 — the release manager files the audit, and the audit waits for its tag

- **The filing:** `release-manager.yml` §5 files exactly one `propose_task` with `template_id: repo:template:ai-on-gke:grain:release-audit` and `template_fields: {version: vX.Y.Z}`, unless an open `Release audit for vX.Y.Z` already exists, and does so before `nothing_needed`. `TestTheReleaseManagerFilesTheAuditWhenItCloses` pins this. The version is the one the manager pinned (`Release manager pin:`), never the field's default `the newest rc`. §0.1 of the audit catches a malformed version.
- **Before the tags exist — not guarded:** the proposal carries **no `depends_on`**. The only guard was the manager's wording ("When core vX.Y.Z is tagged and published"). An audit filed early, by hand or by a manager closing on a `-pending` release, would have run every check against a missing tag and filed fix tasks for a release that simply was not out yet. **Fixed** (F4): §0.4 now checks `git rev-parse -q --verify 'refs/tags/{{version}}^{commit}'`. With an rc or `-pending` but no tag, the audit ends in `resume_later` (`at: 2h`) without checking anything. With neither, it asks. `TestTheReleaseAuditWaitsForItsTag` pins this.
- **Scratch train:** `train-then-audit.sh` runs the real `scripts/release-train-task.sh` → `release-from-trunk.sh` in a scratch clone against a bare remote, with a fake `grain` for CI verdicts (from the `2026-11-10-release-train` harness, with today's Stage 1 job list and the publishing-hold probe). It applies F4's guard after each tick (`train-then-audit.log`):
  - before the cut: tags `v2.1.0`, so "no such release: ask_question";
  - after tick 1: `v2.2.0-rc.1` cut, so "not tagged yet but has an rc or -pending: resume_later, no check run";
  - after tick 2: rc.1 qualified, `v2.2.0` tagged `Promoted-From: v2.2.0-rc.1`, `latest` moved onto it, so "tagged (…), the audit runs";
  - tick 3: noop.
- **Not covered:** a live LLM release-manager run in a scratch daemon. I did not dispatch one: the manager is an agent, and filing the audit is one sentence in its §5, which the deploy test pins.
- **Side observation:** with `RELEASE_QUALIFICATION_APP=none`, the promoted tag's message still says "passed Stage 1 (nightly.yml) and Stage 2 (grain/release-qualification)", although Stage 2 was skipped. Real releases have an app, so I only note it.

## Template fixes (`f4d670f16`)

| # | Flaw | Reproduction | Fix |
|---|---|---|---|
| F1 | `--raw` named as the source of labels | `docker buildx imagetools inspect --raw docker.io/grainai/grain:v3.13.0 \| grep -c revision` → `0`; `--format '{{json .Image}}'` → labels | §0.5 gives the `--format` command and the jq path. `TestTheReleaseAuditReadsLabelsFromTheImageConfig` also checks that filling `{{version}}` leaves `{{json .Image}}` intact. |
| F2 | "`images.json`'s images are the digests above" cannot hold | `images-v3.13.0.json`: `grain.imageId` `9d418a90…` vs `grain:v3.13.0` `dbd04963…` (re-stamped by design) | Compare `guest`/`k8s-sandbox` `imageId` with the `:vX.Y.Z` index digest; check `grain`'s `ref` and `commit`. Also adds the `git cat-file -e origin/main:docs/releases/…` command. |
| F3 | minor `latest` rule fails a release already superseded | `v3.12.0`: git `latest` → `46fdc1104` (`v3.13.0`) | `latest` must name the newest `vX.Y.Z`; a newer release passes |
| F4 | nothing stops an audit before its tag exists | `git rev-parse -q --verify 'refs/tags/v9.9.0^{commit}'` → exit 1, then every §1 command prints nothing | §0.4 adds `resume_later`/`ask_question`. `TestTheReleaseAuditWaitsForItsTag`. |

Both new tests fail on the template at `848f5271c` (5 errors) and pass on `f4d670f16`. All of
`go test ./tests/deploy ./tests/hygiene/...` passes.

## Real release defects (follow-ups proposed)

- **R1:** `grain`/`hub` `:sha-*` and `:main` (and `channel-main`) have not been published since 2026-11-17 09:03Z. `release.yml`'s "Publish the images" refuses every `main` build because `ad8ebd4` (PR #1747) and `ce6520f717ac` (PR #1758) reached `main` without a reviewed merge, while `sandbox`/`sandbox-k8s` publish anyway. Deployments on `main` are stuck 271+ commits back, and the next promotion has no `grain:sha-<short>` to re-stamp (on-demand build only). Whether to accept #1758 after the fact is a person's call.
- **R2:** `:candidate`/`:rc` image tags still name a `v3.2.0-rc` build and no `:vX.Y.0-rc.N` image exists. The train task runs `RELEASE_IMAGES=off` (`scripts/release-train-task.sh`), and `release-train-assets.yml` publishes GitHub Releases only. `docs/release-candidate-management.md` §6/§8 and `pkg/upgrade`'s `candidate` channel still promise moving images.
- **R3:** `publish-release.yml` publishes no `hub`/`registry` image for `v3.13.0` ("v3.13.0's tree publishes no registry image"), so `hub:latest` lags the release until a `promote.yml` tick on `main` publishes it: 8h43m for `v3.13.0`, 1.5h for `v3.12.0`.
- **R4:** the `k8s-sandbox` tarball in each GitHub Release (`images.json` `imageId`) is a different build from `sandbox-k8s:vX.Y.Z` on Docker Hub (`v3.13.0`: `7becfcf7…` vs `bb928be4…`; `v3.12.0`: `2a80d101…` vs `5689227d…`). For `v3.13.0` the cause is visible: `release.yml` on the `candidate` push rebuilt the same commit and overwrote `:sha-46fdc11`, which promotion then retagged, and `sandbox-k8s` does not build reproducibly.

This run holds no `tasks:read`, so it could not search for open tasks that already own these. Each proposal says to check first.
