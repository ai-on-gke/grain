# 2026-10-29 Repository, Merge Queue, and Release UI Walkthrough

A walkthrough of the repository-centric operator workflow, driven
through visible controls in a real headless Chromium against real
`grain demo` servers over throwaway embedded stores. It follows
[2026-10-26](2026-10-26-repository-release-memory-ui.md) and records
where the page has changed since.

## Environment

- **Commit tested**: `99581723` (`main`, merge of #143) for the
  walkthrough; the fix below is `279f51ce` on
  `grain/049f1e09/task-1756`.
- **Platform**: Linux sandbox (`kubernetes-granule`), Go and Node from
  the image (Node v20.19.0), Playwright's Chromium from
  `/var/cache/playwright` (`PLAYWRIGHT_BROWSERS_PATH`).
- **Build**: `cd ui && npm run build`, then
  `go build -o /var/tmp/grain ./cmd/grain` (the frontend is embedded).
- **Servers** (all sign in with an access code written to
  `/var/tmp/code`, posted to `/api/auth/access-code` for the session
  cookie):
  - **A**, the main walkthrough, `:8433`:
    `grain demo -addr 127.0.0.1:8433 -open=false -access-code-file /var/tmp/code -github-sim -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification -reconcile=500ms`
  - **B**, base-branch gate warnings, `:8434`:
    `grain demo -addr 127.0.0.1:8434 -open=false -access-code-file /var/tmp/code -github-sim`.
    A's `-base-branch-ci` seeds a base-branch row for acme/widgets, and
    an enrolled branch is never "not enabled" (`pkg/ui/basebranchgate.go`
    `baseBranchGateOf` only reports `grain-ci-off` with no row), so the
    gate cannot be raised on A at all.
  - **C**, the fix's after-screenshots, `:8435`: the rebuilt binary with
    `-github-sim`.
- **Viewports**: 1280x800 desktop and 390x844 phone. Every page was
  watched for `console` errors and warnings, `pageerror`,
  `requestfailed`, HTTP responses `>= 400` and `dialog`s.
- **Gemini**: `/home/debian/.gemini-api-key` is not present in this
  sandbox, so `LOOK=1` was not available. Layout was checked with
  bounding boxes and scroll widths measured in the browser.

## Stubs

None. Grain's API and React UI were never intercepted. The only
external boundary, GitHub, is `grain demo`'s own `-github-sim`
(`pkg/github/githubsim`): branches, comparisons, pull requests and
release branch moves over a real bare repository on disk. One commit
was pushed to that bare repository with `git push` (a declared trigger,
step 4). The demo's own seeds (`-merge-queue-states`, `-qualification`,
`-base-branch-ci` and so on) supplied the merge batch, release `1.0`
and its qualification tasks.

## Walkthrough

### 1. Repos list

- **Rail and URL**: the rail's `Repos 1` item (a `role="button"` list
  item, not a link) and a direct load of `/repos` both open the page:
  the add-repo picker, filter and sort in the page header, the "No
  repositories discovered" caption, the one-repo allowlist notice, and
  `acme/widgets` with its state chips and `15 tasks`.
- **Adding**: added `acme/gadgets` and an 82-character
  `organization-with-a-very-long-name/repository-with-an-equally-long-descriptive-name`
  through **Repo to add** and **Add repo**. The rail went to `Repos 3`
  and stayed there across reload; the allowlist notice went away.
- **Long name**: at 1280 nothing in `.repo-list` overflows; at 390 the
  long name is cut with an ellipsis (`text-overflow: ellipsis`, full
  name in `title`) and the document does not scroll sideways.
  Observation: at 390 each count chip on the `acme/widgets` row is
  squeezed to a 50px label ("Propo…"), so the counts themselves are not
  readable on the list at phone width. `style.css` says the row's chips
  are the part allowed to give way and that every count is on the
  repo's own page, so this is recorded rather than changed.
- **Filter and sort**: `GADG` leaves `acme/gadgets`; `zzz-no-match`
  shows "No repos match your search."; clearing restores all three.
  Sort `Name (A–Z)`, `Active first`, `Most tasks` and `Custom order`
  each reorder as named; the move handles exist only under `Custom
  order` (cursor `grab`).
- **Reordering**: keyboard (focus **Move acme/gadgets**, Enter, Tab ×7
  to "Move here, to the end of the list", Enter; focus returns to the
  handle), Escape cancelling a pick-up, tap (**Move organization-…**,
  then "Move here, above acme/widgets"), and an HTML5 pointer drag
  (`dragTo`) each moved the row and survived reload. A **fresh browser
  context** shows the default order: the custom order is this browser's
  own (`listOrder.js`, as `RepoList.jsx` documents), not a server
  setting. The 2026-10-26 report's `PUT /api/settings` no longer
  applies.

### 2. Repo row, task count, Back

- Clicking the `acme/widgets` row opens `/repos/acme/widgets`;
  **← Repos** returns to `/repos` with focus on that row's link.
  Browser Back/Forward step between the two.
- **15 tasks filed here** opens `/?f=repo:any:acme/widgets` with 15
  rows, all `acme/widgets`; Back returns to the repo page. Back from the
  task list onto `/repos/acme/widgets/settings/merge-queue` comes back
  to the Merge queue tab.

### 3. Filing a task from the repo page

- On `/repos/acme/gadgets` (`0 tasks filed here`), **New task** opens
  `/new` with the Repos field's select at `acme/gadgets`.
- Typed a title and description, reloaded `/new`: title, description
  and `acme/gadgets` survived. **Create Task** posted
  `repo: "acme/gadgets"`; the page now reads `1 task filed here`.
- Observation: after that reload, Create went to the task list rather
  than back to the repo page; a second task filed without a reload
  returned to `/repos/acme/gadgets` (`2 tasks filed here`). Where the
  form was opened from is not part of `/new`'s address.

### 4. Repo tabs and Settings → Repositories

- The strip is a `role="tablist"` (`acme/widgets sections`).
  ArrowRight/Left, Home and End move focus with wrap-around; Enter
  opens the focused tab. The 2026-10-26 "Compare" tab is gone: compare
  lives on **Branches**.
- Each of Overview, CI, Merge queue, Agents, Branches, Releases and
  Advanced opens at `/repos/acme/widgets` or
  `/repos/acme/widgets/settings/<tab>`, survives reload, and opens
  directly; `/settings/bogus` falls back to Overview.
- Switching tabs **replaces** the history entry: after clicking through
  all seven, one Back leaves the repo page. `App.jsx` does this on
  purpose ("Back leaves the page rather than stepping through its
  tabs"); Back and Forward onto the page restore the tab it was left
  on.
- **Settings → Repositories** (`/settings/repositories`) lists all
  three repos. Each row links its name to the repo page, its release
  branches ("Default branch" or `main`) to `settings/releases`,
  declarations ("Read" / "Not read") to `settings/advanced`, CI mode
  ("Grain CI" / "GitHub CI") to `settings/ci`, the git connection ("Not
  listed") to `/settings/git`, and **Merge queue** to
  `settings/merge-queue`.
- **Phone, deep-linked tab: finding 1** below.
- **Edits, each confirmed after reload and again through the API**:
  - Merge queue: **Require branches to be up to date**, **Merge when
    green**, **Base-branch repair PRs may merge without a human** (the
    Overview cards read `Required` / `On` / `On`); added `license/cla`
    as a check that cannot pass. Observation: the base repair bypass,
    whose own text spells out the risk, takes effect on one click with
    no confirmation.
  - Agents: default capability **memory** (the summary reads "Self
    debug, GCP key, Gemini key, Memory"), prompt extension "Run make
    lint before pushing (task 1756).", setup command `make deps`.
  - CI: capability grant **gcp-key** through the grant picker (it lists
    capability-server methods now, not the old checkbox list); workflow
    `lint1756.yml`. Invalid YAML (`name: [unclosed`) is refused with
    the YAML error shown and the editor kept. `runs-on: grain` is
    refused with "runs-on must be "ubuntu-latest""; the corrected
    workflow saved as `.grain/workflows/lint1756.yml`.
  - Advanced: **Read acme/widgets's own declarations** off, reload
    (still off), on again. After a `.grain/triggers` file was pushed to
    the simulated repository, the status line counted it (see "Declared trigger sync" at the end).
    Advanced in this build holds only declarations and **Remove**:
    there is no read/write repository relationship editor or declared
    trigger list on it, so those parts of the brief do not match this
    build.
- **Unsaved edits**: with the refused workflow on screen, **← Repos**
  asked "You have unsaved changes here. Leave and discard them?";
  dismissing it kept the page, and a reload was held by
  `beforeunload`. A prompt-extension edit left by **← Repos** without
  a blur was saved on the way out (`PUT .../prompt-extension`), which
  is the autosave contract `App.jsx`'s `onPopState` describes.

### 5. Merge queue and base-branch gates

- **CI mode on server A**: GitHub, No CI and grain each saved
  (`PUT .../ci-mode`), and the CI gate card followed. There was no
  gate warning in any mode: the seeded base-branch row makes the branch
  enrolled (see Environment).
- **Server B**: CI mode **GitHub** plus **Watch acme/widgets's default
  branch**. The header warned "Nothing is testing acme/widgets's
  default branch, and the merge queue is merging onto it anyway. grain
  CI is not enabled for this repository…", the CI gate card read
  "GitHub CI — base branch not judged", the CI tab carried a red dot,
  and `/repos/acme/widgets/releases` showed the same warning. The Inbox
  did not. **Go to CI mode** opened the CI tab. Choosing **Make grain
  CI the gate** cleared the header, card, dot and Releases warning
  without a reload. CI mode **No CI** with the watch on raised the same
  warning, and grain cleared it again.
- **Merge batch**: the Overview panel shows "2 changes on main",
  testing, `grain-merge-queue/5c22822d/main at abcdef012345`, PRs
  `#101` and `#102`, and tasks `#5` and `#6`. `/tasks/5`'s dialog
  carries the same "merge batch" panel. Its link to "Cache the
  capability readiness probe" opened `/tasks/6`, and Back stepped
  `/tasks/6` → `/tasks/5` → `/repos/acme/widgets`.

### 6. Branches and compare

- **Add branch** `feature/walk-1756` and `feature/walk-1756b`: both
  listed "-- pending" and the Overview card reads `Branches 2`. They
  stay pending: `grain demo -reconcile` runs only the suite and release
  steps (`pkg/demo/demo_reconcile.go`), never `reconcileBranches`. This
  is a demo limit, not a UI fault.
- **Compare**: Base starts at `main`. The Head picker suggests
  `1.0.latest`, `candidate`, `main` and `release/1.0` from the repo's
  own GitHub branches; its rows are not `role="option"`. Typing `rel`,
  ArrowDown and Enter picked `release/1.0`, and **Compare** opened
  `/repos/acme/widgets/compare/main...release/1.0`: "Merged into main
  … There are no changes between these branches." Reload kept it.
- An unknown head `missing-branch-xyz` warns `No branch
  "missing-branch-xyz" on acme/widgets: there is nothing to compare`.
  **Compare** stays enabled, since BranchField refuses nothing on
  purpose.
- Direct links: `main...main` shows no changes; `main...no-such-branch`
  (404) and the pending `main...feature/walk-1756` (404) say GitHub has
  nothing to compare; `compare/feature/walk-1756` with no base reads
  "Into the default branch"; `/repos/acme/gadgets/compare/main...dev`
  (502, a repo the simulator does not have) shows "an upstream service
  returned an error (ref …)".

### 7. Releases

- `/repos/acme/widgets/releases` shows `1.0 -- Active`, `latest:
  1.0.latest, prod: 1.0`, candidate `1.0.rc.1 -- active`, the
  pending-approval banner with **Approve all**, qualification task
  **Qualify** (`/tasks/22`, opened and returned with Back), history,
  merge-back, **Delete release** and the qualification plan.
  **Cut new RC** is disabled with "Cut new RC: 1.0.rc.1 is active: a
  new RC is cut once it is promoted."
- `/releases/nope-404` is rewritten to `/releases` with the error `no
  release "nope-404"` and falls back to the newest release.
- Validation: `bad release name` and `x.latest` are refused (400) with
  the ref naming rules. `1.0` is refused with "this repo already has an
  unmerged release with this name". An empty name is held by the
  browser's own required check.
- **Lifecycle of `wt1756`**:
  - Created at `/releases/wt1756`; it went Active with `wt1756.rc.1`.
  - **Promote current RC**: the confirm "Promote wt1756.rc.1 to wt1756?
    This moves acme/widgets's wt1756 branch to wt1756.rc.1." was
    dismissed (nothing changed), then accepted. While the candidate was
    promoting, both buttons were disabled with their reasons ("…is
    promoting: a new RC is cut once it is promoted", "…only an active
    candidate can be promoted").
  - **Cut new RC** gave `wt1756.rc.2 -- cutting` ("…is still being
    cut"), then active, with `wt1756.rc.1 -- promoted` in history.
  - Reload kept all of it. The Release select switched between `1.0`
    and `wt1756`, and Back/Forward stepped between their URLs.
  - Qualification plan: Repeat `2` and **Promote automatically**
    saved and survived reload.
  - **Delete release**: the confirm was dismissed (the release stayed),
    then accepted. The pane fell to `1.0`, the URL became `/releases`,
    no false "no release" error was raised (the 2026-10-26 fix holds),
    and `GET .../releases` lists only `1.0`.
- `/repos/acme/gadgets/releases`: "acme/widgets has no releases yet --
  create one above…" is the empty state, worded per repo.

### 8. Console and network

- No `pageerror`s. `console.error` appeared only for the deliberate
  400/404/502 responses above.
- `GET /api/tasks` and the release `DELETE` show up as
  `requestfailed: net::ERR_ABORTED` in Playwright. The server answered
  (`curl` gets `204` for the DELETE and the release is gone): these are
  the browser dropping 204/304 bodies the app never reads. Nothing
  shows to the user.
- At 390 and 1280, none of `/repos/acme/widgets` (every tab),
  `/repos/acme/widgets/releases`, the compare page, the long-named
  repo's page and releases, `/settings/repositories` or
  `/repos/acme/gadgets/releases` scrolls the document sideways. Only
  the tab strip scrolls, on purpose.

## Findings

### 1. Fixed: a phone opening a repo page at a late tab saw the selected tab hidden under the scroll button

- **Reproduction** (390x844, any demo):
  1. Open `/repos/acme/widgets/settings/advanced` directly, or through
     Settings → Repositories → the row's "Read" link.
  2. Measure `.repo-page-header .MuiTabs-scroller` and
     `[role=tab][aria-selected=true]`: the strip's visible part spans
     x=56–334, and **Advanced** spans x=322–414 with `scrollLeft=299`.
     Only 12px of the selected tab shows, under the right-hand arrow.
     `/settings/releases` does the same.
- **Cause**: MUI Tabs (`variant="scrollable"`, `scrollButtons="auto"`)
  scrolls the selected tab into view against the strip's width before
  its scroll buttons exist (358px: 657 − 358 = 299). Then it finds the
  tabs overflow, adds both buttons, and the visible strip narrows to
  278px. MUI scrolls again only when the indicator moves, and adding
  buttons does not move it.
- **Fix**: `ui/src/tabStrip.js` gains `revealSelectedTab`, which
  scrolls only the strip (never the page) until the selected tab fits,
  and `useRevealSelectedTab`, a callback ref that runs it whenever the
  strip changes width (a `ResizeObserver`), and once more after MUI's
  300ms animated scroll, which would otherwise overwrite it. It stands
  aside while a tab has keyboard focus, which `revealFocusedTab`
  already follows. `RepoPage` and `SystemPage`, the two strips that
  use `revealFocusedTab`, use it.
- **Regression coverage**:
  - `ui/src/tabStrip.test.js`: the scroll arithmetic (right edge, left
    edge, already in view, not a strip).
  - `ui/e2e/repo-tab-strip-phone.spec.js`: at 390px, the selected
    tab's bounding box lies inside the strip's for `/settings/advanced`,
    `/settings/releases` and `/settings/ci`. Against the unfixed build
    the first two fail (`414 <= 335`); with the fix all pass 12/12
    under `--repeat-each=4`.
  - Also re-run green: `repo-tabs-walkthrough`,
    `deployment-operations-navigation` (the End-key strip race
    `revealFocusedTab` documents), `repo-pane`,
    `repo-page-unsaved-edits` and `base-branch-gate-warnings-walkthrough`.
    The `RepoPage`, `SystemPage` and `tabStrip` vitest suites passed
    (96 tests).
- **Rendered**: `make ui-screenshots` (via `node e2e/screenshot.mjs`)
  of `/repos/acme/widgets`, `/repos/acme/widgets/settings/advanced` and
  `/repos/acme/widgets/settings/releases`, at desktop and phone, light
  and dark, before and after. At phone the selected tab moved from
  x=322–414 to x=242–334; desktop is unchanged, since that strip does
  not scroll.

### Observations not changed here

- The repo-list count chips are unreadable at 390px (§1). This is
  documented as intended.
- The rail entries are `role="button"`, not links, so a rail page
  cannot be opened in a new tab. This is app-wide, not this area's.
- **Base-branch repair PRs may merge without a human** has no
  confirmation (§4).
- A reload of `/new` forgets the repo page it was opened from (§3).
- The demo never creates branches added on the Branches tab (§6).

### Declared trigger sync

After `.grain/triggers/grain-declarations.yml` was pushed to the
simulated `acme/widgets` (a copy of this repository's own), the
orchestrator re-read it within the sync window (`acme/widgets at
d5988dd56551 declares … 1 trigger(s)` in the demo log). The Advanced
tab then read "Read at d5988dd56551: 0 template(s), 0 suite(s), 0
schedule(s), 1 trigger(s)". The tab gives the trigger as a count only:
it does not list the trigger or link to it.
