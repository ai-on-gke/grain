# Walkthrough Report: `grain-ci` Schedules on `ci_mode: github` Repos, Weekly Dead-Code Scan, and `nothing_needed` Finishes (`docs/test-plans/2026-11-10.md` §4)

- **Date:** 2026-11-10
- **Task:** [Task #3951](/tasks/3951) (filed from [Task #3930](/tasks/3930))
- **Plan section:** `docs/test-plans/2026-11-10.md` §4 ("Schedules on grain CI in a `ci_mode: github` repository, the weekly dead-code scan, and `nothing_needed` finishes")
- **Scope under test:**
  - PR #1058 (`6554a4bc`, `3ae6304b`): schema 131 `schedule.grain_ci`, `Schedule.WorkflowRunsUnder`, `scheduledRunKeepsGrainCI` in `reconcileCI`, and the `startPromotion` guard.
  - PR #1055 (`84e48cfc`, `117a0f26`): `.grain/schedules/weekly-dead-code.yml`, `.grain/templates/weekly-dead-code.yml`, and `.grain/baselines/dead-code-allowlist.txt`.
  - PR #1040 (`528336c1`): `overlap: skip` standing templates finishing no-op runs via `nothing_needed`.

---

## 1. Environment & Binaries

All checks ran inside the assigned Debian sandbox VM against real `grain daemon -sandbox-port-base=0` processes (`-reconcile -poll-interval 1s -ci -host-sandboxes`), a real local Git + GitHub REST/Checks simulator (`docs/test-runs/2026-11-10-schedule-grain-ci/githubsim_server.go` wrapping `pkg/github/githubsim` and `git http-backend`), embedded SQLite (`sqlite3`), and a live PostgreSQL 15.19 cluster (`127.0.0.1:5432`).

| Binary / Tool | Revision / Version | Purpose |
|---|---|---|
| `grain` (HEAD + fixes) | `c7651098f` + branch fixes (schema 133) | Live daemon walkthroughs (Items 1–3, 4, 6) |
| `grain-129` | `c08832a2` (schema 129) | Two-version-back upgrade & `prepare-upgrade` baseline (Item 4) |
| `grain-130` | `6554a4bc~1` (schema 130) | Immediate pre-131 upgrade & `prepare-upgrade` baseline (Item 4) |
| `grain-131` | `3ae6304b` (schema 131, PR #1058 tip) | Direct 129/130 → 131 migration & state-sync verification (Item 4) |
| `deadcode` | `v0.38.0` (`golang.org/x/tools/cmd/deadcode`) | Weekly dead-code scan scratch dry-run (Item 5) |
| `staticcheck` | `2025.1.1` (`honnef.co/go/tools/cmd/staticcheck`) | `U1000` unused symbol scan in root & `grain-plc/` (Item 5) |
| `knip` | `5.70.0` | TypeScript dead-code scan in `ui/`, `ui-shared/`, `ui-hub/` (Item 5) |

Committed reproducible scripts and fixtures live in `docs/test-runs/2026-11-10-schedule-grain-ci/`:
- `githubsim_server.go` (`//go:build ignore`): Git smart-HTTP + GitHub REST/Checks/Statuses simulator with `/_sim/state` inspection.
- `walk_items_1_2_3.sh`: end-to-end automated harness for Items 1, 2, and 3 against a live `grain daemon`.
- `walk_item_4_upgrade.sh`: automated matrix for Item 4 (`129→131`, `130→131`, `129→HEAD`) across both SQLite and PostgreSQL, including `prepare-upgrade`, `state sync`, `state check`, YAML boolean `grain-ci: true/false` round-trip, and SQLite → PostgreSQL cross-backend state-repo import.
- `walk_item_5_dead_code.sh`: `/tmp` scratch-clone dry-run of every command in `.grain/templates/weekly-dead-code.yml`.
- `walk_item_6_nothing_needed.sh`: end-to-end `fake-agy` MCP harness executing all nine `overlap: skip` templates with `nothing_needed` plus a `comment_on_issue` contrast control.

---

## 2. Summary of Walkthrough Items & Verdict

| Item | Area | Initial Result | Post-Fix Result | Notes |
|---|---|---|---|---|
| **1a–1e** | `ci_mode: github` + `grain-ci: true` cadence firing, no GH status/check, review on failure only, pushes/PRs gated off, removing `grain-ci: true` | **FAIL (F1)** on manual-run / suppression copy; runtime gating **PASS** | **PASS** | Cadence firing ran `event=schedule`, `statusWrites=0`, `checkWrites=0`; green run filed `0` tasks (`review_not_needed=1`); red run filed review task `#2`; push to `main` & PR `#1` ran `0` grain CI runs; removing `grain-ci: true` dropped due firings and deleted unstarted `scheduled_workflow_run` rows. Fixed **F1** (`RunScheduleNow` & `SuppressedFiring` falsely blamed standing work instead of CI mode). |
| **2a–2e** | Confinement rules (`push` run-job/retrigger, forged `#infra-retry-x`, cross-repo schedule, settings row without workflow, release-train not starting `promote.yml`) | **FAIL (F2)** on upfront `run-job`/`retrigger` API gate (relied on async sweep) | **PASS** | Sweep cancelled forged `#infra-retry-x`, cross-repo `acme/other` schedule, non-workflow schedule row, and in-flight `push` runs; `startPromotion` did not start `promote.yml` on `ci_mode: github` (`promote.yml` count = `0`). Fixed **F2** (`StartTaskCIRunOpt` & `ciEnabledFor` now refuse `run-job`/`retrigger`/repo start upfront with HTTP 403 on `ci_mode: github`/`none`). |
| **3** | Docs' "Running a script on a cadence without waking an agent" (`api:tasks:read`, `api:tasks:from-template`) | **FAIL (F4)** on non-`ai-on-gke/grain` repos (doc mismatch with `ciapi` repo allowlist) | **PASS** (docs fixed + follow-up proposed) | Works end-to-end on `ai-on-gke/grain` (`POST /api/tasks` → 403, `GET /api/settings` → 403, cross-repo `from-template` → 403, quiet run → 0 tasks, work run → HTTP 201 task `#8`). Refused on `acme/widgets` because `ciapi.DynamicProvider` offers `api:*` only to `ai-on-gke/grain` by default. Clarified docs (`docs/schedules.md`, `docs/declarations.md`, `docs/agent-api.md`) and filed a `propose_task` follow-up for configurable multi-repo `ciapi` opt-in. |
| **4** | Schema `129→131`, `130→131`, `129→HEAD` upgrades + `prepare-upgrade` + `state sync`/`check` on SQLite & PostgreSQL | **FAIL (F3)** on SQLite `state sync` (`grain-ci: 0/1` integers instead of YAML booleans) | **PASS** | Migrations backfilled `grain_ci=0/false` cleanly on SQLite & PostgreSQL; `prepare-upgrade` stayed schema-compatible with 129/130; PostgreSQL exported `grain-ci: false/true` booleans; cross-backend import succeeded. Fixed **F3** (`"grain-ci": true` in `pkg/staterepo/decl.go` `booleanKeys` so SQLite also exports `grain-ci: false/true`). |
| **5** | Scratch `/tmp` dry-run of `.grain/templates/weekly-dead-code.yml` | **PARTIAL (F5a)** — template names `ui-registry/` instead of `ui-hub/` | **PASS** (follow-up proposed for `.grain/templates/`) | All Go (`deadcode`, `staticcheck U1000`), `trustboundary.Classify`, allowlist (21/21 live, 0 stale), and `knip` commands executed cleanly in `/tmp`. `ui-hub/` had 1 unused devDependency + 3 unlisted workspace imports missed because the template still says `ui-registry/`. Filed via `propose_task` (avoiding `human: required` `.grain/**` trigger on this PR). |
| **6** | Audit & live `fake-agy` execution of `overlap: skip` templates ending with `nothing_needed` | **PASS** (minor stale repo name **F5b** in `nightly-security-review.yml`) | **PASS** | All 9 `overlap: skip` templates (`release-train`, `release-watchdog`, `release-process-review`, `prune-packages`, `nightly-security-review`, `nightly-features-test-plan`, `weekly-dead-code`, `ui-walkthrough-review`, `update-agent-clients`) closed immediately (`state=closed`, `nothing_needed_action=""`) on `nothing_needed`, unblocking immediate re-fire (`HTTP 201`), whereas `comment_on_issue` parked in `awaiting_reply` and blocked re-fire (`HTTP 409`). |

---

## 3. Detailed Walkthrough Evidence

### Item 1: `ci_mode: github` Repository with `grain-ci: true` Schedule (`walk_items_1_2_3.sh`)

Fixture repository `acme/widgets` was registered with `ciMode: "github"` and `declarations: true`, containing `.grain/workflows/check.yml` (`on: { push: ..., schedule: [{ cron: "11 2 * * *" }] }`) and `.grain/schedules/check.yml` (`workflow: check`, `grain-ci: true`, `every: { minutes: 10 }`).

1. **1a — Real cadence firing runs `event=schedule`:** Moving `schedule.next_run_at` two seconds into the past caused `reconcileSchedules` (`DueSchedules`) to record a `scheduled_workflow_run` row and `startScheduledWorkflowRun` to launch `ci_run` `1`:
   ```text
   id  repo          ref   event     requested_by                                    conclusion  cancel_reason
   --  ------------  ----  --------  ----------------------------------------------  ----------  -------------
   1   acme/widgets  main  schedule  workflow-schedule:repo:schedule:acme:widgets:check  success
   ```
2. **1b — Zero GitHub statuses or check-runs written:** Inspecting `githubsim` state (`GET /_sim/state`) after both green and red scheduled workflow runs showed:
   ```json
   {"statusWrites": 0, "checkWrites": 0, "widgetsChecks": null}
   ```
   `reportCIToGitHub` skips runs with `Event == ci.EventSchedule` (`pkg/orchestrator/orchestrator.go:1006`).
3. **1c — Review task filed on failure only (`review: on-failure`):**
   - **Green run (`ci_run` `1`):** `scheduled_workflow_run` settled with `conclusion=success`, `review_task_id=""`, `review_not_needed=1`. Tasks filed on `acme/widgets`: `0`.
   - **Red run (`ci_run` `2`, `status.txt="fail"`):** `scheduled_workflow_run` settled with `conclusion=failure`, `review_task_id="2"`, `review_not_needed=0`. Task `#2` was filed titled `Review failed "check" workflow run on main` with `origin="schedule:repo:schedule:acme:widgets:check"`.
4. **1d — Pushes to `main` and pull requests run NO grain CI:** Pushing commits to `acme/widgets` `main`, pushing branch `feat/widget-pr`, opening PR `#1` in `githubsim`, and creating an approved task `#1` on `feat/widget-pr` left `SELECT COUNT(*) FROM ci_run WHERE repo='acme/widgets'` unchanged (`1 -> 1` across 6 reconcile ticks). Neither `reconcileBaseBranchCI` nor `reconcileTaskCI` started a grain CI run.
5. **1e — Removing `grain-ci: true`:** Pushing `.grain/schedules/check.yml` without `grain-ci: true` synced `schedule.grain_ci = 0`:
   - Subsequent due cadence firings were dropped (`ci_run` count `2 -> 2`), recording a `scheduled_firing_skip` row with `reason=standingWork` and `detail="this repository uses github CI mode"`.
   - Manual `POST /api/schedules/repo:schedule:acme:widgets:check/run` was refused (`HTTP 400`); after fixing **F1**, it reports:
     ```json
     {"error":"this schedule cannot be run now: this repository uses github CI mode; set `grain-ci: true` on the schedule, or set the repository's CI Mode to \"Use Grain CI\""}
     ```
   - An unstarted `scheduled_workflow_run` row was deleted on the next reconcile tick (`Unstarted scheduled_workflow_run rows remaining after reconcile: 0`) by `startScheduledWorkflowRun` (`!sched.WorkflowRunsUnder(mode)`), while an already-settled historical `scheduled_workflow_run` row stays visible as `LastWorkflowRun` until the schedule itself is undeclared (`forgetUndeclaredWorkflowRun`).

---

### Item 2: Confinement Rules (`walk_items_1_2_3.sh`)

1. **2a — Task `grain ci run-job` and `grain ci retrigger` (`event=push`):**
   - **Before fix (F2):** A task on `acme/widgets` holding a pre-existing `https://grain.dev/capabilities/grain#ci={}` grant could call `POST /api/ci/runs` with `{"taskId":"3","workflow":".grain/workflows/check.yml","jobs":["check"]}` or `{"taskId":"3","recheck":true}` and receive **HTTP 202**, starting a real `push`-event sandbox job! Although `reconcileCI` cancelled long-running jobs (`conclusion=cancelled`, `cancelled_by=ci-mode`), `reconcileCI` runs on `pollInterval` (10s in production), so any fast step completed before cancellation.
   - **After fix (F2):** Both `run-job` and `retrigger` (as well as repo-level `POST /api/ci/runs` when stale `repo_workflow` rows exist) are refused upfront with **HTTP 403** (`grain CI is not enabled for acme/widgets...`). Furthermore, starting an in-flight `push` run under `ci_mode: grain`, forging `requested_by='workflow-schedule:repo:schedule:acme:widgets:check'`, and flipping the repo to `ci_mode: github` proved `reconcileCI` still cancels it (`conclusion=cancelled`, `cancelled_by=ci-mode`) because `scheduledRunKeepsGrainCI` requires `r.Event == ci.EventSchedule`.
2. **2b — Forged `#infra-retry-x` suffix (`workflow-schedule:repo:schedule:acme:widgets:check#infra-retry-x`):** Cancelled by `reconcileCI` (`cancelled_by=ci-mode`, `cancel_reason="CI mode switched away from Grain CI"`): `cutInfraRetry` requires digits after `#infra-retry-`, whereas legitimate `#infra-retry-1` survived (`conclusion=running`).
3. **2c — Cross-repo schedule ID (`workflow-schedule:repo:schedule:acme:other:othercheck` on an `acme/widgets` run):** Cancelled by `reconcileCI` (`cancelled_by=ci-mode`) because `strings.EqualFold(sched.Target.FullName(), r.Repo)` is false (`acme/other != acme/widgets`), even though `acme/other`'s schedule had `grain_ci=1`.
4. **2d — `settings/schedules.yml` task schedule row (`grain_ci=1`, `template_id=""` / non-workflow template):** Cancelled by `reconcileCI` (`cancelled_by=ci-mode`) because `Schedule.WorkflowRunsUnder` checks `IsWorkflowReviewTemplateID(*s.TemplateID)`.
5. **2e — Release-train workflow on `ci_mode: github` does NOT start `promote.yml`:** With `acme/widgets` seeded with `.grain/workflows/promote.yml` (`on: workflow_dispatch`) and a `release` row in `stage='canary'` with `promote_workflow='.grain/workflows/promote.yml'`, calling `POST /api/releases/{id}/advance` (`{"to":"stable"}`) under `ci_mode: github` started **0** `promote.yml` runs (`promote.yml runs under ci_mode: github: 0`). Switching `acme/widgets` to `ci_mode: grain` and advancing a second release immediately started `promote.yml` (`promote.yml runs under ci_mode: grain: 1`, `event=workflow_dispatch`).

---

### Item 3: Docs' "Running a Script on a Cadence Without Waking an Agent" (`walk_items_1_2_3.sh`)

We authored the exact `.grain/workflows/triage.yml` + `.grain/schedules/triage.yml` + `.grain/templates/triage-item.yml` declaration from `docs/schedules.md`:

- **Finding F4 (repo allowlist mismatch):** On `acme/widgets` (`ci_mode: github`), firing the schedule started a `schedule` CI run that immediately errored before running steps:
  ```text
  CI capability "api:tasks:read" refused: api:tasks:read is not offered to acme/widgets's jobs: it reads deployment-wide state, and grain's API is offered to CI for ai-on-gke/grain only
  ```
  `pkg/daemon/capabilities.go:47` wires `ciapi.DynamicProvider(CIAPI, ciapi.Config{})` with empty `Repos`, which defaults to `model.GrainSourceRepo()` (`ai-on-gke/grain`) only (`pkg/capability/ciapi/ciapi.go:93–98`, pinned by `TestAPIGrantsAreGrantedToTrustedSchedulesAndReleasesOnly` in `pkg/daemon/ciapi_test.go`).
- **End-to-end verification on `ai-on-gke/grain` (`ci_mode: github`):** Declaring the same `triage` workflow, `grain-ci: true` schedule, and `triage-item` template on `ai-on-gke/grain` under `ci_mode: github` worked end-to-end:
  - **Quiet run (`work_items.txt` empty):** Step exited 0 (`conclusion=success`), `review_not_needed=1`, **0** tasks filed. Inside the step, probing token confinement confirmed:
    - `POST /api/tasks` (free-form prompt) → `HTTP 403`
    - `GET /api/settings` → `HTTP 403`
    - `POST /api/tasks/from-template` targeting `acme/widgets` → `HTTP 403`
    - `GET /api/tasks` → `HTTP 200`, returning only `ai-on-gke/grain` tasks (`acme/widgets` tasks filtered out by repo-narrowed `tasks:read`).
  - **Work run (`work_items.txt="widget-alpha"`):** Step called `POST /api/tasks/from-template` (`HTTP 201`) and filed task `#8` (`Triage widget-alpha` on `ai-on-gke/grain`).
  - *(Note: under `-host-sandboxes`, credential files placed at `/home/debian/.grain-api/env` land at `$HOME/home/debian/.grain-api/env`, previously tracked as U1 in `docs/test-runs/2026-10-31-ci-restart-reattach.md`.)*

---

### Item 4: Schema 129/130 → 131 Upgrade & State Sync/Check (`walk_item_4_upgrade.sh`)

Executed six automated scenarios (`129→131`, `130→131`, `129→HEAD` × `{SQLite, PostgreSQL 15}`) plus a cross-backend SQLite `state-repo` → PostgreSQL import:

1. **In-place migration:** Seeded `sched-1` (inline task), `sched-2` (template), and `sched-wf` (`system:workflow-review:check`) on `grain-129` and `grain-130`. Starting `grain-131` (and `grain` HEAD, schema 133) migrated every row cleanly with `grain_ci = 0` (`false` on Postgres) and preserved all existing columns.
2. **Two-step rolling upgrade (`grain state prepare-upgrade`):** Running `grain-131 state prepare-upgrade` against live 129 and 130 SQLite and PostgreSQL stores applied additive DDL (`ALTER TABLE schedule ADD COLUMN grain_ci ... DEFAULT 0/ FALSE`) without bumping `schema_migrations` past 129/130; restarting `grain-129` / `grain-130` against the prepared stores succeeded and read/wrote schedules normally before the binary cutover.
3. **`grain state sync` & `grain state check` (`settings/schedules.yml`):**
   - Setting `grain_ci = true` on `sched-wf`, running `grain state sync`, and running `grain state check` exited `0` (`ok: grain can load this`) across all 6 combinations, and importing into a fresh store restored `sched-wf` with `grain_ci = true` and `sched-1`/`sched-2` with `grain_ci = false`.
   - **Finding F3 (fixed):** On unpatched `grain-131`, SQLite `grain state sync` wrote `grain-ci: 0` / `grain-ci: 1` in `settings/schedules.yml` because `"grain-ci"` was missing from `booleanKeys` in `pkg/staterepo/decl.go`, whereas PostgreSQL wrote `grain-ci: false` / `grain-ci: true`. After adding `"grain-ci": true` to `booleanKeys`, `s129_to_head_sqlite` exports `grain-ci: false` / `grain-ci: true` YAML booleans identically to PostgreSQL, while `intFromBool` still accepts `0`/`1` from older state dumps.

---

### Item 5: Scratch Dry-Run of `.grain/templates/weekly-dead-code.yml` (`walk_item_5_dead_code.sh`)

Cloned `/home/debian/work` into `/tmp/deadcode-scratch` and ran every command from `.grain/templates/weekly-dead-code.yml`:

| Check | Command | Output Lines / Findings |
|---|---|---|
| Root Go production graph | `deadcode ./cmd/...` | `316` lines (includes test-only exports & trust-boundary helpers) |
| Root Go `cmd` test graph | `deadcode -test ./cmd/...` | `208` lines |
| Root Go all-packages test graph | `deadcode -test ./...` | `114` lines (`107` in `trustboundary.Classify` packages, `7` outside: `pkg/ci/container.go` `ParseCPU`/`ParseMemory`, `pkg/gitcache` `SetFetchTimeout`, `pkg/github` `SetBaseURL`, `pkg/host` `DialService`, `pkg/sandbox/gce` `RunScript`/`ScriptStepResult`) |
| Root `staticcheck` | `staticcheck -checks U1000 ./...` | `33` lines (`32` in `trustboundary.Classify` packages, `1` test helper `pkg/gitcache/cache_test.go:44` `writeFile`) |
| `grain-plc` Go production / test / `U1000` | `deadcode ./cmd/...`, `deadcode -test ./...`, `staticcheck -checks U1000 ./...` | `5` prod lines (all `pkg/plcconfig` exercised by tests), **`0`** `-test ./...` lines, **`0`** `U1000` lines |
| TypeScript `ui/` | `npx -y knip@5.70.0 --no-progress --reporter compact` | `12` lines (`e2e/grain-Setup.ts`, `src/test-setup.ts`, `playwright`/`lucide-react`, 9 design-token/helper exports) |
| TypeScript `ui-shared/` | `npx -y knip@5.70.0 --no-progress --reporter compact` | `3` lines (`src/test-setup.ts`, `@testing-library/jest-dom`, `jsdom` — allowlisted) |
| TypeScript `ui-hub/` (**F5a**) | `npx -y knip@5.70.0 --no-progress --reporter compact` | `5` lines (`src/test-setup.ts`, unused devDep `@testing-library/user-event`, 3 unlisted `@grain/ui-shared` workspace imports) |
| Allowlist audit | `.grain/baselines/dead-code-allowlist.txt` | **21 / 21** entries resolve to live packages/files (`0` stale entries) |
| Shell / workflow / `.grain/` scripts | Cross-reference `scripts/` & `.grain/` | **`0`** unreferenced scripts or orphaned templates |

- **Finding F5a:** `.grain/templates/weekly-dead-code.yml` line 35 still says `If ui/, ui-shared/, or ui-registry/ has a package.json` instead of `ui-hub/` (renamed from `ui-registry/` in PR #994 / #1007), leaving `ui-hub/` unscanned on weekly runs. Because `.grain/triggers/grain-declarations.yml` has `human: required` (which would block `auto_merge` on this PR), this template update is filed via `propose_task`.

---

### Item 6: Audit & Live `fake-agy` Execution of `nothing_needed` Finishes (`walk_item_6_nothing_needed.sh`)

1. **Static audit of `.grain/schedules/*.yml` and `.grain/templates/*.yml`:**
   - Every task-filing schedule in `.grain/schedules/` uses `overlap: skip` (explicitly or by default) and sets no `nothing-needed: ack` override (`nothing_needed_action == ""`, which closes the task immediately):
     1. `release-train` (`.grain/templates/release-train.yml`) — instructs `nothing_needed` on green soak / waiting gates / quiet cycles.
     2. `release-watchdog` (`.grain/templates/release-watchdog.yml`) — instructs `nothing_needed` when healthy or duplicate task exists.
     3. `release-process-review` (`.grain/templates/release-process-review.yml`) — instructs `nothing_needed` when zero incidents occurred.
     4. `prune-packages` (`.grain/templates/prune-packages.yml`) — instructs `nothing_needed` when under cap or dry-run.
     5. `nightly-security-review` (`.grain/templates/nightly-security-review.yml`) — instructs `nothing_needed` when zero security-relevant commits landed (*Note **F5b**: line 84 still says `repo: bwsalmon/grain` instead of `repo: "ai-on-gke/grain"`; included in the `.grain/templates/` follow-up proposal*).
     6. `nightly-features-test-plan` (`.grain/templates/nightly-features-test-plan.yml`) — instructs `nothing_needed` when zero user-visible changes landed.
     7. `weekly-dead-code` (`.grain/templates/weekly-dead-code.yml`) — instructs `nothing_needed` when nothing safe to delete and allowlist clean.
     8. `ui-walkthrough-review` (`.grain/templates/ui-walkthrough-review.yml`) — instructs `nothing_needed` only when Playwright cannot launch at all; normal findings use `comment_on_issue` + `propose_task`.
     9. `update-agent-clients` (`.grain/templates/update-agent-clients.yml`) — instructs `nothing_needed` when pinned CLIs already match npm `latest`.
2. **Live `fake-agy` execution against `grain daemon`:**
   - Configured a `fake-agy` binary that parses `$HOME/.gemini/config/mcp_config.json` and calls the live `grain-sandbox` MCP server over JSON-RPC (`initialize` → `tools/call` `nothing_needed`).
   - Fired all **9** schedules via `POST /api/schedules/{id}/run` (`HTTP 201`), let the real orchestrator dispatch each task in a host sandbox, verified every task transitioned directly to `state=closed` (with the `nothing_needed` reason recorded as its closing comment and `Suppressed == nil` on the schedule), and verified an immediate second `POST /api/schedules/{id}/run` succeeded with **`HTTP 201`** (not blocked by `overlap: skip`).
   - **Contrast control (`comment_on_issue`):** Firing `weekly-dead-code` with `fake-agy` calling `comment_on_issue` instead of `nothing_needed` left the task in `state=awaiting_reply`, and an immediate second `POST /api/schedules/{id}/run` was refused with **`HTTP 409`** (`cannot run schedule now: #19 from the previous firing is still open (awaiting_reply)`).

---

## 4. Fixes Landed in This Branch & Security Review Impact

1. **F1 — Separate workflow CI-mode suppression from standing-work suppression (`pkg/model/schedule_fire.go`, `pkg/model/schedule_suppression.go`, `pkg/orchestrator/schedule.go`, `pkg/ui/schedules.go`):**
   - Added `FireScheduleResult.WorkflowGrainCIOff` (`*CIMode`), `WorkflowGrainCIOffReason(mode)`, and `Store.workflowGrainCIOff` (scoped strictly to workflow schedules via `workflowReviewSchedule`).
   - `RunScheduleNow` (`POST /api/schedules/{id}/run`), `SuppressedFiring` (`GET /api/schedules`), and `reconcileSchedule` logs/skip details now state `"this repository uses github CI mode; set grain-ci: true on the schedule, or set the repository's CI Mode to \"Use Grain CI\""` (or `"this repository has CI disabled (ci_mode: none)"`) instead of falsely telling the operator that standing work is disabled.
   - **Regression tests:** `TestWorkflowGrainCIOffSuppressionReportsCIModeRatherThanStandingWork` (`pkg/orchestrator/scheduledworkflow_test.go`) and `TestRunScheduleNowReportsCIModeRefusalRatherThanStandingWorkOff` (`pkg/ui/workflow_schedule_test.go`).
2. **F2 — Refuse narrowed (`run-job`) and `recheck` (`retrigger`) task CI starts upfront on `ci_mode: github` / `none` (`pkg/ui/ci.go`):**
   - Moved the `explicitDisabled` (`CIModeGitHub` / `CIModeNone` when merge rules do not require Grain CI) refusal in `StartTaskCIRunOpt` ahead of `if full`, and aligned `ciEnabledFor` so repo-level `POST /api/ci/runs` cannot bypass `CIModeGitHub`/`CIModeNone` via stale `repo_workflow` rows. `reconcileCI` remains the async sweep backstop for live mode switches.
   - **Regression test:** `TestStartTaskCIRunOptRefusesFullSuiteWhenRepoUsesGitHubCIMode` (`pkg/ui/ci_test.go`).
3. **F3 — Export `schedule.grain_ci` as a YAML boolean on SQLite (`pkg/staterepo/decl.go`):**
   - Added `"grain-ci": true` to `booleanKeys` so SQLite `grain state sync` writes `grain-ci: false` / `grain-ci: true` in `settings/schedules.yml`, matching PostgreSQL and `docs/schedules.md` while keeping `0`/`1` imports backwards-compatible.
   - **Regression test:** `TestScheduleGrainCIRoundTripsAsYAMLBooleanInSettingsSchedules` (`pkg/staterepo/decl_test.go`).
   - **Security review note:** Neither `pkg/ci/*` nor `.github/workflows/*` was modified. Touching `pkg/staterepo/decl.go` triggers the automated `state-repo` review trigger (`.grain/triggers/state-repo.yml`, `on_findings: hold`), which re-enables `auto_merge` automatically once clean.
4. **F4 (docs half) — Document `ciapi` repository confinement (`docs/schedules.md`, `docs/declarations.md`, `docs/agent-api.md`):**
   - Clarified in `docs/schedules.md` and `docs/declarations.md` that CI `api:*` grants (`api:tasks:read`, `api:tasks:from-template`) are offered only to repositories `ciapi` is configured for (`ai-on-gke/grain` by default), and updated retired `bwsalmon/grain` references in `docs/agent-api.md` to `ai-on-gke/grain`.

---

## 5. Follow-up Tasks Proposed (`propose_task`)

1. **Allow operator-configured repositories to opt into repo-narrowed CI `api:*` grants (`api:tasks:read`, `api:tasks:from-template`):** Extends `pkg/capability/ciapi` / repository settings so external `ci_mode: github` repositories can adopt the documented "Running a script on a cadence without waking an agent" pattern without relaxing `TestAPIGrantsAreGrantedToTrustedSchedulesAndReleasesOnly` by default.
2. **Update `.grain/templates/weekly-dead-code.yml` (`ui-registry/` → `ui-hub/`) and `.grain/templates/nightly-security-review.yml` (`bwsalmon/grain` → `ai-on-gke/grain`):** Split out into a dedicated `.grain/templates/` task so `.grain/triggers/grain-declarations.yml` (`human: required`) does not block unattended `auto_merge` of the production fixes in this PR.
