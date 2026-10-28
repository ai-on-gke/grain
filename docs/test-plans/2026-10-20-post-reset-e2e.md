# 2026-10-20: post-reset end-to-end qualification, flow inventory

Task 4828. This is the checklist for qualifying grain after three changes:

- the single-tenant reset (`docs/single-tenant-reset.md`, stages 4680–4686,
  the 3x/4x sub-stages and 6-ii-a to 6-ii-b5, schema 25);
- the google/ isolation (4660–4665);
- the Silo hub removal (4826, `docs/hub-removed.md`).

It replaces 4789 and 4716. The results are in
[`docs/test-runs/2026-10-20-post-reset-e2e/`](../test-runs/2026-10-20-post-reset-e2e/README.md).

The inventory was built from the tree at `main` b93646ebc:

- **Docs:** `docs/user-journeys.md`, `quickstart.md`, `git-hosts.md`,
  `connections-and-models.md`, `schedules.md`, `merge-queue-batching.md`,
  `release-candidate-management.md`, `closing-a-task-from-a-run.md`,
  `local-policy.md`, `access-code.md`, and `google/docs/` (`cloudtop-install.md`,
  `ghop.md`, `user-journeys.md`).
- **API:** the 251 API routes in `pkg/ui/server.go`, grouped by resource (repos 55, tasks 39, executors 10,
  ci 10, state-repo 7, schedules 7, auth 7, agent-backends 7, upgrade 6,
  suites 6, settings 6, …).
- **UI:** the page paths the frontend links to (`ui/src`).
- **CLI:** the `grain` subcommands (`pkg/cli/main.go`).
- **MCP:** the tools in `pkg/mcp`.

A flow that touches many routes is one row. Per-route authorization is its own
row (S1), covered route by route by `tests/security`.

**How each row is qualified**

| Code | Means |
|---|---|
| **D** | `scripts/post-reset-e2e.sh`: real daemons built from the tree, run four ways (core and Google builds, each on SQLite and on Postgres). Row ids D-I1 etc. match the script's own |
| **H** | the e2e harness, `go test ./tests/e2e/`: a real daemon, real git and git proxy, and real sandboxes, with GitHub played by `pkg/github/githubsim`. The test is named |
| **C** | a grain CI job in `.grain/workflows/tests.yml` (`installer`, `ui e2e`, `postgres`, `frontend`, `go`, `run host`) |
| **B** | the browser pass in this run: Playwright (Chromium) against a core build with the frontend embedded |
| **L** | a live external service: the Gemini API through a real `agy` |
| **M** | manual. A human on a Cloudtop, or with a real github.com sandbox repo. These rows are in the release checklist (`docs/release-candidate-management.md`) |

The **Standing** column says whether `.grain/workflows/post-reset-e2e.yml`
re-runs the row every day. **CI** means `tests.yml` already runs it on every
pull request.

## Install and upgrade

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 1 | Fresh install boots; the data dir layout; the access code file is 0600; no `tenancy` file | access-code.md, reset stage 3e-iii-c4 | D-I1 | yes |
| 2 | Access-code sign-in: no code gives 401, a wrong code 401, the code 200 (as a bearer); `grain access-code` prints it | access-code.md | D-I2 | yes |
| 3 | The login form takes the code in lower case with spaces and sets the session cookie | access-code.md | D-I2b, B | yes |
| 4 | `scripts/setup.sh` (core build) on a fresh host: the unit runs, the API answers, data ownership, the CLI wrapper | quickstart.md, user-journeys §1–2 | C `installer` (`TestSetupRunsToCompletion`, `TestTheDaemonServiceExistsAndIsRunning`, `TestTheCLIWrapperTalksToTheDeploymentItInstalled`, …) | CI |
| 5 | The Cloudtop installer (Google build): fresh install, re-run changes nothing, no account made, model seeded, depot opt-in | google/docs/cloudtop-install.md | `google/scripts` tests (stubbed grain/gh/forgecli) + **M** on a real Cloudtop | yes (security job) + M |
| 6 | Installer re-run: setup.sh and the Cloudtop installer | cloudtop-install.md | `TestCloudtopInstallerReRunChangesNothing`, `…ReRunKeepsAnExistingGitConnection`, `…ReRunKeepsTheInstalledAddress` + C `installer` | yes + CI |
| 7 | Upgrade from v1.13.0: the new build refuses the old data dir, leaves it untouched, and says to start fresh and seed with `grain connections import` | single-tenant-reset.md "What grain does about it itself" | D-I5 (builds v1.13.0 from the tag) | yes |
| 8 | The startup guard on a pre-reset or multi-tenant dir; a store a schema back; a dump with a second team or team members | reset stage 5, 6-ii | D-I5; `go` (`TestADumpWithASecondTeamIsRefused`, `TestADumpWithAnotherTeamsMemberIsRefused`, `TestTheGoldenDumpMigratesIntoThisBuildsSchema`); interim check 8 rows 5–8 | yes + CI |
| 9 | Restarting on a dir this build wrote is clean | reset stage 5 | D-I3 | yes |
| 10 | `grain connections import`: git, model and default model; run twice; the token file is 0600 | connections-and-models.md, git-hosts.md | D-I4 | yes |
| 11 | In-place upgrade of a post-reset deployment (Upgrade pane, `POST /api/upgrade`, schema step) | user-journeys §3 | H `TestAScheduledUpgrade…` (drain window); **M** for a real image upgrade | partly |
| 12 | `grain upgrade-postgres` (SQLite to Postgres) | connections-and-models.md | interim check 8 row 21a; C `postgres` | CI |

## Connections

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 13 | A PAT git connection against a **real github.com** sandbox repo | git-hosts.md | **M** (github-sandbox isn't configured on this deployment; the owner said to use git servers and githubsim). The githubsim equivalent is H (every lifecycle test runs with a PAT-style connection) | M |
| 14 | A GitHub App connection against real github.com | git-hosts.md | **M**. githubsim equivalent: H `TestARunGitHubRefusesTheDeploymentsCredential…`, `TestARunWrongHostCredentialFails…` | M |
| 15 | The command-backed forge (`forgecli`) is advertised only by the Google build | git-hosts.md, google/docs/ghop.md | D-C3 (fake `forgecli` on PATH) | yes |
| 16 | Core refuses the `forgecli` source cleanly | git-hosts.md | D-C3b | yes |
| 17 | Depot / GHOP (`depot.code.corp.example`) through the real `forgecli` | google/docs/ghop.md | **M** on a Cloudtop | M |
| 18 | Gemini model discovery: saving a key advertises the provider's models | connections-and-models.md "Advertised models" | L (real `agy` 1.2.2 + the Gemini API) | no (no live key in a scheduled run) |
| 19 | Per-model pause and resume; pipeline pause | connections-and-models.md "Per-model pauses" | D-L3; H `TestWalkthroughRefusalBurstPausesTheFrameworkAndQueuedTasksWait`, `…ReplayedClaudeSessionLimitPausesUntilItsReset`, `…AShortPauseExpires…` | yes |
| 20 | Default model on a fresh install: none until set; then it reads back | connections-and-models.md | D-C5, D-I4 | yes |

## Task lifecycle

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 21 | Create from the CLI; approve, close, reopen | user-journeys §6 | D-L1; H `TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt` | yes |
| 22 | Create from the API (what the UI's New task posts), with `depends_on` and `not_before` | user-journeys §6 | D-L2; C `ui e2e` | yes |
| 23 | Create from MCP (`propose_task`), approval, a chain of proposals | closing-a-task-from-a-run.md | H `TestProposedTaskWaitsForApprovalThenRunsThroughTheCLI`, `TestProposedChainStaysBlockedUntilTheTasksItNamedClose`, `TestMCPProposeTaskWalkthroughSection4` | yes |
| 24 | Create from a template; run a schedule now | schedules.md | D-S1, D-S2 | yes |
| 25 | Dispatch, sandbox, clone, push, PR | user-journeys §6 | H `TestE2EBuildAppAndManageTasks`, `TestDispatchPreClonesTheRepoSoTheAgentNeverHasTo`, `TestCLICreateWithBaseOpensPullRequestAgainstThatBranch` | yes |
| 26 | CI checkout with the deployment's connection | git-hosts.md | H `TestCIToolsWalkthrough…`; `go` (`pkg/cli/ci_e2e_test.go`) | yes + CI |
| 27 | Merge queue: single head, stale head, conflicts, repair, escalation | merge-queue-batching.md | H `TestMergeQueue…` (8 tests), `TestSyncPullRequestsRepairsAndLandsAConflictedMergeQueueHead` | yes |
| 28 | Merge queue: batched lanes, flake recheck, bisect | merge-queue-batching.md | H `TestMergeQueueLanesContentionWalkthrough`, `TestCIFailureEvictedOnlyByGreenAtItsOwnBase`, `TestCIToolsWalkthroughRecheckOnlyTheFailedJob`; `go` (`pkg/orchestrator` batch/bisect tests) | yes + CI |
| 29 | Merge and port to `candidate` | release-candidate-management.md §5 | H `TestPortWalkthroughPairedBranches`, `TestPortMechanicalWalkthrough`, `TestPortTypesWalkthrough` | yes |
| 30 | Closing paths: merge closes; PR closed unmerged; comment with no push; nothing_needed; close while live; close queued then reopen | closing-a-task-from-a-run.md | H `TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt`, `TestSyncPullRequestsClosesATaskWhosePullRequestWasClosedWithoutMerging`, `TestAgentCompletesByCommentingWithNoPushOpensNoPullRequest`, `TestClosingATaskWhileItsRunIsStillLive…`, `TestCLIClosingAQueuedTaskThenReopeningItDispatchesForReal`, `TestClosingATaskWithAnOpenPullRequestDropsItFromTheMergeQueueForGood` | yes |
| 31 | Cancel, re-run (retry), supersede | user-journeys §6 | H `TestFailedRunReturnsTaskToQueueForRetry`, `TestARedispatchIsToldWhatTheAttemptBeforeItDid`, `TestSecondAttemptPushReusesTheFirstAttemptsOpenPullRequest`; `go` (`pkg/ui/supersede_test.go`) | yes |
| 32 | `depends_on`, `not_before` and pick-up-now | user-journeys §6 | H `TestDependsOnTaskWaitsForRealPushMergeAndCloseBeforeDispatch`, `TestCLIDependsOnAttachesAndDetachesThroughTheAPI`, `TestParentBlockedUntilChildrenClose`; D-L2 | yes |
| 33 | Questions, replies and resumption | user-journeys §7 | H `TestAgentQuestionParksTaskThenReplyResumesAndItCompletes`, `TestCLICommentAnswersAParkedQuestionAndResumesTheTask`, `TestAPersonsCommentReopensAnAwaitingAckTask…` | yes |
| 34 | Daemon restart mid-run | — | H `TestADaemonRestartInterruptsTheRunAndTheTaskResumes`; C `run host` | yes + CI |

## Sessions

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 35 | The director chat is filed on first start | user-journeys §7 | D-I1 (the daemon log: "filed task 1 for the director console session") | yes |
| 36 | Interactive chat: a message posted while the run answers | user-journeys §7 | H `TestWalkthroughAMessagePostedWhileTheRunAnswersIsDispatchedFor`; C `ui e2e` | yes + CI |
| 37 | Handover and propose_task from a session | — | H `TestMCPProposeTaskWalkthroughSection4`, `TestWidenedProposalsWalkthrough`; `go` (`pkg/orchestrator` handover tests: `finish_test.go`, `consolesessions_test.go`) | yes |

## Standing work

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 38 | Schedules (daily and cron) on SQLite and Postgres | schedules.md | D-S1/S2, run on both databases | yes |
| 39 | Workflow schedules (`.grain/workflows` `on.schedule`) | schedules.md | `go` (`pkg/orchestrator` scheduledworkflow tests); this repo's own workflows (a live run is **M**: scheduled runs are silent on the eu host, see the team notes) | CI |
| 40 | Suites and templates | schedules.md | D-S1 | yes |
| 41 | Triggers (path triggers, security holds) | local-policy.md | H `TestPathTriggersWalkthrough`, `TestSecurityHoldWalkthrough`, `TestSecurityHoldV2Walkthrough`, `TestCapabilityEscalatedProposalsWalkthrough` | yes |
| 42 | The release cut and gate on a scratch repo | release-candidate-management.md | `go` (`pkg/gitproxy/release_backmerge_push_test.go`); **M**: a real cut on bwsalmon/grain is paused (`release-candidate.yml`) until this qualification passes | M |

## CI

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 43 | Push runs, base-branch gates, caching and recheck, logs and artifacts | — | H `TestCIToolsWalkthroughRecheckOnlyTheFailedJob`, `TestCIToolsWalkthroughStuckGateAdmits…`, `TestCircuitBreakerHoldsDispatchOnBrokenBase…`; grain CI on this pull request (the run is the evidence) | yes + CI |

## UI

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 44 | Every page loads after sign-in; team, admin, hub and login paths redirect | ui routes | B | no (browser; C `ui e2e` covers the pages) |
| 45 | **No** team, account, admin or hub UI anywhere | reset stage 3e-ii, 3e-iv | B; source search of `ui/src` | no |
| 46 | No page calls a removed route (`/api/teams*`, `/api/users`, `/api/hub/*`) | reset 3e-iv-b2 | B; D-U1 for the routes themselves | yes (D-U1) |
| 47 | Add a repo with a full host-qualified name; repo settings; releases; boards; custom fields; Settings; Upgrade | user-journeys §10 | C `ui e2e`; H `TestASetValuedCustomFieldGoesFromTheStateRepoThroughARun`; D-S1 (boards); B (the pages) | CI |

## State repository

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 48 | `grain export` / `grain import` round trip (tasks, default model, templates) on SQLite and Postgres | reset stage 7 | D-R1 | yes |
| 49 | A dump at an older schema imports into the baseline | reset 6-ii | `go` (`TestTheGoldenDumpMigratesIntoThisBuildsSchema`); interim check 8 | CI |
| 50 | `grain sync` | — | H `TestCLICreatesTaskWithAutoMergeAndSyncMergesItWithNoHuman` | yes |

## Security

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 51 | Route authorization, every route in `pkg/ui/server.go` | tests/security | `go test ./tests/security/...` | yes |
| 52 | The daemon writes nothing its own guard refuses | reset stage 5 | D-I3: restart after every write the script makes. The test the reset doc names, `TestASingleTenantDaemonWritesNothingItsGuardRefuses`, was deleted in 6-ii-b4 (ee87a3b42) once no table could hold a second team; the doc's reference to it is stale | yes |
| 53 | The trust-boundary review flow (`.grain/triggers`) | local-policy.md | H `TestSecurityHoldWalkthrough`, `TestSecurityHoldV2Walkthrough` | yes |
| 54 | Secrets are never logged: git token, model key, access code | — | D-X1; L (the Gemini key isn't in the daemon log) | yes |
| 55 | Removed routes stay removed | reset 3d, 3e-iv, 4826 | D-U1 | yes |

## Self-debug tools

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 56 | MCP tools over stdio: open_pull_request, pull_request_status, recreate_sandbox, update_status, diagnostics | pkg/mcp | H `TestMCPServer…` (7 tests), `TestMCPDiagnosticsAndQuarantineWalkthrough` | yes |
| 57 | The grain source checkout for self-debug | — | H `TestGrainSourceCheckoutWalkthrough` | yes |

## Live agents

| # | Flow | Source | Qualified by | Standing |
|---|---|---|---|---|
| 58 | Live agent runs (agy, claude, codex) end to end | — | `live-agent.yml`, `live-frameworks.yml` (scheduled; need provider keys). The `TestLive*` tests in tests/e2e skip without them | separate workflows |
