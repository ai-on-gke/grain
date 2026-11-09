# Test Run: Release approval, rollback and upgrades (2026-11-04 §2)

This run walks `docs/test-plans/2026-11-04.md` §2, "Release approval, rollback and upgrades", for task 3068. It covers "What is NOT proved" items 1–6, following the section's procedures. Commands, outputs and screenshots are in [`2026-11-04-release-approval/`](2026-11-04-release-approval/). The whole sheet is [`contact-sheet.png`](2026-11-04-release-approval/contact-sheet.png).

## Summary

| Item | Result |
|---|---|
| 1. Approve end to end over githubsim | **Pass after fixes F1–F4.** `release.pending` is raised for `-pending` tags, minor and patch. A person's Inbox Approve dispatches `promote-release.yml` once, and the record verifies against the deployment's key. A second press is refused (CLI exit 2, API 400). An agent holding `api:release-manager` is refused (CLI exit 3, API 403). Tagging `vX.Y.Z` clears the notification at the watcher's next poll. Two things are filed: F1b (unset-CI-mode repos are not watched) and F5 (re-raise after a CLI approve). |
| 2. Record tampering | **Pass.** `scripts/verify-release-approval.sh` refuses all of these: an expired record, a record for another commit, verification against another key, a record signed by another key, an edited field, and a changed rollback reason or `rollback_to`. It was run on records the demo really signed. |
| 3. Rollback, person and agent, grain CI and Actions | **Pass.** The Actions repo gets a signed rollback record dispatched to the sim. `reason_sha256` equals SHA-256 of the dispatched `reason`. The grain CI repo gets a `promote-release.yml:rollback` run. An agent is refused on both, through the CLI (exit 3) and the API (403). |
| 4. Upgrade on each release, real daemon | **Pass.** Two releases published 11 minutes apart produced one drain, to the newer release. A failing health check raised `upgrade.failed` once (raise count 1) and was not retried. The next release upgraded and cleared it. |
| 5. Retracted release on a live Upgrade pane | **Pass.** "Upgrade to v3.4.1" started the upgrade to `v3.4.1`, for real, through the cut-over. "Move back to latest (v3.2.1)" sends `{"branch":"latest"}`; that request was captured and refused so the daemon was not downgraded. |
| 6. Settings UI and Inbox screenshots | **Pass.** All three modes were saved through the picker and survived a reload. Screenshots at 1440/390 in light and dark cover Settings → General → Updates (×3 modes), Settings → Upgrade (retracted state) and the Inbox with its patch-release row. |

## Findings and fixes

1. **F1: Approve/Rollback on a repository that never set a CI mode went down the grain CI path.** Fixed in `pkg/ui/release_approval_github.go` and the new `pkg/model/release_forge.go`.
   - **Repro** (before): on `grain demo -github-sim`, `acme/widgets` has no CI mode set, and `GET /api/repos/acme/widgets/ci-mode` answers `ciMode: github`. Seeding `v2.5.0-pending` and running `grain release approve -repo acme/widgets v2.5.0` exited 3: `grain CI is not enabled for acme/widgets: set its CI Mode to "Use Grain CI"…`.
   - **Root cause:** `publishesFromGitHub` compared `EffectiveRepoConfig(...).CIMode`, which stays `""` for an unset repository, with `"github"`. Everywhere else (`RepoConfig.EffectiveCIMode`, the merge queue, `ci_actions.go`) an unset mode means GitHub Actions.
   - **Fix:** `model.Store.ReleasesFromGitHubActions`. An explicit mode decides. Otherwise the repo is an Actions repo unless grain CI is in fact on for it: the config repo, legacy `IgnoreGitHubCI`, or declared `.grain/workflows`.
   - **Regression test:** `TestAnActionsRepoThatNeverSetACIModeIsApprovedThroughActions` (`pkg/orchestrator/releasepending_test.go`). It fails before the fix with "this deployment has no grain CI wired".
   - **F1b (filed, not fixed):** the release-pending *watcher* also requires `ci_mode: github` explicitly, and only reads repos that have a `repo_config` row. Repro: [`out/release-pending-final-build.txt`](2026-11-04-release-approval/out/release-pending-final-build.txt) and the steps in the proposal. Widening it changes which repositories are polled every 10 minutes, and that needs a decision. It is filed as "Release watch: raise release.pending for GitHub Actions repos that never set a CI mode". For this walkthrough, `acme/widgets` was set to `ci_mode: github` explicitly on the final build. The first, interim build also widened the watcher, and was reverted (see below).
2. **F2: `grain demo -github-sim` could not walk the Actions release flow.** Fixed in `pkg/github/githubsim/githubsim.go`, `pkg/demo/demo_githubsim.go` and `pkg/demo/demo_reconcile.go`.
   - **Repro:** the sim had no `POST …/actions/workflows/{w}/dispatches`. `demoPullRequests` implemented neither `github.ReleaseTagReader` nor `github.WorkflowDispatcher`, so `releaseForge()` found no forge. The demo's reconcile loop never ran `release-pending`, so a seeded `-pending` tag raised nothing.
   - **Fix:**
     - The sim records each dispatch and answers 204, or 422 with no `ref`. (After `main` gained its own workflow-dispatch support in `pkg/github/githubsim/actions.go`, the branch was rebased onto it: dispatches are `Sim.Dispatches`, and `Sim.WorkflowFiles` declares `promote-release.yml` so the demo can dispatch it before it has ever run.) A GET of the same path (loopback-only through the demo hook) lists the recorded dispatches.
     - `demoPullRequests` reads tags and dispatches through the sim, held to the sim's one repository.
     - The demo's cycle runs `release-pending` after `issue-imports`, as `orchestrator.Reconcilers` does.
   - **Regression tests:**
     - `TestSimRecordsAWorkflowDispatchAndListsIt` (`pkg/github/githubsim`).
     - `TestDemoGitHubSimApprovesAPendingReleaseThroughTheSim` (`pkg/demo`). It goes from tag to raise, Approve, the dispatch read back through the hook, the record's commit, and the second-press refusal.
     - Playwright `ui/e2e/release-approval-actions-sim.spec.js`. It approves a patch from the Inbox, checks one dispatch, verifies the ECDSA signature with Node `crypto.verify` against `/api/release-approval-key`, checks the second press gets 400, and rejects a minor release from the Inbox.
3. **F3: the demo had no directory for the release-approval key, so Approve and `grain release approval-key` answered 500.** Fixed in `pkg/ui/labels.go`, `pkg/ui/release_approval_github.go` and `pkg/demo/demo.go`.
   - **Repro:** `grain release approval-key` against the demo printed `internal server error (ref 830604ad3bbe)`, and the log said `release approval: this deployment has no data directory to keep its signing key in`.
   - **Fix:** `ui.Config.ReleaseApprovalDir`, which falls back to `DataDir`. The demo sets it to its throwaway store's directory. It does not set `DataDir` itself, because that also drives the install-track probe and the state-repo reads.
   - **Regression test:** the Go and Playwright tests above sign through it.
4. **F4: the person-only refusal read `"20" is a agent`.** Fixed in `pkg/ui/release_approval.go`.
   - **Fix:** `kindName` now supplies the article ("an agent", "an automation", "a service", "a caller nobody identified").
   - **Regression test:** `TestTheReleaseRefusalNamesTheCallersKindWithItsArticle` (`pkg/ui`).
   - Before and after: [`out/approve-interim-build.out`](2026-11-04-release-approval/out/approve-interim-build.out) and [`out/approve.out`](2026-11-04-release-approval/out/approve.out).
5. **F5 (filed): a release approved from the CLI or API before the watcher raised it is raised anyway at the next poll.** The "already answered" refusal is in-memory and lasts 15 minutes, so a later press dispatches again.
   - **Repro** (final build): `v2.6.0-pending` was tagged and `grain release approve -allow-breaking v2.6.0` dispatched at 05:17. At 05:26:44, `release.pending:v2.6.0` was raised and left open; see [`out/release-pending-final-build.txt`](2026-11-04-release-approval/out/release-pending-final-build.txt).
   - Filed as "Release watch re-raises release.pending for a release already approved from the CLI or API".

### Observations, not fixed

- **The watcher polls each repository at most every 10 minutes** (`releasePendingPollInterval`). "Clears within one reconcile" therefore means within one *poll*. Measured on the final build: raised within one 2s cycle of `acme/widgets` becoming `ci_mode: github`, and cleared 9m31s after `v3.0.0` was tagged. On the interim build: raised exactly 10 minutes after the tags were seeded, because the first poll ran at startup, and cleared 7m48s after tagging.
- **An Actions decision's JSON still carries an empty `"run": {...}`,** because `ReleaseDecision.Run` has no `omitempty`. The UI and CLI both ignore it.
- **A patch release's dispatch runs on `ref: main`, like a minor's.** The workflow reads the global `vX.Y.Z-pending` tag, so this is correct. It is noted because §1 says patch releases are cut from `release/vX.Y.Z`.

## Environment

- Sandbox: Debian, 7 CPUs, 30 GB, Go 1.26.10, Docker 29.9.3, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, sqlite3 and shellcheck installed per shared step 8.
- Built per shared step 1: `cd ui && npm ci && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain`, from this branch on top of `origin/main` `c6004bc9`. It was rebuilt after each fix.
- **Two builds were exercised.**
  - The *interim* build, `/home/debian/run1` on port 18080, had F2+F3. Its watcher also accepted unset-CI-mode repositories, a change later reverted, see F1b. It did not have F4.
  - The *final* build, `/home/debian/run2` on port 18082 and the daemon, is what is committed.
  - Item 1 and item 3 were rerun on the final build with `acme/widgets` set to `ci_mode: github` explicitly. The `*-interim-build.*` files are kept as the before-state.
- **Demo:** `start-demo.sh` runs `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:<port> -access-code-file …` (shared steps 2, 6). `acme/gadgets` was added and set to `ci_mode: grain` as the grain CI repository.
- **Real daemon:** `start-daemon.sh` (shared step 3) runs `grain daemon -data-dir … -ui-addr 127.0.0.1:18090 -sandbox-port-base=0 -disable-native-sandboxes -upgrade-image localhost:5000/grain -upgrade-image-source registry -upgrade-image-ref-file image.env -upgrade-restart-cmd restart.sh -upgrade-allowed-branches 'latest,v[0-9]*.[0-9]*.[0-9]*'` under a supervisor loop. `restart.sh` logs the new `GRAIN_IMAGE`, then stops the daemon, and the loop starts it again on the same data dir. That makes each cut-over a real process restart. No run host was needed: nothing here dispatches a run.
- **Registry:** `docker run -d -p 127.0.0.1:5000:5000 registry:2`.
  - Release images are `FROM debian:bookworm-slim` plus this build's `grain`, with a per-release label so each release has a distinct digest.
  - The broken `v3.3.0` wraps the entrypoint so that `schema-version` exits 1.
- **Stubbed boundaries:**
  1. The sandbox proxy, through which a run's CLI acts as its task. [`agent-proxy.py`](2026-11-04-release-approval/agent-proxy.py) adds `X-Grain-Acting-Task: 20` and `X-Grain-Channel: sandbox-cli` to the CLI's requests. Task 20 was filed with `api:release-manager`, which carries `releases:write`. `api:*` is not a grantable capability: the demo answers "unknown capability api:*".
  2. GitHub itself, replaced by githubsim (shared step 6). The sim records dispatches and runs no workflow, so the `vX.Y.Z` tags a promote run would push were pushed by hand.
  3. `-fake-ci`, which accepts the grain CI rollback run and executes nothing.
- Nothing touched the real `ai-on-gke/grain` tags or workflows. `scripts/release-from-trunk.sh` was not run.

## Item 1: Approve end to end over githubsim

Scripts: [`inbox-approve.mjs`](2026-11-04-release-approval/inbox-approve.mjs) drives the Inbox in Playwright. [`approve.sh`](2026-11-04-release-approval/approve.sh) covers the second press, the agent, a CLI approve, and checks every dispatch with openssl.

1. **Raise.** The tags were seeded with `git tag -a vX.Y.Z-pending` in the sim's bare repository: a minor (`v3.0.0`, `v2.5.0`) at `main`, and a patch (`v2.9.1`, `v2.4.1`) at `release/1.0`.
   - The notifications carry the tag message and say the notes file has not merged yet. On the interim build they were raised at 05:00:50. On the final build they were raised at 05:16:44, one cycle after `ci_mode: github` was set.
   - The Inbox showed both rows with Approve/Reject: [`inbox-before-approve-v2.4.1.png`](2026-11-04-release-approval/screenshots/inbox-before-approve-v2.4.1.png) and the `inbox--*` shots.
2. **Approve (person, Inbox, patch release).** The confirm read `Approve v2.9.1? This tags v2.9.1, moves latest onto it and publishes its images.` The response was 202 with `dispatched.workflow = promote-release.yml`, `ref = main`, the `-pending` commit, and `by = debian`. The row left the Inbox. See [`out/inbox-approve-v2.9.1.json`](2026-11-04-release-approval/out/inbox-approve-v2.9.1.json).
3. **The dispatch and its record** ([`out/approve.out`](2026-11-04-release-approval/out/approve.out), [`out/dispatches.json`](2026-11-04-release-approval/out/dispatches.json)):
   - The inputs were `version`, `decision: approve`, `short_soak_ok: false`, `allow_breaking: false`, `approval`, and `approval_signature`. The record binds repo, version, decision, the `-pending` commit and the approver, with a 15-minute expiry (`expires - issued = 900`).
   - `openssl dgst -sha256 -verify <(grain release approval-key)` printed **Verified OK** for every dispatch.
   - `grain release approve -allow-breaking v2.6.0` passed `allow_breaking: true` through, both in the inputs and in the record.
4. **Second press:** `grain release approve -repo acme/widgets v2.9.1` exited **2** with `v2.9.1 was already answered: debian dispatched promote-release.yml for it at …; watch that run rather than starting another`. `POST …/approve` returned **400** with the same text. The sim still held one dispatch for v2.9.1.
5. **Agent:** the CLI through the proxy stub exited **3** with `only a person may approve a release: "20" is an agent, …`. `POST …/approve` with the acting-task header returned **403** with the same text. Nothing was dispatched.
6. **Tagging clears:** tagging `v3.0.0` (05:17:13) cleared `release.pending:v3.0.0` at 05:26:44 as `automation:release-watch`, on the watcher's next poll. On the interim build: `v2.5.0` was tagged at 05:03:04 and cleared at 05:10:52.
7. **Reject** from the Inbox dispatches `decision: reject`. That is covered by the Playwright spec rather than repeated live.

## Item 2: Record tampering

Script: [`tamper.sh`](2026-11-04-release-approval/tamper.sh) takes one real dispatch, clones the sim repository, and runs `scripts/verify-release-approval.sh` exactly as the workflow does. Outputs: [`out/tamper-approve.out`](2026-11-04-release-approval/out/tamper-approve.out) (v2.4.1 approve) and [`out/tamper-rollback.out`](2026-11-04-release-approval/out/tamper-rollback.out) (v2.4.0 rollback).

| Case | Exit | Verifier says |
|---|---|---|
| as dispatched | 0 | `debian` |
| `NOW = expires` / `expires + 3600` | 1 | `the record expired at … press Approve again` |
| tag moved to another commit | 1 | `the record's commit is "90526bb…", but this run is for "34e6012…"` |
| `GRAIN_RELEASE_APPROVAL_KEY` = another key | 1 | `signature does not verify against GRAIN_RELEASE_APPROVAL_KEY` |
| same record signed by another key | 1 | same |
| approver edited after signing | 1 | same |
| rollback: `reason` input changed | 1 | `the record's reason_sha256 is "7ed88a…", but this run is for "183476…"` |
| rollback: `rollback_to` changed | 1 | `the record's rollback_to is "v2.3.0", but this run is for "v2.2.0"` |

## Item 3: Rollback

Script: [`rollback.sh`](2026-11-04-release-approval/rollback.sh). Output: [`out/rollback.out`](2026-11-04-release-approval/out/rollback.out) on the final build.

- **Actions repo `acme/widgets`, person, CLI:** `grain release rollback -repo acme/widgets -to v2.3.0 -reason 'v2.4.0 drops queued widgets on restart (#4242)' v2.4.0` exited **0**. It dispatched `promote-release.yml` on `main` with:
  - inputs `version: v2.4.0`, `decision: rollback`, `rollback_to: v2.3.0`, `reason: <the text>`, and the signed record;
  - record lines `commit: <v2.4.0's commit>`, `rollback_to: v2.3.0` and `reason_sha256: 7ed88a3158fbf63362ad4333fc23dc3eeb9b5448b37cffb0b0c1503526e5ffff`.
  - SHA-256 of the dispatched `reason` matches the record and equals SHA-256 of the reason given. A second rollback exits **2** ("rollback was already started").
- **Actions repo, agent:** the CLI exited **3**, `only a person may roll back a release: "20" is an agent, …`. The API returned **403** with the same text.
- **grain CI repo `acme/gadgets`, person:**
  - The CLI exited **0** and started `fake-ci-N` with job `.grain/workflows/promote-release.yml:rollback`, `event: workflow_dispatch`, `requestedBy: debian`.
  - `POST /api/releases/v1.1.0/rollback` with `{"repo":"acme/gadgets","reason":…}`, as the UI sends it, returned **202** with the same kind of run.
  - `-fake-ci` does not expose the request's release fields. The mapping into `GRAIN_CI_REQUESTED_BY_KIND=human`, `RELEASE_ROLLBACK_TO` and `RELEASE_ROLLBACK_REASON`, and the workflow's refusal unless the caller is human, are covered by `pkg/ui/release_approval_test.go`, `pkg/ci/requested_by_env_test.go` and `tests/deploy/release_promotion_test.go`. All three ran green here.
- **grain CI repo, agent:** the CLI exited **3** and the API returned **403**, both with the message above.

## Item 4: Upgrade on each release, real daemon

Log excerpt: [`out/daemon-upgrade.log`](2026-11-04-release-approval/out/daemon-upgrade.log). Cut-overs: [`out/restarts.log`](2026-11-04-release-approval/out/restarts.log). The setting was `PUT /api/settings {"autoUpgradeOn":"release"}`, with the channel left `""` so the upgrade goes to the exact tag.

**How the hour was shortened:** without changing the code or the reconciler. After the reconciler logged `release vX is published; upgrading to it once it has stood for 1h0m0s`, the row it reads was back-dated:

```
sqlite3 data/store/grain.db "update auto_upgrade_release set seen_at='<more than an hour ago>'"
```

The next 10s tick then treated the release as settled. The registry tag list is cached for 10 minutes (`tagListTTL`), so each new tag was seen at the next refresh.

| Time (UTC) | Event |
|---|---|
| 04:38:46 | `v3.1.0` (running) seen. After the back-date: `already runs it` → `unchanged`, no drain. |
| 04:39:44 / 04:48:56 | `v3.2.0` pushed / seen: the settle starts. |
| 04:49:26 / 04:59:06 | `v3.2.1` pushed / seen inside v3.2.0's hour: `Newest` moves to v3.2.1 and the settle restarts. |
| 05:00:56 | After the back-date: **one** `scheduled the automatic upgrade to v3.2.1`. Queue held → idle → pull, signature (skipped, no verify cmd), health check, unit flags, schema (119 = 119), record image, restart. `v3.2.0` was never scheduled. |
| 05:01:02–03 | `restart.sh` logged `GRAIN_IMAGE=localhost:5000/grain:v3.2.1@sha256:9c6b…`. The daemon exited and came back. Outcome `ok`. |
| 05:01:51 / 05:11:06 | Broken `v3.3.0` pushed / seen. |
| 05:11:56–59 | After the back-date: the upgrade to v3.3.0 failed at **health check** (`schema-version: simulated health-check failure`). The deployment stayed on v3.2.1. |
| 05:12:06 | `raised notification upgrade.failed:v3.3.0`: "Automatic upgrade to v3.3.0 failed at health check", with a log excerpt. `raiseCount` was 1. Over the next 9 minutes, `upgrade: v3.3.0: requested` appeared exactly once (outcome `failed`, `release_tag = v3.3.0`). |
| 05:11:47 / 05:21:06 | `v3.4.0` pushed / seen. |
| 05:21:26–34 | After the back-date: v3.4.0 upgraded and cut over. Then `cleared notification upgrade.failed:v3.3.0` (cleared by `automation:auto-upgrade`). |

Two releases close together therefore give one drain, a failing health check raises `upgrade.failed` once without a retry, and the next release clears it. The queue was idle, so each drain was immediate. A drain with work in flight is the scheduled-upgrade hold that `releaseupgrade_test.go` covers.

## Item 5: Retracted release on a live Upgrade pane

Before the v3.4.0 cut-over, `v3.4.0-retracted`, `v3.3.0-retracted`, a fix-forward `v3.4.1` and `latest` → v3.2.1 were pushed. The restarted daemon reported `retractedRelease: {running: v3.4.0, latest: v3.2.1, newer: v3.4.1}` on both `GET /api/upgrade` and `/api/upgrade/targets`.

[`retracted.mjs`](2026-11-04-release-approval/retracted.mjs) drove the pane. Output: [`out/retracted.json`](2026-11-04-release-approval/out/retracted.json). Shot: [`upgrade-retracted-alert.png`](2026-11-04-release-approval/screenshots/upgrade-retracted-alert.png).

- The alert read: "Release v3.4.0 was retracted. v3.4.1 has been published to fix forward from it. … Moving back to latest (v3.2.1) is only safe if the database schema has not changed since then …".
- "Move back to latest (v3.2.1)" → Upgrade sent `POST /api/upgrade {"branch":"latest","afterTasksFinish":false}`. The request was intercepted and refused so the daemon was not downgraded.
- "Upgrade to v3.4.1" → Upgrade sent `{"branch":"v3.4.1","afterTasksFinish":false}` and got 202 with `phase: running`.
  - The upgrade succeeded in 6s, and `restart.sh` logged `GRAIN_IMAGE=localhost:5000/grain:v3.4.1@sha256:5672…`.
  - Afterwards, `/api/upgrade` showed `phase: ok` and no `retractedRelease`.

## Item 6: Settings UI and Inbox

- [`settings-mode.mjs`](2026-11-04-release-approval/settings-mode.mjs) picks each mode in "Upgrade automatically" as a person would, reloads, and reads both the picker and `GET /api/settings`. All three passed ([`out/settings-*.json`](2026-11-04-release-approval/out/)):
  - **Off:** `Off (manual only)`, `autoUpgradeOn: off`.
  - **Once a day:** `PUT /api/settings {"autoUpgradeOn":"daily","autoUpgradeChannel":"latest","autoUpgradeTime":"03:00"}`, and `Once a day` after the reload.
  - **On each release:** `PUT {"autoUpgradeOn":"release","autoUpgradeChannel":"latest"}`, and `On each release (recommended)` after the reload.
  - Picking "On each release" in the UI also sets the target to `latest` when none was set, so item 4's exact-tag channel was set through the API.
- Screenshots were taken with `node e2e/screenshot.mjs --url … --code …` (the `make ui-screenshots` script, pointed at the live servers) at 1440 and 390 px in light and dark:
  - `/settings/general#updates` once per mode, on the demo;
  - `/settings/upgrade` on the real daemon in its retracted state;
  - `/inbox` on the demo, with `v2.5.1` (patch), `v2.6.0` and `v2.4.0` pending.
- The 20 shots are in [`screenshots/`](2026-11-04-release-approval/screenshots/), and [`contact-sheet.png`](2026-11-04-release-approval/contact-sheet.png) lays them out in five rows: updates-off, updates-release, updates-daily, upgrade-retracted, inbox.

## Tests run

- `go test ./pkg/ui/ ./pkg/orchestrator/ ./pkg/demo/ ./pkg/github/... ./pkg/model/ ./pkg/releaseapproval/ ./pkg/cli/ ./tests/deploy/ -count=1`: all green.
- An interim widening of the watcher (F1b) broke `TestRunCycleTellsARunWhichReposAProposalMayName` and two target-repo tests in that run. It was reverted rather than fixed, and is filed.
- `cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/release-approval-actions-sim.spec.js e2e/release-from-trunk-approval-walkthrough.spec.js e2e/auto-upgrade-settings-layout.spec.js e2e/upgrade-retracted-release.spec.js`.
- `shellcheck docs/test-runs/2026-11-04-release-approval/*.sh` and `prettier --check` on the new JS: clean.
- Re-verified on 2026-11-07 (attempt 2, on top of `origin/main` `e49f0b62`): the Go packages above plus `./tests/hygiene/`, `gofmt -l pkg/` (clean), `npx prettier --check .` in `ui/`, and the three Playwright specs above all pass. `retracted.mjs` was reformatted with Prettier. This run had no GitHub repository wired for `wait_for_checks`/`pull_request_status`, so CI's verdict on PR #824 was not read from inside the run.
- Merge-queue repair (2026-11-07, attempt 3): PR #824's `go orchestrator` check failed at its `Test` step, but GitHub served no log and this run could not read CI (no `grain#ci` capability, and no GitHub repository wired to `pull_request_status`/`base_branch_status`). After merging `origin/main` `6045a6a0`, CI's own command, `go test -race -timeout 25m ./pkg/orchestrator/...`, passed locally in 588 s, including `TestAnActionsRepoThatNeverSetACIModeIsApprovedThroughActions` and the release-watch tests. The merge was pushed so CI reruns on it.
- Merge-queue repair (2026-11-07, attempt 4): merging `origin/main` `e6d29b24` conflicted in `pkg/github/githubsim/githubsim.go`, because `main` had added its own workflow-dispatch and tag endpoints. The branch's duplicate dispatch handler, `WorkflowDispatches` field, type and `Dispatches()` method were dropped in favour of `main`'s (`actions.go`). The loopback GET listing was kept, reading `Sim.Dispatches`. `WorkflowDispatch` gained JSON tags, and `Sim.WorkflowFiles` was added so the demo's `promote-release.yml` is dispatchable before it has run (`main` answers 404 for a workflow with no runs). Afterwards, the Go packages above and the two release-approval Playwright specs pass.
