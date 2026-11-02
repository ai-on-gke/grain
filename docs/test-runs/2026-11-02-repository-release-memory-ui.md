# 2026-11-02 Repository, Merge Queue, and Release UI Walkthrough (partial)

**Status: partial.** The operator paused task 2402 before this
walkthrough finished. This file records what was covered before the
pause. Sections 5–7 (merge queue and base-branch gates, compare, and
releases) are still to be walked.

## Environment

- **Commit tested**: `aaa266ef` (`main`, merge of #462).
- **Build**: `cd ui && npm ci && npm run build`, then
  `go build -o /home/debian/bin/grain ./cmd/grain` (the frontend is
  embedded).
- **Servers**, both signed in through the access-code form:
  - **A** on `:8433`:
    `grain demo -addr 127.0.0.1:8433 -open=false -access-code-file … -github-sim -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification -reconcile=500ms`
  - **B** on `:8434`, for the base-branch gates:
    `grain demo -addr 127.0.0.1:8434 -open=false -access-code-file … -github-sim -reconcile=500ms`
- **Driver**: ad hoc Node scripts on Playwright's Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), kept outside the
  repository. They log `console` errors and warnings, `pageerror`,
  `requestfailed`, responses `>= 400`, every non-GET request, and
  `dialog`s.
- **Gemini**: `/home/debian/.gemini-api-key` is not present in this
  sandbox.
- `grain demo` no longer accepts `-sandbox-port-base`. The repository
  notes say to pass it to the daemon; it does not apply to `demo`.

## Stubs

None. GitHub is `grain demo`'s own `-github-sim`. Two pushes were made
to the simulator's bare `acme/widgets.git`:
- On `main`: `.grain/templates/walk-lint.yml`,
  `.grain/schedules/walk-lint.yml` and two path triggers
  (`walk-guard.yml` and `walk-docs.yml`).
- `feature/walk-2402`, adding `WALK.md` and `walk.go`, for the compare
  page.

## Walkthrough so far

### 1. Repos list

- The rail's **Repos** entry and `/repos` both open the list.
- `acme/gadgets` and the 82-character long-named repo were added with
  **Repo to add** / **Add repo**. After a reload the rail read 3.
- **Search**: `GADG` leaves only gadgets, and `zzz-no-match` shows "No
  repos match your search." The box still has no accessible name
  beyond its placeholder.
- **Sort**: Custom order, Name (A–Z), Active first and Most tasks each
  order the list as named.
- **Reorder by keyboard**: Enter on **Move acme/gadgets** offers two
  "Move here" targets. Picking the last one moved the row to the end,
  and focus returned to the handle. Escape clears the targets.
- **Reorder by pointer**: `dragTo` moved the last row to the top, and
  the order survived a reload.
- **Long name**: the document is 1280 wide at 1280 and 390 wide at
  390. The name is ellipsised inside its row.

### 2. Repo row, task count, Back

- The row opens `/repos/acme/widgets`. **← Repos** goes back to the
  list, and browser Back returns to the repo page.
- **16 tasks filed here** opens `/?f=repo:any:acme/widgets` with 16
  rows. Back returns to the repo page.

### 3. Filing a task from the repo page

- **New task** on `/repos/acme/gadgets` opened `/new` with the repo set
  to `acme/gadgets`.
- The title, repo and description survived a reload of `/new`.
  **Create Task** sent `"repo":"acme/gadgets"`, and the count went from
  0 to 1.

### 4. Tabs, Settings → Repositories, edits

- All seven tabs ride in `/repos/acme/widgets/settings/<tab>`
  (Overview is the bare path) and survive a reload.
- Back from Releases, after Branches, returns to `/repos`, so tabs
  replace the history entry. Forward returns to `settings/releases`.
- **Keyboard**: ArrowLeft on Overview wraps to Advanced, Home goes to
  Overview, and End then Enter opens Advanced.
- `/settings/bogus` falls back to Overview. `/repos/no/such-repo`
  still renders a full repo page with defaults.
- **Settings → Repositories** rows:
  - acme/gadgets: `Default branch`, `Not read`, `GitHub CI`,
    `Not listed`.
  - acme/widgets: `main`, `Read`, `Grain CI`, `Not listed`.
  - The cells link to the repo page, `settings/releases`,
    `settings/advanced`, `settings/ci` and `/settings/git`.
- **Agents** (each read back after a reload):
  - Grant `api:task-admin` (`PUT …/capabilities`). The summary reads
    "Self debug, GCP key, Gemini key, API preset: task-admin".
  - Prompt extension "Run make lint before pushing (task 2402)."
  - Setup command `make deps-2402`.
  - Issue import with label `grain-2402` every 15 minutes
    (`issueImportIntervalSeconds: 900`).
- **CI**:
  - The CI grant
    `capabilities.demo.grain.example › cloud-viewer` survived a reload.
  - Workflow `lint2402.yml` was refused with 400 twice: once for
    `name: [unclosed` ("yaml: line 1: did not find expected ',' or
    ']'"), and once for `runs-on: grain`. Both times the editor kept
    the text.
  - The `ubuntu-latest` version saved. The previous refusal's alert
    stayed on screen for a few seconds after that save, then cleared.
- **Unsaved edit**: with the refused workflow on screen, **← Repos**
  and the rail's **Tasks** each asked "You have unsaved changes here.
  Leave and discard them?". Dismissing it kept the page. A reload or a
  `goto` raised `beforeunload`.
- **Advanced**:
  - A trigger with no `template:` shows "Last read failed: … a trigger
    needs a `template:` to file — still running what c06d71ae7029
    declared".
  - After the fix was pushed, the next read was 1 template, 1 schedule
    and 2 triggers. The listing of declared triggers on Advanced has
    not been checked yet.

## Still to do

- Advanced's declared-trigger listing.
- Merge queue switches and the merge batch panel.
- Base-branch gate warnings on B.
- Compare branches.
- Releases and release trains.
- Phone widths.
