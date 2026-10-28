# Stale-copy save refusal, skipped firings and the retired memory agent (2026-10-15)

Task 3947, filed by the nightly test-plan task 3749: the walkthrough in §4 of
[`test-plans/2026-10-15.md`](../test-plans/2026-10-15.md), run against a real
`grain demo` and a real `grain daemon`.

## Commits under test

All seven are on `origin/candidate` and `origin/main` (`git branch -r --contains`):
`9d55be291`, `f98d67b43`, `3112fd9a9`, `66e6f170b`, `9c86d191f`, `987eb8575`,
`babea026e`. Branch base: `591f0f35f`, an ancestor of `origin/candidate`.

## Environment

- **Browser steps (1–2).**
  - `go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false -reconcile 2s` (built
    as `/tmp/grain`). The UI was built first with `cd ui && npm ci && npm run build`.
  - Playwright Chromium, installed with `NODE_EXTRA_CA_CERTS` set to grain's CA.
    Without it the browser download fails with `SELF_SIGNED_CERT_IN_CHAIN`.
  - The browser walk is the new spec
    `ui/e2e/stale-save-two-contexts-walkthrough.spec.js`. It opens two
    `browser.newContext()`s, so two sessions rather than two tabs of one, on a
    suite and a template. It runs against the per-worker `grain demo` that
    `e2e/fixtures.js` starts.
  - The API checks for step 2 ran against the hand-started `-reconcile 2s` demo.
- **Readiness.** Every server was polled on `/api/sandboxes` before use.
- **Skip-history steps (3–5).** The demo logged no schedule firing in its
  `-reconcile 2s` run (`grep -c schedule` on its log: 0), so these steps used a
  real `grain daemon`:
  `-data-dir /tmp/gd/data -ui-addr 127.0.0.1:4343 -poll-interval 5s -native-sandboxes=false -ci=false -agent-framework claude -claude-path /tmp/fake-claude -default-target-repo acme/widgets`.
  It used its own embedded SQLite store and its own local state repository.
  - **The fake agent.** The repository has no fake agent binary. As in the
    2026-10-14 run-hosts report, a shell stub (`/tmp/fake-claude`) holds until a
    release file appears. It was never invoked. The daemon has no model backend
    configured, so it reports `0 ordinary agent run(s) at once` and never
    dispatches. Each task the schedule filed therefore stayed open
    (`awaiting_reply`), which is the "task left open" the plan asks for.
  - **The clock.** grain's shortest cadence is `everyNHours: 1`, so "every
    minute" cannot be configured. Each "wait for later occurrences" step
    instead set the schedule's `next_run_at` hours into the past in the store
    (`sqlite3 … update schedule set next_run_at=…`) while the daemon ran. Every
    firing, skip, fold and resolution after that was the daemon's own
    reconciler, on its own 5s cycle.

## Stubs

| Boundary | Stub |
|---|---|
| Forge (GitHub) | none needed: the daemon never dispatched, and `acme/widgets` was never cloned. |
| Model / agent | no backend configured. `/tmp/fake-claude` (a hold-until-released shell script) was set as the claude path but never reached. |
| Time | `next_run_at` backdated in the daemon's SQLite store, as described above. |

No grain API, React component or store method was mocked.

## Steps and outcomes

1. **Two browser contexts on a suite and on a template.** Pass.
   - *Suite.* Context A raised Max passes 2→4 and saved. Context B, opened on 2,
     ticked Require approval and saved. B's pane stayed open (heading "Edit
     suite") with the box still ticked and Max passes still 2. It showed the
     warning "…changed by somebody else… Your changes here were not saved;
     Reload replaces them with the saved copy." The server kept `maxPasses: 4`
     and `requireApproval: false`.
   - *Suite, Reload.* The notice went away and the pane showed 4 with the box
     unticked: nothing from the stale copy was kept. A save made after that
     went through, and A's 4 was kept.
   - *Template.* A retitled the template; B, opened on the old title, rewrote
     the description. B was refused the same way and kept "second" in its
     description box. Reload showed A's title and the saved description
     "first". A save made after that applied, again keeping A's title.
2. **A PATCH without `revision` applies.** Pass. On the demo, for template-2,
   suite-1 and sched-2:
   - With no `revision`, the PATCH returned 200.
   - With the revision as it stood before that PATCH, it returned 409
     `{"code":"stale"}`.
   - A garbage revision (`"zzz"`) returned 409 `stale`.
   - `"revision": ""` was treated as absent and returned 200.

   The spec also checks the no-revision PATCH on its own template.
3. **A skip held by an open task, then fired late.** Pass.
   - The hourly schedule `sched-1` (overlap `skip`) fired once, filing #2,
     which was left open.
   - Its next occurrence was moved to 07:52:47, three hours back. Over four
     cycles the daemon recorded **one** row: reason `task`, task 2
     (`awaiting_reply`), outcome held. It logged one line, "the
     2026-10-15T07:52:47Z firing is held: it fires once as soon as that clears".
     The pane read "skipped at 2026-10-15 10:53 UTC: #2 from the previous
     firing is still open (awaiting_reply) -- held; it fires once as soon as
     that clears", under the suppression banner.
   - Restarting the daemon on the same data dir kept that one row and added no
     duplicate.
   - Closing #2 led to one firing (#3) on the next cycle. The row became
     `fired`, `fired_task_id 3`, `folded 3`. The pane read "fired late as #3 at
     … standing in for 3 later firings". `next_run_at` moved to the next regular
     occurrence (11:52:47), and only one task was filed.
4. **Paused schedule and archived team.** Pass, as `docs/schedules.md`
   describes. Neither case is "dropped": both are **held**. The plan's wording
   ("reads 'dropped' or as documented") allows for this. Only an off-target
   repo, standing work turned off, and now a retime (finding 1) drop an
   occurrence.
   - *Paused.* The schedule was paused with its occurrence two hours overdue.
     One `paused` row was recorded, held, and the pane read "the schedule is
     paused -- held; it fires once as soon as that clears". Resuming fired
     once (#4) with `folded 2`.
   - *Archived team.* Team `payments` was created, and `sched-2` was put in it.
     After `POST /api/teams/payments/archive` with `sched-2` overdue, one
     `teamArchived` row was recorded, held, and logged once. The pane's
     summary read "the schedule's team is archived". Nothing was filed while
     the team was archived.
   - **Finding 1** came up here: an occurrence held while paused, then
     stranded by a cadence edit.
5. **A retired `system:memory-agent` arriving from grain/config.** Pass.
   - A `system:memory-agent` entry (template `system:memory-agent`, due at
     03:00 today, so overdue) was committed to the daemon's state repository in
     `settings/schedules.yml`. Today's exports keep schedules in that file, not
     in `tables/schedule.json` (see finding 2).
   - In the next cycle (11:00:17) the daemon imported it, wrote a settings
     snapshot, and then logged "deleted schedule system:memory-agent: this
     build no longer ships the job it fires". It filed no task. The export two
     commits later no longer listed the row.
   - System schedules after the import: `system:release-notes`,
     `system:infra-review`, `system:memory-agent:default`. After creating team
     `payments`, `system:memory-agent:payments` was seeded as well. That is one
     memory agent per team, `default` included, and no deployment-wide row.

## Findings

### 1. A held skip stranded by a cadence edit said it would fire, then was credited to a regular firing (fixed)

To reproduce on a real daemon (before the fix):

1. Pause `sched-1` (`PATCH {"enabled":false}`) and let its occurrence come due.
   One `paused` row is recorded, held.
2. `PATCH {"recurrence":{"kind":"everyNHours","everyNHours":2}}` moves
   `next_run_at` to now+2h. Then `PATCH {"enabled":true}`.
3. The row (occurrence 10:26:32) stays held behind `next_run_at` 12:56:41, so
   it can never fire. The pane still says "held; it fires once as soon as that
   clears".
4. When 12:56 comes round, `resolveHeldScheduleSkips` settles every held row
   as `fired`. The skipped occurrence then reads "fired late as #N", crediting
   it with an on-time firing. `TestScheduleSkipStrandedByARetimeIsDroppedNotFired`
   shows this without the fix: `Outcome:fired … FiredTaskID:2`.

**The fix** (`fix(schedules): drop a held skipped firing once an edit retimes
the cadence past it`):

- The new `model.Store.DropStrandedScheduleSkips` marks held rows from before
  a given `NextRunAt` as `dropped`.
- `ui.UpdateSchedule` calls it whenever an edit moves `NextRunAt`.
- `fireSchedule` calls it before settling held rows, which also covers a
  retime written straight to the store (a declaring repository's sync).
- The `docs/schedules.md` outcome list says so.

Re-run on the live daemon with the fixed build, the same row became `dropped`
on the next retime, and the pane read "the schedule is paused -- dropped; the
schedule moved on to its next run".

**Tests**, each failing without the fix:

- `pkg/orchestrator` `TestScheduleSkipStrandedByARetimeIsDroppedNotFired`
  covers the firing-time backstop.
- `pkg/ui` `TestUpdateScheduleRetimeDropsAHeldSkip` checks that a resume keeps
  the row held, and that a retime drops it.

### 2. The retirement log line pointed at a file the importer ignores (fixed)

`retireSystemSchedule` logged "remove it from tables/schedule.json there".
Schedules are now exported to `settings/schedules.yml`, and `staterepo` reads
`tables/schedule.json` only when that file is absent (`dumpRowsOf`). An
operator following the hint would edit a file that decides nothing, and the
row would come back on every import.

To reproduce, do step 5 and read the daemon log. The line now names
`settings/schedules.yml`, keeping the old name for older exports, and
`docs/declaration-schema.md`'s retirement paragraph says the same. This is
log wording only, so it has no test of its own. `go test ./pkg/orchestrator
-run 'SystemSchedule|Retire'` passes.

### Notes (no change)

- **The plan names `tables/schedule.json`.** On this build an export writes
  `settings/schedules.yml`. The step was run with that file. A legacy
  `tables/schedule.json` placed beside it is ignored by design.
- **The task state on a held row is the last one seen while held.** Row 1 kept
  `awaiting_reply` after #2 closed, because the late firing does not refresh
  it. That is as documented ("as of the latest cycle that re-skipped it").
- **A deployment change is needed.** grain/config's own `settings/schedules.yml`
  should not list `system:memory-agent`. If it does, every import brings the
  row back and the next cycle deletes it again. That fix is a pull request
  against grain/config, not anything in this repository.

## Tests added

- `ui/e2e/stale-save-two-contexts-walkthrough.spec.js` covers steps 1–2: two
  browser contexts, suite and template, refusal, kept edit, Reload, and a
  PATCH without a revision. It passed locally on Chromium (2.6s).
- `pkg/orchestrator/schedule_skip_test.go`:
  `TestScheduleSkipStrandedByARetimeIsDroppedNotFired`.
- `pkg/ui/schedule_suppression_test.go`: `TestUpdateScheduleRetimeDropsAHeldSkip`.
- Locally, `go test ./pkg/model/... ./pkg/orchestrator/... ./pkg/ui/... ./pkg/staterepo/...`
  passed.

## Not run

- **`default`'s memory agent writing `default`'s own general memory row**
  (`babea026e`). That needs an agent run, and this environment has no model
  backend. `pkg/daemon`'s `mcpserver_memory_test.go` covers it in CI.
- **A real one-minute cadence.** grain has none; time was simulated, as
  described under Environment.
- **Importing a legacy `tables/schedule.json`-only tree on a fresh data dir.**
  The retirement acts on the database row whatever file it came from. The
  live import used `settings/schedules.yml`.
- **WebKit.** The new spec ran on Chromium only, locally. CI runs whatever the
  Playwright projects select.

## grain CI

The verdict for the final commit is recorded in the pull request (bwsalmon/grain#3908)
and in the closing note of task 3947.
