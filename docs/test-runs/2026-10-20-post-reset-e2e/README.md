# 2026-10-20: post-reset end-to-end qualification

Task 4828 (it replaces 4789 and 4716). This qualifies grain after the
single-tenant reset, the google/ isolation and the Silo hub removal, flow by
flow, against the inventory in
[`docs/test-plans/2026-10-20-post-reset-e2e.md`](../../test-plans/2026-10-20-post-reset-e2e.md).
The row numbers below are that plan's.

## Setup

| | |
|---|---|
| Commit | `main` at **b93646ebc** (merge of 4864, 6-ii-b5, **schema 25**). Every stage of all three changes is on `main`: google/ 4660–4665; the hub removal 4826; reset stages 1–5 and 7 (4680–4684, 4686), every 3x and 4x sub-stage, and stage 6 (4685, 4768, 4839, 4861, 4862, 4863, 4864). The owner was asked about the stages still open at 04:10Z (6-ii-b2 to b5); all four had landed by 16:35Z |
| Builds | **core** `go build ./cmd/grain`; **Google** `go build ./google/cmd/grain`; core again with the frontend embedded (`cd ui && npm ci && npm run build`) for the browser pass; **v1.13.0** built from its tag (a `git worktree`) for the upgrade and guard row |
| Deployments | Four fresh single-tenant installs: core on SQLite, Google on SQLite, core on PostgreSQL 15, Google on PostgreSQL 15. Each runs `grain daemon -sandbox-backend none` on a fresh data dir, and each Postgres install gets its own database in a throwaway cluster. All are driven by `scripts/post-reset-e2e.sh`. The e2e harness runs its own daemons with real sandboxes |
| Stubs | **GitHub**: `pkg/github/githubsim` in the e2e harness. The owner said on this task to use git servers and the GitHub simulator, since github-sandbox isn't set up. **forgecli**: a shell script on the Google daemon's PATH (the script writes it). Nothing of grain's own is stubbed |
| Real | Gemini API (this task's key) through **agy 1.2.2**, copied from `ghcr.io/bwsalmon/grain/grain:main` (`/opt/antigravity/antigravity`); Chromium headless shell (Playwright) |
| Host | Task sandbox, Debian 12, 4 vCPU, 8 GB |

## Evidence files

| File | What |
|---|---|
| [`deployments-core-sqlite.md`](deployments-core-sqlite.md), [`deployments-google-sqlite.md`](deployments-google-sqlite.md), [`deployments-core-postgres.md`](deployments-core-postgres.md), [`deployments-google-postgres.md`](deployments-google-postgres.md) | `scripts/post-reset-e2e.sh`'s rows for each deployment. **All PASS**: 17 rows on core and 16 on Google (C3b, core's refusal of `forgecli`, is core-only) |
| [`e2e-harness.md`](e2e-harness.md) | `go test ./tests/e2e/`: **117 pass, 16 skip, 0 fail** (210.8s). The skips are the live-agent, load, soak and stress tests |
| [`ui-browser.md`](ui-browser.md), [`ui-browser-check.mjs`](ui-browser-check.mjs) | The browser pass over 43 paths |
| this file | The per-flow verdicts, Gemini discovery, and the suites run locally |

Run locally as well: `go test ./tests/security/... ./google/...`. **All
`ok`**: `tests/security` 49.4s, plus 8 `google/...` packages, including
`google/scripts` (the Cloudtop installer tests) and `google/gitproxy/forgecli`.

## Results

| # | Flow | Result | Evidence |
|---|---|---|---|
| 1 | Fresh install; access code 0600; no `tenancy` | **PASS** | D-I1 ×4. A fresh dir holds `cache hub-audit.log local-api-token secrets state state-repo store` (`db-config.json` in place of `store` on Postgres). `hub-audit.log` is the local-policy audit log; it kept its old name on purpose (`daemon.LocalAuditPath`) |
| 2 | Access-code sign-in | **PASS** | D-I2 ×4: `/` 401, `/api/tasks` 401, a wrong code 401, the right code 200; `grain access-code` prints it |
| 3 | Login form, lower case with spaces | **PASS** | D-I2b ×4: `POST /api/auth/access-code` 303, and the cookie gets `/api/tasks` 200. B: typed into the real login page |
| 4 | `scripts/setup.sh` (core) | **PASS (CI)** | grain CI `installer` job on this branch |
| 5 | Cloudtop installer (Google) | **PASS (stubbed)**, **M** on a real Cloudtop | `google/scripts` (33 tests: fresh install, no account made, model seeded, depot opt-in, `forgecli` checks) |
| 6 | Installer re-run | **PASS** | `TestCloudtopInstallerReRunChangesNothing`, `…KeepsAnExistingGitConnection`, `…KeepsTheInstalledAddress`; CI `installer` |
| 7 | Upgrade from v1.13.0 | **PASS** | D-I5 ×4. A v1.13.0 build writes a dir (SQLite, or its own Postgres database). The new build exits 1 with "this build of grain is single-tenant only, and this data dir was written by a build from before the single-tenant reset: schema 17, from before the single-tenant baseline (schema 19)…", and the dir's sha256 manifest is unchanged |
| 8 | Startup guard, pre-reset and multi-tenant | **PASS** | D-I5; `go` (`TestADumpWithASecondTeamIsRefused`, `TestADumpWithAnotherTeamsMemberIsRefused`, `TestTheGoldenDumpMigratesIntoThisBuildsSchema`); interim check 8 rows 5–8 |
| 9 | Clean restart on its own dir | **PASS** | D-I3 ×4 |
| 10 | `grain connections import` | **PASS** | D-I4 ×4: "imported 1 git and 1 model connections" twice; `secrets/github/bot.token` 0600; `defaultModel` reads back |
| 11 | In-place upgrade of a post-reset deployment | **PASS (harness)**, **M** for a real image | H `TestAScheduledUpgradeStopsWaitingWhenItsDrainWindowRunsOut`, `…WithNoDrainWindowWaitsForTheRunToFinish` |
| 12 | `grain upgrade-postgres` | **PASS (CI)** | `postgres` job; interim check 8 row 21a |
| 13 | PAT against real github.com | **NOT TESTABLE** | github-sandbox isn't configured on this deployment (attempt 1 was refused for it), and the owner said to use githubsim. Every H lifecycle test runs a PAT-style connection against githubsim. **M** |
| 14 | GitHub App against real github.com | **NOT TESTABLE** | As row 13. githubsim: H `TestARunGitHubRefusesTheDeploymentsCredentialOnFailsAndTellsTheAgentWhy`, `TestARunWrongHostCredentialFailsAndTellsAgentAndTaskNotes`. **M** |
| 15 | `forgecli` forge only in the Google build | **PASS** | D-C3: Google `[forgecli]`, core `[]` |
| 16 | Core refuses `forgecli` cleanly | **PASS** | D-C3b: exit 1, `grain: unknown ambient source "forgecli": gh, git, auto` |
| 17 | Depot / GHOP with the real `forgecli` | **NOT TESTABLE** | Corp hosts can't be reached from a sandbox. **M** on a Cloudtop |
| 18 | Gemini discovery | **PASS (live)** | See "Gemini discovery" below |
| 19 | Per-model pause and resume; pipeline pause | **PASS** | D-L3 ×4: `grain pause -set` / `-resume`; `DELETE /api/pause?connection=gemini&model=…` 200. H `TestWalkthroughRefusalBurstPausesTheFrameworkAndQueuedTasksWait`, `…ReplayedClaudeSessionLimitPausesUntilItsReset`, `…AShortPauseExpiresAndTheQueuedTaskDispatches` |
| 20 | Default model on a fresh install | **PASS** | D-C5 ×4: unset; D-I4: set by the seed and read back |
| 21 | Create from the CLI; approve, close, reopen | **PASS** | D-L1 ×4. See the observation on close below. H `TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt` |
| 22 | Create from the API with `depends_on`, `not_before` | **PASS** | D-L2 ×4 (`dependsOn ['2']`, `deferredUntil 2099-01-01T00:00:00Z`) |
| 23 | Create from MCP (`propose_task`), approval | **PASS** | H `TestProposedTaskWaitsForApprovalThenRunsThroughTheCLI`, `TestProposedChainStaysBlockedUntilTheTasksItNamedClose`, `TestMCPProposeTaskWalkthroughSection4` |
| 24 | Create from a template; schedule run-now | **PASS** | D-S1, D-S2 ×4 (`POST /api/schedules/sched-1/run` 201 with a `taskId`) |
| 25 | Dispatch, sandbox, clone, push, PR | **PASS** | H `TestE2EBuildAppAndManageTasks`, `TestDispatchPreClonesTheRepoSoTheAgentNeverHasTo`, `TestCLICreateWithBaseOpensPullRequestAgainstThatBranch`, `TestStandaloneTaskImplementsTestsAndLandsAChange` |
| 26 | CI checkout with the deployment's connection | **PASS** | H `TestCIToolsWalkthrough…`; `go` (`pkg/cli/ci_e2e_test.go`) |
| 27 | Merge queue: single, stale, conflicts, repair | **PASS** | H: 8 `TestMergeQueue…` tests and `TestSyncPullRequestsRepairsAndLandsAConflictedMergeQueueHead` |
| 28 | Batched lanes, flake recheck, bisect | **PASS** | H `TestMergeQueueLanesContentionWalkthrough`, `TestCIFailureEvictedOnlyByGreenAtItsOwnBase`, `TestCIToolsWalkthroughRecheckOnlyTheFailedJob`; `go` (`pkg/orchestrator` `mergebatch_test.go`, `autonomous_merge_queue_batching_bisection_walkthrough_test.go`) |
| 29 | Merge and port to `candidate` | **PASS** | H `TestPortWalkthroughPairedBranches`, `TestPortMechanicalWalkthrough`, `TestPortTypesWalkthrough` |
| 30 | Every closing path | **PASS** | H: the six tests in plan row 30 |
| 31 | Cancel, re-run, supersede | **PASS** | H `TestFailedRunReturnsTaskToQueueForRetry`, `TestARedispatchIsToldWhatTheAttemptBeforeItDid`, `TestSecondAttemptPushReusesTheFirstAttemptsOpenPullRequest`; `go` (`pkg/ui/supersede_test.go`) |
| 32 | `depends_on`, `not_before`, pick-up-now | **PASS** | H `TestDependsOnTaskWaitsForRealPushMergeAndCloseBeforeDispatch`, `TestCLIDependsOnAttachesAndDetachesThroughTheAPI`, `TestParentBlockedUntilChildrenClose`; D-L2 |
| 33 | Questions and replies | **PASS** | H `TestAgentQuestionParksTaskThenReplyResumesAndItCompletes`, `TestCLICommentAnswersAParkedQuestionAndResumesTheTask` |
| 34 | Daemon restart mid-run | **PASS** | H `TestADaemonRestartInterruptsTheRunAndTheTaskResumes`; CI `run host` |
| 35 | Director chat filed on first start | **PASS** | D-I1 daemon log: "orchestrator: filed task 1 for the director console session" |
| 36 | Interactive chat | **PASS** | H `TestWalkthroughAMessagePostedWhileTheRunAnswersIsDispatchedFor`; CI `ui e2e` |
| 37 | Handover, propose_task from a session | **PASS** | H `TestMCPProposeTaskWalkthroughSection4`, `TestWidenedProposalsWalkthrough`; `go` (handover tests in `pkg/orchestrator`) |
| 38 | Schedules on both databases | **PASS** | D-S1/S2 on SQLite and Postgres |
| 39 | Workflow schedules | **PASS (unit)**; live firing **NOT TESTED** | `go` (`pkg/orchestrator/scheduledworkflow*_test.go`). The eu host fires no scheduled workflow (team notes), so a live firing is **M** (Run now) |
| 40 | Suites and templates | **PASS** | D-S1 ×4 |
| 41 | Triggers | **PASS** | H `TestPathTriggersWalkthrough`, `TestSecurityHoldWalkthrough`, `TestSecurityHoldV2Walkthrough`, `TestCapabilityEscalatedProposalsWalkthrough` |
| 42 | Release cut and gate on a scratch repo | **NOT TESTABLE** | No scratch repo is in this task's git proxy scope, and the real cut is paused. `go` covers the back-merge push (`pkg/gitproxy/release_backmerge_push_test.go`). **M** |
| 43 | CI: push runs, gates, caching, recheck, logs | **PASS** | H `TestCIToolsWalkthroughRecheckOnlyTheFailedJob`, `…StuckGateAdmits…`, `TestCircuitBreakerHoldsDispatchOnBrokenBaseAndCacheForgetsTheOldBase`; this branch's own grain CI runs |
| 44 | Every page loads; old paths redirect | **PASS** | B: 43 paths, all 200. `/t/default/tasks`, `/teams`, `/hub`, `/login` and `/register` land on `/`; `/admin` on `/settings/general`; `/system/accounts` on `/settings/access` |
| 45 | **No team, account, admin or hub UI anywhere** | **FAIL** | B: `/repos` says "couldn't discover repositories from the team's git connections"; `/settings/credentials` says "backend credentials are set on a team's Models & connections tab". `grain create` with no git connection says `team "default" has no GitHub credential … choose another in Team settings → Git`. Many more strings point at a "Team settings" page that no longer exists: `pkg/gitproxy`, `pkg/orchestrator/cycle.go` wait reasons, `pkg/daemon/agents.go`, `google/scripts/cloudtop-install.sh` output, `ui/src` "Team default (…)". **Bug filed**: "Remove the 'Team settings' / team wording …" |
| 46 | No page calls a removed route | **PASS** | B: none in the whole pass; D-U1 ×4: `/api/teams`, `/api/teams/default`, `/api/users`, `/api/auth/login`, `/register`, `/change-password` and `/api/hub/status` all 404; `POST /api/tasks/1/move` 404 |
| 47 | Add a repo; repo settings, releases, boards, custom fields, Settings, Upgrade | **PASS** | CI `ui e2e`; H `TestASetValuedCustomFieldGoesFromTheStateRepoThroughARun`; D-S1 (boards); B (the pages load) |
| 48 | Export/import round trip | **PASS** | D-R1 ×4: the task, the default model and the template survive. A Postgres bundle carries `db-config.json` and leaves the rows in the database, so its import must go into an empty dir, which is how the script runs it |
| 49 | Older dump into the baseline | **PASS (CI)** | `go` (`pkg/staterepo/migrate_test.go`) |
| 50 | `grain sync` | **PASS** | H `TestCLICreatesTaskWithAutoMergeAndSyncMergesItWithNoHuman` |
| 51 | Route authorization | **PASS** | `tests/security` locally (49.4s) and in CI |
| 52 | The daemon writes nothing its guard refuses | **PASS** | D-I3 ×4. The test the reset doc named was deleted with the `team` table in 6-ii-b4. This change corrects the doc (`docs/single-tenant-reset.md`) |
| 53 | Trust-boundary review flow | **PASS** | H `TestSecurityHoldWalkthrough`, `TestSecurityHoldV2Walkthrough` |
| 54 | Secrets never logged | **PASS** | D-X1 ×4: the git token, the model key and the access code are in no daemon log. Live: the real Gemini key is not in the log after discovery |
| 55 | Removed routes stay removed | **PASS** | D-U1 ×4 |
| 56 | Self-debug MCP tools | **PASS** | H `TestMCPServer…` (7 tests), `TestMCPDiagnosticsAndQuarantineWalkthrough` |
| 57 | Grain source checkout | **PASS** | H `TestGrainSourceCheckoutWalkthrough` |
| 58 | Live agent runs | **NOT TESTABLE** | The `TestLive*` tests skip without provider keys in the harness. `live-agent`/`live-frameworks` are scheduled workflows that haven't fired on the eu host since 10-14. **M** (Run now) |

### Counts

| | Rows |
|---|---|
| Flows in the inventory | 58 |
| **PASS** | 52 (rows 4, 12 and 49 by grain CI on this branch; 5 and 11 also have a manual part) |
| **FAIL** | 1 (row 45) |
| **NOT TESTABLE** from a sandbox | 5 (rows 13, 14, 17, 42, 58), plus the live firing in row 39 |

## Gemini discovery (row 18)

The setup: a core build with agy 1.2.2 on the daemon's PATH, and a connection
seeded with `grain connections import` (`framework: antigravity`,
`credential_file`, no pinned models).

1. `grain connections import` alone writes the connection but doesn't start
   discovery. `discovered` stayed empty for 15s, until the next ten-minute pass.
2. `PUT /api/agent-backends/gemini/credential {"value": <key>}` answered 200
   `{"set":true}`. Within 10s `discovered.models` held 11 models:
   `gemini-3.8-flash-{high,medium,low}`, `gemini-3.7-flash-{…}`,
   `gemini-3.6-flash-{…}`, `gemini-3.1-pro-high`, `gemini-3.1-pro-low`, with
   `refreshedAt` set.
3. The key appears nowhere in the daemon log.

`GET /api/agent-models` (the provider-wide listing, which uses agy's own
ambient sign-in rather than a connection) still answers agy's "Please sign in
to view available models". That was already noted in interim check 8.

## Observations (not filed as bugs)

- **A close can land a tick later.** On Postgres, `grain close` on a task that
  had just been approved sometimes read back `queued`, then `closed` within
  12s. This happened in 2 of 4 tries, on a daemon with no sandboxes whose
  dispatch fails at once. The close always landed, and it is what
  `TestClosingATaskWhileItsRunIsStillLive…` describes. The script polls for it.
- **Postgres task ids jump after a restart** (2, 4, then 34): sequence caching.
  Harmless.
- **`grain connections import` while the daemon runs doesn't start model
  discovery.** Saving through the API does. A seeded connection advertises
  its models only after the next ten-minute pass or a credential save.
- **The `team` field is still accepted by `POST /api/schedules` and
  `POST /api/suites`, and `/api/config` still carries `teams` and `tenancy`.**
  Every flow works, but this is team-shaped API surface with no stage to
  retire it. **Filed** as its own task.
- Still present from interim check 8, and not re-filed here:
  - `PUT /api/memory/documents` with no `text` answers 200;
  - `/api/secrets/copy` answers 405;
  - `grain git-ambient` with no arguments adds a second host-default connection;
  - `status.host` is wrong for an anonymous `git.example.test` connection.

## Bugs filed

1. **Remove the "Team settings" / team wording grain still shows after the
   single-tenant reset** (row 45, the one failure). Branch `main`; repro, file
   list and done-when are in the task.
2. **Retire the team-shaped fields left on grain's API after the single-tenant
   reset** (`/api/config` `teams` and `tenancy`; the schedule and suite `team`
   fields).

This run had no task-listing tool, so neither could be checked against open
tasks from the soak (4827) or the interim checks. Each task says to look first.

## Standing workflow

`.grain/workflows/post-reset-e2e.yml` runs daily at 05:23 UTC on `main`, and
on demand:

- **deployments**: builds core, Google and v1.13.0, installs PostgreSQL, and
  runs `scripts/post-reset-e2e.sh` on all four deployments. That covers
  rows 1–3, 7–10, 15, 16, 19–22, 24, 35, 38, 40, 46, 48, 52, 54 and 55.
- **harness**: `go test ./tests/e2e/`, the lifecycle rows 21–37, 41, 43, 50,
  53, 56 and 57 against githubsim.
- **security**: `tests/security` and `google/...`, rows 5, 6 and 51.

`.grain/schedules/post-reset-e2e-candidate.yml` runs the workflow on
`candidate`. It is `enabled: false` until the first post-reset cut: `candidate`
is still the multi-team v1.14.0-rc.1, and the cut is paused. The manual-only
rows (5 and 11 in part, 13, 14, 17, 39's live firing, 42 and 58) are in
`docs/release-candidate-management.md`, under "Before a cut: the post-reset
qualification checklist", with the steps that wire this workflow into the
candidate gate once the cut is re-enabled.

## For the owner on a Cloudtop

From that checklist:
1. Cloudtop install and re-run (Google build).
2. Depot / GHOP through the real `forgecli`.
3. PAT and GitHub App against a real github.com scratch repo.
4. An image upgrade of a post-reset deployment. Not the eu host.
5. A release cut and gate.
6. A Run now of `live-agent` and `live-frameworks`.
