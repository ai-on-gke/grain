# Walkthrough: Run now through parking runs, early `resume_later` wakes, and `grain ci test-history` on GitHub Actions (`2026-11-10.md` §9)

- **Date:** 2026-11-10
- **Task:** [Task #3956](/tasks/3956) (origin [Task #3930](/tasks/3930))
- **Plan section:** [`docs/test-plans/2026-11-10.md`](../test-plans/2026-11-10.md) §9
- **Audited PRs:**
  - PR #1029 (`ba4e06aa`, `c9951341` — `SuspensionKind.KeepsRunNow`, stale-stamp cleanup in `Store.Init`, `runNowStale` in the task pane)
  - PR #995 (`4e47815f` — `SuspensionResumeAt` ignores `SuspendRequested`, `"held until <t> by its suspension"` setup-hold wait & log)
  - PR #1034 (`b43acac2` — `grain ci test-history --provider grain|github-actions`, Actions failure tails folded into `grain ci show`)

---

## 1. Environment & Harnesses

All walkthrough scripts live in [`docs/test-runs/2026-11-10-dispatch-wakes/`](2026-11-10-dispatch-wakes/) with Apache-2.0 headers:

- [`env.sh`](2026-11-10-dispatch-wakes/env.sh): initializes a bare `acme/widgets` repository (default branch `main`), seeds git and Antigravity model connections via `grain connections import`, starts `ghsim` (`127.0.0.1:4284`) and a real `grain daemon -sandbox-port-base=0 -host-sandboxes` (`127.0.0.1:4280`) over SQLite.
- [`ghsim.go`](2026-11-10-dispatch-wakes/ghsim.go) (`//go:build ignore`): serves `pkg/github/githubsim` alongside real `git http-backend` CGI and `/_sim/*` endpoints to seed check-runs and GitHub Actions workflow runs (`nightly.yml`, `tests.yml`).
- [`fake-agy.py`](2026-11-10-dispatch-wakes/fake-agy.py): scriptable Antigravity (`agy`) CLI subprocess driven per task via JSON queues (`resume_later`, `sleep`, `usage_limit`, `mcp_sequence`, `comment_on_issue`, `nothing_needed`, `plain_reply`), invoking the run's real `grain-sandbox` stdio MCP server from `$HOME/.gemini/config/mcp_config.json` and emitting authentic `step_update` tool events.
- [`ui_probe.mjs`](2026-11-10-dispatch-wakes/ui_probe.mjs): Playwright Chromium driver signing in via `ui/e2e/accesscode.js` to click `Run now` / `Undefer` and inspect `.run-now-hint`, `.framework-paused-hint`, and button `disabled` state on `/tasks/<id>`.
- [`item1_run_now.sh`](2026-11-10-dispatch-wakes/item1_run_now.sh), [`item2_early_wakes.sh`](2026-11-10-dispatch-wakes/item2_early_wakes.sh), [`item3_test_history.sh`](2026-11-10-dispatch-wakes/item3_test_history.sh): end-to-end runners for §9 items 1–3.

Binaries built for the walkthrough:
1. `/home/debian/bin/grain`: `HEAD` (`09427fc3a` + fixes on this branch) with `ui/dist` embedded.
2. `/tmp/grain-c08832a2`: pre-batch baseline `c08832a2` (schema 129) built from a detached `/tmp` worktree for the store-upgrade check.
3. `/tmp/grain-short-deadline`: scratch `/tmp` build with `orchestrator.DefaultMaxRunRuntime = 3 * time.Second` so wall-clock deadline runs (`SuspendDeadline` and standard `cancelled`) trigger in 3s over the real daemon.

*(Sandbox image note: `sqlite3` was not preinstalled in the sandbox image and was installed via `sudo apt-get update && sudo apt-get install -y sqlite3` in ~3s.)*

---

## 2. §9 Item 1 — `Run now` Through Real Suspensions & `c08832a2` Upgrade

### 2.1 Results across suspension & ending kinds

Each case started from a **deferred** task on a real `grain daemon` (`-poll-interval 400ms`), triggered via either the Chromium task pane's **Run now** button or `POST /api/tasks/{id}/run-now`:

| Case | Trigger | Run outcome / `suspend_kind` | `Run now` spent or kept? | Task pane `Run now` button & `.run-now-hint` (after fix) | Posted `grain` comment (after fix) | Next run start |
|---|---|---|---|---|---|---|
| **1a. `resume_later {at: "1h"}`** | UI click | `parked` / `requested` (`resume_at = +1h`) | **Spent** (`run_now_at = NULL`, `runNowStale = false`) | **Enabled**, no `.run-now-hint` (`Undefer` offered; task is `deferred`) | `"grain: this task's run parked it until <+1h>. It goes back in the queue once that time has passed; Undefer releases it sooner."` (no `"Run now kept"` comment) | At `deferredUntil` (`+1h`), or immediately on Undefer / fresh `Run now` |
| **1b-dur. Deadline (`resume_on_deadline = 1`)** | `POST .../run-now` | `suspended` / `deadline` | **Spent** (`run_now_at = NULL`, `runNowStale = false`) | **Enabled**, no `.run-now-hint` once queued | *(none — `SuspendDeadline` spends the request)* | **Immediate** automatic resume: Attempt #2 started **4.3 ms** after Attempt #1 finished (`04:50:27.987Z → 04:50:27.991Z`) |
| **1b-std. Deadline (standard task)** | `POST .../run-now` | `cancelled` / `NULL` (`"the run exceeded its 3s wall-clock limit"`) | **Spent** (`run_now_at = NULL`, `runNowStale = false`) | **Enabled**, no `.run-now-hint` | *(none)* | After 1m failure-streak retry backoff (at backlog priority, no longer ahead of queue) |
| **1c-std. Setup failure (standard task, `chmod 0555 sandboxes`)** | `POST .../run-now` | `setup-failed` / `NULL` | **Kept** (`run_now_at` preserved, `runNowStale = true`) | **Enabled**; hint: `"A run was already started for the last Run now and gave it back. It waits out the retry backoff that run earned. Press Run now to start it now."` | `"Run now kept: run 3-1 ended setup-failed before reaching its agent, so the request still stands and it starts ahead of the queue again once the retry backoff has passed."` | Ahead of queue after 1m retry backoff, or **at once** if operator presses `Run now` again |
| **1c-dur. Setup failure (durable task, `chmod 0555 sandboxes`)** | UI click | `suspended` / `setup` (`resume_at = +2m`) | **Kept** (`run_now_at` preserved, `runNowStale = true`) | **Enabled**; hint: `"A run was already started for the last Run now and gave it back. It is held until <+2m> by its suspension. Press Run now to start it now."` | `"Run now kept: run 4-1 ended setup-failed before reaching its agent, so the request still stands and it starts ahead of the queue again once its suspension's hold has passed."` | Ahead of queue at `resume_at` (`+2m`), or **at once** on re-click (measured **2,255 ms** including Playwright click round-trip) |
| **1d. Operator pause mid-run (`POST .../pause`)** | `POST .../run-now` | `interrupted` / `operator-pause` | **Kept** (`run_now_at` preserved, `runNowStale = true`) | **Disabled** (request already stands; `Resume` button is active); hint: `"Asked for a run. It starts ahead of the queue once this task is resumed."` | `"Run now kept: run 5-1 was interrupted before it could do the work, so the request still stands and it starts ahead of the queue once the task is resumed."` | **44 ms** after `POST /api/tasks/{id}/resume` (ahead of queue, `BackoffCount == 0`) |
| **1e. Usage-limit stub (`RESOURCE_EXHAUSTED`, `30m0s`)** | `POST .../run-now` | `paused` / `usage-limit` | **Kept** (`run_now_at` preserved, `runNowStale = true`) | **Disabled** (request already stands while connection is paused); hint: `"A run was already started for the last Run now and gave it back. It starts ahead of the queue once the usage-limit pause ends."` + `.framework-paused-hint` below | `"Run now kept: run 6-1 was paused on a usage limit before it could do the work, so the request still stands and it starts ahead of the queue again once the usage-limit pause ends."` | **490 ms** after `DELETE /api/pause` (or at `until = +30m`, ahead of queue) |

### 2.2 Store upgrade from `c08832a2` (schema 129 → 131) with stale stamps

On `/tmp/grain-c08832a2`:
1. Task `#2` (`UP_PARKED`): deferred → `Run now` → run called `resume_later {at: "1h"}`.
   - `c08832a2` restored `task_observation.run_now_at = 2026-11-10 05:43:34.899Z` (`<= started_at`) and posted `"Run now kept: run 2-1 was suspended (requested). That was not a run of this task, so the request still stands and the next cycle starts it ahead of the queue again."`
   - Calling `POST /api/tasks/2/undefer` on `c08832a2` left the task stuck in `queued` (`attemptCount: 1`), logging `dispatch: candidate 2 skipped: backing off after failed run` every cycle because `c08832a2`'s `SuspensionResumeAt` held it to `resume_later`'s `+1h` timestamp.
2. Task `#3` (`UP_CLOSED`): paused, stamped with `Run now`, and closed — `c08832a2` left `run_now_at = 2026-11-10 05:43:35.696Z` alongside `closed_at = 2026-11-10 05:43:35.708Z`.
3. Task `#4` (`UP_KEPT`): deferred → `Run now` → failed sandbox setup (`setup-failed`) — held a genuine unspent `run_now_at = 2026-11-10 05:43:35.756Z`.

Stopping `c08832a2` and starting `HEAD` `/home/debian/bin/grain` on the same SQLite store migrated schema `129 → 131` and ran `Store.Init` (`clearSpentRunNow`):
- Task `#2` (`UP_PARKED`): `run_now_at` cleared to `NULL`, and Attempt #2 dispatched and succeeded immediately on the first cycle.
- Task `#3` (`UP_CLOSED`): `run_now_at` cleared to `NULL`.
- Task `#4` (`UP_KEPT`): `run_now_at = 2026-11-10 05:43:35.756Z` preserved intact.

### 2.3 Task-pane visual check (1440×900 desktop & 390×844 phone, light & dark)

Captured via `ui/e2e/screenshot.mjs` across all six Item 1 task-pane states (`1a-resume-later`, `1b-deadline-std`, `1c-setup-std`, `1c-setup-durable`, `1d-operator-pause`, `1e-usage-limit`) at `1440×900` and `390×844` in both `light` and `dark` themes, plus Playwright bounding-box checks in `ui/e2e/run-now-stale-pause.spec.js`:
- `.run-now-hint` sits directly below the `Run now` button (`hintBox.y > btnBox.y`) and above `.framework-paused-hint` (`fwBox.y > hintBox.y`), strictly within `.actions` (`[colBox.x, colBox.x + colBox.width]`), with zero horizontal viewport overflow (`scrollWidth <= innerWidth`) at both 1440px and 390px.

---

## 3. §9 Item 2 — Early Wakes, Console Sessions / Goal Tasks, and Setup Holds

Measured on a real `grain daemon` (`-poll-interval 500ms`, `acme/widgets` set to `ci_mode: github`):

| Scenario | Suspension row recorded (`task_suspension`) | Early wake action | Delay from wake to Attempt #2 `startedAt` | Notes |
|---|---|---|---|---|
| **2a. `{at: "2h", message: true}`** | `kind="requested"`, `resume_on={"at":"+2h","message":true}`, `resume_at=+2h` | `POST /api/tasks/2/comments` | **39 ms** | `WakeOnMessage` clears `deferred_at`/`deferred_until` and nudges reconcile; `SuspensionResumeAt` ignores `SuspendRequested`; `startRun` sets `resumed_at` & `resumed_run_id="2-2"`. |
| **2b. `{at: "2h", ci: "my-branch"}`** | `kind="requested"`, `resume_on={"at":"+2h","ci":"my-branch"}`, `resume_at=+2h` | Complete `Tests / go` check-run (`conclusion: "success"`) on `ghsim` | **289 ms** | Reconciler observes green GitHub check-run on pushed branch SHA, clears deferral, and dispatches on the same tick (~0.6 cycles). |
| **2c-i. `{at: "2h", task: "<id>"}`** | *(refused before parking)* | — | — | MCP `resume_later` returns `isError: true`: `"when.task takes no other condition: wait on tasks, or on ci, at and message, not both"` (`ResumeLaterWhen` & `ui.Client.ResumeLater`). |
| **2c-ii. `{task: "<id>"}`** | `kind="requested"`, `resume_on={"tasks":["4"]}`, `resume_at=NULL` | `POST /api/tasks/4/close` | **533 ms** | ~1 reconcile cycle (`500ms`); dependency closure unblocks `task_ready` and dispatches Attempt #2. |
| **2d. `{at: "2h"}` + UI Undefer** | `kind="requested"`, `resume_on={"at":"+2h"}`, `resume_at=+2h` | Click **Undefer** in Chromium (`ui_probe.mjs`) | **25 ms** after `POST /undefer` completed | `Undefer` clears deferral and triggers immediate woken dispatch cycle despite `task_suspension.resume_at` still naming `+2h`. |

### Console sessions & goal tasks ("woken, waiting for dispatch")
- Created console session `ops` and `person-closes` goal task `#8`. After Turn 1, each rested with an open `SuspendRequested` row (`resumed_at IS NULL`) and `deferred_at != NULL`.
- Held worker dispatch via `POST /api/pipeline-pause`, woke the resting goal task via `POST /api/tasks/8/comments`, and let 4 reconcile cycles (`2.0s`) run:
  - `state` remained `"queued"`, `deferredAt: null`, `deferredUntil: null`, `goal.deferredByGoal: null`, `reason.reason: "queued/ready"`, and `task_suspension.resumed_at` remained `NULL` — confirming `reconcileGoals` (`restingOnGoal`) and `reconcileConsoleSessions` read the open `SuspendRequested` row with cleared deferral as woken and waiting for dispatch rather than re-deferring it.
  - Deleting `/api/pipeline-pause` immediately dispatched Attempt #2 and marked the suspension resumed.

### Setup suspension hold & log line
- Durable task `#9` (`admission: "durable"`) failed setup (`chmod 0555 sandboxes`) → `SuspendSetup` with `resume_at = 2026-11-10T05:39:01Z` (`+2m`).
- After restoring directory permissions (`chmod 0755`), across subsequent reconcile cycles task `#9` stayed held at `attemptCount: 1` with:
  - `reason`: `{"reason": "queued/held_by_suspension", "class": "driven", "resource": "held until 2026-11-10T05:39:01Z by its suspension", "driver": "reconcileDispatch", "control": "Run now"}`
  - `activity`: `"waiting: held until 2026-11-10T05:39:01Z by its suspension"` (`activityBySetup: true`)
  - Daemon log: `dispatch: candidate 9 skipped: held until 2026-11-10T05:39:01Z by its suspension`

---

## 4. §9 Item 3 — `grain ci test-history` on GitHub Actions

### 4.1 Live sandbox refusal without `api:ci-read` (this task #3956 on `ai-on-gke/grain`)
Because `ai-on-gke/grain` is `ci_mode: github`, default `grain#ci` is stripped from ordinary tasks; [Task #3956](/tasks/3956) holds only `https://grain.dev/capabilities/corp#research={}`. Running both the image's preinstalled `/usr/local/bin/grain` (older build predating `--provider`) and the freshly built `HEAD` `/home/debian/bin/grain` inside this sandbox returned exit `1`:

```text
grain: forbidden: grain ci: that tool belongs to the "https://grain.dev/capabilities/grain#ci" capability, and this run does not hold it (this run does not hold the "https://grain.dev/capabilities/grain#ci" capability). Nothing you can do from inside this run will add it -- a human attaches it to the task.
```

The identical refusal was reproduced on a task dispatched without `api:ci-read` on our local `HEAD` daemon (`Item3c`), while a host CLI caller using an external-agent `gma_` token holding `tasks:read` (lacking `ci:read`) exited `3` with:

```text
grain: external:tasks-read-only holds no api grant for scope ci:read, which GET /api/ci/runs needs
```

### 4.2 Real `HEAD` daemon (`ci_mode: github`) with `nightly.yml` Actions failures & retired Grain CI runs
On `acme/widgets` (`ci_mode: github`), we seeded:
- **2 retired Grain CI runs** (`ci-oct31-1`, `ci-oct31-2`, `2026-10-31 12:00..15:00 UTC`) passing `TestOutboundTunnelConnectPeerForwardingReconnectGraceAndImmediateRevocation` (a real flake from `nightly.yml` `go-long`).
- **5 GitHub Actions runs** (`2026-11-08 01:00 UTC .. 2026-11-10 03:00 UTC`):
  - `9505` and `9503` (`.github/workflows/nightly.yml`, `failure`, job `go-long`) with recorded `failure_tail` rows in `github_ci_job` failing `TestOutboundTunnelConnectPeerForwardingReconnectGraceAndImmediateRevocation`;
  - `9502` (`.github/workflows/nightly.yml`, `failure`, job `go-long`) failing `TestUntailedNightlyFailure` on the forge **without** a recorded `github_ci_job` tail;
  - `9504` and `9501` (`.github/workflows/tests.yml`, `success`).

After fixing Defects 1–3 below, **in-sandbox** (`grain` inside a dispatched task holding `api:ci-read@acme/widgets`) and **host** (`grain -server ... -repo acme/widgets`, with both admin access code and a `ci:read`-only `gma_` token) produced **identical** reports (differing only in the hint's flag prefix `--provider grain` vs `-provider grain`):

- **Default (no `--provider`)**:
  ```text
  Test History: TestOutboundTunnelConnectPeerForwardingReconnectGraceAndImmediateRevocation (repo: acme/widgets)
  Runs examined: 5 | Executions found: 2
    • GitHub Actions: 5 run(s) (2026-11-08 01:00 UTC .. 2026-11-10 03:00 UTC)
    GitHub Actions runs count only this test's failures: grain keeps the log tails of failed jobs alone, so a pass there is not seen and the rates below are over the executions found, not over every run.
    Note: acme/widgets's CI is GitHub Actions: its 2 Grain CI run(s), from before, are left out (--provider grain reads them)

  Summary Statistics:
    • Passed:  0 (0.0%)
    • Failed:  2 (100.0%)
    • Flaked:  0 (0.0%)

  Recent Executions:
    • 2026-11-10 03:00 UTC | run gha:acme:widgets:9505 (nightly.yml, main @ 8887a48) | job "go-long": FAIL
    • 2026-11-09 03:00 UTC | run gha:acme:widgets:9503 (nightly.yml, main @ 8887a48) | job "go-long": FAIL
  ```
- **`--provider github-actions`**: byte-for-byte identical between sandbox and host (omits the left-out Grain CI note since the provider was explicitly requested).
- **`--provider grain`**: byte-for-byte identical between sandbox and host (`Runs examined: 2 | Executions found: 2`, `Grain CI: 2 run(s) (2026-10-31 12:00 UTC .. 2026-10-31 15:00 UTC)`, `Passed: 2 (100.0%)`).
- **Actions failure without a recorded `github_ci_job` tail (`TestUntailedNightlyFailure`)**: `Runs examined: 5 | Executions found: 0`, printing `"No recorded failures of this test in the requested runs."` rather than claiming the test passed or never ran.
- **`grain ci show gha:acme:widgets:9505` (host)**: folds both recorded job timestamps (`22m0s`) and recorded step `failure_tail` output (`--- output of "go test -race" --- ... --- FAIL: TestOutboundTunnel...`).

---

## 5. Defects Found & Fixed on This Branch

> **Trust-boundary / security review note:** Fixes 2 and 3 touch `pkg/ci/cli/wait.go` and `pkg/ci/test_history.go` (`pkg/ci/*`), which triggers grain's automated security review (`.grain/triggers/` / trust-boundary classifier). No human-required trigger paths (`.grain/**`, `scripts/release*`, `promote*.yml`) were modified.

1. **`sandboxExternalCI` dropped `ci.ExternalRunsRepos`, breaking default GitHub Actions `test-history` inside real daemon sandboxes (`pkg/daemon/sandbox_cli.go`)**
   - **Reproduction:** On unpatched `HEAD`, running `grain ci test-history <test>` (no `--provider`) inside a real daemon sandbox on a `ci_mode: github` repo examined 7 runs (5 Actions + 2 retired Grain CI), reported `Passed: 2 (50.0%)`, and omitted the left-out Grain CI note.
   - **Cause:** `pkg/daemon/servers.go` sets `nsServer.ExternalCI = sandboxExternalCI{}`. PR #1034 (`b43acac2`) implemented `ci.ExternalRunsRepos` (`ServesRepo`) on `ui.actionsExternalRuns` (`pkg/demo` uses it directly), but `sandboxExternalCI` did not forward `ServesRepo`, so `ci.ExternalCIFor` always returned `false` in `gitproxy.serveTestHistory`.
   - **Fix & regression test:** Implemented `ci.ExternalRunsRepos` on `sandboxExternalCI`; pinned by `TestSandboxExternalCIForwardsServesRepo` in `pkg/daemon/sandbox_namespace_test.go`.

2. **Host `ProviderTestHistory` needed `capabilities:read` to detect `ci_mode: github`, silently falling back on `ci:read`-only callers (`pkg/ci/cli/wait.go`, `pkg/ui/ci.go`)**
   - **Reproduction:** Running host `grain ci test-history -repo acme/widgets <test>` with a `gma_` token holding `ci:read` (without `capabilities:read`) hit `403 Forbidden` on `GET /api/repos/acme/widgets/capabilities` (`c.RepoDefaults`), swallowed the error, left `f.Provider` empty, and merged retired Grain CI passes (`Passed: 66.7%`) with no left-out note (and on a `grain/viewer` human role would return retired Grain CI runs alone).
   - **Cause:** `ProviderTestHistory` queried `RepoDefaults` (`capabilities:read`) instead of carrying the repository's active CI provider on `GET /api/ci/runs` (`ci:read`).
   - **Fix & regression test:** Added `RepoProvider` to `ui.CIRunsResponse` (`pkg/ui/ci.go`) and taught `cicli.ProviderTestHistory` (`pkg/ci/cli/wait.go`) to fall back to a `limit=1` `ci:read` probe when `RepoDefaults` is forbidden; pinned by `TestCITestHistoryDefaultsToGitHubActionsWithoutCapabilitiesRead` in `pkg/cli/cicmd_test.go`.

3. **Host `grain ci test-history` printed `unknown time` when forge job payloads omitted `started_at` (`pkg/ui/ci_actions.go`, `pkg/ci/test_history.go`)**
   - **Reproduction:** Diffing sandbox vs host `grain ci test-history --provider github-actions` showed sandbox printing `2026-11-10 03:00 UTC` while host printed `unknown time` (and sorted `unknown time` below `2026-10-31` runs when merged).
   - **Cause:** `foldRecordedTails` copied `tail.Output` from `recorded.Jobs` without copying `StartedAt`/`FinishedAt` onto jobs missing timestamps, and `jobTestExecutions` did not fall back to `run.StartedAt` when `job.StartedAt` was `nil`.
   - **Fix & regression tests:** Folded missing `StartedAt`/`FinishedAt` in `foldRecordedTails` and fell back to `run.StartedAt` in `jobTestExecutions`; pinned in `pkg/ui/ci_actions_fold_internal_test.go` and `TestQueryTestHistoryFallsBackToRunStartedAt` (`pkg/ci/test_history_test.go`).

4. **`stoprule.Decide` did not treat `resume_later` as a closing move, forcing a spurious extra turn on background runs (`pkg/agent/stoprule/stoprule.go`)**
   - **Reproduction:** Every background run calling `resume_later` without `git push` (`{at: "1h"}`, `{at: "2h", message: true}`, `{task: "<id>"}`) and stopping as instructed was blocked by `stoprule.Decide` and resumed with `BackgroundContinueReason` (`"A reply without a tool call ends your run, and this run has not finished..."`).
   - **Cause:** `MadeClosingMove` recognized `wait_for_task` (`park`/`resume_at`) and `closingTools`, but omitted `resume_later`.
   - **Fix & regression test:** Treated non-errored `resume_later` calls as closing moves in `MadeClosingMove` and named `resume_later` in `BackgroundContinueReason`; pinned in `TestDecideStopBackground` (`pkg/agent/stoprule_test.go`).

5. **Task pane left `Run now` enabled and showed a false retry-backoff hint on stale requests held by a usage-limit or operator pause (`ui/src/components/detail/Actions.jsx`)**
   - **Reproduction:** After a `Run now` run ended `paused` on a usage limit, `.run-now-hint` claimed `"It waits out the retry backoff that run earned. Press Run now to start it now."` directly above `.framework-paused-hint` (`"A run asked for now waits for that"`), and left `Run now` enabled even though clicking it cannot override a usage-limit or operator pause.
   - **Fix & regression tests:** Kept `Run now` disabled when a request already stands under `isTaskPaused(t)` or `frameworkPaused` (while keeping it enabled for retry-backoff and setup-suspension holds that re-stamping overrides), and updated `runNowHint` under `frameworkPaused` to `"A run was already started for the last Run now and gave it back. It starts ahead of the queue once the usage-limit pause ends."` Pinned by Vitest (`ui/src/components/detail/Actions.test.jsx`) and Playwright bounding-box e2e (`ui/e2e/run-now-stale-pause.spec.js`), with before/after screenshots rendered at `1440×900` and `390×844` in light and dark.

6. **`settleRunNow` doubled `"before ..."` on setup failures, claimed a retry backoff on zero-backoff interruptions, and classified operator pauses as `SuspendRestart` (`pkg/model/runnow.go`, `pkg/model/suspend.go`)**
   - **Reproduction:** Setup failures posted `"ended setup-failed before reaching its agent before it could do the work"`; durable `SuspendSetup`, `PausedOutcome`, and `InterruptedOutcome` notes claimed `"once the retry backoff has passed"` despite `BackoffCount == 0`; and `FinishRun(..., InterruptedOutcome, OperatorPausedDetail)` wrote `suspend_kind = "restart"` instead of `"operator-pause"`.
   - **Fix & regression test:** Omitted the duplicate `"before it could do the work"` suffix when `ending` already contains `"before "`, matched the `when` clause in `settleRunNow` to the actual hold (usage-limit pause, suspension hold, operator pause, next dispatch cycle, or retry backoff), and recorded `OperatorPausedDetail` interruptions as `SuspendOperatorPause` in `recordSuspensionTx`; pinned by `TestSettleRunNowNotesAndOperatorPauseKind` in `pkg/model/runnow_test.go`.
