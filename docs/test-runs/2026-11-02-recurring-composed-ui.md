# Test run — 2026-11-02 — Processes: schedules, templates, suites, suite runs and triggers

An end-to-end walkthrough of grain's recurring and composed-work UI: the
Processes page, and the schedules, templates, suites, suite runs and triggers
it lists. Every step ran in a real Chromium against a real `grain demo`
server. Run by task 2403.

Two bugs were found and fixed on this branch (below). One server-side cause
behind the first was filed as a follow-up.

## Environment

- Tested `main` at `aaa266ef` (Merge pull request #462). The fixes are
  `d81398a5` and `2b99e2c9`; `origin/main` (`40353767`) was merged in
  afterwards.
- UI built with `npm ci && npm run build`. `grain` built with
  `go build -tags grain_e2e ./cmd/grain`, so the demo serves the real UI.
  `ui/e2e/releasehelper` was built from the tree. Go 1.26, Node 20.19.0,
  Playwright's Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- Server:
  `grain demo -addr 127.0.0.1:4290 -open=false -access-code-file … -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 2s -sensitive-hold`,
  over a throwaway store under `/tmp/grain-demo-*`. The browser context
  signed in through `POST /api/auth/access-code`. The demo was restarted
  once, on the fixed binary, to check the fixes live.
- Browser: ad-hoc Playwright scripts (not committed), mostly at 1400×900.
  Phone and dark checks ran at 390 px with `colorScheme: dark`. Every script
  logged console errors and warnings, page errors, failed requests and every
  response ≥ 400. The only ones were the expected 400s noted under the steps
  that provoked them.
- No Gemini key at `/home/debian/.gemini-api-key`, so nothing used Gemini.
  The demo's connections advertise `gemini-2.5-pro` (the deployment default)
  and `gemini-2.5-flash`.

## Stubs and seeds

Every create, edit, run, pause, resume and delete went through visible
controls in the browser. Only these stood in for a boundary the demo does not
run:

| What | How | Why |
|---|---|---|
| Repository declarations | Pushed into the `-github-sim` bare repo (`main`, `candidate`): `.grain/templates/{nightly,pairing,opus}.yml` (nightly has an options input; pairing is `interactive: true`; opus asks for `claude-opus-5-5`), `.grain/suites/sweep.yml` (until-clean, max 3, approval), `.grain/schedules/{nightly,sweep,wf}.yml` (template / suite with **no `base:`** / `workflow: nightly`), `.grain/workflows/nightly.yml` and `.grain/triggers/design.yml`. Declarations were already on for acme/widgets (Repository → Advanced). | `-scheduled-workflows` reads the sim repo, which starts empty. The real `declarations` reconciler did every read. |
| Agent finishing a suite pass | `releasehelper -dir <store> complete-suite-pass`, then `sync-suites` (one `orchestrator.SyncSuites`). | No agents run in the demo. |
| An agent's `propose_task` | `POST /api/tasks` with `proposedBy: "28"` (a run-3 task). | Records the same `proposed-by` link an agent's proposal does. |
| CI executing a workflow run | `POST /api/e2e/fake-ci/runs/fake-ci-1/finish {"conclusion":"failure","job":"test"}` | `-fake-ci` never finishes a run by itself. |
| Probes, not gestures | `curl` reads of `/api/schedules`, `/api/templates`, `/api/suites`, `/api/suite-runs`, `/api/tasks/<id>`. `POST /api/templates` for a template with `model: claude-opus-5-5` (template-7) and one with grant `api:ci-read@acme/widgets` (template-8). `POST /api/suite-runs` with `base: ""`. | To confirm what was persisted, to seed the two presentation cases, and to find out what the API accepts. |

## Steps and outcomes

### 1. Processes list, chips, search, query, old links

1. Rail → **Processes** at `/processes`. On a cold load the rail said 9 and
   the page **All 10**. Once Processes had been opened, the rail said 10,
   which is the documented trigger trade-off (2026-11-01 notes). After the
   declarations read it was 14, and **All 14** matched the rows.
2. Each kind chip on and off: Schedules `?kind=schedule` (4), Workflow
   schedules `?kind=workflow` (2), Templates (5), Suites (2), Triggers (1).
   With Schedules, Templates or Suites alone, the header button became
   **+ New schedule / template / suite**. With Workflow schedules, Triggers
   or none, it stayed **+ New**.
3. Templates + Suites + search "declared" gave
   `?kind=template,suite&q=declared`: 3 rows, and every chip's count
   followed the search. Reload kept the field, chips and rows. Rail → Inbox,
   Back, Forward and Back again all landed on the same narrowed view.
4. Old links: `/schedules` → `?kind=schedule`; `/templates` →
   `?kind=template`; `/suites` → `?kind=suite`; `/suites/suite-1` → its Edit
   suite pane. `/suites/runs/1` (before any run existed) and
   `/schedules/nope` fell back to `/processes` with `no suite run "1"` and
   `no schedule "nope"`. `?kind=bogus` and `?sort=bogus:up` normalised to
   `/processes`. `?q=✓` kept the query and matched nothing.
5. Sort: the default is **Next run · Soonest first**. Next run, Last run,
   Name and Kind each worked in both directions, wrote `?sort=<key>:<dir>`
   and survived reload. Removing the key gave **Custom order**
   (`?sort=custom`) and a Move handle on all 14 rows. Each way of moving
   worked:
   - Tap: the handle, then **Move here, above …**.
   - Keyboard: focus the handle, Enter, focus the last slot, Enter.
   - Escape cancelled a pick (0 slots left).
   - Pointer drag of row 4 to the top.
   The order survived reload.
6. Templates and suites show the last and next run of the schedules that
   fire them, and "on demand" when none does. This held throughout §3–§5
   without a reload.

### 2. New menu

**+ New** offers Schedule, Template and Suite, with the hint "Workflow
schedules and triggers are declared in a repository's .grain/ directory --
declarations." The hint links to `docs/declarations.md` on GitHub.

### 3. Schedules

1. **Raw task schedule** (New → Schedule, Template "None"):
   - Fires offers A task and A suite.
   - Repeat offers every N hours, every N minutes, daily, weekly and
     monthly.
   - Every N minutes = 5 was refused by the browser ("Value must be greater
     than or equal to 10."). The helper reads "At least 10; counted from the
     last firing".
   - Saved at 15 minutes with **Start it anyway**, model gemini-2.5-flash,
     network Egress, grant `api:ci-read` (from **Add grant…**, whose
     listing groups the Grain API presets: task-admin, ci-read,
     dispatch-control, deployment-ops (dangerous), release-manager,
     self-debug, …), and read-only repo acme/upstream.
   - The live **What this schedule files** preview listed each value,
     marked "from the schedule". `GET /api/schedules` had every one
     (`everyNMinutes: 15`, `overlap: allow`, `networkAccess: open`,
     `capabilities: [api:ci-read]`, `reads`). The row read
     `every 15m · can overlap`.
2. **Model picker**: Deployment default (gemini-2.5-pro), gemini-2.5-pro and
   gemini-2.5-flash, which is only what the connections advertise. A
   schedule firing a template offers "The template's model" as its empty
   choice. One firing a suite has no model picker.
3. **From a template** (`wt2403-full`, daily 09:30): the form says title,
   description, reads, writes, capabilities, fields and auto-merge come from
   the template. The template's row went from "on demand" to 9:30 without a
   reload.
4. **Run now** confirmed with: "Run "WT2403 check staging config (edited)"
   now? It starts a task from the template "wt2403-full" straight away; the
   schedule's own next run is unchanged." Both the schedule row and the
   template row then showed last 9:06:47, with next still 9:30, before and
   after a reload.
5. **Pause**: the row read "Held -- fires … 9:30 if resumed by then",
   "paused", and the template row went back to "on demand". **Resume**
   restored both. Neither asks for confirmation; both are reversible.
6. **Edit** (deep link `/processes/schedules/sched-3`): monthly on day 31 at
   08:00 read `monthly on day 31 at 08:00`, next 11/30, clamped.
7. **Suite schedule** (Fires: A suite, WT2403 suite B): the form needs a
   base branch ("required: a suite run stacks its tasks against one
   branch"). Leaving it empty blocked the submit natively. Weekly Friday
   17:45 America/New_York read `fridays at 17:45 america/new_york`, next
   11/6 22:45 UTC (EST), and the suite's row followed without a reload.
8. **Delete**: "Delete the schedule "…"? Tasks it already filed are not
   affected." (for a suite schedule: "Suite runs it already started are not
   affected.") Dismissing kept it, in the UI and the API. Accepting removed
   it, and its template's row went back to "on demand" without a reload.
9. Leaving a dirty New schedule form raised the browser's unsaved-changes
   prompt.

### 4. Templates

1. **Fully populated** (`wt2403-full`):
   - Title and description with `{{env}}`, and an input `env` labelled
     Environment with options staging/production and default staging.
   - Read-only acme/upstream, reviewer ana, Task priority 8, auto-merge,
     **Interactive**.
   - Network None, nothing-to-do **Leave for acknowledgement**, model
     gemini-2.5-flash, grant `api:self-debug`, custom field Requested by.
   - Typing the options as "staging, production" was refused by the server
     with `field "Environment": default "staging" is not one of its
     options`, shown in the form (one HTTP 400). That is by design: options
     go one per line, as the placeholder says.
   - The base branch typed with no bound repo was dropped, as its helper
     says ("only used with a bound repo").
2. **Edit and reopen**: binding acme/widgets, base main and a new title
   saved. Reopening after a reload showed every value, the options one per
   line included.
3. **Use…** shows the input as a picker, "Files against acme/widgets @ main
   -- this template is bound to it", and the interactive note: "This
   template files a live chat: the agent can talk with you while it works,
   and the chat opens as soon as the task is filed." Choosing production
   updated the preview title. **Create task** opened `/tasks/24` on its
   chat.
4. **Model no connection serves** (template-7, and declared `opus.yml`): the
   picker reads "claude-opus-5-5 (no connection advertises it)" and lists
   it after the advertised models.
5. **Dependency-aware delete**: Delete on `wt2403-full` confirmed with
   "Schedules and suites using it must be repointed or deleted first". The
   server refused with "template is used by 1 schedule(s) ("WT2403 check
   staging config (edited)") and 2 suite(s) ("WT2403 suite", "WT2403 suite
   B"); repoint or delete the schedules and remove it from the suites first",
   shown in the pane, and the template stayed. Once those were gone, the
   same delete went through.

### 5. Suites

1. **New suite**: the Templates picker names each template's title and
   bound repo. **+ New template** opened a nested New template form. Saving
   it added `wt2403-inline` to the suite's selection, with the pass preview
   listing both.
2. Run mode until-clean shows Max passes (default 5). 101 was refused
   ("Value must be less than or equal to 100."). 99 is valid and saved
   (`maxPasses: 99`).
3. Fixed-number mode refused 0 ("Value must be greater than or equal to
   1."). Count 2, **Require approval** on and auto-merge off (it defaults to
   on) saved as `count: 2, requireApproval: true, autoMerge: false`. The row
   read `run 2× · 2 templates · requires approval`.
4. **Delete** a throwaway suite: "Delete the suite "WT2403 throwaway
   suite"? Runs already started from it are unaffected." Dismissing kept
   it. Accepting removed it, and it was still gone after a reload.

### 6. Suite runs

1. **Scheduled**: at 09:00 the demo's schedule loop fired the declared
   weekly sweep. Run 1 showed `active · acme/widgets · scheduled`, its tasks
   unapproved, because the suite requires approval. This is also where
   **bug 1** showed: the run has an empty base.
2. **From the suite row** (WT2403 suite B → Run…, branch main): the dialog
   shows "what each pass files" ("2 passes of these tasks"). Run 2 appeared
   active, pass 1, tasks 26 and 27 unapproved.
3. **From the generic + Run** (Suite: WT2403 suite, branch release/1.0):
   "repeated until a pass files no pull request and proposes nothing, up to
   99 passes". Run 3 appeared active, with tasks 28 and 29.
4. **Follow-up**: task 30, proposed by 28 (stub above). Run 3's pane lists
   it under 28, marked unapproved. The API marks 28 `proposed: true`.
5. **Failed**: suite-2, then `wt2403-inline`, deleted through the UI (the
   template's confirm does not mention the active run; see Notes). One
   complete + sync left run 3 **failed** with "pass 2 could not be filed:
   template template-5 no longer exists", in the list and on the pane.
6. **Succeeded**: two complete + sync rounds took run 2 to **succeeded**.
   `/processes/suites/runs/2` groups four tasks under pass 1 and pass 2.
7. Direct URLs `/processes/suites/runs/{1,2,3}` open each pane. The task
   links work: `/tasks/30` opened, and Back returned to the run.

### 7. Repository-declared rows

- Every declared template, suite, schedule, suite schedule and workflow
  schedule shows **declared by acme/widgets** on its row. Its pane says, for
  example: "This schedule is declared by acme/widgets in
  .grain/schedules/wf.yml, so it cannot be edited or deleted here. Change
  that file in the repository and grain picks it up within a couple of
  minutes. Run now still fires it once." A suite's pane adds "It can still
  be run from the suites list".
- No input is editable: 0 of 8, 10, 32, 8, 12, 26 and 26, and typing was
  refused. Delete, Pause, Save, + Add input, Add grant… and + New template
  are disabled, while Run now and "New task from this template" stay
  available.
- A **failed read** (the first push had a `workflow:` schedule whose
  workflow had no `on: schedule`) was explained on Repository → Advanced:
  "Last read failed: … give it a `schedule:` trigger … — still running what
  0f1e2d3c4b5a declared." The Processes page itself says nothing (see
  Notes).
- **Workflow schedule Run now** confirmed with: "It starts a CI run of the
  workflow it was declared in straight away, to be reviewed when it
  finishes". The row read "Last run fake-ci-1 on main: running". Once the
  run was finished as a failure, it read "failure 9:16:37 AM, reviewed in
  task 33".
- The **trigger** row `design.yml` (next "on change") links
  `/processes/triggers/acme/widgets/.grain/triggers/design.yml`, which
  opens `/repos/acme/widgets/settings/advanced` listing
  ".grain/triggers/design.yml".

### 8. Keyboard, phone, dark, cleanup

- From the search box, Tab reaches Filter, Sort, the suite runs' + Run, the
  header, every kind chip, then each row's name link, its linked last-run
  CI run and review task, and its Use…/Run… button. Enter on a row link
  opens its pane.
- 390 px light and dark, and 1400 px dark: `/processes`, `?kind=suite`, a
  failed run's pane, a suite schedule's pane, a template pane, the declared
  workflow schedule's pane and the scheduled run's pane. Only the workflow
  schedule's pane scrolled sideways (**bug 2**).
- Cleanup through the UI: every `WT2403`/`wt2403` schedule, suite and
  template was deleted, each with its confirmation, and `?q=wt2403` then
  shows 0 rows. Seeded and declared rows were left alone. Tasks filed during
  the run stay in the throwaway store.

## Bugs found and fixed

### 1. A suite run with no base drew an empty chip

**Reproduction:** declare a suite schedule with no `base:` (allowed by
`docs/declarations.md`) and let it fire, or press Run now on it. Then open
Processes → Suite runs.
- The run's row read `active · acme/widgets · [ ] · scheduled`. The third
  chip was an empty 16×24 px outlined pill.
- Its pane at `/processes/suites/runs/1` carried the same empty chip.
- `GET /api/suite-runs` has `"base": ""`.

**Cause:** `ProcessesList.jsx` and `SuiteRunPane.jsx` both drew
`<Chip label={run.base}>` unconditionally.

**Fix (`d81398a5`):** one `RunBaseChip`, used by both, which says "default
branch" when the run names none. That is where such a run's tasks go: task
23 was filed with `base: null`.

**Coverage:** `SuiteRunPane.test.jsx` ("names the default branch for a run
with no base") fails without the fix. Live on the fixed binary, Run now on
the declared suite schedule gave `active · acme/widgets · default branch ·
scheduled` at 1400 px light and dark, with no empty chip on the pane.

**Follow-up (proposed):** the server is inconsistent here.
`POST /api/suite-runs` with `base: ""` answers 400 "base is required: a
suite run has no default branch to fall back to", and the schedule form and
API refuse a base-less suite schedule. But a declared one is accepted and
fires a base-less run. Whether declarations should refuse it or firing
should resolve the default branch is a server decision, filed separately.

### 2. A long grant pushed the composition preview off a phone screen

**Reproduction:** at 390 px, open a template with grant
`api:ci-read@acme/widgets` from Use… (or the declared workflow schedule's
pane, whose system template carries that grant).
- The "what this files" Capabilities row measured `scrollWidth` 413 against
  `clientWidth` 358 (357 on the workflow pane).
- The "from the template" chip ended at x = 430, past the viewport, and the
  overlay pane scrolled sideways (429 > 389).

**Cause:** in `CompositionPanel.jsx` the value `<dd>` is `flex: 1` with the
default `min-width: auto`, and a grant chip never wraps. 110 (label) + 169
(grant) + 117 (source chip) + gaps is 412 px.

**Fix (`2b99e2c9`):** the value column gets `minWidth: 0`, and the grant
chips' wrapper `maxWidth: 100%`, so the grant chip (already `maxWidth: 100%`)
ellipsizes inside its column. Its tooltip still has the whole grant.

**Coverage:** `e2e/processes-phone-layout.spec.js` ("A template's Use…
preview on a phone › keeps a long grant inside its row") measures the row
and the scrolling pane. Without the fix it fails with 413 > 358. On the
fixed binary, the workflow pane at 390 light and dark had no overflowing
row, and the grant chip ended at x = 249.

## Notes, not defects (for the next pass)

- **The Processes page does not say a repository's declarations failed to
  read.** Only Repository → Advanced does. Declared rows keep running from
  the last good commit, and the Advanced tab says so.
- **Use… shows an unserved model bare.** The picker says "claude-opus-5-5
  (no connection advertises it)", but the Use… and schedule previews show
  only "claude-opus-5-5". The API also accepts such a model on a template.
- **Deleting a template an active suite run still needs is not warned
  about.** The dependency check covers schedules and suites, not active
  runs. The suite's confirm says runs "are unaffected", and the template's
  confirm says nothing about them, yet the run fails at its next pass. That
  is how run 3 was failed on purpose here and on 2026-11-01. It may deserve
  a warning; it was left as is.
- **Template input options go one per line.** A comma list is a single
  option, and the server's refusal says why.
- **Pause and Resume do not confirm.** Both are reversible.
- Playwright reports the app's 304 polls and 204 deletes as
  `net::ERR_ABORTED`; these were filtered, as on 2026-11-01. At 390 px,
  ChipStrip hides the run row's chips that do not fit, which is by design.

## Not reached

- Real agent runs and real CI execution, both stubbed as above.
- A trigger actually firing on a merge.

## Tests run

- `npx vitest run` (the full UI unit suite) on the merged tree: 233 files,
  3640 tests, all passed.
- `npx playwright test` of `processes-walkthrough`, `processes-narrowing`,
  `processes-phone-layout`, `processes_workflows`,
  `schedule-every-n-minutes`, `suite-run-dialog-order`, `template-priority`
  and `built-in-template`: 26 passed. Then
  `suite-release-batch-walkthrough`,
  `workflow-schedules-candidate-walkthrough` and
  `path-triggers-sensitive-hold-walkthrough`: 10 passed.
