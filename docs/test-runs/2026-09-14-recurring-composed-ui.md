# Recurring and composed-work UI walkthrough — 2026-09-14

## Test subject and environment

- Commit: `807d2f32f2bf8ba244bde632a39406bc27de680f` (plus the fixes described below).
- Debian bookworm, Go 1.26.6, Node 20.19.0, npm 10.8.2.
- Playwright 1.62.1 with Chrome for Testing 151.0.7922.34 at 1440×1100.
- Built with `make build`, then served by the real `bin/grain demo -addr 127.0.0.1:18420 -open=false`. Each clean pass used a new `mktemp` directory and the demo's real embedded SQLite store and `pkg/ui.Server`.
- Gemini 2.5 Flash inspected the rendered empty Schedules page and final suite-run state matrix from temporary screenshots. The key was read from `/home/debian/.gemini-api-key` into process environment only; it was not printed, logged, or written to the checkout. No screenshot or browser artifact is committed.

## Walkthrough and outcome

All create, edit, run, pause/resume, and delete mutations below were made through visible browser controls. Persistence checks used list returns, reloads, and direct URLs.

- Empty/list behavior: Schedules, Templates, and Suites showed correct zero counts and empty copy. Tab entered the page at the first visible configuration action. Once populated, search narrowed each list correctly and the sort menus accepted Name (templates/suites) or Title (schedules). A delayed 400 ms templates read showed no explicit loading indicator; an injected GET-only 503 produced the expected visible error banner.
- Template validation/composition: submitting blank required fields identified `name` and `title`. Created a template with name/title/description, bound repo and base, an extra writable repo/base, read-only repo, auto-merge, egress policy, Self debug capability, enum/multi-enum/text/date/number/boolean custom fields. Reopen verified every value; name/title edit survived reload and direct URL. A second template was created inline from the suite editor and preselected there.
- Suites: created a two-template until-clean suite with max 3, approval required, and auto-merge off. Preview showed both templates' effective title, body, placement, read/write repos, capability, custom fields, network, approval, and merge policy. Edited through fixed-count mode (2 passes), switched back to until-clean max 4, enabled auto-merge, saved, reloaded, and reopened successfully.
- Schedules: created (1) a bound-template task schedule, daily 09:15, overlap allowed; (2) a raw task schedule with repo/base/read/write/capability/network/custom fields, weekly Tuesday 14:30, overlap allowed; and (3) a suite schedule, monthly day 15 at 07:45, skip overlap. A throwaway editor exercised every-N-hours, daily, weekly, and monthly controls. Composition preview reflected template values. The task schedule deep-linked at `/schedules/sched-1`; pause survived reload/reopen, resume worked, and Run now required confirmation. Its row retained the unchanged next run and a persisted “last ran” timestamp after reload.
- Suite runs: started one run from the suite row's `Run…` action and a distinct run from the restored Runs-header `+ Run` action. Both returned to the list and opened at direct `/suites/runs/:id` URLs. The active pane grouped pass 1, immediately showed the two real spawned task titles and updated task counts, and a task click navigated to `/tasks/13`.
- Execution-state presentation: through the execution-boundary stub below, the real UI showed an Active scheduled run (Scheduled marker, `scheduled-branch`, pass 1), a Failed run (red status and deterministic failure text), and a Succeeded run (green status, pass 2). The succeeded pane grouped Pass 1 and Pass 2 and nested “Task 38 proposed follow-up”; that row navigated to `/tasks/18`. Each run's direct URL survived reload. Gemini independently read the same three statuses, branches, pass numbers, scheduled marker, and failure text and reported no obvious visual/labeling defect.
- Destructive behavior: declining schedule and suite confirmations preserved each row; accepting removed it. Schedule copy stated that already-filed tasks remain, and suite copy stated that existing runs remain. Deleting a referenced template first showed its confirmation and then the server's visible dependency refusal (“template is used by 1 schedule(s)”). After dependants were removed, only the throwaway schedules, suite, and templates were deleted. Historical runs and their spawned tasks remained, as promised.

## Stubs and browser diagnostics

The demo intentionally has no agent/orchestrator. Only that execution boundary was seeded, after the runs themselves had been started in the browser: a temporary Go helper opened the live demo directory through `sqlite.Open`/`model.New` and used Grain's store methods `FireNextPass`, `ObserveField`, `PutTask` with `LinkProposedBy`/`LinkFixes`, `CompleteSuiteRun`, and `CreateScheduledSuiteRun`. Two minimum templates were temporarily restored so `FireNextPass` used the real composition path; both were then deleted through browser confirmations. No HTTP handler, React component, or server response was replaced.

The only other stub was a browser route returning 503 for one GET `/api/templates` to inspect the error state; it did not intercept writes.

A focused pass over the seeded run pages had zero console errors and zero non-2xx responses. The full composition pass produced only understood diagnostics:

- 404 from `/api/repos/.../github-branches`: expected because `grain demo` has no GitHub branch lister; `BranchField` independently retained the real stored-branch response and free-text behavior.
- 400 from the intentional dependency-aware template deletion.
- 503 from the intentional error-state route.
- aborted task/delete requests at immediate navigation boundaries; persisted state checks passed afterwards.

No unexpected JavaScript exception, failed mutation, or server 5xx occurred.

## Bugs found and fixed

1. **Generic suite launch was unreachable.** Reproduction: open `/suites`; the Runs header had no action, although `SuitesList` retained the `running === true` generic-overlay path and documented a bare launcher. Fix: restore a `+ Run` Runs-header action. Regression: `SuitesList.test.jsx` now opens the real Run-a-suite overlay from that button and verifies it does not open the editor.
2. **A newly started run showed stale task data.** Reproduction: start a suite run and immediately open its row; only `/api/suite-runs` refreshed, so the pane used snapshot template names and global task counts stayed stale until polling/reload. Fix: refresh `/api/tasks` together with suite runs after a successful start. Regression: the run-action test now requires both refresh callbacks; the real-browser pass immediately showed actual task titles/counts and task-detail navigation.

## Verification

- `npm test -- src/components/SuitesList.test.jsx src/App.schedules.test.jsx`: 38 passed.
- Rebuilt embedded frontend and server with `make frontend grain`.
- Two complete clean-store Chromium passes after the fixes: passed.
- Focused seeded-state Chromium pass: passed with zero console/network errors.
