# Walkthrough Report: Release-Train Schedules Held While Publishing Is Held (2026-11-15)

Plan: [`docs/test-plans/2026-11-15.md`](../test-plans/2026-11-15.md) §5 (PR #1513, [Task #4444](/tasks/4444)). Walked by [Task #5917](/tasks/5917), filed by [Task #5885](/tasks/5885).

## Result

| # | Item | SQLite | PostgreSQL | Notes |
|---|---|---|---|---|
| 1 | Gated `baseBranchPublishing` on `main` with an open release triage task, produced from its real input | pass | pass | Three failed `release.yml` push runs on `main` in the GitHub sim. `recordGitHubBaseBranchRelease` wrote the row (`failures: 3`) and filed triage task 2. `/api/sandboxes` reported `gated: true, triageTaskID: "2"`. |
| 1 | Neither schedule files a task while held; Stage 1 ticks dropped and recorded | pass | pass | `release-train` was three cadences overdue and stayed held: one `publishingHeld` skip row, `next_run_at` unchanged, no task over ~45 reconcile cycles. Two `nightly.yml` runs on `v3.9.0-rc.1`/`rc.2` left two `dropped` skip rows naming task 2. |
| 2 | Triage closed: cadence fires **once**; Stage 1 resumes | pass | pass | Exactly one release-train task (5). The skip row resolved to `outcome: fired, firedTaskId: 5, folded: 3`, and `next_run_at` moved to the next grid slot. After the group's tasks closed, the next Stage 1 run filed task 7. |
| 2 | Overlap group `release-train` (`skip: true`) vs. the post-hold firing | pass | pass | A Stage 1 run concluded in the same cycle that triage task 2 closed. The cadence fired (task 5) and the Stage 1 tick was dropped with `reason: task, task_id: 5, held_by_schedule_id: …:release-train`. No second task was filed. The held tick did not count as running. |
| 3 | Publishing held on `main` does not hold a `release/v3.5.1` schedule | pass | pass | `walk-release-v351` (unbound template, `base: release/v3.5.1`) fired task 3 while `main` was held. See O1 for the template-bound variant, which is held by design. |
| 4 | Visibility: `/next/` schedule pages | pass (with gaps) | — | `/next/processes/schedules` detail says `Suppressed (publishingHeld): skipped: publishing held by task 2 (…) — Open task #2`, and both schedules list their skips. Gaps are listed under O3. |
| 4 | Visibility: `grain health` shows the gated finding | **fail → fixed (F1)** | **fail → fixed (F1)** | It printed `no counters: nothing on this daemon registers any` while the finding was gated. It now prints a `WARNING: base branches publishing nothing:` section naming the holding triage task. |
| 4 | Visibility: `/next/system` | **gap (F2, not fixed here)** | — | `/next/system` redirects to `/next/fleet`, which never mentions `baseBranchPublishing`. Only classic System → Health (`/system/health`) shows it ("Nothing is publishing from acme/grain main"). |

## Environment

- **Commit walked:** `c6743dc5d` (`origin/main` at dispatch). The fix commit is on this branch.
- **Daemon:** a real `grain daemon -sandbox-port-base=0 -host-sandboxes -poll-interval 1s` (the reconcile cadence, shortened), built with `make frontend && go build ./cmd/grain`, Go 1.26.10.
  - Stores: SQLite 3.40.1, and PostgreSQL 15.20 (Debian 15.20-0+deb12u1).
  - Dispatch paused with `POST /api/pipeline-pause`, so filed tasks stay open and nothing but the schedules acts on them. Schedules fire through a pipeline pause.
- **GitHub sim:** `pkg/github/githubsim` via [`ghsim.go`](2026-11-15-release-train-publishing-held/ghsim.go), adapted from `2026-11-13-grain-health-exit-codes/`. It serves `acme/grain` (`ci_mode: github`, declarations on).
  - The sim's repository is this checkout's own `.grain/`, `.github/workflows/` and `README.md` (`git archive HEAD`), unmodified. Two walkthrough-only declarations are added for item 3: `walk-release-v351.yml` with an unbound `walk-release-v351` template, and `walk-release-v351-template.yml`, which fires `release-train`.
  - The sim has a `release/v3.5.1` branch. GitHub Actions runs reach the daemon through its polling fallback (`/_sim/seed-actions`).
- **Shortened schedule cadence:** declarations refuse `every:` under 10 minutes (`everyNMinutes must be at least 10`), so `release-train.yml` keeps its committed 20 minutes. Missed ticks are made by moving `next_run_at` 65 minutes into the past (three missed ticks). Only that clock edge is edited, directly in the store.
- **Browser:** Playwright Chromium (`chromium-1234`), Node 20.19.0, 1440 px desktop and 390 px phone, light theme.
- **Harness:** [`env.sh`](2026-11-15-release-train-publishing-held/env.sh), [`run_walkthrough.sh`](2026-11-15-release-train-publishing-held/run_walkthrough.sh) (`STORE=sqlite|postgres`; `PAUSE_FOR_SCREENSHOTS=1` stops while held), and [`pagetext.mjs`](2026-11-15-release-train-publishing-held/pagetext.mjs), which prints a page's text for checking what a screenshot says. Captures for each store are under [`captures/`](2026-11-15-release-train-publishing-held/captures/) and contact sheets under [`screenshots/`](2026-11-15-release-train-publishing-held/screenshots/).

## Steps and outcomes

Both stores gave the same rows. The numbers below are from the SQLite run (`captures/sqlite/*.txt`); `captures/postgres/` matches row for row.

1. **0. Setup.** Pause dispatch and put `acme/grain` on `ci_mode: github`.
2. **1a. Produce the finding through its real input.** Seed three failed `Release` (`.github/workflows/release.yml`, `event: push`, `headBranch: main`) runs, created 30, 20 and 10 minutes ago.
   - Daemon log: `filed 2 to triage failed release on acme/grain main at 734a55c4b919 (failure)`.
   - `GET /api/sandboxes` `.baseBranchPublishing` reported `failures 3, gated true, triageTaskID "2"`, with "…Further releases on this branch are held pending resolution of triage task 2." (`1a-finding.json`)
   - `grain health` printed only `no counters: nothing on this daemon registers any` (`1a-health.txt`): **F1**.
3. **1b. Turn declarations on**, so the schedules arrive with the hold already in force. `acme/grain at … declares 26 template(s), 3 suite(s), 18 schedule(s), 5 trigger(s)`.
   - `release-train` is `everyNMinutes 20, overlap skip, overlap_group release-train, skip_when publishing-held`.
   - `release-train-on-stage1` is `github_workflow_run nightly.yml on v*-rc.*`, with the same group and the same `skip_when`.
4. **1c. Hold.** Backdate `release-train`, `walk-release-v351` and `walk-release-v351-template` to 65 minutes ago. Deliver `Nightly` runs on `v3.9.0-rc.1` (success) and `v3.9.0-rc.2` (failure), then let the daemon reconcile about 45 more times (`1c-held.txt`):
   - `release-train`: one skip row (`occurrence 06:17:25, publishingHeld, task 2`, no outcome yet), `next_run_at` still `06:17:25`, no task filed. Journal: `…the 2026-11-15T06:17:25Z firing is held: it fires once as soon as that clears`.
   - `release-train-on-stage1`: two skip rows, `publishingHeld, task 2, dropped`, one per run. No task filed.
   - `walk-release-v351` (`release/v3.5.1`) fired task 3, `Walkthrough tick on release/v3.5.1`: item 3.
   - `walk-release-v351-template`: held like `release-train` (O1).
   - The red `rc.2` Stage 1 run filed task 4 (`Fix the red Stage 1 (Nightly)`). That is `stage1repair.go`, which the trigger declaration opts into, not the schedule. By step 2a it was no longer open (the green `rc.3` run came after it).
5. **Screenshots while held:** see Visibility below.
6. **2a. Release, racing a Stage 1 tick.** Seed `Nightly` on `v3.9.0-rc.3` (success) and close triage task 2 at the same moment (`2a-released.txt`):
   - `release-train` fired **once**, as task 5. The skip row became `fired, fired_task_id 5` (the API also reports `folded: 3`), and `next_run_at` became `07:37:25`, the next slot on its grid.
   - The `rc.3` Stage 1 tick was dropped by the overlap group: `reason task, task_id 5, outcome dropped, held_by_schedule_id repo:schedule:acme:grain:release-train`.
   - `grain health` (after the fix): `acme/grain main (not held): …` (`captures/sqlite/2a-health-after-fix.txt`).
7. **2b. Settle.** About 20 more cycles: still exactly one release-train task, and no new skip rows (`2b-settled.txt`).
8. **2c. Stage 1 resumes.** Close every open task but the director's, then seed `Nightly` on `v3.9.0-rc.4` (success). `release-train-on-stage1` filed task 7 and its `last_run_at` was set (`2c-resumed.txt`).

### Visibility (held state; contact sheets attached to the closing note)

- `/next/processes/schedules` → **Release train** (`screenshots/rt-contact-sheet.png`): `Suppressed (publishingHeld): skipped: publishing held by task 2 (acme/grain main has published nothing since 2026-11-15 06:56 UTC) — Open task #2`. Under "Recent fired tasks & skips" it lists `skipped at … publishing held by task 2 (…) #2`.
- `/next/processes/schedules` → **Release train (Stage 1 verdict)** (`s1-contact-sheet.png`): both dropped ticks are listed with `#2`.
- `/next/processes/schedules` → **Walkthrough release/v3.5.1 tick** (`v351-contact-sheet.png`): not suppressed, `Last:` set by its firing.
- After release (`rt-after-contact-sheet.png`): not suppressed. `Next: 07:41:57Z`, `Last: 07:32:00Z`.
- `/next/fleet` and classic `/system/health` (`sys-contact-sheet.png`):
  - `/next/fleet` never says that publishing is held (F2).
  - Classic System → Health shows the warning "Nothing is publishing from acme/grain main … held pending resolution of triage task 2."

The text each page rendered is in `captures/text-*.txt`, read through `pagetext.mjs`, because the sandbox cannot view PNGs and has no Gemini key for `LOOK=1`.

## Findings

### F1: `grain health` said "no counters" over a gated publishing finding (fixed)

- **Symptom:** with `/api/sandboxes` carrying `baseBranchPublishing: [{repo: acme/grain, branch: main, gated: true, triageTaskID: "2"}]`, `grain health` printed only `no counters: nothing on this daemon registers any` and exited 0. That is true on both stores (`captures/*/1a-health.txt`).
  - `pkg/cli/health.go`'s printer has no section for `BaseBranchPublishing`, so the one CLI health reading said nothing while the release train was held.
  - This is the same class of bug as `2026-11-04-capabilities.md` F7 and `2026-11-13-grain-health-exit-codes.md` F1.
- **Deterministic reproduction:** `STORE=sqlite ./run_walkthrough.sh` step 1a. Or, offline, `(&printer{}).health(ui.SandboxHealthResponse{BaseBranchPublishing: …})`.
- **Fix:** `grain health` prints `WARNING: base branches publishing nothing:`, one line per branch: `<repo> <branch> (held by release triage task N | not held): <detail>`.
  - It is a WARNING, matching classic System → Health's `severity="warning"` (a failed release holds no merge), so the exit code stays 0.
  - `-json` already carried the field.
- **Regression tests:** `TestGrainHealthNamesBaseBranchesPublishingNothing`, plus a `publishing held warning only` case (exit 0, no `no counters`) in `TestGrainHealthExitsOneWhenACheckIsFailing`. Both are in `pkg/cli/health_test.go`, beside the printer. Verified live after the fix: `captures/sqlite/1a-health-after-fix.txt` and `2a-health-after-fix.txt`.

The `skip-when` implementation itself (`pkg/model/schedule_publishing_hold.go`) behaved as documented everywhere this walkthrough looked, so no test was added beside it.

### F2: `/next/` has no surface for `baseBranchPublishing` (not fixed here; follow-up proposed)

`ui-ng/` has no reader of `baseBranchPublishing` at all. `/next/system` redirects to `/next/fleet`, and neither says that publishing from `main` is held, or by which task. Classic `/system/health` (`BaseBranchPublishingAlerts`) is the only page that does. Adding it is a new `ui-ng/` feature under the design book's review mandate, so it is proposed as its own task rather than landed in a walkthrough.

### Observations (behaving as designed, or minor)

- **O1, a bound template's base wins.** `.grain/templates/release-train.yml` binds `base: main`, and a bound template beats the schedule's `base:` (`placementOf`). So a schedule that fires `release-train` with `base: release/v3.5.1` still files into `main` and is, correctly, held by `main`'s finding. The skip detail names `acme/grain main`, which is honest. Item 3 holds for a schedule whose firing really goes into `release/v3.5.1`.
- **O2, nothing in the journal for event-trigger drops.** `fireOneGitHubWorkflowRunSchedule` records its `publishingHeld` and overlap-group drops as skip rows only, with no journal line. The cadence schedule's hold is logged. The rows are visible on `/next/` and `/api/schedules`.
- **O3, minor `/next/` schedule-detail gaps:**
  - It shows the raw reason code (`Suppressed (publishingHeld)`).
  - It does not show the `release-train` overlap group or `skip-when`.
  - A skip row shows its summary but not its outcome (`fired` → `#5`, `folded: 3`), so after the hold the page still reads only "skipped … held by task 2".
  - An event trigger shows a `Next:` a year out (`2027-11-16…`, the trigger's sentinel).
  - The `release/v3.5.1` schedule's `Repo:` line omits its base.
- **O4, declarations refuse `every:` under 10 minutes**, hence the store-backdated ticks described above.

## Fix commits

- `fix(cli): grain health names base branches publishing nothing`: `pkg/cli/health.go` and `pkg/cli/health_test.go`, together with this report and its harness.
