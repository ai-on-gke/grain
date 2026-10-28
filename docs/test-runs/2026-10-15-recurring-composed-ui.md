# 2026-10-15 recurring and composed work UI walkthrough

Task 3753 (`grain/eu/task-3753`). This is an end-to-end walkthrough of
schedules, templates, suites and suite runs, in a real browser against a
real `grain demo` server. It follows
[2026-10-13-recurring-composed-ui.md](2026-10-13-recurring-composed-ui.md).
Since then `candidate` has gained several changes in this area, and this
pass covered them:

- stale-save refusal across tabs (`9d55be291`, `f98d67b43`);
- a suite-filed task's link back to its run (`3e9718953`);
- team-scoped `/t/<team>/…` routes with no team selector on the create
  forms (`0ed7310e0`).

It also covered ground no earlier pass had:

- a fully populated template, with an input that has options, a default
  and is required;
- a schedule running from that template, and its preview;
- two tabs where the other tab's change is a Pause, a Run now or a
  template rename;
- a suite run reaching `failed`, with a proposed follow-up;
- deleting a suite and a schedule that existing runs still name.

It found two bugs, both fixed on this branch:

1. **A template-backed schedule's preview said the template's
   capabilities came "from the schedule".**
2. **Each list's search ignored the cadence, branch or mode its rows
   show.**

See [Bugs and fixes](#bugs-and-fixes).

## Environment

| | |
| --- | --- |
| Commit walked | `9788ecb1e3aa` (`candidate` tip, base of `grain/eu/task-3753`) |
| Fixes verified at | `5e711ebbc` (`grain/eu/task-3753`). The demo was restarted on the rebuilt binary over a fresh store. |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 / 10.8.2 |
| Browser | `playwright` 1.62.1, Chromium headless shell revision 1234 from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| Server | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /tmp/grain ./cmd/grain`, then `TMPDIR=<throwaway> /tmp/grain demo -addr 127.0.0.1:8899 -open=false` (embedded SQLite under the throwaway `TMPDIR`) |
| Browser identity | `localStorage["grain.username"] = "alice"` from an init script; every request carried `X-Grain-Admin-Mode: on`, as `ui/playwright.config.js` does |
| Viewport / zone | 1400×900, browser time zone UTC |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

Each step was a Node script (`/tmp/walk/s*.mjs`, not committed) driving
Playwright's Chromium through visible controls: role and label locators,
clicks, typing and key presses. Every page logged:

- console errors and warnings, and page errors;
- failed requests, and every HTTP response ≥ 400 with its body;
- every `confirm()` text, accepted unless a step says it was dismissed.

The demo redirects every list to its team-scoped path (`/schedules` →
`/t/default/schedules`). The create forms show "In team admin" (the demo's
default team is named `admin`) and offer no team selector.

## Stubs

- **Agent execution boundary (suite-run states).** The demo runs no agents.
  A throwaway Go program (`cmd/_walkstub`, never committed) opened the
  demo's own store with `sqlite.Open(sqlite.DefaultConfig(<demo dir>))` and
  stood in for agents:
  - `running <ids>`: `Store.StartRun` with an unfinished `model.Run`, which
    is what dispatch records when an agent starts.
  - `complete <ids>`: `Store.ObserveField(..., o.CompletedAt = now)`.
  - `close <ids>`: `Store.ObserveField(..., o.ClosedAt = now)`, a task
    closed without completing.
  - `propose <parent> <title>`: copies the parent under a new `NewTaskID`
    with `Origin.Reason = proposal`, no approval and one
    `LinkProposedBy -> parent` link, the shape a relayed proposal has.
  - `sync`: the real `orchestrator.SyncSuites`. Every pass decision, every
    later pass's tasks and each run's terminal status came from grain's
    own reconciler.

  Creates, edits, runs, pauses, deletes and every view went through the
  browser and the real server.
- **GitHub.** The demo has no GitHub, so
  `GET /api/repos/acme/widgets/github-branches` answers 404 ("GitHub branch
  listing is not available on this UI server") and the branch boxes offer
  no suggestions. Those console errors were ignored as an external
  boundary.

## Steps and outcomes

### 1. A fully populated template (`/templates`)

**+ New template** `walk5-tmpl`, filled in with:

- title `walk5 audit {{area_name}}` and a two-line description using the
  same input;
- **+ Add input** `area_name`: label "Area to audit", options
  `frontend`/`backend`, default `backend`, Required;
- repo `acme/widgets` and base `release-1.0`;
- another writeable repo `grain/config-default` and a read-only repo
  `other/lib`;
- auto-merge on, network "None (this deployment only)";
- nothing-needed "Leave for acknowledgement", model `gemini-2.5-pro`;
- capabilities GCP key and Memory;
- fields: priority high, area backend, requested-by `Ops
  <ops@example.com>`, due 2026-12-01, estimate 2.5, customer-facing Yes.

**Add template**: the row read `walk5-tmpl | acme/widgets @ release-1.0 |
walk5 audit {{area_name}}`, and `GET /api/templates` returned every value
above. That included `fields[0]` with its options, default and required
flag, `writes: [{repo: grain/config-default}]`, `reads: [other/lib]`,
`capabilities: [gcp-key, memory]` and all six custom fields.

**Edit and reopen**:

- `/templates/template-3`, typed straight in, opened the editor with the
  name filled.
- I appended ` (edited)` to the title, set priority low, and removed
  `grain/config-default` with its × button. Save.
- I reloaded and opened the row again: title and priority held, and the
  stored template had no `writes`.

**List**:

- Search `AUDIT` found `security-audit` and `walk5-tmpl`.
- `zzz` showed "No templates match your search." with the count at 0.
- Search for `release-1.0`, a branch the row shows, found nothing
  ([bug 2](#bugs-and-fixes)).
- Sorts "Name (A–Z)", "Newest first" and "Oldest first" reordered the rows
  correctly. A reload goes back to "Custom order"; the sort is not kept,
  the same as on the other two lists.

**Script slip, not a product bug.** Pressing Escape after choosing an
option in one of the form's selects lands on the pane, not the menu, which
has already closed. The pane then asks `You have unsaved changes here.
Leave and discard them?`. That guard is correct; my first script had
answered it OK.

### 2. Schedules (`/schedules`)

**Composing from the template.** I pressed **+ New schedule** and picked
`walk5-tmpl`.

- The form said `Fires against acme/widgets on release-1.0 -- the selected
  template is bound to that repo`, and that title, description, reads,
  writes, capabilities, fields and auto-merge "come from the selected
  template".
- The preview showed the title with the input's default filled in (`walk5
  audit backend (edited)`) and every value marked "from the template".
- The one exception was **Capabilities `gcp-key, memory` — from the
  schedule** ([bug 1](#bugs-and-fixes)).

I saved it as weekly, Friday 09:15, `Europe/London`. The row read `fridays
at 09:15 europe/london | template: walk5-tmpl | Next run 10/16/2026,
8:15:00 AM`, which is 09:15 BST.

**Validation on a raw schedule.** Each refusal came from the browser's own
form validation; no request was sent, and the pane stayed open:

| Input | Refusal |
| --- | --- |
| Add with no title | `title: Please fill out this field.` |
| Every N hours `0` | `≥ 1` |
| Every N hours `1.5` | `nearest valid values are 1 and 2` |
| Every N hours `100000` | `≤ 8784` |
| Day of month `32` | `≤ 31` |
| Time `25:00` | refused by the `type=time` box itself |

**Full raw schedule** `walk5 raw full`:

- repo `grain/config-default` on `main`, also writes `acme/widgets`, reads
  `other/docs`;
- network egress, capability self debug, priority high;
- overlap "Start it anyway";
- monthly, day 31 at 07:45 in `Asia/Kabul`.

The time box's label followed the zone (`Time (Asia/Kabul, GMT+4:30)`).
The preview listed each value "from the schedule", plus the implicit
`grain-ci` capability. The row read `monthly on day 31 at 07:45
asia/kabul | can overlap | Next run 10/31/2026, 3:15:00 AM`, which is
correct for UTC+4:30. The API returned every field.

**Suite-running schedule.** Fires "A suite", Release Qualification Suite,
daily 23:30.

- Add with an empty base is held by the browser: the base is required,
  with the hint `required: a suite run stacks its tasks against one
  branch`.
- With base `main` and no suite chosen, the pane showed `choose a suite
  for this schedule to run` and stayed open.
- The preview listed both passes' tasks, with auto-merge and approval
  "from the suite".
- Saved, the row read `suite: release qualification suite | Next run
  10/15/2026, 11:30:00 PM`.

**Search and sort.**

- `walk5` found 3 rows, `GRAIN/CONFIG` found the raw schedule, and
  `.grain/schedules` found the declared one.
- `europe/london` and `suite: release`, both text on the rows, found
  nothing ([bug 2](#bugs-and-fixes)).
- "Title (A–Z)", "Next run" and "Newest first" sorted correctly.

**Throwaway lifecycle** on `sched-3`:

- **Edit.** Retitled `walk5 throwaway` and changed to daily 12:00. Saved,
  the row showed it at once.
- **Pause** (from the pane). The row read `paused | Held -- fires
  10/15/2026, 12:00:00 PM if resumed by then`, and still did after a
  reload.
- **Run now, confirm dismissed.** The confirm read `Run "walk5 throwaway"
  now? It starts the task "walk5 throwaway" straight away; the schedule's
  own next run is unchanged.` Dismissed, nothing was filed.
- **Run now, confirm accepted.** Task 13 was filed: `author: schedule`,
  queued, on `acme/widgets`. The row, still paused, gained `· last ran
  10/15/2026, 1:44:06 AM`.
- **Resume.** After a reload the row read `Next run 10/15/2026, 12:00:00 PM
  · last ran …`.
- **Delete, confirm dismissed.** The confirm read `Delete the schedule
  "walk5 throwaway"? Tasks it already filed are not affected.` Dismissed,
  the pane stayed open.
- **Delete, confirm accepted.** The row was gone. Then I opened
  `/schedules/sched-3` directly: it fell back to the list with `no
  schedule "sched-3"`.

### 3. Two tabs, after the stale-save fix

| Tab B does | Tab A then saves | Result |
| --- | --- | --- |
| Pause `sched-4` | a description edit | Saved. The schedule **stayed paused**: A's save does not carry `enabled`. |
| Run now on `sched-4` | a description edit | Saved. Still paused, and `lastRunAt` kept. |
| Renames `walk5-tmpl`'s title | a cadence edit (10:45) on the schedule using it | Saved. The schedule shows the renamed title at 10:45. |
| Saves a description on `sched-4` | a day-of-month edit from the stale copy | 409 `code: stale`. The pane stays open with `this schedule was changed by somebody else after you opened it … Your changes here were not saved; Reload replaces them with the saved copy.` **Reload** showed B's description and the stored day 31, and A's 15 was not saved. |

So the follow-up the 10-13 pass filed holds. Runtime changes (pause, a
firing, the template-name cache) do not invalidate an open editor, and a
real conflict is refused with a way forward.

### 4. Suites (`/suites`)

**+ New suite.**

- Add with no name was held by the browser.
- With a name and no template, the pane showed the 400 `a suite needs at
  least one template` and stayed open.
- I picked `walk5-tmpl`, then **+ New template** opened a template form
  inside the suite form. I saved `walk5-inline` there and it came back
  preselected (`walk5-tmpl, walk5-inline`).
- "Run a fixed number of times": `Number of times` refuses `0` and `-1`
  (`≥ 1`) and `1000` (`≤ 100`). The cap is new since the 10-13 report,
  which listed "no upper bound".
- 3 times, auto-merge off, require approval on. The row read `run 3× | 2
  templates | requires approval`, and the API matched.

**Edit.** "Run until no repo changes", max passes 2, approval off. After
Save and a reload the row read `run until clean (max 2) | 2 templates`.
The API had `mode: until_clean, maxPasses: 2, requireApproval: false`.

**Search.**

- `walk5` matched.
- `until clean`, which the row shows, matched nothing
  ([bug 2](#bugs-and-fixes)).
- `WALK5-INLINE`, a template name, matched nothing. That is consistent:
  the row shows a template count, not names.

Sorts: "Name (A–Z)" and "Most templates".

### 5. Suite runs

**From the suite row.** `walk5-suite` → **Run…**. The dialog opened on
`walk5-suite` with repo `acme/widgets`, and said `repeated until a pass
files no pull request and proposes nothing, up to 2 passes`.

- Its preview showed the bound template's own repo and branch "from the
  template".
- The unbound one showed `acme/widgets`, "the branch this run targets",
  "from the suite".
- Auto-merge showed `off — from the suite`, although the template asks
  for it.

Branch `main`, **Run**: run 1 was listed as `active | acme/widgets | main
| pass 1 · run until clean (max 2)`.

**From the generic + Run.** It preselects the first suite. Submitting
with no branch was held by the browser. Then Release Qualification Suite,
branch `release/2026.10`, **Run**: run 2 started.

**States, via the stub and the real reconciler:**

- **Running.** Tasks 14 and 15 were marked running. In the run pane each
  task row carries a state figure with an accessible name ("Awaiting
  ack", "Proposed", "Closed", and so on).
- **Pass 1 completes, with a follow-up.** 14 and 15 completed, and 14
  proposed task 18. `SyncSuites` filed pass 2 (19, 20) for run 1 and
  pass 2 (21, 22) for run 2.
- **Run 1 fails.** I completed 19 and closed 20 without completing it.
  Run 1 became `failed`, with `pass 2 had a task that failed, or closed
  without completing` on the row and in the pane.
- **Run 2 succeeds.** All four tasks completed, and run 2 became
  `succeeded` after its 2 passes.

**The run pane** (`/suites/runs/1`, opened directly and after a reload):

- Tasks are grouped `pass 1 (3)` and `pass 2 (2)`.
- The follow-up 18 sits under pass 1 beside its proposer 14, with the
  figure "Proposed".
- The header count is 5.
- `/suites/runs/99` fell back to the list with `no suite run "99"`.

**Keyboard and navigation.**

- Tab from "← Suites" moved through the task links in pass order.
- Enter on task 18's link opened `/t/default/tasks/18`.
- Task 14's page reads `pass 1 of walk5-suite run 1`, and its link goes
  to `/suites/runs/1` (feature `3e9718953`).
- The proposed follow-up 18 has no such link. It was proposed by 14, not
  filed by the suite, so this is noted rather than called a bug.

**A scheduled run.** Run now on the suite schedule showed the confirm `Run
"Release Qualification Suite" now? It starts a run of the suite …` and
started run 3. Its row and pane carry a "scheduled" chip, `aria-label
"Scheduled by sched-6"`, linking to the schedule.

### 6. Repository-declared rows (`docs/declarations.md`)

I checked `nightly-maintenance` (template), `Nightly Repo Maintenance
Sweep` (schedule) and `Smoke Verification Suite` (suite). Each was opened
from its row with keyboard focus and Enter.

- **Chip.** `declared by acme/widgets`, with the title `Declared in
  acme/widgets, .grain/templates/nightly.yml` (or `.grain/schedules/…`,
  `.grain/suites/smoke.yml`).
- **Deep link.** The colon-carrying link
  (`/t/default/templates/repo:template:acme:widgets:nightly`, and so on)
  still opens after a reload.
- **Notice.** It names the repo and file, for example `This template is
  declared by acme/widgets in .grain/templates/nightly.yml, so it cannot
  be edited or deleted here. Change that file in the repository and grain
  picks it up within a couple of minutes.` The schedule's adds "Run now
  still fires it once", and the suite's adds "It can still be run from the
  suites list".
- **Controls.** Every input, select and button inside the form matches
  `:disabled`, including "+ Add input", the suite's inline "+ New
  template" and the Model picker. Delete, Save and Pause are disabled.
  Only Close is live, and Run now on the schedule.

### 7. Dependency-aware deletes and cleanup

- **`walk5-tmpl` while in use.** The confirm read `Delete the template
  "walk5-tmpl"? Schedules and suites using it must be repointed or deleted
  first.` It was then refused with `template is used by 1 schedule(s)
  ("walk5 audit backend (renamed in B)") and 1 suite(s) ("walk5-suite");
  repoint or delete the schedules and remove it from the suites first`.
  The pane stayed open.
- **`walk5-suite`.** The confirm read `Delete the suite "walk5-suite"?
  Runs already started from it are unaffected.` It was deleted. Run 1
  still renders in full at `/suites/runs/1`, with its name, status,
  failure reason and both passes.
- **`walk5-tmpl` again.** Refused, now naming the schedule alone.
- **The suite schedule.** The confirm read `… Suite runs it already
  started are not affected.` It was deleted. Run 3's "scheduled" chip
  still links to `/schedules/sched-6`; following it by keyboard lands on
  the list with `no schedule "sched-6"`. That is a fair answer, noted
  below.
- **The remaining throwaway rows.** I deleted both throwaway schedules
  through their panes, then `walk5-tmpl` and `walk5-inline`.
  - Afterwards the API held only the seeded rows: 3 schedules, 3
    templates (the declared one included) and 2 suites.
  - Run 1 still renders after its templates are gone.

The store was thrown away with its demo. Nothing persists past the demo.

### 8. Console and network

Across every script the only errors were:

- the GitHub-branches 404s above;
- the deliberate 400s and the 409 listed above;
- the `type=time` box's own warning for `25:00`.

There were no page errors. The only failed requests were navigation-aborted
`GET /api/tasks` polls (`net::ERR_ABORTED`), which earlier reports already
explain.

## Bugs and fixes

| # | Bug | Fix | Regression coverage |
| --- | --- | --- | --- |
| 1 | A template-backed schedule's preview (draft and saved) said the template's capabilities came **"from the schedule"**, while the form beside it said capabilities come from the template. Cause: a template-backed schedule stores a copy of its template's grants (`ui.scheduleContentFromTemplate`). `model.ComposeScheduleTask` credited the schedule whenever that copy was non-empty, even though it added nothing. | `pkg/model/compose.go`: the schedule is credited only when it adds a capability the template does not grant. The combined grants are unchanged. | `pkg/model` `TestComposeScheduleTaskCreditsCopiedGrantsToTheTemplate`. e2e `recurring-composed-walkthrough.spec.js` test 18 drives the schedule editor's preview. Both fail without the fix (`Received string: "Capabilitiesgcp-keyfrom the schedule"`) and pass with it. |
| 2 | Each list's search ignored the summary chip its rows draw. A schedule's cadence or zone ("fridays at 09:15 europe/london") wasn't matched, nor a template's `repo @ base`, nor a suite's mode ("run until clean (max 2)", "run 3×"). | `SchedulesList.jsx` also matches `describeRecurrence`, `TemplatesList.jsx` the `repo @ base` string the row draws, and `SuitesList.jsx` `describeMode`. | Vitest: "finds a schedule by the cadence its row shows", "finds a template by the repo and branch its row shows", "finds a suite by the mode its row shows". All three fail without the fix. e2e test 18 searches all three lists. |

**Reproductions (before the fixes):**

1. Create a template with capability GCP key. On `/schedules`, press + New
   schedule and pick that template. The preview's Capabilities row reads
   `gcp-key — from the schedule`.
2. Create a weekly schedule in `Europe/London`. On `/schedules`, type
   `europe/london` into the search: no rows. The same happens for
   `release-1.0` on a template bound to that branch, and for `until clean`
   on `/suites`.

Both were verified fixed in the browser on the rebuilt demo (`v1.mjs`).
The preview reads `gcp-key — from the template`, and each search finds its
row.

**Noted, not changed:**

- A suite run started by a schedule that was later deleted keeps its
  "scheduled" chip and link. The link lands on `no schedule "sched-N"`.
  That is a clear answer, and the run's history is right to keep naming
  where it came from.
- A follow-up proposed inside a suite run is listed in the run pane, but
  its own task page does not link to the run. Only tasks the suite filed
  do (`3e9718953`).
- The suite Run dialog's preview keeps showing "the branch this run
  targets" for an unbound template after a branch is typed, rather than
  the branch itself.
- The sort choice on all three lists resets on reload. They behave alike,
  so this reads as a choice rather than a bug.
- Carried forward from earlier reports:
  - Run now on a paused schedule does not mention the pause.
  - A template delete asks for confirmation before the server refuses
    it.
- Not re-walked this time: phone-width layout, which the 10-13 pass
  covered and this change does not touch.

## Tests run

| Suite | Result |
| --- | --- |
| `go test ./pkg/model` | pass. The new test fails without the fix. |
| `go test ./pkg/ui` | pass |
| `npx vitest run src/components/{Schedules,Templates,Suites}List.test.jsx` | 146 passed. The 3 new tests fail without the fix. |
| `npx playwright test e2e/recurring-composed-walkthrough.spec.js -g "18\."` | pass with the fixes, fail without the compose fix |
| `npx playwright test e2e/recurring-composed-walkthrough.spec.js e2e/{schedules,suites,templates}_workflows.spec.js --project=chromium` | 21 passed |
| `npx prettier --check src e2e` | clean |
| grain CI full suite on the pushed branch | see the pull request |
