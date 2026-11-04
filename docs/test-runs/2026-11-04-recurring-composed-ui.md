# Test run — 2026-11-04 — Processes: schedules, templates, suites, suite runs and triggers

An end-to-end walkthrough of grain's recurring and composed-work UI: the
Processes page, and the schedules, templates, suites, suite runs and triggers
it lists. Every step ran in a real Chromium against a real `grain demo`
server. Run by task 2962. It follows
[2026-11-03](2026-11-03-recurring-composed-ui.md). It concentrates on what has
changed since then, chiefly the searchable pickers (`SearchSelect`) that
replaced the schedule, template and suite forms' selects (`af3016a6`,
`32924959`), and the live-only task poll (`05729d6c`, `3417e3da`).

This run was interrupted three times by provider usage limits, and each
resume got a fresh sandbox. The browser walkthrough below is the one the
first run completed. Later runs went into the merge queue's `frontend`
repair (bug 2).

Two bugs were found and fixed on this branch.

## Environment

- Tested `main` at `a3bcafa9` (Merge pull request #693). The fixes are
  `7b68edbe` (bug 1) and `711d29ff` (bug 2).
- UI built with `npm ci && npm run build`. `grain` built afterwards with
  `go build -tags grain_e2e ./cmd/grain`, so the binary embeds the built UI.
  `ui/e2e/releasehelper` was built from the tree. Go 1.26, Node 20.19.0,
  Playwright's Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- Server:
  `grain demo -addr 127.0.0.1:4290 -open=false -access-code-file … -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 2s`,
  over a throwaway store (`TMPDIR=/home/debian/walk/demo-tmp`). The browser
  context signed in through `POST /api/auth/access-code`.
- Browser: ad-hoc Playwright scripts (not committed), at 1400×900. Every
  script logged console errors and warnings, page errors, failed requests
  and every response ≥ 400. **None was seen in any script of this run.**
- No Gemini key at `/home/debian/.gemini-api-key`, so nothing used Gemini.
  The demo's connections advertise `gemini-2.5-pro` (the deployment default)
  and `gemini-2.5-flash`.

## Stubs and seeds

Every create, edit, run, pause, resume and delete went through visible
controls in the browser. Only these stood in for a boundary the demo does not
run:

| What | How | Why |
|---|---|---|
| Repository declarations | Pushed into the `-github-sim` bare repo's `main`:<br>• `.grain/templates/nightly.yml`: priority 7, a `scope` field (go/npm, default go), `capabilities: [read-issues]`.<br>• `.grain/templates/pairing.yml`: `interactive: true`, `keep-running: true`.<br>• `.grain/templates/opus.yml`: `model: claude-opus-5-5`.<br>• `.grain/suites/sweep.yml`: pairing + opus, until-clean, max 3, approval.<br>• `.grain/schedules/{nightly,sweep,wf}.yml`: template / suite with `base: main` / `workflow: nightly`.<br>• `.grain/workflows/nightly.yml` (`on: schedule`) and `.grain/triggers/design.yml` (`on: merge`, `design/**`, template opus). | `-scheduled-workflows` reads the sim repo, which starts empty. Declarations were switched on through the repository's Advanced tab ("Read acme/widgets's own declarations"), and the real `declarations` reconciler did every read. |
| A capability policy | `PUT /api/capability-policy`:<br>• deny `…/cloud#cloud-viewer` to tasks on acme/widgets.<br>• admit `…/cloud#cloud-key` on acme/gadgets only. | Gives the pickers verdicts to show. The policy editor itself is Settings' area. |
| Probes | `curl`/`ctx.request` reads of `/api/schedules`, `/api/templates`, `/api/tasks`, `/api/schedules/<id>/composition`, `/api/repos/acme/widgets/declarations`, `/api/capability-methods`. | To confirm what was persisted. |

## Steps and outcomes

### 1. Declarations, the rail and the list

1. A first push declared `capabilities: [repo-read:acme/upstream]` on
   `nightly.yml`. The declarations status said, verbatim, "storing
   acme/widgets's declarations: .grain/templates/nightly.yml: this
   deployment may not work in acme/upstream (its target repos are
   acme/widgets), so this template's capabilities cannot reach it". That is
   the documented team boundary (`docs/declarations.md`), refused rather
   than dropped silently. Changed to `read-issues`, then turning
   declarations off and on through the Advanced tab read the new commit.
   - The brief says a declared template "carries no capabilities of its
     own". That is out of date: `docs/declarations.md` documents
     `capabilities:` on declared templates and schedules, bounded by the
     filing team, and this run's `nightly` carried `read-issues` into Use…'s
     preview ("Capabilities · read-issues · from the template").
2. Inbox → rail **Processes** → `/processes`. The rail read "Processes 15",
   and the page **All 15**: schedules 4, workflow schedules 2, templates 6,
   suites 2, triggers 1.
3. Templates and suites derive last and next runs from the schedules that
   fire them directly:
   - `release-qualification` and `Declared opus` show their schedules'
     next runs (11/5 00:00 and 02:00).
   - `Declared sweep` shows its weekly schedule's 11/6 18:00.
   - `Declared pairing` (reached only through the scheduled suite),
     `Declared nightly`, `security-audit` and `Release Qualification Suite`
     read "on demand". The trigger reads "on change".

### 2. New menu

**+ New** offers Schedule, Template and Suite, with "Workflow schedules and
triggers are declared in a repository's .grain/ directory -- declarations",
linked to `docs/declarations.md` on GitHub. With the chips narrowed to
Schedules, Templates or Suites the button reads **+ New schedule / template
/ suite** and opens that form directly. Narrowed to Triggers it stays a plain
**+ New** menu, since nothing there is creatable.

### 3. A raw task schedule (`WT2962 raw sweep`)

1. **New schedule** offers Fires (A task / A suite), the Template
   `SearchSelect` ("None -- fill in the fields below"), Repos, Base branch,
   another writable repo, Model, Title, Description, Read-only repos,
   Auto-merge, Keep running, Network access, When the agent finds nothing to
   do, Legacy capabilities, Grants, the deployment's custom fields, Repeat,
   Time zone and the overlap policy, with a live "What this schedule files"
   preview.
2. Repeat offers every N hours, every N minutes, daily, weekly and monthly.
   Every N minutes = 5 is refused by the browser ("Value must be greater
   than or equal to 10."), under "At least 10; counted from the last
   firing". Saved at 15.
3. Overlap: "Skip this run" / "Start it anyway". The latter explains
   "Firings can pile up: this schedule can have several unfinished at once."
4. Model offers only Deployment default (gemini-2.5-pro), gemini-2.5-pro and
   gemini-2.5-flash: what the connections advertise. Chose flash.
5. Read-only repo `acme/upstream` (typed), Keep running ticked, network
   Egress, custom field Requested by = walker.
6. **Add grant…** opens "Grants to add": the Grain API presets (task-admin,
   ci-read, dispatch-control, deployment-ops (dangerous), release-manager,
   self-debug, self-upgrade (dangerous), pipeline-control, team-broadcast,
   memory, release-notes, test-quarantine, release-gate-read, external-dev,
   external-read, ops/triage), the scopes by group, then "Capability server
   methods". `ci-read` was added and showed as `api:ci-read`.
7. Saved, `GET /api/schedules` had every value: `everyNMinutes: 15`,
   `overlap: allow`, `networkAccess: open`, `reads: [acme/upstream]`,
   `capabilities: [api:ci-read]`, `model: gemini-2.5-flash`,
   `keepRunning: true`, `customFields.requested-by: walker`. The row read
   `every 15m · can overlap`, next 15 minutes on.
8. `/processes/schedules/sched-3` reopened the pane directly with Keep
   running ticked, 15 minutes, acme/upstream and the model box on
   gemini-2.5-flash. **But its preview said "Model · the deployment's
   default model (gemini-2.5-pro) · the deployment decides"** (bug 1).
9. **Policy verdicts on a schedule bound to acme/widgets**, in its Add
   grant…:
   - cloud-key: "the capability policy does not admit …#cloud-key for this
     task on acme/widgets (settings → capabilities)".
   - cloud-viewer: "the capability policy denies …#cloud-viewer to this
     task on acme/widgets (rule 1: deny …#cloud-viewer) (settings →
     capabilities)".

### 4. Templates

1. **New template** `wt2962-full`:
   - Task priority 2.5, 0, -1 and `abc` are all blocked by the browser's own
     whole-number rule ("The two nearest valid values are 2 and 3.",
     "Value must be greater than or equal to 1."), under "optional -- every
     task this template files starts at this priority; default 5, higher
     numbers run first". Saved at 8.
   - Reviewers ana, bo; Interactive, Keep running and Auto-merge on;
     network None; model gemini-2.5-flash (same three choices); Requested
     by; grant `self-debug`.
   - `GET /api/templates` had all of it: `priority: 8`, `interactive`,
     `keepRunning`, `autoMerge`, `networkAccess: none`,
     `model: gemini-2.5-flash`, `capabilities: [api:self-debug]`,
     `reviewers: [ana, bo]`.
2. **Policy verdicts on an unbound template**: cloud-key and cloud-viewer
   read "may be refused by the capability policy for agent tasks (settings →
   capabilities)", since no one repository is known.
3. **From a schedule**: New schedule → the Template `SearchSelect` lists
   None, wt2962-full, the three declared templates, security-audit and
   release-qualification. Typing `wt2962` narrows it to wt2962-full. Picking
   it hides Title and Keep running, and the model box reads "The template's
   model". The preview read every row "from the template", including
   `Keep running · on` and `Task priority · 8`. Saved daily 09:30 as
   sched-4 (`type: template`).
4. **Run now** confirmed with: "Run "WT2962 tidy the release notes" now? It
   starts a task from the template "wt2962-full" straight away; the
   schedule's own next run is unchanged." Task 20 was filed with
   `priority: 8`, `keepRunning`, `interactive` and `gemini-2.5-flash`. The
   schedule and template rows both showed last 9:54:10 AM and next 11/5
   9:30.
5. **Pause** from the pane (no confirmation, reversible) closed it. Without
   a reload, the schedule row read "Held -- fires 11/5/2026, 9:30:00 AM if
   resumed by then · paused" and the template row went to "on demand". Still
   so after a reload. **Resume** restored both rows without a reload.
6. **New task from this template** on wt2962-full opened `/new` with
   priority 8. On the declared `Declared nightly`, which has an input, it
   opened **File a task from a template** (Use…) instead, with the scope
   picker on `go` and a preview reading "Bump go dependencies".
7. **Use…** on wt2962-full shows "This template files a live chat: the agent
   can talk with you while it works, and the chat opens as soon as the task
   is filed." Its preview lists `Keep running · on` and `Task priority · 8`.
   **Create task** opened `/tasks/21`, filed with `priority: 8`,
   `keepRunning`, `interactive`, `gemini-2.5-flash` and `api:self-debug`.
8. **Edit**: the title was changed and saved. `/processes/templates/template-3`
   reopened with the new title, priority 8, Keep running, Interactive and
   gemini-2.5-flash.
9. **A model no connection serves**: the declared `opus.yml` pane's model
   box reads "claude-opus-5-5 (no connection advertises it)".
10. **A declared template's pane** says "This template is declared by
    acme/widgets in .grain/templates/opus.yml, so it cannot be edited or
    deleted here. Change that file in the repository and grain picks it up
    within a couple of minutes." All 26 of its inputs are disabled, and so
    are Delete and Save. New task from this template stays available.

## Bugs found and fixed

### 1. A saved schedule's preview ignored its own model

**Reproduction:** save a raw task schedule with Model gemini-2.5-flash, then
open it (`/processes/schedules/<id>`). The model box reads gemini-2.5-flash,
but "What this schedule files" says "Model · the deployment's default model
(gemini-2.5-pro) · the deployment decides". Picking gemini-2.5-pro makes the
preview read "gemini-2.5-pro · from the schedule". Picking flash again
reverts it to the deployment default. `GET /api/schedules/<id>/composition`
had `model: gemini-2.5-flash · schedule`, so only the pane was wrong. A
template schedule with its own model override would preview the template's
model the same way.

**Cause:** the pane POSTs its request to `/api/schedules/composition`, a
stateless draft endpoint. The request leaves the model out when it has not
changed, so an edit leaves the saved model alone
(`ui.UpdateScheduleRequest.Model`). The draft endpoint has no saved row to
fall back on, so it read the missing model as no model.

**Fix (`7b68edbe`):** `ScheduleOverlay.jsx` previews `previewRequest()`,
which is the save request with the form's model always in it. The save is
unchanged.

**Coverage:** `App.schedules.test.jsx` "previews a saved schedule with the
model it asks for" checks the preview's body carries the saved model, and
that the save still leaves it out. It fails without the fix.

### 2. Leaving an edited schedule through its task link could ask twice

**Reproduction:** `frontend` on this pull request, and on `main` at
`d665c1a`. `App.schedules.test.jsx` "guards unsaved schedule edits before
following the suppressed task link" failed with "expected … to be called 2
times, but got 3 times": after the person had said "discard" once, the
question was put again. It is load-dependent. The file, six concurrent runs
of it, and the whole suite with `--maxWorkers=4` (246 files, 3856 tests) all
pass locally.

**Cause:** the suppressing-task link asks `DISCARD_PROMPT` itself, then
closes the pane and opens the task. Until the pane has unmounted, every other
guard (its `Overlay`'s, and App's `leavingLosesEdits()` callers) still reads
the form as dirty, so any of them asked in that window asks again.

**Fix (`711d29ff`):** once the person has confirmed the discard through that
link, `ScheduleOverlay.jsx` reports no unsaved edits for the rest of the
pane's life, so nothing can ask a second time. The existing test, which pins
exactly two asks, is the regression coverage.

## Notes, not defects

- Task priority's whole-number rule is now the browser's own validation
  message, not the custom "a whole number, 1 or more" helper 2026-11-03
  recorded. Submission is still blocked.
- A template reached only through a scheduled suite still reads "on demand"
  (as on 11-03).
- Pause and Resume still do not confirm. Both are reversible.
- Processes rows are `.process-row` list items, not table rows. A script
  looking for `tr` finds nothing.

## Not reached in this run

The usage-limit interruptions ended the browser walkthrough after §4. These
parts of the brief were covered on 2026-11-03, against `main` at `0fdeff3b`,
and were not repeated here:
- Search, sort and custom-order reorder, and `?kind=`/`?q=` through reload
  and Back/Forward.
- Old `/schedules`, `/templates` and `/suites` links.
- Suite create/edit, inline template, modes, and suite delete.
- Suite schedules, suite runs (from the row and from + Run), pass grouping,
  follow-ups, failure, and the base-less "default branch" chip.
- A declared workflow schedule's Run now, and the trigger row's link to the
  Advanced tab.
- Dependency-aware template delete, and cleanup.
- Phone width and dark.
- Real agent runs and real CI execution.

## Tests run

- `npx vitest run src/App.schedules.test.jsx`: 25 passed, both before and
  after the fixes. The new test fails without bug 1's fix.
- `npx vitest run --maxWorkers=4` (the CI `frontend` command), whole UI
  suite: 246 files, 3856 tests passed.
- `npx vitest run src/App.schedules.test.jsx src/components` after bug 2's
  fix: 170 files, 2484 tests passed.
