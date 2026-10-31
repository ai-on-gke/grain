# 2026-10-30 Repository, Merge Queue, and Release UI Walkthrough

A walkthrough of the repository-centric operator workflow. It was driven
through visible controls in a real headless Chromium, against real
`grain demo` servers over throwaway embedded stores. It follows
[2026-10-29](2026-10-29-repository-release-memory-ui.md) and records
what has changed since, and the two faults found and fixed this time.

## Environment

- **Commit tested**: `e1b148a4` (`main`, merge of #212) for the
  walkthrough. The fix is on `grain/049f1e09/task-1902`.
- **Platform**: Linux sandbox (`kubernetes-granule`), Go and Node from
  the image (Node v20.19.0), and Playwright's Chromium from
  `/var/cache/playwright` (`PLAYWRIGHT_BROWSERS_PATH`).
- **Build**: `cd ui && npm ci && npm run build`, then
  `go build -o /var/tmp/grain ./cmd/grain` (the frontend is embedded).
- **Servers**: all three sign in with an access code written to
  `/var/tmp/code` and posted to `/api/auth/access-code` for the session
  cookie.
  - **A**, the main walkthrough, on `:8433`:
    `grain demo -addr 127.0.0.1:8433 -open=false -access-code-file /var/tmp/code -github-sim -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification -reconcile=500ms`
  - **B**, base-branch gate warnings, on `:8434`:
    `grain demo -addr 127.0.0.1:8434 -open=false -access-code-file /var/tmp/code -github-sim -reconcile=500ms`.
    A's `-base-branch-ci` enrols acme/widgets's base branch, so A can
    never raise the gate.
  - **C**, the fixed build, on `:8435`: as B, from the rebuilt binary.
- **Driver**: ad hoc Node scripts on `playwright`'s `chromium`, kept
  outside the repository. Every page was watched for `console`
  errors and warnings, `pageerror`, `requestfailed`, HTTP responses
  `>= 400` and `dialog`s, and every non-GET request was logged.
- **Viewports**: 1280x800 or 1280x844 for desktop, 390x844 for phone.
- **Gemini**: `/home/debian/.gemini-api-key` is not present in this
  sandbox, so `LOOK=1` was not available. Layout was checked with
  bounding boxes and scroll widths measured in the browser.
- **Grain CLI**: the sandbox's `grain` offers only `host-script`, so
  `grain ci wait` could not be run from here. See "Tests run".

## Stubs

None. Grain's API and React UI were never intercepted. The only
external boundary, GitHub, is `grain demo`'s own `-github-sim`
(`pkg/github/githubsim`). It serves branches, comparisons, pull
requests and release branch moves over a real bare repository on disk.
One branch, `feature/walk-1902` (a new `WALK.md` and `main.go`), was
pushed with `git push` to the simulator's
`/tmp/grain-demo-*/githubsim/acme/widgets.git`, so the compare page had
a real diff to show. The demo's own seeds (`-merge-queue-states`,
`-qualification`, `-base-branch-ci`) supplied the merge batch, release
`1.0` and its qualification task.

## Walkthrough

### 1. Repos list

- **Rail and URL**: the rail's `Repos` entry and a direct load of
  `/repos` both open the page. It shows the add-repo picker, the "No
  repositories discovered" caption and the one-repo allowlist notice.
  `acme/widgets` is listed with state chips and `16 tasks`.
- **Adding**: added `acme/gadgets` and an 82-character
  `organization-with-a-very-long-name/repository-with-an-equally-long-descriptive-name`
  through **Repo to add** and **Add repo**. The rail read `Repos 3`
  after a reload, and the allowlist notice went away.
- **Search**: `GADG` leaves only `acme/gadgets`. `zzz-no-match` shows
  "No repos match your search." Clearing restores all three.
  Observation: the filter box has no label of its own. Its accessible
  name comes from the placeholder "Filter added repos…"
  (`ListPrimitives.ListSearchField`, shared by every list).
- **Sort**: `Name`, `Active first`, `Most tasks` and `Custom order` each
  reorder as named. The three **Move …** handles exist only under
  `Custom order`.
- **Reorder**:
  - Keyboard: on **Move acme/gadgets**, Enter offers "Move here, above
    organization-…" and "Move here, to the end of the list". Choosing
    the end target moved the row there, and focus returned to the
    handle.
  - Tap: **Move organization-…**, then "Move here, above acme/widgets".
  - Escape after a pick-up left no targets behind.
  - Pointer: an HTML5 `dragTo` moved the row.
  - All three survived a reload. A fresh browser context shows the
    default order: the custom order belongs to this browser.
- **Long name**: at 1280 nothing in `.repo-list` overflows. At 390 the
  name is cut with an ellipsis (full name in `title`), and the document
  is 390 wide. At 390 only the first count chip shows (`Proposed 3`,
  75px); the others collapse to 0px. That changed since 2026-10-29,
  when every chip was squeezed to an unreadable 50px.

### 2. Repo row, task count, Back

- The `acme/widgets` row opens `/repos/acme/widgets`. **← Repos**
  returns to `/repos`. Back and Forward step between the two.
- **16 tasks filed here** opens `/?f=repo:any:acme/widgets` with 16
  rows. Back returns to the repo page.

### 3. Filing a task from the repo page

- On `/repos/acme/gadgets` (`0 tasks filed here`), **New task** opened
  the `/new` overlay over the repo page, targeting `acme/gadgets`.
- Typed a title and a description, then reloaded `/new`. Both survived,
  and **Create Task** filed task 24 with `repo: "acme/gadgets"`. The
  page then read `1 task filed here`. After that reload, Create went to
  `/`, as on 2026-10-29.
- A second task filed without a reload returned to
  `/repos/acme/gadgets` (`2 tasks filed here`). `GET /api/tasks` lists
  tasks 24 and 25 on `acme/gadgets`.

### 4. Repo tabs and Settings → Repositories

- The strip is a `role="tablist"` (`acme/widgets sections`). Each of
  Overview, CI, Merge queue, Agents, Branches, Releases and Advanced
  opens at `/repos/acme/widgets` or
  `/repos/acme/widgets/settings/<tab>`, and stays selected after a
  reload.
- Keyboard: ArrowLeft wraps from Overview to Advanced, Home and End go
  to the ends, and Enter opens the focused tab.
- Tabs replace the history entry. After `/repos`, then `agents`, then
  clicking Branches and Releases, one Back leaves the page; Forward
  returns to Releases.
- `/settings/bogus` falls back to Overview. An unknown repository
  (`/repos/no/such-repo`) renders an empty page for it with `0 tasks
  filed here`, as before.
- **Settings → Repositories** has the columns repository, base branch,
  declarations, CI mode and git connection:
  - `acme/gadgets`: `Default branch`, `Not read`, `GitHub CI`,
    `Not listed`.
  - `acme/widgets`: `main`, `Read`, `Grain CI`, `Not listed`.
  - The long-named repo: as acme/gadgets.
  - Each cell links to the repo page, `settings/releases`,
    `settings/advanced`, `settings/ci` or `/settings/git`. Each row has
    its own **Merge queue** link, and the page has a per-repo list of
    base-repair bypass switches.
- **Edits**, each confirmed by reloading and reading the field back:
  - **Agents**:
    - Default capability **memory**. The summary read "Self debug, GCP
      key, Gemini key, Memory" (`PUT .../capabilities`).
    - Prompt extension "Run make lint before pushing (task 1902)."
    - Setup command `make deps-1902`.
  - **CI workflow** `lint1902.yml`:
    - `name: [unclosed` is refused with 400. The YAML error is shown
      as an alert, and the editor keeps the text.
    - `runs-on: grain` is refused with "runs-on must be
      "ubuntu-latest"…".
    - The corrected workflow saved as `.grain/workflows/lint1902.yml`
      and survived a reload.
    - Observation: one edit followed by a blur sent the refused `PUT`
      twice.
  - **Advanced** now lists any declared path-trigger files by path
    under the status line ("Path triggers: …"). There were none in this
    demo ("Read at 71b962a9632c: 0 template(s), 0 suite(s), 0
    schedule(s), 0 trigger(s)").
  - As on 2026-10-29, this build has no repo-scoped read/write
    repository editor or custom configuration on the repo page. Those
    fields live on the new-task form, templates, schedules and sessions.
    That part of the brief does not match this build.
- **Unsaved edits**:
  - With the refused workflow on screen, **← Repos** asked "You have
    unsaved changes here. Leave and discard them?". Dismissing it kept
    the page.
  - A setup-command edit left through **← Repos** without a blur was
    saved on the way out (`PUT .../setup-command`), which is the
    autosave contract.
  - Switching to another tab with the refused edit keeps it, with no
    prompt, as `repo-tabs-walkthrough` specifies.

### 5. Merge queue and base-branch gates

- **Switches on A**: **Require branches to be up to date**, **Merge
  when green and clean** and **Base-branch repair PRs … may merge
  without a human** each saved with one click. `license/cla` was added
  as a check no commit can clear. After a reload all three were on and
  `license/cla` was listed. The Overview cards read `Required`, `On`
  and `On`. The base-repair bypass still takes effect with no
  confirmation, as recorded on 2026-10-29.
- **Merge batch**: the Overview reads "1 change on main", testing, "16m
  of 90m", `grain-merge-queue/2a1e78c7/main at abcdef012345`, and links
  task 5 ("Rotate the Gemini signing key") and PR `#101`. `/tasks/5`'s
  dialog carries the same "merge batch" panel. Back from `/tasks/5`
  returns to the repo page.
- **Gates on B**:
  - CI mode **GitHub** plus **Watch acme/widgets's default branch**
    raised "Nothing is testing acme/widgets's default branch, and the
    merge queue is merging onto it anyway. grain CI is not enabled…".
    It appears on the Overview, with a red dot on the CI tab, and on
    `/repos/acme/widgets/releases`.
  - The Overview's **Go to CI Mode** opens `settings/ci`.
  - **Finding 1**: the Releases pane's link went to the Overview
    instead.
  - Choosing **Make grain CI the gate** cleared the warning, the dot
    and the Releases warning without a reload.
  - **No CI** with the watch on raised the warning again, with the card
    reading "No CI — base branch not judged". Grain cleared it again.
  - **Finding 2**: the Overview's "-ci=false" note pointed "above" at
    workflows that are not on that tab.

### 6. Branches and compare

- Base starts at `main`. Typing `rel`, ArrowDown and Enter picked
  `release/1.0`, and **Compare** opened
  `/repos/acme/widgets/compare/main...release/1.0`: "Merged into main …
  There are no changes between these branches." The page survived a
  reload, and Back returned to the Branches tab.
- `main...feature/walk-1902`, after the push described under Stubs, is
  `open`, "Into main on acme/widgets -- forked at 71b962a9". It has
  side-by-side, inline and raw views, and lists `WALK.md` (+1) and
  `main.go` (+3). The document is 1280 wide at desktop and 390 at
  phone.
- Direct links:
  - `main...candidate`, `release/1.0...main` and `main...main` say
    there are no changes.
  - `main...no-such-branch` and `nope...main` return 404, and the page
    says "GitHub has nothing to compare for … one of the two branches
    does not exist…".
- An unknown head (`missing-branch-xyz`) warns `No branch
  "missing-branch-xyz" on acme/widgets: there is nothing to compare`.
  **Compare** stays enabled.
- Observation: the picker's GitHub branch list is cached for five
  minutes on the server (`githubBranchCacheTTL`). For that window after
  `feature/walk-1902` was pushed it was not suggested, and typing it in
  full showed the "No branch" warning. **Compare** still worked, and
  the cache answered with the branch by the time it expired.

### 7. Releases

- `/repos/acme/widgets/releases` shows `1.0 -- Active`, `latest:
  1.0.latest, prod: 1.0` and candidate `1.0.rc.1 -- active`. It also
  shows the pending-approval banner with **Approve all**, the
  qualification task **Qualify** (`/tasks/23`), history, merge-back,
  **Delete release** and the qualification plan.
- **Cut new RC** is disabled with "Cut new RC: 1.0.rc.1 is active: a
  new RC is cut once it is promoted."
- **Validation**:
  - `bad release name` and `x.latest` are refused (400) with the
    naming rules.
  - `1.0` is refused with "this repo already has an unmerged release
    with this name".
- **Lifecycle of `wt1902`**:
  - Created at `/releases/wt1902`; it went active as `wt1902.rc.1`.
  - **Promote current RC**: the confirm "Promote wt1902.rc.1 to wt1902?
    This moves acme/widgets's wt1902 branch to wt1902.rc.1." was
    dismissed (no request), then accepted.
  - While the candidate was promoting, both buttons gave their reasons:
    "…is promoting: a new RC is cut once it is promoted" and "…only an
    active candidate can be promoted".
  - **Cut new RC** gave `wt1902.rc.2 -- cutting` ("…is still being
    cut"), then active, with `wt1902.rc.1 -- promoted` in history.
  - A reload kept all of it. `/releases/1.0` opens directly, and Back
    returns to `wt1902`.
  - `/releases/nope-404` is rewritten to `/releases` with `no release
    "nope-404"`.
  - **Delete release**: the confirm "Remove release wt1902? This drops
    the release and its candidates from grain." was dismissed (no
    request), then accepted (`DELETE`). The pane fell back to
    `/releases` with no false error.
  - The simulator's branch list still holds `wt1902`, `wt1902.latest`,
    `wt1902.rc.1` and `wt1902.rc.2`. That matches the confirm's "from
    grain".

### 8. Console and network

- No `pageerror`s. `console.error` appeared only for the deliberate
  400 and 404 responses above.
- `GET /api/tasks` shows up as `requestfailed: net::ERR_ABORTED` on
  navigation, as recorded on 2026-10-29: polls the browser drops on
  unload. Nothing shows to the user.

## Findings

### 1. Fixed: the Releases pane's base-gate warning did not lead to the fix

- **Reproduction** (server B, any `grain demo -github-sim`):
  1. On `/repos/acme/widgets/settings/ci`, keep **Use GitHub CI** and
     tick **Watch acme/widgets's default branch with grain CI**.
  2. Open `/repos/acme/widgets/releases`. The "Nothing is testing…"
     warning's only control is **Open repository settings**.
  3. Click it. The browser lands on `/repos/acme/widgets` (Overview),
     with nothing focused. The same warning is shown again, and a
     second link, **Go to CI Mode**, is needed to reach the switch that
     fixes it.
- **Cause**: `RepoReleases` could only call `onBack`, which closes the
  pane onto the repo page's current tab. The repo page's own warning
  already sent `grain-ci-off` gates to CI Mode.
- **Fix**: `RepoReleases` takes `onOpenTab`, and `App` passes
  `openRepoPage(openRepo, tab)`. A `grain-ci-off` gate's control is now
  **Go to CI Mode**, which opens `/repos/<repo>/settings/ci` with the
  CI Mode radio in view. Other gate reasons keep **Open repository
  settings**. Back from the CI tab returns to the Releases pane.
- **Regression coverage**:
  - `RepoReleases.test.jsx`, "sends a grain-CI-off gate's warning
    straight to the CI tab", covers both reasons.
  - `e2e/base-branch-gate-warnings-walkthrough.spec.js` now asserts
    that the Releases warning lands on `settings/ci` with the radio in
    the viewport, and that the Overview link still does too.

### 2. Fixed: the Overview's "-ci=false" note pointed at workflows "above" it

- **Reproduction**: on any demo started without CI (B), open
  `/repos/acme/widgets`. Under "CI runs" the Overview reads "…started
  with -ci=false, so the workflows above are stored but never run".
  Nothing above it on the Overview is a workflow. The workflow form is
  on the CI tab, since the repo page was split into tabs.
- **Fix**: the note reads "…so the workflows on this repository's CI
  tab are stored but never run". The source comment no longer claims it
  sits under the form.
- **Regression coverage**: `RepoRuns.test.jsx` asserts the new wording
  and the absence of "workflows above".

### Observations not changed here

- The repo filter box is labelled only by its placeholder (§1). The box
  is shared by every list, so a fix belongs app-wide.
- **Base-branch repair PRs may merge without a human** still has no
  confirmation (§5).
- A refused workflow edit sends its `PUT` twice (§4).
- The branch picker's five-minute GitHub cache can warn "No branch" for
  a branch pushed moments ago (§6).
- A reload of `/new` forgets the repo page it was opened from (§3).
- The brief's repo-scoped read/write relationships and custom
  configuration are not on the repo page in this build (§4).

## Tests run

- `npx vitest run` (all of `ui/`): 225 files, 3513 tests passed.
- `npx playwright test e2e/base-branch-gate-warnings-walkthrough.spec.js e2e/repo-tabs-walkthrough.spec.js e2e/repo-pane.spec.js`:
  7 passed.
- `npx prettier --check` on the changed files.
- Re-checked in the browser on server C: the Releases warning's **Go to
  CI Mode** opens `/repos/acme/widgets/settings/ci` with the radio in
  view, and the Overview note carries the new wording.
- Rendered with `node e2e/screenshot.mjs --url …` (the
  `make ui-screenshots` tool):
  - Pages: `/repos/acme/widgets/releases` and `/repos/acme/widgets`.
  - Widths and themes: desktop and phone, light and dark.
  - Builds: before on B, after on C.
  - Bounding boxes: at 1280 and 390, the warning's button lies inside
    the warning, and the document is no wider than the viewport.
  - Screenshots are not committed.
