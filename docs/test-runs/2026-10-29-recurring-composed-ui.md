# Recurring and Composed-Work Automation UI Walkthrough (2026-10-29)

Task 1757 (`grain/049f1e09/task-1757`). End-to-end walkthrough of
[Processes](/processes) -- schedules, workflow schedules, templates,
suites, suite runs and path triggers -- in a real headless Chromium driven
by Playwright against a real `grain demo` server. Every create, edit, run,
pause, delete and navigation gesture went through visible UI controls; the
API was read only to confirm what a gesture persisted, except where the
[Stubs](#stubs) say otherwise.

One defect was found and fixed on this branch (see
[Bug found and fixed](#bug-found-and-fixed)): a path trigger row was the
only declared row on Processes that never said it was declared, and the
Advanced tab it opens only counted triggers, so the file the row names
appeared nowhere on that page.

## Environment

| Item | Value |
| --- | --- |
| Commit walked | `9958172316fbbb293c4c4040fdf2311877284689` (`main`, base of this branch) |
| Go / Node / npm | `go1.26.10 linux/amd64` / `v20.19.0` / `10.8.2`, Linux |
| Browser | Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC |
| Build | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /var/tmp/grain ./cmd/grain` |
| Server | `TMPDIR=<throwaway> /var/tmp/grain demo -addr 127.0.0.1:0 -open=false -github-sim -fake-ci -scheduled-workflows -sensitive-hold -reconcile 500ms -schedules -access-code-file <file>` |
| Sign-in | The "Enter access code" page, typed in the browser |
| Viewports | 1440×900 / 1440×1400 / 1440×1600 desktop; 390×844 phone for the fix; light and dark |
| Gemini key | `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling (or `LOOK=1`) was used. The demo's connection advertises `gemini-2.5-pro` and `gemini-2.5-flash`. |

The walkthrough scripts were throwaway files under `/var/tmp/walk` and are
not committed; nor are screenshots.

## Stubs

- **Agent execution boundary.** `grain demo` runs no agents. A throwaway Go
  helper (`/var/tmp/walkseed`, built from a scratch `cmd/` directory that was
  deleted before committing) opened the demo's SQLite store with
  `sqlite.Open(sqlite.DefaultConfig(dir))` / `model.New` and recorded only
  what an agent run leaves behind:
  - `complete-clean <task>`: `Observation.CompletedAt`;
  - `complete-changed <task> <pr>`: a `LinkFixes` link to `acme/widgets#<pr>`, then `CompletedAt`;
  - `close <task>`: `Observation.ClosedAt` with no completion;
  - `proposed-by <task> <parent>`: a `LinkProposedBy` link, on a follow-up task created with `POST /api/tasks` (what an agent's `propose_task` produces).

  Every suite-run transition (pass 1 → pass 2 → succeeded; pass 1 → failed)
  was made by the real reconciler (`-reconcile 500ms`). A first version of the
  helper set `ClosedAt` together with `CompletedAt`; the reconciler then
  correctly read those tasks as closed and failed runs 1 and 2, which became
  the walkthrough's failed-run samples. Run 3 used the corrected helper.
- **Simulated GitHub and fake CI** (`-github-sim -fake-ci -scheduled-workflows`).
  Repository declarations were pushed with plain `git` to the simulated
  `acme/widgets` bare repository the demo names (`GET /api/e2e/scheduled-workflows`):
  `.grain/workflows/nightly.yml` (with its own `on: schedule`),
  `.grain/schedules/nightly-wf.yml` (`workflow: nightly`, daily 03:00),
  `.grain/templates/walk-declared.yml` and `.grain/triggers/docs.yml`
  (`on: merge`, `paths: [docs/**]`, `template: walk-declared`). Two first
  attempts were refused by the reader with clear errors (a `description:`
  key a template does not have, and a trigger naming that refused
  template); both errors showed on the repository's Advanced tab.
- **One API-seeded row.** A template whose `model` is `claude-opus-5-5` was
  created with `POST /api/templates`, since the UI only offers models a
  connection serves; it exists to show how such a model is presented.

## Steps and outcomes

### 1. Processes list

1. The rail's **Processes** entry (count 9, later 10-12 as rows were added)
   opened `/processes`, with chips **All 9 · Schedules 3 · Workflow
   schedules 0 · Templates 4 · Suites 2 · Triggers 0** and, after the
   declarations were read, **All 10 · Schedules 2 · Workflow schedules 2 ·
   Templates 4 · Suites 1 · Triggers 1**. No console errors or failed
   requests, apart from `/api/tasks` polls the page aborts on navigation
   (`net::ERR_ABORTED`), which are harmless.
2. **Kind chips** are toggles: Schedules → `?kind=schedule`, adding Workflow
   schedules → `?kind=schedule,workflow`, and so on; selecting all five
   returns to `/processes`. The narrowing survived reload. Search for
   `release` wrote `?q=release` and survived reload.
3. **Back/Forward.** Narrowing replaces the history entry rather than
   pushing one (by design, `App.jsx`: a narrowing is "a change of question
   rather than of page"), so Back from a narrowed list leaves the page;
   moving between pages (list → item → Back) keeps the narrowing.
4. **Sort** (`Choose the order this list is shown in`): Last run
   (`sort=last:asc`), Name (`sort=name:asc`), Kind (`sort=kind:asc`: schedules,
   workflow schedules, templates, suites, triggers) and Next run each
   reordered the rows as named. Removing the last sort key is **Custom
   order** (`sort=custom`, "The page's own order, which is the one a drag
   changes").
5. **Reorder.** Tap/keyboard: Enter on `Move docs.yml` put up "Move here,
   above …" slots; picking one moved the row, and the order survived reload.
   Pointer: dragging `Move Weekly Dependency & Vulnerability Audit` onto the
   first row moved it to the top and the order survived reload. (A first
   attempt in a 900px-tall viewport moved a different row: Playwright
   scrolled the page between pressing and moving, so the drag started over
   another row. In a viewport tall enough not to scroll it behaved; not an
   app defect.) The custom order is per browser (`localStorage`
   `grain.list-order.processes`), as `listOrder.js` documents.
6. **Derived runs.** `release-qualification` showed the next run of the
   daily schedule that fires it; templates and suites nothing fires showed
   "on demand"; triggers "on change".
7. **Old links.** `/schedules`, `/templates` and `/suites` landed on
   `/processes?kind=…`; `/suites/repo:suite:acme:widgets:smoke` and
   `/templates/security-audit` opened those items under `/processes/…`;
   `/schedules/nope` and `/processes/suites/runs/999` fell back to the list;
   `/suites/runs/3` landed on `/processes/suites/runs/3`.

### 2. New menu

`+ New` offered **Schedule · Template · Suite** and the note "Workflow
schedules and triggers are declared in a repository's .grain/ directory --
declarations" linking to `docs/declarations.md`. With the chips narrowed to
Schedules, Templates or Suites the button read `+ New schedule` / `+ New
template` / `+ New suite` and opened that form directly; narrowed to
Workflow schedules, Triggers, or two kinds at once, it kept the menu.

### 3. Schedules

1. **Raw task schedule.** Title `Walk raw schedule {{date}}`, description,
   base `main`, read-only repo `acme/shared`, model `gemini-2.5-flash` (the
   picker offered only "Deployment default (gemini-2.5-pro)",
   `gemini-2.5-pro`, `gemini-2.5-flash`), network **None**, priority
   **high**, grant **ci-read** from the Grain API presets, **Every N
   minutes**. With 5 the browser refused to submit ("Value must be greater
   than or equal to 10", helper "At least 10; counted from the last
   firing"); with 10 it saved, and the composition preview was re-requested
   as each field changed. Row: `every 10m · can overlap` (overlap **Start it
   anyway**), next run ten minutes out.
2. **Edit.** Reopening showed every value as saved. Changing the title and
   switching to **Daily, at a time** 09:30 saved to `daily at 09:30`, next
   run 10/30 09:30, and survived reload.
3. **Pause / Resume.** Pause closed the pane; the row read "Held -- fires
   10/30/2026, 9:30:00 AM if resumed by then · paused" and kept it through
   reload. Resume restored it.
4. **Run now** asked "Run "Walk raw schedule edited" now? It starts the task
   … the schedule's own next run is unchanged." The row's last run filled in
   without a reload, and the filed task carried the schedule's model,
   network, reads, `api:ci-read` and priority.
5. **Template schedule** (template `walk-full`, every 2 hours): the form said
   the bound template decides repo and branch and that title, description
   and the rest come from the template. The template's row picked up the
   next run without a reload; after **Run now** ("It starts a task from the
   template "walk-full"…") it picked up the last run too.
6. **Suite schedule** (Fires **A suite**, `Walk suite`): an empty base was
   refused ("required: a suite run stacks its tasks against one branch");
   with `main` and **Monthly** (day 1, 09:00) it saved as `monthly on day 1
   at 09:00 · suite: walk suite`. The suite's row showed 11/1 09:00 without a
   reload, "on demand" while the schedule was paused (also after reload),
   and 11/1 09:00 again after Resume.
7. **Weekly** recurrence and the time-zone picker were shown;
   **Overlap** offers "Skip this run" (default) and "Start it anyway".

### 4. Templates

1. **Full template** `walk-full`: title `Walk {{service}} check`, input
   `service` (label "Which service?", default `api`), bound to
   `acme/widgets @ main`, read-only `acme/shared`, reviewers `ana,bo`,
   auto-merge, **Interactive**, network **Egress**, nothing-needed **Leave
   for acknowledgement**, model `gemini-2.5-pro`, grant `self-debug`,
   priority high. Adding `acme/docs` as a write repo was refused with a clear
   message (not on this deployment's target repo list); without it the
   template saved and reopened with every value intact.
2. **Use…** hid the Approved box and said "This template files a live chat:
   … the chat opens as soon as the task is filed." The "What this files"
   preview credited every row to the template, including "Approval:
   approved when filed — from the template". The preview recomposes when an
   input is left, not per keystroke (documented in `UseTemplateOverlay.jsx`).
   **Create task** filed task 22, "Walk billing check", interactive,
   approved, with the template's settings.
3. **Unserved model.** A template whose model no connection advertises
   shows `claude-opus-5-5 (no connection advertises it)` in its picker.
4. **Dependency-aware delete.** Deleting `walk-full` while a schedule and a
   suite used it asked to confirm, then the pane showed the server's
   refusal: "template is used by 1 schedule(s) ("Walk api check") and 1
   suite(s) ("Walk suite"); repoint or delete the schedules and remove it
   from the suites first".

### 5. Suites

`Walk suite` was created from the New menu with `walk-full` and a template
created inline (`walk-inline`, which appeared in the suite's picker and the
list at once). Modes: "Run until no repo changes or new tasks" (Max passes)
and "Run a fixed number of times" (Number of times, 1-100; 0 refused by the
browser). Saved as fixed ×3 with **Require approval** and **Auto-merge**:
row `run 3× · 2 templates · requires approval`.

### 6. Suite runs

1. **From the suite row** (`Walk suite` → Run…) and **from the generic `+
   Run`** (`Release Qualification Suite`). The run dialog listed what each
   pass files, per template, with each value's source. Both runs appeared
   under **Suite runs** as `active · pass 1`.
2. **Failed:** runs 1 and 2 ended `failed · pass 1 had a task that failed,
   or closed without completing`, on the list and on the run page.
3. **Running → succeeded (run 3, started from the suite row):** with pass 1
   completed (one task with a PR link, one clean) and a proposed follow-up,
   the reconciler filed pass 2. `/processes/suites/runs/3` showed "Pass 1 ·
   3" (the follow-up nested under its parent) and "Pass 2 · 2"; completing
   pass 2 turned the open page to **succeeded** without a reload. The suite
   row's last run became run 3's start time.
4. Clicking the follow-up opened `/tasks/30`; Back returned to the run page;
   the pass task opened `/tasks/28`.

### 7. Repository-declared rows

1. Declared template `walk-declared` and the two workflow schedules (one
   from the workflow's own `on: schedule`, one from
   `.grain/schedules/nightly-wf.yml`) carried "Declared by acme/widgets"
   with the file in its title.
2. Opening them showed the declared notice naming repository and file
   ("…so it cannot be edited or deleted here. Change that file in the
   repository…"; the schedule adds "Run now still fires it once."). Every
   field, `+ Add input`, `Add grant…`, Save, Delete and (schedule) Pause
   were disabled; Run now stayed enabled. No PATCH or DELETE was sent.
3. **The trigger row `docs.yml`** carried no declared chip, and opened
   `/repos/acme/widgets/settings/advanced`, which said only "Read at …: 1
   template(s), 0 suite(s), 2 schedule(s), 1 trigger(s)" -- the bug below.

### 8. Keyboard, cleanup

Tab from the rail reaches `+ New`, `Search processes…`, `Filter`, the sort
button and `+ Run`; rows open with Enter on their name link. Every
throwaway schedule, suite and template was deleted through its pane's
Delete (each with a confirmation; dismissing one left the row in place) and
was gone after reload.

## Bug found and fixed

### Trigger rows were not shown as declared, and their file was nowhere on the page they open

- **Reproduction:** `grain demo -sensitive-hold`, open
  `/processes?kind=trigger`. The `payments.yml` row has no `.declared-chip`,
  unlike every other declared row (`docs/design/settings-panel.md` §5.1:
  "Every row also shows the existing `DeclaredChip` when `declaredIn` is
  set"). Click it: `/repos/acme/widgets/settings/advanced` says "1
  trigger(s)" and never names `.grain/triggers/payments.yml`.
- **Cause:** `ProcessesList.jsx` passed `declaredIn={r.kind === "trigger" ?
  null : r.declaredIn}`; `RepoPage.jsx` rendered declarations only as
  counts (`declaredSummary`).
- **Fix:** trigger rows pass their `declaredIn` like the rest, so the chip
  says "Declared by acme/widgets" with "Declared in acme/widgets,
  .grain/triggers/payments.yml" as its title; the Advanced tab adds a "Path
  triggers: `<path>`, …" line (`.declared-triggers`) under the read summary.
- **Coverage:** `ProcessesList.test.jsx` (the trigger row's chip and title;
  the declared-chip count now includes it), `RepoPage.test.jsx` (the
  trigger file listed by path), and the Playwright test "a trigger row says
  it is declared and opens the tab that names its file" in
  `ui/e2e/path-triggers-sensitive-hold-walkthrough.spec.js`.
- **Rendered:** `ui/e2e/screenshot.mjs` over `grain demo -sensitive-hold` for
  `/processes?kind=trigger` and `/repos/acme/widgets/settings/advanced`, at
  desktop and phone widths, light and dark. Bounding boxes at 1440×900 and
  390×844, both themes: the chip lies inside its row, on the row's chip line
  like other declared rows' chips; the triggers line lies inside the
  viewport; neither page overflows horizontally (`scrollWidth -
  clientWidth = 0`); the line uses the theme's secondary text colour.

## Notes, not defects

- The Next run sort's directions read "Oldest first / Newest first", which
  is the date wording for a time that is in the future.
- A trigger row opens the repository's Advanced tab rather than its own
  file, so with several triggers the reader finds theirs in the list by
  name.
