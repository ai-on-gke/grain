# Test run — 2026-11-03 — Processes: schedules, templates, suites, suite runs and triggers

An end-to-end walkthrough of grain's recurring and composed-work UI: the
Processes page, and the schedules, templates, suites, suite runs and triggers
it lists. Every step ran in a real Chromium against a real `grain demo`
server. Run by task 2701. It follows
[2026-11-02](2026-11-02-recurring-composed-ui.md) and adds the cases that run
did not reach:
- Keep running.
- Capability policy verdicts in the grant pickers.
- New task from this template, and how it differs for a template with inputs.
- The priority shown in composition previews.

Two bugs were found and fixed on this branch, both about Keep running (below).
A third finding, outside Processes, was filed as a follow-up.

## Environment

- Tested `main` at `0fdeff3b` (Merge pull request #540). The fixes are
  `993ff305` (forms) and `4a8d1341` (composition preview), with the
  Playwright spec in `75275777`.
- UI built with `npm ci && npm run build`. `grain` built afterwards with
  `go build -tags grain_e2e ./cmd/grain`, so the binary embeds the built UI.
  (Building both at once embeds an empty `pkg/ui/static`, and the demo then
  answers 404 for `/repos/...`. That cost one restart.) `ui/e2e/releasehelper`
  was built from the tree. Go 1.26, Node 20.19.0, Playwright's Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- Server:
  `grain demo -addr 127.0.0.1:4290 -open=false -access-code-file … -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 2s -sensitive-hold`,
  over a throwaway store (`TMPDIR=/home/debian/walk/demo-tmp`). The browser
  context signed in through `POST /api/auth/access-code`.
  - §1–§8 ran on a binary of `main` plus `993ff305`. The form fix was made
    early, once its absence had been seen in the browser.
  - §9 ran on a binary with both fixes, after a restart on a fresh store.
- Browser: ad-hoc Playwright scripts (not committed), mostly at 1400×900.
  Phone checks ran at 390×844 with touch, and dark with `colorScheme: dark`.
  Every script logged console errors and warnings, page errors, failed
  requests and every response ≥ 400. The only one was the expected 400 on
  the dependency-refused delete (§4.6). Playwright's `net::ERR_ABORTED` on
  the app's 304 polls and 204 deletes was filtered, as on 11-01 and 11-02.
- No Gemini key at `/home/debian/.gemini-api-key`, so nothing used Gemini,
  and `LOOK=1` could not describe screenshots. Layout was checked with
  bounding boxes instead (§9). The demo's connections advertise
  `gemini-2.5-pro` (the deployment default) and `gemini-2.5-flash`.

## Stubs and seeds

Every create, edit, run, pause, resume and delete went through visible
controls in the browser. Only these stood in for a boundary the demo does not
run:

| What | How | Why |
|---|---|---|
| Repository declarations | Pushed into the `-github-sim` bare repo's `main`:<br>• `.grain/templates/nightly.yml`: priority 7, a `scope` field with options go/npm.<br>• `.grain/templates/pairing.yml`: `interactive: true`, `keep-running: true`.<br>• `.grain/templates/opus.yml`: `model: claude-opus-5-5`.<br>• `.grain/suites/sweep.yml`: pairing + opus, until-clean, max 3, approval.<br>• `.grain/schedules/{nightly,sweep,wf}.yml`: template / suite with `base: main` / `workflow: nightly`.<br>• `.grain/workflows/nightly.yml` (`on: schedule`) and `.grain/triggers/design.yml`. | `-scheduled-workflows` reads the sim repo, which starts empty. The real `declarations` reconciler did every read, on its two-minute poll. |
| A capability policy | `PUT /api/capability-policy`, the rules `capability-policy-pickers.spec.js` uses:<br>• deny `…/cloud#cloud-viewer` to tasks on acme/widgets.<br>• admit `…/cloud#cloud-key` on acme/gadgets only. | Gives the pickers verdicts to show. The policy editor itself is Settings' area, not this one. |
| An agent finishing a suite pass | `releasehelper -dir <store> complete-suite-pass`, then `sync-suites` (one `orchestrator.SyncSuites`). | No agents run in the demo. |
| An agent's `propose_task` | `POST /api/tasks` with `proposedBy: "24"` (run 1), and again from run 3's first task. | Records the same `proposed-by` link an agent's proposal does. |
| CI executing a workflow run | `POST /api/e2e/fake-ci/runs/fake-ci-1/finish {"conclusion":"failure","job":"test"}` | `-fake-ci` never finishes a run by itself. |
| Probes and one seed | `curl`/`ctx.request` reads of `/api/schedules`, `/api/templates`, `/api/suites`, `/api/suite-runs`, `/api/tasks/<id>`, `/api/capability-methods`. `POST /api/suite-runs` with `base: ""`. `POST /api/templates` for `wt2701-long-grant` (grant `api:ci-read@acme/widgets`, priority 9, keep running) for the §9 phone checks. | To confirm what was persisted, and to seed one presentation case. |

## Steps and outcomes

### 1. Processes list, chips, search, query, old links

1. Inbox → rail **Processes** → `/processes`. On a cold load the rail said
  14 and the page **All 15**; after opening Processes the rail said 15.
  That is the documented trigger trade-off (2026-11-01 notes).
2. Kind chips: Schedules 4 (`?kind=schedule`), Workflow schedules 2,
  Templates 6, Suites 2, Triggers 1. Each toggled on and off, back to
  `/processes`.
   - The two workflow rows are by design: `.grain/workflows/nightly.yml`'s
     own `on: schedule` cron is one (docs/schedules.md "a workflow's own
     cron is a schedule too"), and `wf.yml` is the other.
3. Templates + Suites + search "declared" gave
  `?kind=template,suite&q=declared`, and every chip's count followed the
  search (all 4, schedules 2, workflow schedules 1, templates 0, suites 1,
  triggers 0). Reload kept the query, the chips and the search text.
  Rail → Inbox, Back, Forward and Back again all landed on the same
  narrowed view.
4. Old links:
   - `/schedules` → `?kind=schedule`; `/templates` → `?kind=template`;
     `/suites` → `?kind=suite`.
   - `/suites/repo:suite:acme:widgets:sweep` → that suite's pane, with its
     declared notice.
   - `/suites/runs/99` and `/schedules/nope` fell back to `/processes` with
     `no suite run "99"` and `no schedule "nope"`.
   - `?kind=bogus` and `?sort=bogus:up` normalised to `/processes`.
5. Sort, through the toolbar: **Sort** → Direction "Latest first" wrote
  `?sort=next:desc`, and Attribute "Name" wrote `?sort=name:desc`. The
  attribute list names kind, source, type, name, target repo, base branch,
  template, suite, enabled, cadence, time zone, auto-approve proposals,
  auto-merge, overlap, requires approval, next run, last run, last outcome
  and created. **Remove sort 1** gave `?sort=custom` / "Custom order", which
  survived reload.
   - `name:asc`, `name:desc`, `last:desc` and `kind:asc` each ordered the
     rows as labelled.
6. Custom order: 17 **Move …** handles. Each way of moving worked:
   - Tap: handle 3, then "Move here, above Declared nightly" put it first.
   - Keyboard: focus a handle, Enter shows 16 slots, Escape leaves 0.
   - Keyboard: focus handle 1, Enter, focus "Move here, to the end of the
     list", Enter moved it to the end.
   - Pointer drag of handle 4 onto the first row.
   - The order survived reload.
7. Last and next runs on templates and suites follow the schedules that
  fire them directly (§3.4–§3.7). A template reached only through a
  scheduled suite stays "on demand": `opus` sits in the weekly declared
  sweep. That is how `processes.js` derives it (`nextThrough("templateId")`),
  and it is noted below, not changed.

### 2. New menu

**+ New** offers Schedule, Template and Suite, with "Workflow schedules and
triggers are declared in a repository's .grain/ directory -- declarations."
The link goes to `https://github.com/ai-on-gke/grain/blob/main/docs/declarations.md`.
With the chips narrowed to Schedules, Templates or Suites, the button reads
**+ New schedule / template / suite** and opens that form directly.

### 3. Schedules

1. **Raw task schedule** (`WT2701 raw keep-running sweep`):
   - Fires offers "A task" and "A suite". Repeat offers every N hours, every
     N minutes, daily, weekly and monthly. The Template picker lists "None
     -- fill in the fields below" and every template.
   - Every N minutes = 5 is refused by the browser ("Value must be greater
     than or equal to 10."), under "At least 10; counted from the last
     firing". Saved at 15.
   - Overlap **Start it anyway**; model gemini-2.5-flash; read-only repo
     `acme/upstream` (typed: the demo offers only acme/widgets); network
     Egress; custom field Requested by = walker.
   - **Add grant…** opens the Grain API group: task-admin, ci-read,
     dispatch-control, deployment-ops (dangerous), release-manager,
     self-debug, self-upgrade (dangerous), pipeline-control,
     team-broadcast, memory, release-notes, test-quarantine and
     release-gate-read, then the scopes by group. `api:ci-read` was
     added.
   - **Keep running** ticked. This box is new on this branch (bug 1).
   - Saved, the API had every value: `everyNMinutes: 15`,
     `overlap: allow`, `networkAccess: open`, `reads: [acme/upstream]`,
     `capabilities: [api:ci-read]`, `model: gemini-2.5-flash`,
     `keepRunning: true`. The row read `every 15m · can overlap`, next 15
     minutes on. After a reload, `/processes/schedules/sched-3` reopened
     with Keep running ticked and 15 minutes.
2. **Model picker**: a raw schedule offers Deployment default
   (gemini-2.5-pro), gemini-2.5-pro and gemini-2.5-flash, which is only what
   the connections advertise. One firing a template offers "The template's
   model" as its empty choice. One firing a suite has no model picker.
3. **From a template** (`wt2701-full`, daily 09:30): the content fields,
   Keep running included, give way to the template. The live preview read
   `Task priority 8 · from the template`, `Model gemini-2.5-flash · from the
   template` and `Capabilities api:self-debug · from the template`. It had
   **no Keep running row**, although the template keeps running (bug 2).
4. **Run now** confirmed with: "Run "WT2701 tidy the release notes (edited)"
   now? It starts a task from the template "wt2701-full" straight away; the
   schedule's own next run is unchanged." Both the schedule row and the
   template row then showed last 3:25:20 and next 9:30, without a reload.
5. **Pause** closed the pane. The row read "Held -- fires 11/3/2026, 9:30:00
   AM if resumed by then · paused", and the template row went to "on
   demand". **Resume** restored both, and both stayed after a reload.
   Neither asks for confirmation; both are reversible.
6. **Edit** to monthly on day 31 at 08:00 read `monthly on day 31 at 08:00`,
   next 11/30, clamped.
7. **Suite schedule** (Fires: A suite, `WT2701 suite`, weekly Friday 17:45
   America/New_York):
   - An empty base branch blocked the submit ("Please fill out this
     field.", helper "required: a suite run stacks its tasks against one
     branch").
   - With `main` it saved with next 11/6 22:45 UTC, and the suite's row
     went from "on demand" to that time without a reload.
8. **Delete**:
   - The dialog reads "Delete the schedule "WT2701 suite"? Suite runs it
     already started are not affected." (for a task schedule: "Tasks it
     already filed are not affected.").
   - Dismissing kept it. Accepting removed it, and the suite's row went
     back to "on demand".
9. Escape inside an open autocomplete closes the whole pane, behind the
   browser's "You have unsaved changes here. Leave and discard them?"
   prompt. The prompt guards the edit, so nothing is lost silently.

### 4. Templates

1. **Task priority** in New template: `2.5`, `0` and `-1` each showed "a
   whole number, 1 or more; default 5, higher numbers run first" under the
   box, with no request. `1e3` is valid and saved as 1000 (the pane
   reopened with 1000). That throwaway was deleted from its pane: dismiss
   kept it, accept removed it.
2. **Fully populated** `wt2701-full`:
   - Title, description, reviewers ana and bo, priority 8.
   - Interactive, **Keep running** (new, bug 1) and auto-merge.
   - Network None; model gemini-2.5-flash (the picker lists only the
     advertised models); custom field Requested by.
   - Grant `api:self-debug`.
   - The API had all of it: `priority: 8`, `interactive: true`,
     `keepRunning: true`, `networkAccess: none`, `model: gemini-2.5-flash`,
     `capabilities: [api:self-debug]`, `reviewers: [ana, bo]`. After a
     reload, `/processes/templates/template-4` reopened with every value.
     Its title was edited and saved.
3. **Capability policy verdicts** in the template's Add grant…:
   - An unbound template's grants are judged for no one repository:
     cloud-key and cloud-viewer read "may be refused by the capability
     policy for agent tasks (settings → capabilities)".
   - `GET /api/capability-methods?kind=task&repo=acme/widgets` gives the
     per-repository answers: cloud-viewer "denies … (rule 1: deny …)", and
     cloud-key "does not admit … for this task on acme/widgets".
   - The in-process **repo server › read** reads "denied by the capability
     policy" whatever the policy says. See the follow-up below.
4. **New task from this template** on `wt2701-full` opened `/new` on that
   template, with its title and **priority 8**. On the declared `Nightly
   bump`, which has an input, it opened **Use…** ("file a task from a
   template") instead, with the Scope picker and a preview reading "Bump go
   dependencies".
5. **Use…** on `wt2701-full` shows "This template files a live chat: the
   agent can talk with you while it works, and the chat opens as soon as
   the task is filed." **Create task** opened `/tasks/22`, filed with
   `priority: 8`, `keepRunning: true`, `interactive: true` and
   `model: gemini-2.5-flash`.
6. **Dependency-aware delete** of `wt2701-full`, used by schedule sched-4
   and suite `WT2701 suite`:
   - The confirm reads "Schedules and suites using it must be repointed or
     deleted first".
   - The server refused (HTTP 400) with "template is used by 1 schedule(s)
     ("WT2701 tidy the release notes (edited)") and 1 suite(s) ("WT2701
     suite"); repoint or delete the schedules and remove it from the suites
     first", shown in the pane, and the template stayed.
   - Once both were deleted through their panes, `wt2701-inline` deleted
     cleanly (§6.5).
7. **A model no connection serves**: the declared `opus.yml` pane's model
   field reads "claude-opus-5-5 (no connection advertises it)".

### 5. Suites

1. **New suite** `WT2701 suite`:
   - The Templates picker names each template's title and bound repo.
   - **+ New template** opened a nested New template form. Saving
     `wt2701-inline` added it to the suite's selection
     (`wt2701-full, wt2701-inline`).
2. Until-clean Max passes: 101 was refused ("Value must be less than or
   equal to 100.") and 99 saved. The row read `run until clean (max 99) · 2
   templates`.
3. `WT2701 throwaway suite`, fixed number of times:
   - 0 was refused ("Value must be greater than or equal to 1.").
   - Count 2, Require approval on and auto-merge off saved as
     `mode: count, count: 2, requireApproval: true, autoMerge: false`.
   - The row read `run 2× · 1 template · requires approval`.
4. **Delete** the throwaway: "Delete the suite "WT2701 throwaway suite"? Runs
   already started from it are unaffected." Dismissing kept it. Accepting
   removed it, and it was still gone after a reload.

### 6. Suite runs

1. **From the suite row** (`WT2701 suite` → Run…, branch main):
   - The dialog composes "what each pass files", "repeated until a pass
     files no pull request and proposes nothing, up to 99 passes", with
     each field's source.
   - Run 1 appeared active, pass 1, tasks 24 and 25 approved.
2. **From the generic + Run** (Suite: Declared sweep, branch release/1.0):
   - The options are declared sweep, release qualification suite and
     wt2701 suite. The preview reads "up to 3 passes".
   - Run 2 appeared `active · acme/widgets · release/1.0 · pass 1 · run
     until clean (max 3) · each pass's tasks wait for approval`, with tasks
     27 and 28 unapproved.
   - The preview's Branch row reads "the repo's default branch · from the
     template", and tasks 27 and 28 were filed with no base. That is the
     documented rule (docs/schedules.md, `Template.FiringTarget`): a bound
     template, which every declared template is, keeps its own empty base
     rather than taking the caller's. The preview said so before the run.
3. **Follow-up**: task 26, proposed by 24 (stub). Run 1's pane lists it
   under 24, marked unapproved.
4. **Succeeded**:
   - One complete + sync left run 2 `succeeded`.
   - Run 1, with its proposal, filed pass 2 (tasks 29 and 30), and a second
     complete + sync left it `succeeded`.
   - `/processes/suites/runs/1` groups pass 1 (24, its follow-up 26, 25)
     and pass 2 (29, 30). Each task shows its chips: p8, "keeps running"
     and "api preset: self-debug".
5. **Failed**:
   - Run 3 (`WT2701 suite` row → Run…, main) got a proposal from its first
     task.
   - Its sched-4, the suite and then `wt2701-inline` were deleted through
     the UI.
   - complete + sync left run 3 **failed** with "pass 2 could not be filed:
     template template-5 no longer exists", on its row and its pane.
6. **Navigation**: `/processes/suites/runs/{1,2,3}` open each pane directly.
   Run 1's task link `/tasks/24` opened the task, and Back returned to
   `/processes/suites/runs/1`.
7. **Base-less run**: `POST /api/suite-runs` with `base: ""` is a 400 ("base
   is required: a suite run has no default branch to fall back to"). Since
   2026-11-02's follow-up, `docs/declarations.md` also refuses a `suite:`
   schedule with no `base:` at read time. So no supported path makes a
   base-less run any more, and its "default branch" chip
   (`SuiteRunPane.test.jsx`) could not be shown live.

### 7. Repository-declared rows

- Every declared template, suite, schedule, suite schedule and workflow
  schedule shows **declared by acme/widgets** on its row.
- Each pane names the file, for example: "This schedule is declared by
  acme/widgets in .grain/schedules/wf.yml, so it cannot be edited or deleted
  here. Change that file in the repository and grain picks it up within a
  couple of minutes. Run now still fires it once." A suite's pane adds "It
  can still be run from the suites list".
- No input is editable (`:disabled`): 0 of 22, 6, 6, 7 and 5. Delete, Save,
  Pause, + Add input, Add grant… and + New template are disabled. Run now
  and "New task from this template" stay available.
- **Workflow schedule Run now** confirmed with: "It starts a CI run of the
  workflow it was declared in straight away, to be reviewed when it
  finishes; the schedule's own next run is unchanged".
   - The row read "Last run fake-ci-1 on main: running".
   - Once the run was finished as a failure: "failure 11/3/2026, 3:32:44 AM,
     reviewed in task 34".
- The **trigger** row `design.yml` (next "on change") links
  `/processes/triggers/acme/widgets/.grain/triggers/design.yml`. That opens
  `/repos/acme/widgets/settings/advanced`, which lists
  ".grain/triggers/design.yml".
- Turning a repository's declarations off and straight back on (1.5 s
  apart) did not force a read. The status must clear first, as
  `workflow-schedules-candidate-walkthrough.spec.js` waits for. The read
  came on the two-minute poll.

### 8. Keyboard

From the search box, Tab reaches:
1. Filter, Sort, and the suite runs' + Run.
2. The header (Alerts, System, Settings).
3. Every kind chip.
4. Each row's name link, a workflow row's linked CI run (`fake-ci-1`) and
   review task (`task 34`), and Use….

Enter on a focused row link opened `/processes/schedules/sched-3`.

### 9. On the fixed binary: phone, dark, layout, cleanup

On a fresh demo built with both fixes, once the declarations had been read:
- **Use…** on `wt2701-long-grant` (seeded: `api:ci-read@acme/widgets`,
  priority 9, keep running), at 390 and 1400, light and dark:
   - "Keep running · on · from the template" and "Task priority · 9 · from
     the template".
   - No preview row's `scrollWidth` exceeds its `clientWidth`. The pane is
     390/390 and the document does not scroll sideways, so 2026-11-02's
     grant fix holds.
- The declared `pairing` template's Use… reads "Keep running · on · from the
  template". Its pane shows the Keep running box ticked and disabled, where
  before nothing showed the file's `keep-running: true`.
- **Bounding boxes** of the new boxes at 390 and 1400, light and dark:
   - The template pane's Keep running label sits at the same x as
     Interactive, directly under it (y 397 → 439 at 390; 425 → 467 at
     1400), 42 px tall.
   - The schedule pane's sits under Auto-merge the same way.
   - All of them are inside the pane, with no sideways scroll.
- `/processes` at 390 and 1400, light and dark: no sideways scroll.
- Cleanup through the UI: every `WT2701`/`wt2701` schedule, suite and
  template was deleted, each with its confirmation. `?q=wt2701` then shows
  "No processes match your search." Seeded and declared rows were left
  alone. Tasks filed during the run stay in the throwaway stores.

## Bugs found and fixed

### 1. Neither the template nor the schedule form could say Keep running

**Reproduction:** Processes → + New → Template. The form's only boxes are
"Auto-merge once checks pass" and "Interactive (agent can chat with you)".
New schedule (raw task) has only Auto-merge. Yet:
- `POST /api/templates` and `POST /api/schedules` take `keepRunning` (since
  migration 74).
- `model.Compose` carries it to every task filed.
- A declared template's `keep-running: true` is honoured.

So a person could not make a recurring keep-running task without the API,
and a template that had it set showed nothing of it.

**Fix (`993ff305`):**
- `TemplateOverlay.jsx` gets a **Keep running** checkbox under Interactive,
  read back from the template and sent on Save.
- `ScheduleOverlay.jsx` gets one under Auto-merge for a raw task schedule,
  sent only when no template is attached, as auto-merge is. That avoids the
  API's `refuseIgnoredScheduleContent` 400.

**Coverage:**
- `TemplateOverlay.test.jsx` ("sends Keep running on a new template", "reads
  a saved Keep running back and clears it on an edit").
- `App.schedules.test.jsx` ("reads and saves a schedule's Keep running").
- All three fail without the fix.
- Live in §3.1 and §4.2.

### 2. The composition preview never said Keep running

**Reproduction:** on a template or raw schedule with `keepRunning: true`,
open Use…, the schedule pane, or a suite's Run…. The "What this files" panel
lists title through approval, and priority when set, but no Keep running.
`CompositionPanel.jsx` already has the label (`keepRunning: "Keep
running"`), but `ComposedTask` (`pkg/ui/composition.go`) never sent the
field.

**Fix (`4a8d1341`):** the composition sends a `keepRunning` row when it is
on, with the layer that set it (template or schedule). Like the priority
row, it is left out when off.

**Coverage:**
- `pkg/ui/composition_test.go` `TestCompositionShowsKeepRunningWhereItIsOn`
  covers a template, a draft raw schedule and a template without it.
- `ui/e2e/processes-keep-running.spec.js` (`75275777`):
   - Sets the box from both forms and sees it reopen ticked.
   - Reads "Keep running · on · from the template / from the schedule" in
     the preview, and no row while the box is off.
   - Files a task from Use… and checks its `keepRunning`.
   - Saves `false` on unticking.
- Live in §9.

## Follow-up filed

**Grant pickers blame the capability policy for repo#read being CI-only.**
- `model.CapabilityPolicy.VerdictForKind` returns `denied` for `RepoReadRef`
  on every non-CI run before reading a rule.
- `GrantsPicker.jsx` words every `denied` as "denied by the capability
  policy … (settings → capabilities)". That points at a policy with nothing
  to change, even with `{"policy":{}}`.
- The method's own refusal (`model.CIOnlyRefusal`) says the credential
  "writes to the repository itself", which is wrong for `read`.

It is Settings/capabilities territory rather than Processes, so it was
proposed separately with the reproduction.

## Notes, not defects (for the next pass)

- **A template reached only through a scheduled suite reads "on demand".**
  Only schedules that fire a template directly give it a next run.
- **A bound template ignores a suite run's branch** (§6.2), as documented.
  Every declared template is bound, so a declared suite run "on
  release/1.0" files on the default branch. The preview says so.
- **Deleting a template an active suite run still needs is not warned
  about** (§6.5, as on 11-01 and 11-02).
- **Pause and Resume do not confirm.** Both are reversible.
- **Escape in an open autocomplete** bubbles to the pane's close, behind the
  unsaved-changes prompt.
- The task brief says a declared template "carries no capabilities of its
  own". `docs/declarations.md` now documents `capabilities:` on declared
  templates and schedules, bounded by the filing team.

## Not reached

- Real agent runs and real CI execution, both stubbed as above.
- A trigger actually firing on a merge.
- A live base-less suite run (§6.7): the server no longer makes one.

## Tests run

- `go test ./pkg/ui/ -count=1`: ok (74.6 s).
- `npx vitest run` (the full UI unit suite) on the fixed tree: 235 files,
  3686 tests, all passed.
- `npx playwright test --project=chromium` of `processes-walkthrough`,
  `processes-narrowing`, `processes-phone-layout`, `processes_workflows`,
  `schedule-every-n-minutes`, `suite-run-dialog-order`, `template-priority`,
  `built-in-template`, `capability-policy-pickers` and the new
  `processes-keep-running`: 30 passed.
