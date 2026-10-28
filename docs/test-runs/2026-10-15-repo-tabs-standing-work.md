# Repository page in tabs; standing work switch — 2026-10-15

Task: grain/eu/task-3946 (filed by the nightly test-plan task 3749),
bwsalmon/grain, base `candidate`. Plan: `docs/test-plans/2026-10-15.md` §3.

## Environment

- Commit tested: `591f0f35f` (tip of `candidate` when the branch was cut),
  then the fixes below on top of it. All eleven `candidate` commits named by
  the plan (`c612d0be4` … `b5d9ba5b8`) are ancestors of it.
- `9b0cf6b07` is on `origin/main` and the task branches only, not on
  `candidate` (`git branch -r --contains 9b0cf6b07`), so the part of step 6
  that needs it ("each disabled control describes the refusal") was **not
  run**.
- `cd ui && npm ci && npm run build`, then `go build ./cmd/grain`, so the
  binary embeds the built frontend.
- Manual walk: `grain demo -addr 127.0.0.1:0 -open=false -github-sim
  -fake-ci -base-branch-ci -mechanical-port -scheduled-workflows -schedules
  -reconcile 2s`, driven with curl (admin mode on). `-schedules` is new in
  this change (finding 3). `-git-proxy` was not needed: nothing here pushes.
- Committed walkthroughs, run by grain CI's `ui e2e` job:
  - `ui/e2e/standing-work-switch-walkthrough.spec.js` covers steps 3, 4, 5
    and 7. It runs on its own demo with `-github-sim -fake-ci
    -base-branch-ci -scheduled-workflows -schedules -reconcile 2s`.
  - `ui/e2e/repo-tabs-walkthrough.spec.js` covers steps 2 and 6. It runs on
    `-github-sim` alone, because a demo with `-fake-ci` wired never shows
    an unjudged gate, so the red dot can't be produced there.
- Playwright 1.62.1 Chromium (headless shell 151). It was installed with
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`,
  because the package-cache proxy re-signs the browser CDN's TLS.
- axe-core 4.14.0 was installed into a scratch directory (`/tmp/axe`) and
  injected by an ad hoc script, not committed. The tree has no axe
  dependency, and adding one was out of scope.

## Stubs

- GitHub is the demo's githubsim, a bare repo on disk (`-github-sim`).
- grain CI is the demo's `-fake-ci`, which executes nothing.
- Base-branch verdicts come from the demo's loopback hook `POST
  /api/e2e/base-branch/{red,green}`. The hook drives the real
  `base-branch-ci` reconciler and only chooses the run's conclusion.
- `localStorage["grain.username"]` is set by an init script for the member
  and viewer contexts. They really sign in through `POST /api/auth/login`.
  Those contexts send no `X-Grain-Admin-Mode` header (`extraHTTPHeaders:
  {}`).
- The axe script used `bypassCSP: true`, because the app's CSP (`script-src
  'self'`) blocks injecting axe. This applied only to the ad hoc axe run.
- Nothing in grain's API or React was mocked.

## Steps and outcomes

| # | Step | Outcome |
|---|------|---------|
| 1 | Demo with the plan's flags, a schedule every minute, a port pair, a post-merge job | **Partly.** Schedules have no cadence finer than an hour (`everyNHours` ≥ 1), so "every minute" was walked as the seeded daily schedules `sched-1` and `sched-2`, each moved to the coming minute (`PATCH /api/schedules/{id}` with `recurrence.timeOfDay`). `-mechanical-port` seeds the main↔candidate port pair. No post-merge job was given: the demo runs no `base-branch-dispatch` reconciler, so a job set there could not be watched either running or not running (see Not run). |
| 2 | Each tab by hash, Back/Forward | **Pass.** Each of `#overview`, `#ci`, `#merge-queue`, `#agents`, `#branches`, `#releases` and `#advanced` lands with that tab `aria-selected` and its panel visible. Back walks the list in reverse and Forward walks it again, landing right each time. A tab clicked on the page writes its hash with `replaceState`, so it adds no history entry. That is by design (a65c1ccf9), not a finding. |
| 2 | Unsaved CI edit across tabs, leave prompt | **Fail, fixed (finding 1).** A half-written workflow row on CI (file name only) was still there after a look at Releases, but "← Repos" left with no prompt and dropped it. With the fix, the prompt fires, and dismissing it keeps the page and the value. |
| 2 | CI tab red dot, "Go to CI Mode" | **Pass.** `watch-default-branch` on, with no grain CI wired, gives the gate `{judged:false, reason:"grain-ci-off"}`. The CI tab carries the `.MuiBadge-dot`, and "Go to CI Mode" selects the CI tab (`#ci`) with the CI Mode radio in view. With the gate gone, the dot is gone. |
| 2 | axe on each tab (WCAG 2.0/2.1 A and AA, admin) | **Two violations (finding 5).** Overview, CI, Merge queue, Agents and Branches show none. Releases shows `color-contrast`, serious: the green outlined "runs" chips are 3.27:1. Advanced shows `color-contrast`, serious: the red outlined "Remove" button is 4.22:1. |
| 3 | Standing work off | **Pass.** The switch was turned off on Releases. `GET …/work-ownership` then reads `default:"off"`, with `runs:false` on every item (release, base-ci, post-merge, ports, triage, sched-1, sched-2). In the manual walk, `sched-2` came due at 10:44. It recorded `{reason:"standingWork", outcome:"dropped", occurrence:"2026-10-15T10:44:00Z"}`, its `nextRunAt` moved to the next day, it has no `lastRunAt`, and the task count stayed at 14. The demo log reads "schedule sched-2 skipped … this deployment doesn't run acme/widgets's standing work; … its cadence moved on". The spec checks the same for both `sched-1` and `sched-2` in one minute, then waits further cycles: no task, no CI run. `POST /api/e2e/base-branch/red` answers 409, `GET /api/ci/runs` stays `[]`, and no repair or triage task appears. Ports and post-merge jobs could not be watched (see Not run). |
| 4 | Hand-filed task through the merge queue | **Partly.** `POST /api/tasks` against acme/widgets is accepted while standing work is off, and the task is listed. It was not dispatched or merged: the demo runs no agents and no merge queue (see Not run). |
| 5 | One item back on through its override | **Pass.** "Weekly Dependency & Vulnerability Audit: on" was pressed on Releases. Only `sched-2` then has `runs:true`. In the same minute `sched-2` fired (it got a `lastRunAt` and filed its task, "Weekly Dependency & Vulnerability Audit"), while `sched-1` recorded a second `standingWork` skip. |
| 5 | Default back on, no catch-up | **Pass.** After the switch was turned back on and several reconcile cycles passed: no new task, `sched-1` still has no `lastRunAt`, and its `nextRunAt` is unchanged (the next day's occurrence). The skipped firings are not made up. As a control, main is judged again: red gives `conclusion:"failure"` with a `repairTaskId` filed, and green closes it. |
| 6 | Member who is not a repo admin, and a viewer | **Fail, fixed (finding 2).** Each of the seven tabs was checked as each reader. The only enabled controls left are the Overview tiles (they open other tabs) and the Branches tab's read-only compare. Before the fix, "Jobs dispatched after every merge" (and "Jobs that judge base branches", when drawn) could still be typed in. Each of these sent directly gets 403: `PUT work-ownership` (default and item), `ci-mode`, `watch-default-branch`, `workflows`, `require-branch-up-to-date`, `unfixable-checks`, `prompt-extension`, `setup-command`, `declarations`, `release`, and `POST release-runs`. The work-ownership record is unchanged afterwards. `POST base-branch-runs` is 403 for the viewer. For the member it is not refused, by design (`requireRepoWriter`, d81d94b01): any writer of the repo may ask for the check. |
| 7 | Base-branch check off, "Run grain CI" | **Pass.** "Run the base-branch check now" on Releases shows the server's 400 as a readable sentence: "this deployment doesn't run acme/widgets's standing work, and its base-branch check is part of it: turn the repository's standing work, or its base-ci item, back on to run it". |

## Findings

1. **A half-written CI workflow row was dropped without a prompt** (fixed).
   - Reproduce, as an admin: open `/repos/acme/widgets#ci`, press "Add
     workflow", type `lint.yml` in File, and leave Workflow empty. Then
     press "← Repos". The page leaves with no dialog, and the row is gone
     on return.
   - Cause: the workflow editor saves on blur only once both halves are
     filled, and holds a half row back on purpose. Only the Releases tab's
     job boxes registered with `guardLeaving`, so `leavingLosesEdits()`
     was false.
   - Fix: `RepoPage.jsx` registers a guard asking
     `workflowEditsUnsaved(edits, stored)`. That is true for a half row or
     a refused row, and false for an empty row or one the server stored.
     The prompt now fires from "← Repos", from App's popstate (Back and
     the rail), and from `beforeunload`.
   - Tests: vitests for the helper and for the page
     (`RepoPage.test.jsx`), plus the e2e step. Without the fix, the e2e
     step fails with "Expected: 1, Received: 0" dialogs.
2. **The release pane's job boxes were editable for read-only readers**
   (fixed).
   - Reproduce: as a member of the team targeting acme/widgets who is not
     a repo admin, or as a viewer, open `#releases`. "Jobs dispatched
     after every merge" takes typing. Its Save button is disabled, and the
     server answers 403.
   - Cause: 32aab9020 disabled the buttons but not the two `TextField`s.
   - Fix: both boxes now get `disabled={readOnly}` and the page's refusal
     sentence as their description.
   - Tests: the existing read-only vitest in
     `RepoReleaseWorkflowSettings.test.jsx` now asserts both boxes, and
     the e2e role sweep fails on any enabled write on any tab.
3. **The demo could not fire a schedule on its cadence** (environment gap,
   fixed).
   - Cause: `grain demo` ran only the `releases`, `suites`, `declarations`
     and `scheduled-workflows` reconcilers. Run now is an extra firing
     that records no skip and moves no cadence. So step 3's "the schedule
     records skips" could not be watched.
   - Fix: `grain demo -schedules` runs the daemon's `schedule` reconciler
     every `-reconcile` interval. It is opt-in, so the seeded schedules
     file nothing in other specs' demos.
   - Test: `TestDemoSchedulesFiresADueScheduleOnItsOwn`.
4. **The base-branch hook's refusal said nothing** (fixed).
   - Reproduce: with standing work off, `POST /api/e2e/base-branch/red
     {"branch":"main"}` answered 409 with "grain did not judge the branch:
     acme/widgets main concluded "": " and nothing more. A stood-down row
     carries no detail.
   - Fix: the hook now names the switched-off base-branch check.
   - Test: an added case in `TestDemoBaseBranchHookDrivesARedAndGreenBranch`.
5. **Colour contrast on Releases and Advanced** (not fixed here; proposed
   as a separate task).
   - Reproduce: run axe-core 4.14 with `wcag2aa` on
     `/repos/acme/widgets#releases` and on `#advanced`.
   - What fails: the light theme's `success.main` `#1a9c53` (outlined
     Chip text, e.g. "runs") is 3.27:1 on the `#f7f6f3` background, and
     `error.main` `#d0453a` (the outlined "Remove" button) is 4.22:1. AA
     for small text needs 4.5:1.
   - Why not fixed here: these are palette colours used across the whole
     app, so changing them is not scoped to this page.

## Not run

- **Each disabled control describing its refusal** (step 6, part): needs
  `9b0cf6b07`, which has not reached `candidate`.
- **A hand-filed task dispatching and merging** (step 4): the demo runs no
  agents, no merge queue and no `sync`, so no task can be dispatched or
  merged there. Only "filing is accepted while off" was walked. Covered in
  CI by the Go tests listed in `ab2b18c2f`.
- **No port for a merged change** (step 3): ports are filed by the `sync`
  reconciler when a task merges (`filePorts`), and nothing merges in the
  demo. Covered by `TestStandingWorkOff…` in
  `pkg/orchestrator/standingwork_test.go` (the ports-item cases).
- **No post-merge job** (step 3): the demo runs no `base-branch-dispatch`
  reconciler. Covered by the Go tests in `ab2b18c2f`.
- **A declared workflow cron recording a skip**: the plan doesn't list this
  separately. The firing path is the same `schedule` reconciler, through
  `suppressStandingWorkOff`, which was walked with the template schedules.
  `TestStandingWorkOffStartsNoScheduledWorkflowRun` covers an
  already-recorded firing.
- **Release item**: the release reconciler that reads `model.ReleaseItem`
  (`base-branch-release`) is not in the demo.
- **axe as a non-admin**: axe was run as an admin only.

## grain CI

- The code commits through `ff1b6fe24` passed the full suite: grain CI run
  `77349640486bc25f`, every job green (frontend, go, installer, lint,
  package cache, postgres, relay image, run host, terraform, ui e2e).
- The commit adding this report was rechecked with a full-suite
  `wait_for_checks` before the task finished.
