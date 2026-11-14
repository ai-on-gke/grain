# Walkthrough Report: Host `grain health` Exit Codes Across Live `FAILING`, `WARNING`, `ALERT` and In-Sandbox Checks (2026-11-13)

Plan: [`docs/test-plans/2026-11-13-afternoon.md`](../test-plans/2026-11-13-afternoon.md) §3, auditing PR #1442 (`8bdaedcc1`, filed from [Task #5366](/tasks/5366)).

- **Commit walked:** `ab445d443` (`origin/main` at 2026-11-14 09:17 UTC).
- **Helpers:** [`docs/test-runs/2026-11-13-grain-health-exit-codes/`](2026-11-13-grain-health-exit-codes/) (`env.sh`, `ghsim.go`, `fake-agy.py`, `run_walkthrough.sh`).

## Summary of Results

| # | Walkthrough Item | SQLite | PostgreSQL | Outcome |
|---|---|---|---|---|
| 1a | Clean daemon (`grain health`, `grain health -json`, `grain -json health`) | pass | pass | Exit `0`, empty stderr (`0` bytes), `jq -e .` valid. |
| 1b | Live `WARNING` only: `MergeLoginWarnings` (`GRAIN_MERGE_LOGINS=octocat`) | pass | pass | Exit `0` in text and `-json`, empty stderr, warning printed cleanly. |
| 1c | Live `WARNING` only: `CIWaits` (`resume_later` CI wait > 1h, then task close) | pass after fix | pass after fix | Exit `0` in text and `-json`. **F1 & F2 fixed:** suppressed bogus trailing `no counters` line and stopped closed `resume_later` tasks from leaking into `ciWaits` forever. |
| 1d | Live `ALERT` only: `Daemon.StoreSwitch` (`db-config.json` switched underneath running daemon) | pass after fix | pass after fix | Exit `0` in text and `-json`. **F1 & F3 fixed:** suppressed bogus trailing `no counters` line and preserved `StoreSwitch` on `Enabled: false` health servers. |
| 2a | Single `FAILING` #1: `DispatchBlocked` (git-proxy port held + queued task) | pass after fix | pass after fix | Exit `1` in text (`grain: health: failing: dispatch is blocked` on stderr) and `-json` (`0`-byte stderr, pure JSON stdout). **F1 fixed.** |
| 2b | Single `FAILING` #2: `CapabilityServers` (built-in `git` port `4254` occupied) | pass | pass | Exit `1` in text (`grain: health: failing: 1 capability server(s) out of use` on stderr) and `-json` (`0`-byte stderr, pure JSON stdout); clears to `0` once `4254` is freed. |
| 2c | Single `FAILING` #3: `StaleSchedules` (enabled hourly schedule > 2× cadence without run) | pass | pass | Exit `1` in text (`grain: health: failing: 1 stale schedule(s)` on stderr) and `-json` (`0`-byte stderr, pure JSON stdout). |
| 2d | Single `FAILING` #4: `RefusedDeclarations` (malformed `.grain/templates/broken.yml` on `main`) | pass | pass | Exit `1` in text (`grain: health: failing: 1 repository declaration set(s) not read in full` on stderr) and `-json` (`0`-byte stderr, pure JSON stdout). |
| 2e | Combined 3-way `FAILING` (`CapabilityServers` + `StaleSchedules` + `RefusedDeclarations`) | pass | pass | Exit `1` in text and `-json`; comma-joined stderr summary and pure JSON stdout verified. |
| 2f | Combined 4-way `FAILING` (all four `FAILING` checks active simultaneously) | pass | pass | Exit `1` in text and `-json`; all 4 sections printed in order and all 4 JSON arrays populated. |
| 3 | In-sandbox `grain health`, `grain read deploy://health`, `grain read deploy://health.json` (`api#self-debug`) | pass | pass | All three exit `0` with empty stderr and return the live failing `SandboxHealthResponse` under `.health` while host `grain health` exits `1`. |

---

## Environment & Stubbed Boundaries

- **Binary:** built from `HEAD` via `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` (walked both before and after the fixes on this branch).
- **System packages installed in stock sandbox:** `postgresql` (`15.20`), `postgresql-client`, `sqlite3`, `socat` (`jq` was preinstalled).
- **Daemon:** real `grain daemon -sandbox-port-base=0 -host-sandboxes -ui-addr 127.0.0.1:4280 -as alice@example.com -repo acme/widgets -default-target-repo acme/widgets -poll-interval 500ms` (switching `-sandbox-port-base` to a free 256-aligned port block with offset `0` held by `socat` for items `2a` and `2f`).
- **Stores:**
  - **SQLite:** `$WORK_DIR/data/store/grain.db`.
  - **PostgreSQL 15:** `-db-driver postgres -db-dsn postgres://grain:grain@127.0.0.1:5432/grain_walk?sslmode=disable`, freshly recreated (`DROP DATABASE ... WITH (FORCE); CREATE DATABASE grain_walk`) at the start of each run.
- **Stubbed external boundaries only:**
  - `ghsim.go` on `127.0.0.1:4284`: `pkg/github/githubsim` + `git http-backend` over a real bare `widgets.git` repo, plus `/_sim/merge-logins` (serving `GET /repos/acme/widgets/actions/variables/GRAIN_MERGE_LOGINS` and `GET /users/{login}`) and `/_sim/seed-actions` (seeding an `in_progress` GitHub Actions workflow run for `resume_later`).
  - `fake-agy.py`: scripted Antigravity CLI driving the real `grain mcpserver` over stdio (`run_command`, `resume_later`, `comment_on_issue`, `nothing_needed`).

---

## Bugs Found & Fixed

### F1: `grain health` printed `"no counters: nothing on this daemon registers any"` after `DispatchBlocked`, `StoreSwitch`, and `CIWaits` (fixed)

- **Where:** `pkg/cli/health.go` (`(*printer).health`).
- **Symptom reproduced on live SQLite & PostgreSQL daemons:** on an open-source `grain daemon` where no layer calls `ui.RegisterHealthCounters` (`len(h.Counters) == 0`), the trailing `"no counters"` guard checked only six of the nine rendered sections (`hasHubIssue`, `CapabilityServers`, `TargetRepoHostWarnings`, `MergeLoginWarnings`, `StaleSchedules`, `RefusedDeclarations`) and omitted `DispatchBlocked`, `Daemon.StoreSwitch`, and `CIWaits`. Consequently, in states `1c`, `1d`, and `2a`, `grain health` printed the `WARNING` / `ALERT` / `FAILING` section followed immediately by a contradictory `"no counters: nothing on this daemon registers any"` line:
  ```text
  WARNING: tasks waiting on CI for more than 1h0m0s (resume_later):
    task 2 on acme/widgets:grain/344c81fe/task-2-walkthrough-ci-wait-task: CI gha:acme:widgets:5001 (github), deferred since 2026-11-14T07:44:08Z

  no counters: nothing on this daemon registers any
  ```
  (and identically after `ALERT (since …): …` and `FAILING: dispatch is blocked …`).
- **Fix:** track `var printed bool` across every rendered section in `(*printer).health` and print `"no counters: nothing on this daemon registers any"` only when `!printed` and `len(h.Counters) == 0`.
- **Regression test:** `TestGrainHealthExitsOneWhenACheckIsFailing` in `pkg/cli/health_test.go` (asserting `forbid: "no counters"` across `dispatch blocked`, `ci wait warning only`, `store switch alert only`, and all other non-empty sections, plus checking exact stderr text and post-verb `grain health -json` JSON validity).

### F2: Closing a `resume_later`-deferred task left it permanently in `HeldCIResumes` and `grain health`'s `ciWaits` warning (fixed)

- **Where:** `pkg/model/resume_on.go` (`Store.heldResumes` and `Store.recoveredCIHolds`).
- **Symptom reproduced on live SQLite & PostgreSQL daemons:** after step `1c` parked task `2` via `resume_later(when: {"ci": "gha:acme:widgets:5001"})` and closed it via `POST /api/tasks/2/close`, `task_observation.closed_at` was set while `task.deferred_at` remained non-null (so reopening a closed task restores its prior state). Because neither `heldResumes` nor `recoveredCIHolds` joined `task_state` (or checked `closed_at IS NULL`), closed task `2` continued to appear in `GET /api/sandboxes`'s `ciWaits` (`WARNING: tasks waiting on CI for more than 1h0m0s`) across every subsequent check (`1d`–`3`) and continued to be polled by `orchestrator.reconcileWake` (`DueResumeOnCI` / `DueMessageResumes`).
- **Fix:** join `task_state` and require `st.state = 'deferred'` in both `Store.heldResumes` and `Store.recoveredCIHolds`. Closing a deferred task immediately removes it from `ciWaits`, `DueResumeOnCI`, `DueMessageResumes`, and `WakeOnMessage`, while `t.deferred_at` remains intact so reopening the task restores the hold.
- **Regression test:** `TestClosedTasksDoNotStayInHeldResumes` in `pkg/model/message_resume_test.go` (covering both suspension-backed and comment-recovered CI holds, `WakeOnMessage`, `DueMessageResumes`, and reopen restoration on SQLite and PostgreSQL).

### F3: `GET /api/sandboxes` dropped `Daemon.StoreSwitch` (`ALERT`) when no sandbox/host/daemon reader was wired (fixed)

- **Where:** `pkg/ui/sandbox_health.go` (`handleGetSandboxHealth`).
- **Symptom:** when `Config.StoreSwitch` was set on a `ui.Server` whose `Sandboxes`, `HostStats`, `Daemon`, `StandingSandboxes`, `PersistentSandboxes`, and `Overcommit` callbacks were `nil`, `handleGetSandboxHealth` returned early in its `Enabled: false` branch before reading `Config.StoreSwitch`, omitting `daemon.storeSwitch` from `GET /api/sandboxes` and `grain health`.
- **Fix:** evaluate `Config.StoreSwitch` ahead of the `Enabled: false` early return and populate `Daemon: &DaemonHealth{StoreSwitch: storeSwitch}` in both branches.
- **Regression test:** `TestGetSandboxHealthCarriesStoreSwitchWithoutSandboxPool` in `pkg/ui/sandbox_health_test.go`.

### Observation O1 (tracked separately, untouched here)

- When port `4254` is occupied and built-in `https://grain.dev/capabilities/git` fails to bind `127.0.0.1:4254`, `/api/sandboxes` currently surfaces `state: "needs-config", configRefused: true`, which `grain health` renders as `(refused its saved config)` while still classifying the section as `FAILING` (exit `1`). As noted in the test plan, open [Task #5431](/tasks/5431) is already refining built-in start failure vs config refusal labelling in `pkg/capability/`; this branch leaves `pkg/capability/` untouched so the two changes merge cleanly.

---

## Detailed Walkthrough Evidence (SQLite & PostgreSQL)

All commands below were executed via `docs/test-runs/2026-11-13-grain-health-exit-codes/run_walkthrough.sh` (`STRICT_NO_COUNTERS=1`) against the fixed `/home/debian/bin/grain` binary over both `STORE=sqlite` and `STORE=postgres`, producing identical exit codes, stderr messages, and JSON structures on both stores.

### Item 1: Healthy and Advisory-Only (`WARNING` / `ALERT`) Daemon States

| State | Mode | Exit `$?` (SQLite / PG) | Stderr | Stdout / JSON Verification |
|---|---|---|---|---|
| `1a` Clean daemon | `grain health` | `0` / `0` | `""` (`0` B) | `no counters: nothing on this daemon registers any` |
| `1a` Clean daemon | `grain health -json` & `grain -json health` | `0` / `0` | `""` (`0` B) | `jq -e '.enabled == true'` passes; all 4 failing slices absent. |
| `1b` `MergeLoginWarnings` | `grain health` | `0` / `0` | `""` (`0` B) | Prints `WARNING: reviewed-merge logins that belong to a user account:` (`acme/widgets octocat: …`); no `no counters` line. |
| `1b` `MergeLoginWarnings` | `grain health -json` | `0` / `0` | `""` (`0` B) | `jq -e '(.mergeLoginWarnings \| length == 1) and (.mergeLoginWarnings[0].login == "octocat")'` passes. |
| `1c` `CIWaits` (>1h `resume_later`) | `grain health` | `0` / `0` | `""` (`0` B) | Prints `WARNING: tasks waiting on CI for more than 1h0m0s (resume_later):` (`task 2 … CI gha:acme:widgets:5001`); no `no counters` line; clears to `0` `ciWaits` immediately on `POST /api/tasks/2/close`. |
| `1c` `CIWaits` (>1h `resume_later`) | `grain health -json` | `0` / `0` | `""` (`0` B) | `jq -e '.ciWaits \| length == 1'` passes. |
| `1d` `Daemon.StoreSwitch` (`ALERT`) | `grain health` | `0` / `0` | `""` (`0` B) | Prints `ALERT (since …): the store this daemon writes to is no longer the one …/db-config.json selects: …`; no `no counters` line. |
| `1d` `Daemon.StoreSwitch` (`ALERT`) | `grain health -json` | `0` / `0` | `""` (`0` B) | `jq -e '.daemon.storeSwitch != null'` passes. |

### Item 2: Live `FAILING` Triggers (Single & Combined)

| State | Mode | Exit `$?` (SQLite / PG) | Exact Stderr | Stdout / JSON Verification |
|---|---|---|---|---|
| `2a` Dispatch blocked (`git-proxy` bind conflict) | `grain health` | `1` / `1` | `grain: health: failing: dispatch is blocked` | `FAILING: dispatch is blocked -- no task is dispatched until this clears:` + `[git-proxy] starting git proxy: listen tcp 127.0.0.1:10240: bind: address already in use` + `remedy:`. |
| `2a` Dispatch blocked | `grain health -json` | `1` / `1` | `""` (`0` B) | `jq -e '(.dispatchBlocked \| length == 1) and (.dispatchBlocked[0].name == "git-proxy")'` passes. |
| `2b` Capability server down (`4254` occupied) | `grain health` | `1` / `1` | `grain: health: failing: 1 capability server(s) out of use` | `FAILING: capability servers out of use -- no grant is minted from one until it attaches again:` naming `https://grain.dev/capabilities/git` (`127.0.0.1:4254: bind: address already in use`). |
| `2b` Capability server down | `grain health -json` | `1` / `1` | `""` (`0` B) | `jq -e '(.capabilityServers \| length == 1) and (.capabilityServers[0].id == "https://grain.dev/capabilities/git")'` passes. |
| `2c` Stale schedule (`sched-1` > 2× 1h cadence) | `grain health` | `1` / `1` | `grain: health: failing: 1 stale schedule(s)` | `FAILING: schedules with no run in more than twice their cadence:` (`sched-1 on acme/widgets: every 1h0m0s, last ran …`). |
| `2c` Stale schedule | `grain health -json` | `1` / `1` | `""` (`0` B) | `jq -e '(.staleSchedules \| length == 1) and (.staleSchedules[0].id == "sched-1")'` passes. |
| `2d` Unread `.grain/` declarations (`broken.yml`) | `grain health` | `1` / `1` | `grain: health: failing: 1 repository declaration set(s) not read in full` | `FAILING: repository declarations not read in full:` (`acme/widgets (files held back for a newer grain; in force: …)` with YAML line errors). |
| `2d` Unread `.grain/` declarations | `grain health -json` | `1` / `1` | `""` (`0` B) | `jq -e '(.refusedDeclarations \| length == 1) and (.refusedDeclarations[0].repo == "acme/widgets")'` passes. |
| `2e` Combined 3-way `FAILING` | `grain health` | `1` / `1` | `grain: health: failing: 1 capability server(s) out of use, 1 stale schedule(s), 1 repository declaration set(s) not read in full` | Prints all 3 `FAILING:` sections in order on stdout. |
| `2e` Combined 3-way `FAILING` | `grain health -json` | `1` / `1` | `""` (`0` B) | `jq -e` confirms all 3 failing arrays have length `1` with `0` stderr bytes. |
| `2f` Combined 4-way `FAILING` | `grain health` | `1` / `1` | `grain: health: failing: dispatch is blocked, 1 capability server(s) out of use, 1 stale schedule(s), 1 repository declaration set(s) not read in full` | Prints all 4 `FAILING:` sections in order on stdout:<br>1. `FAILING: dispatch is blocked`<br>2. `FAILING: capability servers out of use`<br>3. `FAILING: schedules with no run in more than twice their cadence`<br>4. `FAILING: repository declarations not read in full` |
| `2f` Combined 4-way `FAILING` | `grain health -json` | `1` / `1` | `""` (`0` B) | `jq -e` confirms `.dispatchBlocked`, `.capabilityServers`, `.staleSchedules`, and `.refusedDeclarations` all have length `1`. |

Verbatim `2f` stdout and stderr from the live daemon:

```text
$ grain health
FAILING: dispatch is blocked -- no task is dispatched until this clears:
  [git-proxy] starting git proxy: listen tcp 127.0.0.1:10240: bind: address already in use (for 0s, since 2026-11-14T10:01:13Z)
    remedy: free the port the git proxy binds (or move the block with -sandbox-port-base) and restart the daemon

FAILING: capability servers out of use -- no grant is minted from one until it attaches again:
  https://grain.dev/capabilities/git (refused its saved config): capability server https://grain.dev/capabilities/git: configure: configure: server reported an error: git: listen tcp 127.0.0.1:4254: bind: address already in use

FAILING: schedules with no run in more than twice their cadence:
  sched-1 on acme/widgets: every 1h0m0s, last ran 2026-11-14T06:01:04Z

FAILING: repository declarations not read in full:
  acme/widgets (files held back for a newer grain; in force: fcf76b081fa0):
    .grain/templates/broken.yml: .grain/templates/broken.yml: yaml: unmarshal errors:
      line 1: cannot unmarshal !!seq into string
      line 3: cannot unmarshal !!map into string (not loaded: no earlier version of it is in force)

# stderr:
grain: health: failing: dispatch is blocked, 1 capability server(s) out of use, 1 stale schedule(s), 1 repository declaration set(s) not read in full
# exit code: 1
```

### Item 3: In-Sandbox `grain health` (`deploy://health`) Parity

While the daemon was in state `2e` (3 simultaneous `FAILING` checks active, host `grain health` and `grain health -json` exiting `1`), a task run holding `https://grain.dev/capabilities/api#self-debug` was dispatched into a live sandbox (`POST /api/tasks` with `"grants":["https://grain.dev/capabilities/api#self-debug"],"approved":true`), and `fake-agy.py` invoked `run_command` inside the sandbox (`GRAIN_SERVER`, `GRAIN_TOKEN`, and `GRAIN_CONTEXT` unset so `sandboxcli` routed through the sandbox's `.git-credentials` proxy):

| In-Sandbox Command | SQLite (`rc` / `stderr` / `stdout_bytes`) | PostgreSQL (`rc` / `stderr` / `stdout_bytes`) | Failing Arrays Present in `.health` |
|---|---|---|---|
| `grain health` | `0` / `""` / `6543` B | `0` / `""` / `6538` B | `capabilityServers: 1`, `staleSchedules: 1`, `refusedDeclarations: 1` |
| `grain read deploy://health` | `0` / `""` / `6524` B | `0` / `""` / `6556` B | `capabilityServers: 1`, `staleSchedules: 1`, `refusedDeclarations: 1` |
| `grain read deploy://health.json` | `0` / `""` / `6543` B | `0` / `""` / `6903` B | `capabilityServers: 1`, `staleSchedules: 1`, `refusedDeclarations: 1` |

---

## Verification Commands Run

```bash
GRAIN_UPDATE_CLI_SPEC=1 go test ./pkg/cli ./pkg/sandboxcli -run CLISpec
GRAIN_REQUIRE_POSTGRES=1 GRAIN_POSTGRES_TEST_URL="postgres://grain:grain@127.0.0.1:5432/postgres?sslmode=disable" \
  go test -race -shuffle=on -count=2 ./pkg/cli -run "TestGrainHealth|TestCmdGrainFilesStaySmall"
GRAIN_REQUIRE_POSTGRES=1 GRAIN_POSTGRES_TEST_URL="postgres://grain:grain@127.0.0.1:5432/postgres?sslmode=disable" \
  go test -race -shuffle=on -count=2 ./pkg/model -run "TestClosedTasksDoNotStayInHeldResumes|TestAPostWhileTheRunEnds|TestDueMessageResumes"
go test -race -shuffle=on -count=2 ./pkg/ui -run "TestGetSandboxHealth|TestSandboxHealth"
go test -race ./pkg/orchestrator -run "ResumeLater"
go test ./pkg/ports/... ./tests/hygiene/...
STRICT_NO_COUNTERS=1 ./docs/test-runs/2026-11-13-grain-health-exit-codes/run_walkthrough.sh
```
