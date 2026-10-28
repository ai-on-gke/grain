# Test run — Processes narrowing, `everyNMinutes`, default `grain-ci` credit (2026-10-25 plan §5)

**Plan:** `docs/test-plans/2026-10-25.md` §5
**Run:** task 931, 2026-10-26 00:20–01:30 UTC
**Tree:** `origin/candidate` at `6d8f57485` for the walkthrough, rebased onto `543ad3fd6` before pushing
**Environment:** Debian sandbox (Linux 6.1), Go 1.26.10, Node 20.19.0, Playwright 1.62.1 Chromium
(`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), `npm --prefix ui run build`, then
`grain demo -addr 127.0.0.1:4391 -open=false -access-code-file …`, signed in through
`POST /api/auth/access-code`. The demo seeds 3 schedules (`sched-1` daily, `sched-2` weekly,
`repo:schedule:acme:widgets:nightly` declared in `acme/widgets`), 3 templates and 2 suites.

## Where the plan does not match the tree

The plan describes some of these features differently from what shipped. Each step below was
run against what is actually in the tree:

| Plan says | Tree has |
|---|---|
| `ParseScheduleQuery` in `pkg/ui/schedules.go`, typed tokens `kind:schedule`, `state:paused`, `trigger:cron`, `capability:`, `model:` | `pkg/schedulefilter.ParseQuery`, reading the address bar's encoding: `q=` free text, `sort=attr:dir`, one `f=attr:op:value\|value` per condition. Attributes: `name id source type repo base template suite enabled cadence timeZone autoApprove autoMerge overlap lastOutcome next last created`. A typed `kind:schedule` is plain search text. |
| `ProcessesView.jsx` / `ProcessFilterBar.jsx`, chips `All / Schedules / Hooks / Sessions / Enabled / Paused` | `ProcessesList.jsx` with the shared `NarrowingBar`. The kind chips are `All`, `Schedules`, `Workflow schedules`, `Templates`, `Suites`, `Triggers`. Enabled/paused is the `Enabled` condition (`f=enabled:any:yes\|no`). The page has no hooks or sessions. |
| `everyNMinutes` is `*/N * * * *` cron in `ScheduleModal.jsx` / `pkg/scheduler/cron.go`, from `31e8c766d` | `44cb4714e` added `model.RecurrenceEveryNMinutes`, a structured cadence counted from the last firing, with a floor of 10 and a ceiling of 527040 (one year). It lives in `ScheduleOverlay.jsx`. The app has no cron presets and no "Custom cron". |
| Default `grain-ci` credit is `44cb4714e` | It is `31e8c766d` (PR #4674), in `pkg/model/compose.go` and `CompositionPanel.jsx`. |

So the plan's expected cron strings (`*/15 * * * *`) are, in this tree,
`{"kind":"everyNMinutes","everyNMinutes":15}`. Also, 60 and 90 are valid intervals here, not invalid
cron steps. The next plan author should be aware of this.

## 1. Processes narrowing and the kind chips

Each kind chip was clicked on `/processes`. The rows shown were compared with `GET /api/schedules`
called with the page's own query string:

| Chip | URL written | Rows on page (schedule rows) | API with the same query, before the fix | After the fix |
|---|---|---|---|---|
| All 8 | `/processes` | 8 (3) | 3 | 3 |
| Schedules 3 | `?kind=schedule` | 3 (3) | 3 (only because `kind` was ignored) | 3 |
| Workflow schedules 0 | `?kind=workflow` | 0, "No processes match your search." | **3** | 0 |
| Templates 3 | `?kind=template` | 3 (0) | **3** | 0 |
| Suites 2 | `?kind=suite` | 2 (0) | **3** | 0 |
| Triggers 0 | `?kind=trigger` | 0 | **3** | 0 |

`/schedules` redirects to `/processes?kind=schedule` with the Schedules chip pressed. Each chip's
count is computed over every condition except kind (for example, `?q=nightly` shows All 2,
Schedules 1, Templates 1).

Conditions and search (page vs API, with the same query, after the fix):

| Query | Page | API |
|---|---|---|
| `f=enabled:any:yes` (with `kind=schedule`) | 3 schedules | 3 |
| `f=enabled:any:no` | 0 rows, empty state, all chips 0 | 0 |
| `f=source:any:deployment` | sched-1, sched-2 + 2 templates + 1 suite | sched-2, sched-1 |
| `f=source:any:acme/widgets` | declared nightly | declared nightly |
| `f=repo:any:acme/widgets` | — | all 3 |
| `f=type:any:task` | sched-2 | sched-2 |
| `f=cadence:any:daily` / `everyNMinutes` | — | sched-1 / the new schedule |
| `q=nightly` | nightly schedule + `nightly-maintenance` template | nightly schedule |
| `kind=schedule&q=Release` | sched-1 | sched-1 |
| `f=source:any:acme/widgets&q=sweep` | — | nightly |
| `q=zzz-nothing` | 0 rows, "No processes match your search.", every chip 0 | 0 |
| `f=bogus:any:x` | dropped; all 8 rows shown | 400 naming the attributes |
| `f=name:contains:x` | — | 400 (`has, hasnot, empty, filled`) |

Typing in **Search processes…** writes `?q=` as you type. Clearing the box goes back to `/processes`
and the full list. Typing `kind:schedule` searches for that text and matches nothing, as expected
(the grammar is not typed tokens).

### Bug found and fixed: the API ignored the page's `?kind=` chips

**Repro (before):** `curl -b jar '/api/schedules?kind=workflow'` returned all 3 schedules, while
`/processes?kind=workflow` showed none. `?kind=template` did the same. `f=kind:none:schedule` and
`sort=kind:asc`, both of which the page writes, returned 400 `schedules have no attribute "kind"`.
`pkg/schedulefilter`'s package comment promises that the page's address-bar query, pasted onto the
API, asks the same question. The existing `processes-narrowing.spec.js` got around this by
stripping `kind=schedule` off the URL before pasting it.

**Fix:** `pkg/schedulefilter` now has the page's `kind` attribute. A schedule whose
`workflowReview` is set (`processes.js`'s `isWorkflowSchedule`) is `workflow`; any other is
`schedule`. Kinds sort in the chips' order. `ParseQuery` reads `?kind=a,b` as "kind is any of a, b".
A kind the page has no chip for (`kind=schedules`) is a 400, like any other condition the API
cannot read.

**Regression tests:** `TestFilterReadsTheKindChips` and new refusal cases in
`pkg/schedulefilter/filter_test.go`, `kind=` cases in `pkg/ui/schedule_filter_test.go`, and the
Playwright test "each kind chip's address asks the API for the schedules it shows" in
`ui/e2e/processes-narrowing.spec.js`. The existing narrowing test now pastes the URL unchanged.

### Noted, not changed

- The empty state with a kind chip and no search reads "No processes match your search." when
  nothing was searched. The wording is a little off but not wrong.
- The page drops an unreadable condition (`f=bogus:any:x`) and shows everything, while the API
  answers 400. Both are intended (package comment). I did not see a visible notice on the page
  that a condition was dropped.

## 2. `everyNMinutes` recurrence

From `/processes?kind=schedule` → **+ New schedule** → Repeat **Every N minutes**. The field
defaults to `30`, with `min=10`, `max=527040`, and the helper text "At least 10; counted from the
last firing".

| Value | Browser validity | Saved? |
|---|---|---|
| `0` | invalid: "Value must be greater than or equal to 10." | no |
| `-5` | invalid (≥ 10) | no |
| `""` | invalid: "Please fill out this field." | no |
| `1`, `9` | invalid (≥ 10) | no |
| `15.5` | invalid: "nearest valid values are 15 and 16" | no |
| `527041` | invalid (≤ 527040) | no |
| `10`, `59`, `60`, `90` | valid | — |

No `POST /api/schedules` was sent for any invalid value (the only POST was
`/api/schedules/composition`, the preview). Saving with `15` created
`{"kind":"everyNMinutes","everyNMinutes":15}`, with `nextRunAt` 15 minutes after creation. The row
on `/processes` shows `every 15m`, `acme/widgets`, and `Enabled`. `f=cadence:any:everyNMinutes`
finds it. Reopening `/processes/schedules/<id>` shows Repeat **Every N minutes** with `15` (no
fallback to another cadence). Changing it to `45` and pressing **Save** saved `everyNMinutes: 45`.

Calling the API directly: 9, 0 and -5 return 400 `recurrence: everyNMinutes must be at least 10`.
527041 returns 400 `must be at most 527040 (a year)`. 10 and 527040 return 201.

No bugs found. Regression coverage added in `ui/e2e/schedule-every-n-minutes.spec.js`
(create, list, reopen, edit, browser refusal, API floor and ceiling). Before this run, no e2e test
covered this cadence.

## 3. Default `grain-ci` composition credit (`31e8c766d`)

With `acme/widgets` on GitHub CI (`ignoreGitHubCI: false`), the Capabilities row reads
`self-debug — from the schedule` on sched-2, and `none — from the schedule` on the new schedule
and in the New schedule form. After `PUT /api/repos/acme/widgets/ignore-github-ci
{"ignoreGitHubCI":true}`:

- sched-2: `self-debug — from the schedule`, then `grain-ci — from the repo`.
- The new schedule and the New schedule form: `grain-ci — from the repo`.

This works as described. The setting was restored afterwards. Existing coverage is walkthrough
test 20 in `recurring-composed-walkthrough.spec.js`.

## 4. Verification

- `go test ./pkg/schedulefilter ./pkg/ui -run TestTheSchedulesAPIAnswersTheProcessesPagesFilter`: pass.
- `npx playwright test e2e/schedule-every-n-minutes.spec.js e2e/processes-narrowing.spec.js
  --project=chromium`: 5/5 pass.
- Screenshots: `make ui-screenshots PAGES=/processes,/processes?kind=schedule,/processes/schedules/sched-1`
  (desktop and phone, light and dark) rendered fine. I could not look at them: the sandbox
  file reader shows PNGs as bytes, and `LOOK=1` needs a Gemini key the sandbox does not have. The
  change touches no `ui/src` file, so the pages render exactly as on `candidate`.
- Full grain CI (`wait_for_checks`): see the pull request.

## Operator note

During the run, the operator asked to "rebase when feasible", since fixes are in `main`. This
task's base is `candidate`, and `main`/`candidate` are paired branches that must not be merged
into each other. So the branch was rebased onto the latest `origin/candidate` (`543ad3fd6`), not
onto `main`.
