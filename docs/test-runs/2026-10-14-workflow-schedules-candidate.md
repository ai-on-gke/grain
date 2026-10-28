# Walkthrough: workflow schedules on candidate, review only on failure (2026-10-14)

Plan: `docs/test-plans/2026-10-13.md` §3. Task 3549 (filed by task 3492).

## Environment

- Base: `candidate` at `a4674d57b`. All four commits under test are on it
  (`git branch -r --contains` lists `origin/candidate` for `3d294d2c3`,
  `68f569693`, `983e5692c` and `c3c8a7a4f`).
- `grain demo -addr 127.0.0.1:0 -open=false -github-sim -fake-ci -scheduled-workflows`
  on its embedded SQLite store, with the frontend built from `ui/` (`npm ci && npm run build`).
  Driven by hand (curl and a headless browser) and then by Playwright (Chromium 1.62) through
  `ui/e2e/workflow-schedules-candidate-walkthrough.spec.js`.
- The scratch repo is `acme/widgets` in the simulated GitHub. It declares `.grain/workflows/x.yml`
  (`on: schedule`, one job) and `.grain/schedules/x-candidate.yml` (`workflow: x`, `base: candidate`,
  `overlap: skip`). Nothing in this repository's own `.grain/` was touched.

### The demo could not walk this section as shipped (finding F0)

Plain `grain demo -github-sim -fake-ci` has no orchestrator. Nothing reads a repository's `.grain`
declarations. Run now on a workflow schedule writes a `ScheduledWorkflowRun` that nothing ever starts.
`-fake-ci` runs only end by being cancelled. The simulated repo has no `candidate` branch. So steps 1–6
could not be walked at all. This change adds `-scheduled-workflows` (needs `-github-sim` and `-fake-ci`;
`pkg/demo/demo_scheduled_workflows.go`). It:

- adds a `candidate` branch to the simulated repo;
- runs the orchestrator's own `declarations` and `scheduled-workflows` reconcilers every `-reconcile`
  interval (default 2s). They are taken from `orchestrator.Reconcilers()`, not reimplemented. Declarations
  are read from the bare repo the way `daemon.RepoDeclarationSource` reads a forge's: a shallow clone,
  then `repodecl.Load`;
- serves `POST /api/e2e/fake-ci/runs/{id}/finish` (`{"conclusion": "success"|"failure"|"errored",
  "job": "smoke"}`) to end a stub run with a verdict. A run that has no jobs of its own gets the one named
  job, so "success" counts as a passing run and not a jobless one;
- serves `GET /api/e2e/scheduled-workflows` (`{"bare": path}`), which tells a spec where to push.

Without the flag the demo does not change.

## Stubs (external boundaries only)

| Boundary | Stand-in |
|---|---|
| GitHub (repo contents, branches, default branch) | `-github-sim`: a bare git repo on disk; declarations pushed straight into it |
| grain CI execution | `-fake-ci`: records the real `ci.Request` the orchestrator sends (event `schedule`, ref, `RequestedBy`, `TrustedSchedule`) and executes nothing; each verdict is set through the finish endpoint |
| Agents | none run; review tasks are filed and stay queued (or are closed by hand) |

Everything in between is the real code: the reconcilers, `FireScheduleNow`, settlement, review filing, the
HTTP API and the React UI.

## Steps and outcomes

| # | Step | Outcome |
|---|---|---|
| 1 | Declare `x.yml` + `x-candidate.yml` and turn on declarations on the repository page | **Pass.** "Read at …: 0 template(s), 0 suite(s), 2 schedule(s)": the workflow's own cron (`workflow.x`, default branch) and `x-candidate`, titled `Review last night's "x" workflow run on candidate`, `base: candidate`, `workflowReview: on-failure`. Run now's confirmation is accepted. The reconciler logs `started grain CI run fake-ci-1 of .grain/workflows/x.yml on acme/widgets candidate`, and the run is `event: schedule`, `ref: candidate`, `requestedBy: workflow-schedule:repo:schedule:acme:widgets:x-candidate`. |
| 2 | The run passes | **Pass.** Logged `… passed; no review filed`. No review task is filed. The schedule row shows `Last run fake-ci-1 on candidate: green <time>, no review needed`, and the run id links to `/ci/fake-ci-1`. |
| 3 | Fire again and fail it | **Pass.** Exactly one task: `Review last night's "x" workflow run on candidate`, base `candidate`, repo `acme/widgets`. Its opening comment (author `workflow-schedule`) reads: "grain CI run `fake-ci-2` of `.grain/workflows/x.yml` on acme/widgets, branch `candidate`, is the run this task is about: grain started it because its schedule `repo:schedule:acme:widgets:x-candidate` came due at … It is recorded as **failure** (failure: smoke)." The row shows `failure …, reviewed in task 13`, and the link opens the task. While that review is open, Run now is refused with 409 and the message names task 13 (see F2). |
| 4 | `review: always`, then a passing run | **Pass.** The row carries the "Reviews every run" chip; schedules without it don't. A green run files a review (`filed 14 … (success)`), and the row shows `green …, reviewed in task 14`. In the spec this is a second schedule, `x-always.yml`, with `name:` and `review: always`, so one declaration read covers both. Its review is still titled from the workflow and base, and the opening comment names `x-always` (F3). |
| 5 | Daemon restart between run end and settlement | **Not walkable in the demo**: its store is a throwaway temp dir and `-fake-ci` keeps runs in memory, so a restarted demo has neither. The re-entry the restart takes (`reconcileOneScheduledWorkflow`: concluded but not settled → `settleScheduledWorkflowRun`) is covered by `TestAGreenWorkflowRunRecordedButNotSettledIsSettledWithoutAReview` (green: settled, no review), `TestAReviewFiledButNotRecordedIsNotFiledTwice` (red: the firing tag finds the filed task) and `TestAWorkflowRunLostWithTheDaemonIsStillReviewed` (run lost with the daemon: one errored review). All three pass on this branch with a real SQLite store. In the demo, one or two extra reconcile cycles after settlement filed nothing more (the Go test `TestDemoScheduledWorkflowsSettleAGreenRunAndReviewARedOne` runs one extra cycle on purpose). |
| 6 | Each refused form | **Pass on content; the display was a bug (F1, fixed).** All five are refused with a clear sentence that names the file: missing workflow (`no workflow "nosuch" … (.grain/workflows/nosuch.yml)`), no `on: schedule` (`… has no on: schedule, so a scheduled run of it would have no jobs in it`), `.yaml` (`… yamlext.yaml is not one -- rename it`), `overlap: allow` (`… cannot overlap -- grain reviews one run of it at a time`), and `workflow:` + `template:` (`one of template:, suite: and workflow:, not several`). The read fails closed: the last good commit's schedules keep running, and the page says so. The sync log (demo stdout) has one line per file. |
| 7 | Template title edit | **Pass.** `recurring-composed-walkthrough.spec.js` test 15 (from `4dd48467a`) drives exactly this step in a real browser on the demo: the template editor's title is changed, then the schedules list row, the Run now confirmation (`Run "<new>" now?`) and the refused template delete (`template is used by 1 schedule(s) ("<new>")`) all show the new title straight away. It passed in this run. |
| — | "Started by schedule" in `list_ci_runs` | The MCP tool isn't served by the demo. The demo's run carries `requestedBy: workflow-schedule:<schedule id>`, which is exactly the prefix `pkg/mcp`'s `renderGrainCIRun` turns into `Started by schedule <id>.` (`TestRenderGrainCIRunNamesTheScheduleThatStartedIt` passes). This deployment's own `list_ci_runs ref: candidate` (48h, newest 8) showed no scheduled runs in that window. It reports build `adea62d`, committed 2026-10-12 08:25. |

## Findings

- **F0 — §3 could not be walked in `grain demo` at all.** See above. Fixed by `-scheduled-workflows`.
- **F1 — a declaration read refused for several files showed every refusal run together in one
  paragraph on the repository page** (Advanced tab). `repodecl.Load` joins the per-file errors with
  `\n`, but the `Typography` used `white-space: normal`. Five refusals became one 200px block with nothing
  to show where one file's refusal ends and the next begins. Repro: declare the five refused forms and open
  `/repos/acme/widgets#advanced`. **Fixed**: the line is `white-space: pre-line`, so each file's refusal
  starts on its own line.
- **F2 — Run now on a workflow schedule whose last firing is unfinished pointed at a way out that does
  not exist.** The 409 said "… or set "If the last run has not finished" to start it anyway". A workflow
  schedule can't overlap: `overlap: allow` is refused on one, and a declared schedule can't be edited
  anyway. Repro: fail a firing, leave its review open, press Run now. **Fixed**: for a workflow schedule
  the message says it "never overlaps one -- grain reviews one run of it at a time". It then names the
  review to close, or says to wait for the run.
- **F3 (observation, not changed)** — a `workflow:` schedule's `name:` sets the schedule's own title in
  the list, but the review it files is still titled `Review last night's "<stem>" workflow run on <base>`.
  So two schedules of the same workflow on the same base file reviews with the same title. The opening
  comment names the schedule, and the plan documents this title, so it is left alone.
- **F4 (demo artefact)** — with `-scheduled-workflows` the first declarations cycle removes the demo's
  seeded declared rows for `acme/widgets` ("no longer declares its own templates…"), because declarations
  start off. The reconciler is behaving correctly; only seeded demo data is affected, and only with the
  flag.

## Fixes and tests

- `pkg/demo/demo_scheduled_workflows.go`, `pkg/demo/demo.go`, `cmd/grain/demo.go`: `-scheduled-workflows`.
- `pkg/demo/demo_scheduled_workflows_test.go`: `TestDemoScheduledWorkflowsSettleAGreenRunAndReviewARedOne`.
  It declares in the sim repo, fires, and checks that a green run settles with no review, a red one files
  exactly one titled `… on candidate` on base `candidate`, and the finish endpoint's refusals.
- `ui/src/components/RepoPage.jsx` + Vitest `puts each refused file of a failed declaration read on a line of its own` (F1; fails without the fix).
- `pkg/ui/schedules.go` (`scheduleBusyError.Workflow`) + an assertion in
  `TestRunningAWorkflowScheduleNowRecordsARunRatherThanFilingATask` (F2; fails without the fix).
- `ui/e2e/workflow-schedules-candidate-walkthrough.spec.js`: steps 1, 2, 3, 4 and 6 in Chromium against
  the demo, including F1 and F2 in the browser. 5/5 pass locally. Recurring test 15 (step 7) also passes.

## CI verdict

The first push (`878fc8b01`) was red only on `frontend` → "Format frontend" (Prettier on
`RepoPage.test.jsx`); `go`, `ui e2e` and the rest passed. Fixed in the follow-up commit. The full-suite
verdict for the final commit is in the task's closing note.
