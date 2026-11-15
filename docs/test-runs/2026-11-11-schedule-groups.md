# Test run: schedule overlap groups, stale schedules and starved firings (§10)

- **Plan:** [`docs/test-plans/2026-11-11.md`](../test-plans/2026-11-11.md) §10, items 1–4 of "What is NOT proved".
- **Features:** PRs #1074 (overlap groups, `HeldByScheduleID`, `schedule-group:` tags), #1182 / #1213 (`Store.StaleSchedules`, `grain health` FAILING, the System → Health and CI pane alert), #1127 (starved held occurrences raised).
- **Run:** task 4523, 2026-11-11, branch `grain/098073e4/task-4523-walkthrough-schedule-overlap` on `main` at `710b7fd53`.
- **Helpers:** [`2026-11-11-schedule-groups/`](2026-11-11-schedule-groups/): `env.sh`, `helpers.sh`, `githubsim_server.go` (`//go:build ignore`, copied from the 2026-11-10 grain-ci run), `walk_items_1_3.sh`, `walk_item_4_upgrade.sh` and `stale_alert.mjs`. Their output is under `out/`.

## Environment

- Built from `HEAD`: `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`. `githubsim_server` was built from the helper.
- `env.sh` starts `githubsim_server` on `127.0.0.1:4260`. It serves `acme/widgets` and `acme/other` over real `git http-backend`.
- It then starts a real `grain daemon -sandbox-port-base=0 -host-sandboxes -ci -reconcile -poll-interval 1s` over SQLite on `127.0.0.1:4261`, with the simulator as its GitHub host.
- Both repositories are `ci_mode: github` with declarations on. The pipeline is paused (`POST /api/pipeline-pause`), so every task a schedule files stays **queued with no run**. That is the never-started blocker the starved rule is about.
  - Without the pause, the fake agent fails setup and each task picks up runs, which hides starvation.
- `acme/widgets` declares one group, `train`, across three kinds of schedule:
  - `a-task.yml`: template `nightly`, `overlap: {skip: true, group: train}`.
  - `b-suite.yml`: suite `sweep`, `base: main`, `overlap-group: train`.
  - `c-workflow.yml`: workflow `check`, `grain-ci: true`, `overlap: {skip: true, group: train}`.
  - `.grain/workflows/live.yml` has a daily cron and no `grain-ci: true` entry. `ci_mode: github` drops it at every firing.
- `acme/other` declares `tidy.yml` in its own group, also named `train`.
- **Cadence:** "firing every minute" cannot be declared. `every:` refuses anything under 10 minutes (`everyNMinutes must be at least 10`), and a workflow cron must be daily, weekly or monthly to become a schedule.
  - So every schedule is `every: {minutes: 10}`, and each step pulls `next_run_at` forward in SQL (`helpers.sh`'s `due`). The **real reconciler** fires it on its next 1 s tick.
  - `faketime` was tried for item 4 and does not work: libfaketime does not reach a Go binary's clock, because `time.Now` reads the vDSO. The daemon logged real time throughout.
- The upgrade (item 4) used a second environment on ports 4262/4263, starting with a binary built from `6c0df5d19^` (schema 131).

## Item 1: one group across task, suite and workflow schedules

`walk_items_1_3.sh` on the fixed build, output in [`out/walk_items_1_3.txt`](2026-11-11-schedule-groups/out/walk_items_1_3.txt):

| Step | What happened | Result |
|---|---|---|
| 1a | All three schedules due at once. | `a-task` filed #3 (`schedule-group:acme/widgets:train`). `b-suite` and `c-workflow` were held, with `held_by_schedule_id = a-task` and the detail naming it. `acme/other`'s `tidy` filed #2 in the same tick: its group is separate. ✅ |
| 1b | #3 closed. | `b-suite`'s held occurrence fired suite run 1. `c-workflow` was held with reason `suiteRun`, by `b-suite`. A due `a-task` was held the same way. ✅ |
| 1b′ | `a-task` pushed out and the suite task closed. | `c-workflow`'s held occurrence fired a grain CI run (`.grain/workflows/check.yml`, slowed to 150 s). A due `a-task` was held with reason `workflowRun`, by `c-workflow`. ✅ |
| 1c | `b-suite.yml` withdrawn (`git rm`, push, sync) while suite run 1 was active; then `a-task` and `c-workflow` came due. | **Bug F1 before the fix:** `a-task` filed #5 beside the still-active suite run (two unfinished at once). **After the fix,** both were held by suite task #4 and named the withdrawn `b-suite`. ✅ |
| 1d | Suite task closed. | Suite run finished. `a-task` fired, then `c-workflow` was held by it. ✅ |
| 1e | `c-workflow.yml` renamed to `c2-workflow.yml` while its CI run was in flight. | The run was cancelled when the schedule row went, so nothing was left to hold the group, and `a-task` fired. That is consistent: there was no unfinished work. The cancel reason was wrong (**F3**). |

Steps 1b′ and 1e were walked by hand on the pre-fix build, with the same `helpers.sh` queries; their output is quoted from that session rather than kept in `out/`. At no point after the fix were two firings of one group unfinished at once. Task renames and withdrawals were already covered by the tag (`TestAWithdrawnSchedulesOpenTaskStillHoldsItsGroup`).

## Item 2: stale detection end to end

- Before the fix ([`out/stale_before.json`](2026-11-11-schedule-groups/out/stale_before.json), [`out/health_before.txt`](2026-11-11-schedule-groups/out/health_before.txt)):
  - `workflow.live` was backdated three days, because a daily cadence needs 48 h. Its next firing was dropped (`next_run_at` moved on, `last_run_at` stayed empty).
  - `GET /api/sandboxes?section=staleSchedules` lists it, with the ci_mode reason and the fix.
  - `grain health` prints it under `FAILING: schedules with no run in more than twice their cadence` with the `why:` line, but **exits 0** (**F4**).
- **Clearing with `grain-ci: true`:** following the alert's advice and declaring `.grain/schedules/live.yml` (`workflow: live`, `grain-ci: true`) ran the workflow green on its next firing. **Bug F2 before the fix:** the alert stayed up for good. `workflow.live`, the cron's own schedule, is still enabled and still dropped. After the fix it clears as soon as the `grain-ci` entry exists ([`out/stale_after_grain_ci.json`](2026-11-11-schedule-groups/out/stale_after_grain_ci.json)).
- **Held schedules:** `c-workflow` sat held behind `a-task` past twice its cadence and showed on Health with no reason. After the fix it reads `held: the task schedule "a-task" (…) filed is still open, and both are in overlap group "train"`.
- **UI** (`stale_alert.mjs` against the real daemon, signed in with the access code; [`out/stale_alert_before.txt`](2026-11-11-schedule-groups/out/stale_alert_before.txt)):
  - System → Health and `/ci` at 1440×900 and 390×844, light and dark: the alert is shown with both rows and the reason under each name.
  - In all eight renders, every row sits inside the alert box and the page has no horizontal overflow.
  - Once the group released and `c-workflow` fired, all eight renders show no alert ([`out/stale_alert_cleared.txt`](2026-11-11-schedule-groups/out/stale_alert_cleared.txt)). `grain health` then printed no FAILING section.
- **Readers:**
  - A `gma_` token with `tasks:read`, `repos:read` and `ci:read` (no `deployment:read`) gets **403** on the stale route. The CI pane, replaying that 403, shows no banner.
  - The page half is now pinned in `ui/e2e/stale-schedules.spec.js` ("a reader without deployment:read sees no stale-schedules banner").

## Item 3: starved firings raised and cleared

- `b-suite`'s held occurrence was set 25 minutes back, which makes three occurrences behind never-started #3. Another queued task was at priority 7.
- On the next tick #3 was raised from 5 to 7. A `pipeline.schedule-starved:repo:schedule:acme:widgets:b-suite` warning was raised, and the daemon logged `held 25m0s (3 occurrences) by task 3, which has never started; priority 5 -> 7`. ✅
- **Bug F5 before the fix:** the title and body said `#3 from its previous firing has never started`. #3 was `a-task`'s, not `b-suite`'s. They now say `#3 from schedule repo:schedule:acme:widgets:a-task's firing in overlap group "train"`.
- **Clearing:** closing #3 cleared the notification (`cleared_by = condition`) in the same tick. The notification clears when the blocker *closes*, as `EscalateStarvedScheduleBlocker` documents, not when it starts (the plan's wording).
  - A blocker that has started is no longer escalated (`len(runs) > 0`), but its notification stays until the task closes.
- **Gap (filed):** escalation only covers a blocker that is an open *task* (`Reason == SuppressedByOpenTask`). A group held by a suite run whose task never started, or by a workflow run, is never raised.

## Item 4: upgrade 131 → HEAD with existing schedules

`walk_item_4_upgrade.sh`, output in [`out/walk_item_4_upgrade.txt`](2026-11-11-schedule-groups/out/walk_item_4_upgrade.txt):

- The schema-131 build (`6c0df5d19^`) declared the same repositories without group fields (`overlap: skip`).
- All four schedules fired: tasks #2 and #3 (with only their `schedule:` tag), suite run 1 and a green CI run.
- The HEAD binary over the same data dir logged `migrated the store in place from schema 131 to 141`. Every schedule has `overlap_group = ''`.
- Due again:
  - `a-task` and `tidy` were held by their own open tasks.
  - `b-suite` was held by its own suite run.
  - `held_by_schedule_id` was empty on every skip.
  - `c-workflow`, whose run had finished, fired a second run.
- Behaviour is unchanged and there is no group. ✅

## Findings and outcomes

| # | Finding | Repro | Outcome |
|---|---|---|---|
| F1 | A suite schedule withdrawn or renamed while its run is active stops holding its overlap group, so a peer fires on top of the run. | Item 1c. | **Fixed:** scheduled suite-run tasks carry the group tag in every pass, and the sweep names the run's schedule. `pkg/model` `TestAWithdrawnSuiteSchedulesRunStillHoldsItsGroup`. |
| F2 | Declaring the `grain-ci: true` entry the stale alert asks for never clears the alert: the workflow's dropped cron schedule stays stale for good. | Item 2. | **Fixed:** `StaleSchedules` leaves out a dropped workflow cron that another enabled schedule runs under the repo's CI mode on the same branch. `TestAGrainCIScheduleForTheWorkflowClearsItsDroppedCron`. |
| F2b | A schedule held by its overlap group past twice its cadence was listed with no reason. | Item 2. | **Fixed:** the reason is `held: <holder detail>`. `TestAStaleScheduleHeldByItsGroupSaysByWhom`. |
| F3 | A `grain-ci: true` schedule renamed or withdrawn mid-run has its run cancelled as "repository switched to GitHub CI". | Item 1e. | **Fixed:** the reason names the schedule that went. `pkg/orchestrator` `TestAWithdrawnGrainCIScheduleSaysWhyItsRunWasCancelled`. |
| F4 | `grain health` exits 0 with FAILING sections (all of them, not only stale schedules); the plan expected non-zero. | Item 2. | **Filed:** changing the exit status of an existing command is a compatibility decision. |
| F5 | The starved notification blamed "its previous firing" for a peer's task. | Item 3. | **Fixed:** names the holder and group. `TestAStarvedGroupFiringNamesTheHolder`. |
| F6 | Starved escalation ignores holders that are suite runs or workflow runs. | Item 3. | **Filed.** |
| F7 | `GET /api/sandboxes?section=staleSchedules` answers `{"staleSchedules":null}` when none. A `.grain/schedules` workflow entry is titled "Review last night's "check" workflow run" in the alert rather than its file name. | Item 2. | Noted only: the UI copes with `null`, and the title is cosmetic. |
| — | The plan's "every minute" is not declarable (10-minute floor), and `faketime` does not move a Go daemon's clock. | Environment. | Worked around in SQL, as described above. |

## Screenshots

`screenshot.mjs --url --code` against the real daemon in the stale state: `/system/health` and `/ci` at desktop/phone in light/dark. The contact sheet is attached to the task's closing note. No `ui/src` file changed, so there is no before/after pair: the only `ui/` change is the e2e spec.
