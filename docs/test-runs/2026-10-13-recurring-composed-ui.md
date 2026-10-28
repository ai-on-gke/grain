# 2026-10-13 recurring and composed work UI walkthrough

Task 3496 (`grain/eu/task-3496`). This is an end-to-end walkthrough of
schedules, templates, suites and suite runs, in a real browser against a
real `grain demo` server. It follows
[2026-10-12-recurring-composed-ui.md](2026-10-12-recurring-composed-ui.md);
that pass's fix (template edits renaming their schedules at once) is in the
tree tested here and held.

This pass went after ground the earlier ones had not covered:

- a monthly schedule at an hour that happens twice (01:30 New York on the
  day daylight saving ends);
- HTML, quotes and non-ASCII in names, through rows, previews, confirms and
  run panes;
- editing a schedule while it is paused;
- a raw schedule with an extra writeable repo, fired by Run now;
- overlap "Skip" and then "Start it anyway" on the same schedule;
- two tabs on one schedule, suite and template;
- a suite run with a running task, a run that reaches its pass limit, and
  a grandchild follow-up;
- the suite Run dialog's branch and repo validation;
- phone-width layout and the run pane's focus trap.

It found two bugs. One is fixed on this branch: **a branch typed with
surrounding spaces was refused by schedules and suite runs**, while their
own previews had shown it as the branch, and templates and tasks trim the
same box. The other, **a second tab's save silently reverts the first tab's
edit** on schedules, suites and templates, needs a concurrency token across
three editors and the server. It is filed as a follow-up rather than
patched here. See [Bugs and fixes](#bugs-and-fixes).

## Environment

| | |
| --- | --- |
| Commit walked | `25cd6f2640ea` (`candidate` tip, base of `grain/eu/task-3496`) |
| Fix verified at | `77bae2df0a45` (`grain/eu/task-3496`). The demo was restarted on the rebuilt binary over a fresh store. |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 / 10.8.2 |
| Browser | `playwright` 1.62.1, Chromium headless shell revision 1234 from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| Server | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /tmp/grain ./cmd/grain`, then `TMPDIR=<throwaway> /tmp/grain demo -addr 127.0.0.1:8899 -open=false` (embedded SQLite under the throwaway `TMPDIR`) |
| Browser identity | `localStorage["grain.username"] = "alice"` from an init script; every request carried `X-Grain-Admin-Mode: on`, as `ui/playwright.config.js` does |
| Viewport / zone | 1400×900 (390×844 for the phone checks), browser time zone UTC |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

Each step was a Node script (`/tmp/walk/s*.mjs`, not committed) driving
Playwright's Chromium through visible controls: role and label locators,
clicks, typing and key presses. Every page logged console errors and
warnings, page errors, failed requests, every HTTP response ≥ 400 with its
body, and every `confirm()`/`beforeunload` text (accepted unless a step says
it was dismissed).

## Stubs

- **Agent execution boundary (suite-run states).** The demo runs no agents.
  A throwaway Go program (`cmd/_walkstub`, never committed) opened the
  demo's own store with `sqlite.Open(sqlite.DefaultConfig(<demo dir>))` and
  stood in for agents:
  - `running <ids>`: `Store.StartRun` with an unfinished `model.Run`, which
    is what dispatch records when an agent starts.
  - `complete <ids>`: `Store.ObserveField(..., o.CompletedAt = now)`.
  - `propose <parent> <title>`: copies the parent under a new `NewTaskID`
    with `Origin.Reason = proposal`, no approval and one
    `LinkProposedBy -> parent` link, the shape a relayed proposal has.
  - `sync`: the real `orchestrator.SyncSuites`. Every pass decision, every
    later pass's tasks and each run's terminal status came from grain's own
    reconciler.
  - `runs`: `Store.ListSuiteRuns`, to read the result.

  Creates, edits, runs, pauses, deletes and every view went through the
  browser and the real server.
- **GitHub.** The demo has no GitHub, so
  `GET /api/repos/acme/widgets/github-branches` answers 404 and the branch
  boxes offer no suggestions. Those console errors were ignored as an
  external boundary.

## Steps and outcomes

### 1. A raw schedule (`/schedules`)

- **+ New schedule**, Fires "A task", no template:
  - title `<b>walk4</b> "raw" & ünïcode ✓`, description containing
    `<script>alert(1)</script>`;
  - base `main`; another writeable repo `grain/config-default` (typed, then
    Enter);
  - Repeat "Monthly, on a day and time", day `1`, time `01:30`, time zone
    `America/New_York`.
- The time box's label followed the zone: `Time (America/New_York, EDT)`.
  The live preview listed `Also writes: grain/config-default` "from the
  schedule", and every other value with its source.
- **Saved.** The row read `monthly on day 1 at 01:30 america/new_york | Next
  run 11/1/2026, 5:30:00 AM`. 1 November 2026 is the day New York leaves
  daylight saving, so 01:30 happens twice; grain picked the first (EDT,
  05:30 UTC), which is the sensible reading. The API showed
  `writes: [{repo: grain/config-default}]`, `base: main`.
- **Escaping.** The title rendered as literal text everywhere: the row, the
  preview, the Run now and Delete confirms. No `<b>` element was created and
  no script ran.
- **Pause** from the pane closed it. The row read `paused | Held -- fires
  11/1/2026, 5:30:00 AM if resumed by then`.
- **Edited while paused** (title `… edited`, Save): after a reload the
  schedule was still paused, with its next run unchanged and the new title.
- **Run now while paused** (confirm: `Run "…edited" now? It starts the task
  "…edited" straight away; the schedule's own next run is unchanged.`) filed
  task 13: `author: schedule`, queued, `base: main`,
  `writes: [grain/config-default]`. The schedule stayed paused with the same
  next run, and gained `lastRunAt`.
- **Run now again**, overlap "Skip this run": refused with a 409 shown as a
  page alert, `schedule sched-3: its previous run has not finished yet, and
  this schedule is set to skip a run that would overlap one -- wait for it,
  or set "If the last run has not finished" to start it anyway`.
- **Overlap "Start it anyway"**, Save, Run now: accepted, no alert.
- **Resume**: `can overlap | Next run 11/1/2026, 5:30:00 AM · last ran
  10/13/2026, 11:52:27 AM`.

### 2. Two tabs on one schedule, suite and template

- **Edit vs delete.** Tab A opened `/schedules/sched-3` and changed the
  title. Tab B opened the same schedule and deleted it (confirm `Delete the
  schedule "…"? Tasks it already filed are not affected.`). Tab A's Save got
  a 404, closed the pane and showed `no schedule sched-3`. That is a fair
  answer for a row that is gone.
- **Edit vs edit (bug 2).** Both tabs opened the template-backed schedule
  `sched-5` (weekly Wednesday 12:00):
  - Tab A changed the time to `13:15` and saved.
  - Tab B, still showing 12:00, changed overlap to "Start it anyway" and
    saved.
  - Result: `{timeOfDay: "12:00", overlap: "allow"}`. A's change was reverted
    and nobody was told.

  The same happened on `/suites/suite-2` (A: max passes 4; B: require
  approval → `maxPasses: 2, requireApproval: true`) and on
  `/templates/template-4` (A: title; B: description → A's title lost).

### 3. Templates and a suite with an inline template

- **+ New template** `walk4-tmpl`: title `walk4 audit <i>x</i>`, a
  description, priority high, auto-merge on, capability `gcp-key`, and no
  repo (left for whatever fires it). Escape inside the open Capabilities
  popup closed the popup and left the editor open. It saved with exactly
  those values.
- **+ New suite** `walk4-suite`: picked `walk4-tmpl`, then **+ New template**
  inline (`walk4-inline`). It came back preselected (`walk4-tmpl,
  walk4-inline`). Max passes `2`, auto-merge off. The row read `walk4-suite |
  run until clean (max 2) | 2 templates`.
- **The Run dialog's preview** said `Auto-merge: off — from the suite`,
  although the template asks for auto-merge: the suite's setting wins, and
  the preview says so. Repo and Branch are "from the suite", and
  capabilities and fields "from the template".

### 4. Suite runs and their states

- **From the row's Run…** on `main`: run 1 (tasks 15, 16). **From the
  generic + Run**, choosing `walk4-suite` and `release-2.0`: run 2 (tasks
  17, 18). Both closed the dialog and listed as `active … pass 1 · run until
  clean (max 2)`.
- **Running.** After stub `running 15`, `/suites/runs/1` showed task 15 as
  "Running" (the state glyph's accessible name) and 16 as "Queued".
- **Pass 2.** After stub `complete 15 16 17 18`, `propose 15`, `sync`:
  - run 2 (nothing proposed) → **Succeeded**;
  - run 1 → pass 2 (tasks 20, 21), because pass 1 proposed task 19.
- **Pass limit.** After stub `complete 20 21`, `propose 21` (→ 22),
  `propose 19` (→ 23, a grandchild of 15), `sync`, run 1 was **Failed**:
  `reached 2 passes without one that produced no repo change or follow-up
  task`.
- **`/suites/runs/1` by direct URL:** `Pass 1 (4)` lists 15, then 19 and 23
  under it (both "Proposed", flattened at one indent as `SuiteRunPane`'s
  comment intends), then 16. `Pass 2 (3)` lists 20, 21 and 22 under 21. Each
  row carries priority, repo and capability. `/suites/runs/2` shows
  Succeeded with `release-2.0` on each row.
- **Runs list:** `succeeded | acme/widgets | release-2.0 | pass 1 …` and
  `failed | acme/widgets | main | pass 2 · run until clean (max 2) | reached
  2 passes …`.
- **Navigation.** Clicking follow-up 22 in run 1's pane opened `/tasks/22`
  (`generated from 21 · origin 21`, with a link to 21). Back returned to
  `/suites/runs/1` with the pane open.
- **Not linked back.** Task 20's own page marks it as a suite task (the
  "suite" chip) but does not say which run, or link to it. `ui.Task` carries
  only `suiteRun: true`. This is noted as a follow-up, not a bug.
- **From a suite schedule.** + New schedule, Fires "A suite",
  `walk4-suite`, base `main`, daily 23:59. Row: `walk4-suite | daily at
  23:59 | suite: walk4-suite | Next run 10/13/2026, 11:59:00 PM`. Run now
  (confirm `It starts a run of the suite "walk4-suite" …`) started run 3 with
  `scheduleId: sched-4`.

### 5. The Run dialog's validation (bug 1)

On `walk4-suite`'s Run…, each refusal was shown as a page alert with the
dialog left open and filled in:

| Repo | Branch | Answer |
| --- | --- | --- |
| acme/widgets | `bad branch..name` | 400 `… is not a usable branch name …` |
| acme/widgets | `-x` | 400, same |
| acme/widgets | `  main  ` | **400, same, before the fix** — run on `main` after it |
| Other… `acme/nonexistent` | `main` | 400 `… is not on this deployment's target repo list …` |
| Other… `not a repo` | `main` | 400 `repo must be owner/name …` (the preview's composition request is refused the same way) |

The schedule editor did the same: with base ` main `, its preview said
`Branch: main`, then Add was refused as `base " main " is not a usable branch
name`. The template editor trims the same box, and so does a new task's
branch.

### 6. Repository-declared rows (`docs/declarations.md`)

For `nightly-maintenance` (template), `Nightly Repo Maintenance Sweep`
(schedule) and `Smoke Verification Suite` (suite):

- The row's chip reads `Declared by acme/widgets`, with the title
  `Declared in acme/widgets, .grain/<templates|schedules|suites>/<file>.yml`.
- The editor opens at its colon-carrying deep link
  (`/templates/repo:template:acme:widgets:nightly`, and so on) and still
  opens after a reload. Its notice names the repo and file: `This template
  is declared by acme/widgets in .grain/templates/nightly.yml, so it cannot
  be edited or deleted here. Change that file in the repository …`. The
  schedule's adds "Run now still fires it once", and the suite's adds "It can
  still be run from the suites list".
- Delete, Save and the schedule's Pause are disabled, and the text boxes are
  read-only. The template's "+ Add input" and the suite's inline "+ New
  template" sit in the disabled fieldset: Playwright reports them as not
  enabled, and a click never lands. Close and the schedule's Run now are
  live.

### 7. Dependency-aware deletes

- **`walk4-tmpl` Delete, confirm dismissed:** the pane stayed open on
  `/templates/template-3`, and nothing was sent.
- **Accepted:** refused with `template is used by 1 suite(s)
  ("walk4-suite"); remove it from those first`. The pane stayed open.
- **`walk4-suite` Delete:** confirm `Delete the suite "walk4-suite"? Runs
  already started from it are unaffected.`, then refused with `suite is used
  by 1 schedule(s) ("walk4-suite"); repoint or delete those first`.
- **Throwaway schedule** (`walk4 padded base`, on the fixed build): deleted
  through its pane after the confirm. Afterwards `/api/schedules` held only
  the three seeded rows.

The first store was thrown away with its demo when the server was restarted
on the fixed binary. Nothing persists past the demo.

### 8. Layout, keyboard, console

- **Phone width (390×844).** `/schedules`, `/schedules/sched-4`,
  `/templates`, `/templates/template-3`, `/suites`, `/suites/suite-2` and
  `/suites/runs/1`: none scrolled sideways, and no button, link, input or
  row reached past the viewport's right edge.
- **Run pane focus.** On `/suites/runs/1`, Tab went from "← Suites" through
  all seven task links in pass order and wrapped back to "← Suites". Focus
  stayed inside the pane.
- **Console and network.** Across every script the only errors were:
  - the GitHub-branches 404s above;
  - the 4xx answers to the refusals listed above;
  - `beforeunload` on leaving a dirty template editor.

  There were no page errors. The only failed requests were the bodiless
  `DELETE` 204s and the navigation-aborted `/api/tasks` polls that the
  10-12 report already explains.

## Bugs and fixes

| # | Bug | Fix | Regression coverage |
| --- | --- | --- | --- |
| 1 | A branch with surrounding whitespace (`" main "`) was refused as "not a usable branch name" by `CreateSchedule`, `UpdateSchedule` and `CreateSuiteRun`. The composition previews beside those forms had already shown it as `main`, and `UpdateTemplate` and `CreateTask` already trimmed the same box. | Fixed. `pkg/ui/schedules.go` and `pkg/ui/suites.go` trim the base at each entry point. `pkg/ui/composition.go` trims it in the draft schedule and suite previews, so preview and save agree. A base that is blank after trimming is still refused as missing on a suite schedule or run. | `pkg/ui` `TestScheduleBaseIsTrimmedOnCreateUpdateAndPreview` and `TestCreateSuiteRunTrimsItsBase`. `ui/e2e/recurring-composed-walkthrough.spec.js` test 16 drives the schedule editor and the suite Run dialog. All three fail without the fix and pass with it. |
| 2 | Concurrent edits are lost. Every schedule, suite and template editor sends its whole form on Save. A second tab, opened before the first tab saved, reverts the first tab's change with no warning, even when the two tabs changed different fields. | Not fixed here: filed as a follow-up. A safe fix needs a revision or `updatedAt` token on all three objects, a 409 from the server on a mismatch, and handling of that 409 in three editors. The obvious alternative, sending only the changed fields, changes which of `UpdateSchedule`'s template and suite branches run, so it is not a small patch either. | The reproduction is in step 2 and in the follow-up. |

**Bug 1 reproduction (before the fix):**

1. Open `/schedules` and press + New schedule.
2. Enter a title and type ` main ` (with spaces) as the Base branch. The
   preview says Branch `main`.
3. Press Add schedule. You get `base " main " is not a usable branch name`.
   The suite Run dialog does the same with `  main  `.

Verified fixed at `77bae2df0a45`, in the browser on a restarted demo:

- a schedule saved with ` main ` read back `base: "main"`;
- an edit to `\trelease-2.0 ` read back `release-2.0`;
- + Run with `  main  ` started run 1 on `main`.

**Noted, not changed:**

- A suite-filed task's page doesn't link to the run that filed it. This is
  filed as a follow-up together with bug 2's task.
- The overlap 409 names the schedule by ID (`schedule sched-3: …`), not by
  title. The page alert is still clear in context.
- Suite search doesn't match template names. That is consistent with the
  schedules list's own rule: it matches what the row shows, and a suite row
  shows a template count, not names.
- Carried forward from earlier reports:
  - Run now on a paused schedule doesn't mention the pause.
  - A template delete asks for confirmation before the server refuses it.
  - A suite's pass count has no upper bound.

## Tests run

| Suite | Result |
| --- | --- |
| `go test ./pkg/ui -run 'TestScheduleBaseIsTrimmedOnCreateUpdateAndPreview\|TestCreateSuiteRunTrimsItsBase'` | pass with the fix, fail without it |
| `go test ./pkg/ui` | pass |
| `npx playwright test e2e/recurring-composed-walkthrough.spec.js -g "16\."` | pass with the fix, fail without it |
| `npx playwright test e2e/recurring-composed-walkthrough.spec.js e2e/{schedules,suites,templates}_workflows.spec.js --project=chromium` | 19 passed |
| grain CI full suite on the pushed branch | see the pull request |
