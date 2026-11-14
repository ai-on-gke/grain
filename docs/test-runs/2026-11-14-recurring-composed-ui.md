# Recurring & composed work (Processes) walkthrough — 2026-11-14 (task 5633)

- **Date:** 2026-11-14, 11:45–12:30 UTC, one run.
- **Task:** task 5633, branch
  `grain/53549e42/task-5633-walk-through-processes`, base `main`.
- **Commit tested:** `3266698e` ("Merge pull request #1455"), binary
  `v0.0.0-20261114110730-3266698eaa13`. The fixes below were re-checked in
  the browser on a rebuild of that commit with them applied (demo on a
  fresh store).
- **Focus:** the Processes area — schedules, event triggers, templates,
  suites, suite runs and repository declarations — walked on the Angular UI
  under `/next/processes` first ("Two UIs" in `PROMPT.md`), with a spot
  check of the classic `/processes` page for the routes that have no
  `/next/` counterpart.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0.
  - Playwright from `ui-ng/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    viewport 1400×900 (phone 390×844 where stated).
  - Built with `make frontend`, then `go build -o ~/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8765 -open=false -access-code-file <f>`
    over its own throwaway embedded store, signed in through
    `POST /api/auth/access-code` (`ui-shared/e2e/accesscode.js`) as
    `alice@example.com`.
  - No Gemini key was present at `/home/debian/.gemini-api-key`, so no
    real agent ran and `LOOK=1` screenshot descriptions were unavailable;
    layout was checked with bounding-box/overflow measurements in the
    browser instead of by eye.
- **Stubs:** none added. Task execution is the demo's own fake agent
  boundary (`grain demo`: "nothing here is real"); suite runs and their
  pass tasks were created through the real UI and server, and stay
  `queued` because no agent picks them up. Seed rows (three schedules
  including a repo-declared one, four templates including a declared and a
  built-in one, two suites including a declared one) come from the demo's
  own seeding. A handful of probes used the real HTTP API with the access
  code to set up states the `/next/` dialogs cannot express (a template
  with `priority`/`admission`/`hosting`/`keepRunning`; a template with a
  required field) and to read back persisted state; every create/edit/run
  gesture under test went through the browser.

## Which UI each area ran against

| Area | `/next/` page | Walked on |
| --- | --- | --- |
| Processes list, tabs, filters | `/next/processes[/<tab>]` | `/next/` |
| Schedules & triggers | `/next/processes/schedules` | `/next/` |
| Templates | `/next/processes/templates` | `/next/` |
| Suites & suite runs | `/next/processes/suites` | `/next/` |
| Repo declarations | `/next/processes/declarations` | `/next/` |
| Kind chips, search, sort, custom order, `?kind=`/`?q=` | none | classic `/processes` (spot check) |
| Header New menu | none (per-tab New buttons) | `/next/` |

The `/next/` Processes page is organised differently from classic: four
tabs (Schedules & Triggers, Templates, Suites & Runs, Repo Workflows &
Declarations) with in-memory filter chips, instead of classic's single
list with kind chips, search and sort. Several scripted cases therefore
have no `/next/` counterpart yet; they are listed under "Gaps" and filed as
a follow-up rather than built here.

## Steps and outcomes

### Processes navigation

1. From `/next/inbox`, the rail's **Processes** link (`/next/processes`)
   opened the page by SPA navigation; tab counts read Schedules & Triggers 3,
   Templates 4, Suites & Runs 2, Repo Workflows & Declarations 1. ✅
2. Each tab link changed the URL (`/next/processes/templates`, `…/suites`,
   `…/declarations`) and showed its list; the first row auto-selects on
   desktop. ✅
3. `/next/schedules`, `/next/templates`, `/next/suites` redirected to the
   matching tab. ✅
4. **Bug 3:** `/next/suites/suite-1` and `/next/processes/suites/runs/1`
   fell through the wildcard route to the Inbox. Fixed (below). ✅ after fix.
5. `/next/processes?kind=templates&q=sec` is accepted but ignored (no
   search or kind narrowing in `/next/`) — gap.
6. Keyboard: Tab from the page start reaches the rail, header, New
   schedule, each tab link and each filter chip in order; rows are buttons
   (Enter/Space select). ✅
7. Classic spot check: `/processes`, `/schedules` → `/processes?kind=schedule`,
   `/templates` → `?kind=template`, `/suites` → `?kind=suite`,
   `/suites/suite-1` → `/processes/suites/suite-1`,
   `/processes/suites/runs/1` all render. ✅ No classic fix needed.

### Schedules

1. **New schedule** (inline prompt): title, prompt, repo `acme/widgets`,
   cadence **Every N min** = 5, Save. The server refused it
   (`400 recurrence: everyNMinutes must be at least 10`) but the dialog just
   stayed open with no message: **Bug 1** — the error went to the page
   banner behind the dialog's scrim (`elementFromPoint` on the banner hit the
   scrim). Fixed. ✅ after fix: the error shows in the dialog footer.
2. Changed to 15 and saved: the dialog closed, the row "Walk sched · Every
   15 min · acme/widgets · overlap: skip" appeared at the top and was
   selected without a reload. ✅
3. **Pause** → badge "Paused", button "Resume"; persisted through reload. ✅
   But "Next:" still showed `2026-11-14T12:06:23.86860835Z`: **Bug 2** (raw
   RFC 3339, and a paused schedule claiming a next run). Fixed. ✅
4. **Git ref push** trigger watching `evil/elsewhere @ refs/heads/main`
   (off the deployment's target list): accepted, row shows **Suppressed** and
   a `Trigger` tag; detail shows "Suppressed (watchedRepoOffTarget): watches
   evil/elsewhere, which is not on this deployment's target repo list". ✅
   "Next:" showed the far-future placeholder `2027-11-15T…` (Bug 2); after
   the fix it reads "suppressed" on the row and in the detail. **Pause**
   still works on it ("Paused"). ✅ The server-side forbidden-repo refusals
   (secrets backup repo, retired config repo) are covered by
   `pkg/ui/schedule_git_ref_test.go`; the demo has no such repo configured,
   so they were not provoked from the browser.
5. Firing target **Template** and **Suite** each swap the prompt fields for
   a picker (security-audit / Release Qualification Suite). ✅
6. **Run now** on "Weekly Dependency & Vulnerability Audit" filed task #22
   at once (no confirmation — it is not destructive) and listed it under
   "Recent fired tasks & skips". ✅
7. Repo-declared schedule "Nightly Repo Maintenance Sweep" (`.grain` tag):
   no Edit/Delete; Pause disabled; banner "Declared in acme/widgets at
   .grain/schedules/nightly.yml. Update that file in git to modify this
   schedule." Run now stays enabled. ✅
8. Editing a schedule created with `keepRunning`, `admission: durable`,
   `model`, `networkAccess`, `reads`, `capabilities` (fields the `/next/`
   dialog cannot show) and saving a new title kept every one of those fields
   (read back from `GET /api/schedules`). ✅ No data loss.
9. Composition preview on every schedule lists title/body/repo/base/reads/
   writes/capabilities/model/autoMerge/approval with their sources. ✅

### Templates

1. **New template** `walk-tmpl` bound to `acme/widgets`: saved, selected,
   composition preview shown. ✅ The dialog offers only name, title, prompt,
   bound repo, parameters and auto-merge — no priority, admission, hosting,
   keep running, interactive, capabilities or model (gap).
2. Template with a parameter `area` (required): saved, "Parameter schema
   (1)". **Bug 4:** its composition preview was missing and the console
   logged `400 GET /api/templates/<id>/composition` on every selection
   (`{"error":"field \"area\" is required"}`). Fixed by previewing with a
   `{{area}}` placeholder. ✅ after fix.
3. **Launch task** from it: the dialog asks for `area (required)`, target
   repo and base; Create task opened `/next/task/21` "Fix the parser". ✅
4. **Edit** → rename to `walk-fields-renamed`; persisted through reload. ✅
   A template carrying `priority: 7, admission: durable, hosting: console,
   keepRunning, networkAccess, reads, capabilities, model` kept them all
   after a title edit through `/next/`. ✅
5. **Delete** on `security-audit` (used by suite-1): the confirmation is
   generic ("Delete template "security-audit"?"); the server refuses with
   `template is used by 1 suite(s) ("Release Qualification Suite"); remove
   it from those first` (checked over the API; shown in the page banner).
   Cancelled. Deleting the throwaway `walk-fields-renamed` asked first and
   removed the row. ✅
6. Declared template `nightly-maintenance`: no Edit/Delete, banner
   "Repo-declared read-only template from acme/widgets
   (.grain/templates/nightly.yml)". ✅

### Suites and suite runs

1. **New suite** "Walk suite": member templates toggled (walk-tmpl,
   security-audit), mode **Until clean** with max passes 2, saved; detail
   lists both members and a two-task composition preview. ✅
2. **Run suite** from the suite's own button: target defaults to
   `acme/widgets @ main`; Start → view switches to Suite runs, run #1
   "Active · Pass 1 · 2 tasks" with tasks #21/#22 `queued`. ✅
3. Header **Run suite** (generic): same dialog with a suite picker. With the
   base cleared, Start is disabled — the server requires a base ("a suite
   run has no default branch to fall back to"), so the scripted "base-less
   run shows default branch" state no longer exists. ✅ (by design)
4. Phone 390×844: run row status pill 69×24 px, `white-space: nowrap`,
   not clipped (`scrollWidth == clientWidth`); detail opens with ← Back; no
   horizontal page overflow. ✅
5. Declared suite "Smoke Verification Suite" (`.grain`): read-only. ✅
6. **Cancel suite run** asks "Cancel active suite run? Stop and close all
   open pass tasks in suite run #N (acme/widgets)?" before acting. ✅
7. Direct URL `/next/processes/suites/runs/<id>` — Bug 3, fixed. ✅

### Declarations

`/next/processes/declarations` lists acme/widgets ("1 awaiting
acceptance", declarations disabled) with its acceptances card, discovered
files and workflow overrides. ✅

### Console and network

Apart from the 400s that Bugs 1 and 4 describe, the only failed request
was `GET /api/config net::ERR_ABORTED` on SPA navigation (a request
cancelled when a route changed, harmless). No uncaught page errors.

## Bugs found and fixed

| # | Bug | Fix | Coverage |
| - | --- | --- | --- |
| 1 | Any refused save in a Processes dialog (schedule, template, launch, suite, run, workflow) showed its error only in the page banner behind the scrim; Save looked dead | Each dialog shows `store.actionError` in its own footer (`data-testid="dialog-error"`); opening a dialog clears a stale error | e2e "a refused schedule save shows its error inside the dialog" |
| 2 | Schedule "Next:"/"Last:" were raw RFC 3339 with nanoseconds; paused schedules and suppressed triggers showed a next time (the latter a far-future placeholder); rows said nothing about the next run | `scheduleNextRunLabel` → "paused" / "suppressed" / localised time, in the detail and on every row | e2e "next-run labels…", `processes.models.spec.ts` |
| 3 | `/next/suites/<id>`, `/next/schedules/<id>`, `/next/templates/<id>`, `/next/processes/suites/runs/<id>` fell through to the Inbox | Routes `processes/:tab/:id` and `processes/suites/runs/:runId` select the item; the classic-shaped links redirect there | e2e "item deep links land on the item" |
| 4 | A template with a required field and no default lost its composition preview (server 400 on every selection) | Preview fills such fields with a `{{name}}` placeholder | e2e "a template with a required field still previews its composition" |

Regression tests: `ui-ng/e2e/next-processes-walkthrough.spec.js` (4 tests,
pass), `ui-ng/src/app/processes/processes.models.spec.ts`, all ui-ng unit
tests (451 pass), `ui-ng/e2e/next-full-system.spec.js` (5 pass), eslint
clean. Classic `ui/` was not changed.

Screenshots (not committed): `/next/processes/schedules`, `/templates`,
`/suites`, `/declarations` and `/next/suites/suite-1` at desktop 1440 and
phone 390, light and dark, via `ui/e2e/screenshot.mjs`. Schedule rows were
measured at 390 and 1440 px: the sub-line with the new "· next: …" wraps
to two lines inside the row (no overflow, `scrollWidth == clientWidth`).

## Gaps: scripted cases with no `/next/` counterpart yet

Filed as one follow-up task. All of these exist on the classic page:

- Kind chips with counts (All, Schedules, Workflow schedules, Templates,
  Suites, Triggers), search, toolbar narrowing and sort (Next run, Last run,
  Name, Kind, Custom order with pointer/keyboard reorder), carried in
  `?kind=`/`?q=` through reload and Back/Forward. `/next/` has per-tab
  filter chips held in memory only, and selection is not reflected in the
  URL (only the new deep links read it).
- Templates and suites showing last/next runs derived from the schedules
  that fire them ("on demand" when none).
- A header **New** menu (Schedule, Template, Suite, with the hint that
  workflow schedules and triggers are declared in a repository).
- Schedule and template dialogs: priority (and its whole-number/`1e3`
  validation), Admission, Hosting, Keep running, interactive switch,
  grants/capability policy verdicts, network, custom fields, reads/writes
  and the searchable agent/model picker. Values set elsewhere are preserved
  on edit (checked), but cannot be seen in the detail pane or changed.
- "New task from this template" carrying priority; the Use… note for
  interactive templates; unbound templates with `Reads`.
- Inline "+ New template" inside the suite editor, and role gating for a
  role lacking `suites:write` (not walked: the demo signs in as an admin).
- Workflow schedules' `System` chip / "starts the run straight away" Run
  now note and trigger rows opening their file on the repository's
  Advanced tab.
- A dependency-aware delete confirmation (the server refusal is clear, but
  the confirmation does not warn first).
- An unsaved-edit guard when following a schedule's task link (the
  `/next/` dialogs are modal; links behind them are not reachable while a
  draft is open, so there is nothing to guard yet).
