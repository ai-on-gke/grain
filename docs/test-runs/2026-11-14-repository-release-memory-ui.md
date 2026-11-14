# 2026-11-14 — Repository, merge queue, compare and release UI walkthrough (task 5632)

## Environment
- **Repository**: `ai-on-gke/grain`, branch
  `grain/53549e42/task-5632-walk-through-repository-merge`.
- **Commit tested**: `3266698ea` (`main` at the start of the run; build
  stamp `v0.0.0-20261114110730-3266698eaa13`). The fixes below were
  re-verified on this branch's builds (`e21fefece`, then `e9bd99879`).
- **Build**: `make build` (the Vite `ui/` bundle and the Angular `ui-ng/`
  bundle embedded in the binary), then `go build -tags grain_e2e ./cmd/grain`
  for the demo's loopback-only GitHub-simulation routes. Fix builds
  re-ran `ng build --configuration production` in `ui-ng/` before the
  `go build`.
- **Browser**: Playwright (ui-ng's pinned version) headless Chromium,
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `timezoneId: UTC`.
  Throwaway node scripts outside the repository drove visible controls
  (rail links, tabs, switches, pill menus, text boxes, buttons, dialogs,
  browser Back/Forward, reload). Viewports 1280×900/844 and 390×844, light
  and dark.
- **Gemini**: this sandbox has no `/home/debian/.gemini-api-key`, so
  nothing was Gemini-backed and `LOOK=1` was unavailable. The file reader
  here shows PNGs as bytes, so layout was checked with DOM geometry
  (bounding boxes, `scrollWidth`, computed colours) alongside the
  `ui/e2e/screenshot.mjs` contact sheets attached to the task.
- **Servers**: each a real `grain demo -open=false -access-code-file …`
  over its own throwaway embedded store, signed in through
  `POST /api/auth/access-code` as `alice@example.com`.

| Demo | Port | Build | Flags | Used for |
| --- | --- | --- | --- | --- |
| A | 5632 | base | `-github-sim -reconcile=1s -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification` | the walkthrough and every reproduction |
| B | 5633 | fixes | `-github-sim -reconcile=1s -fake-ci -base-branch-ci -merge-queue-states -qualification` | re-verifying the fixes, "after" screenshots |

## Stubs and external-boundary helpers
Grain's own API, the React UI and the Angular UI were never mocked or
intercepted; no `page.route` was used.
- **GitHub**: the in-process GitHub simulation (`-github-sim`) over a real
  bare repository stood in for GitHub (branches, compare, issue listing
  and import, release branches, RC cut and promote).
- **Seeding**: `git push` into `grain-demo-*/githubsim/acme/widgets.git`
  of `feature/walk-5632` and `fix/#5632-hash` (one commit adding
  `WALK5632.md`).
- **Direct API calls** only to read persisted state back after a UI
  mutation.

## Which UI each step ran against
`ui-ng/src/app/shell/nav.ts` has no Repos entry and `app.routes.ts` has
no `/repos` list, compare, per-release or Advanced route, so:

| Step | UI |
| --- | --- |
| Repos list (counts, search/sort, long names, Add repo, reorder) | classic `/repos` (no `/next/` page; `/next/repos` falls through to the Inbox) |
| Repo row → task count → filtered task list | classic |
| Repo page tabs, URL, reload, Back/Forward, phone | `/next/repos/<owner>/<name>/<tab>` |
| Settings → Repositories | `/next/settings/repositories`, then classic `/settings/repositories` for the declarations / git-connection columns `/next/` lacks |
| Overview: issue import, lifecycle | `/next/` |
| CI mode, Watch default branch, workflows | `/next/` |
| Merge queue switches, bypass confirmation | `/next/` (repo page and Settings → Repositories) |
| Agents: setup, prompt, default capabilities | `/next/` |
| Branches: Add branch | `/next/` |
| Releases: list, Cut/Promote/Merge back/Delete, create | `/next/` |
| Compare (`/repos/<o>/<n>/compare/<base>...<head>`) | classic (no `/next/` page) |
| Unknown release URL, Advanced, work ownership, qualification plan | classic (no `/next/` page) |

## Steps and outcomes

### Repos list and task counts (classic)
- `/repos` shows `acme/widgets` with `blocked 1` first on the row, then
  proposed/waiting/running/asked/merging/deferred and `17 tasks`.
- **Add repo**: the 70-character
  `acme-observability-platform/an-extraordinarily-long-repository-name-5632`
  was added; the button was disabled while the request was in flight; no
  horizontal overflow at 1280 px (`scrollWidth` 1280). At 390 px Add repo
  sits at x 303–374, on screen.
- **Search**: `zzz-none` reads `No repos match your search.`
- `17 tasks filed here` on `/repos/acme/widgets` opens
  `/?f=repo:any:acme/widgets`; Back returns to the repo.
  `/?f=repo:any:ACME/Widgets` lists the same 17 tasks.

### `/next/` repository page: tabs and addresses
- Each tab button sets `/next/repos/acme/widgets/<tab>` (`ci`,
  `merge-queue`, `agents`, `branches`, `releases`; Overview is the bare
  path). Reload keeps `releases`; Back goes to Overview and Forward to
  Releases.
- **The classic address `/next/repos/acme/widgets/settings/ci` loaded a
  repository called `acme/widgets/settings`**: bug 3.
- **Phone**: a deep link to `/releases` at 390 px left the selected tab
  at x 588, off screen: bug 4.
- `← Repositories` goes to `/next/settings/repositories`, whose row shows
  base branch `main`, CI mode `grain`, the bypass switch and
  Settings/Releases/Remove. There are no declarations or git-connection
  columns there; classic Settings → Repositories shows both (`Read`,
  `Not listed`).

### Overview: issue import
- With source `acme/nosuch-5632`, `Import issues now` answered `200` with
  `error: "… GitHub answered 404: Not Found"`, but the page said
  `Issue import triggered`. There was no queue-at-once switch: bug 1.
- **Unsaved edit**: typing a label and switching to CI dropped it without
  a word. An unsaved setup command also vanished on a tab round trip: bug 2.

### CI, merge queue, agents, branches (`/next/`)
- CI mode pill offers exactly Grain / GitHub / No CI; each choice was
  stored (`github`, `none`, `grain`), as fixed on 11-13.
- Merge queue: `Require branch up to date` persisted across a reload.
  Turning the bypass on opens `Let base-branch repairs merge without a
  human?`, and Escape leaves it off. `Merge on green` reads `Land approved
  pull requests automatically once checks succeed.`, with no ordering claim.
- No base-gate warning appears on Overview or Releases for `github` or
  `none` on `/next/` (the classic page has them). This is a parity gap,
  listed below.
- Branches: `walk/5632-branch` from `main` was created (`status: pending`).

### Releases (`/next/`)
- **Delete** dropped release `1.0` and its candidates on the first click,
  with no question. **Promote RC** promoted `1.0.rc.1` on the first click.
  **Cut RC** was enabled while `1.0.rc.1` was active, with no reason
  given. **`bad name`** was refused in the page-wide banner, not beside the
  box: bug 5.

### Compare, unknown release, Advanced (classic)
- `compare/main...feature/walk-5632` and `compare/main...fix/%235632-hash`
  show `WALK5632.md`, and the second survives a reload.
- `compare/main...nosuch-5632` reads `GitHub has nothing to compare for
  main...nosuch-5632 in acme/widgets: one of the two branches does not
  exist…` (404 from the compare route).
- `/repos/acme/widgets/releases/nosuch-5632` reads `acme/widgets has no
  release named "nosuch-5632" -- the link may be stale, or the release
  deleted.` (no release was left to fall back to, after bug 5's delete).
- Advanced shows declarations (`Read at d7cff07aa058: 0 template(s) …`)
  and Remove repository. There are no extra push refs and no Ports table
  on this store (no paired branches).

### Not exercised
- The overlap re-check panel: no demo task carries a merge re-check, and
  none was stubbed.
- Work ownership and the qualification-plan editor were not re-walked on
  classic this run; the 11-13 report covers them and nothing in this
  diff touches them.

### Console and network audit
- **Uncaught page errors**: 0.
- **Every 4xx** was a deliberate negative case: 400 for `bad name` and
  for Import issues with the importer off, 404 for the unknown compare
  ref. Bug 3's 400s were `GET /api/repos/acme%2Fwidgets/settings/...`.
- `net::ERR_ABORTED` on `/api/tasks` and `/api/config` came from
  navigating away mid-request.

## Bugs found and fixed (all in `ui-ng/`; the classic pages were correct)

### 1. Import issues now hid its outcome, and the queue-at-once switch was missing
- **Repro** (demo A): on `/next/repos/acme/widgets`, turn the importer on,
  set source `acme/nosuch-5632`, Save, then Import issues now.
- **Observed**: the snackbar said `Issue import triggered`, though the
  response carried `error` and `open: 0`.
- **Fix**: the page now words the outcome the way the classic page does
  (`importOutcomeText`): `Could not read the open issues on <src>: …`,
  or `Read N open issues on <src>: imported X, Y already imported.` (plus
  `Some could not be imported: …`). It shows in an inline `role=status`
  line in the error colour, and the button is off while a pass runs.
  Added the `Queue imported tasks at once` switch with the classic trust
  warning, naming the source repository.
- **Verified** (demo B): `Could not read the open issues on
  acme/nosuch-5632: … 404`. With the source cleared, `Read 1 open issue on
  acme/widgets: imported 1, 0 already imported.` The queue-at-once switch
  persisted across a reload. At both widths the switch lines up with the
  enable switch (x 1011 at 1280, x 313 at 390), the hint stays inside the
  card, and `scrollWidth` equals the viewport.

### 2. Unsaved edits were dropped without asking
- **Repro**: type an issue-import label (or setup command, prompt, default
  capabilities, workflow YAML), then click another tab, `← Repositories`,
  a rail link or browser Back.
- **Fix**: the page keeps what the server last said for each Save-button
  group. `canLeave()` asks `Discard unsaved changes?` in the shared
  confirm dialog, naming the sections; it runs from the tab bar and from a
  `canDeactivate` guard on every repository route (duck-typed in
  `app.routes.ts`, so the page stays lazy), plus `beforeunload`. Discard
  restores the saved values.
- **Verified**: tab switch, rail `Tasks` and Back all ask. Cancel keeps
  the URL and the text. Discard leaves and nothing is stored. A saved edit
  leaves with no question.

### 3. The classic tab address loaded the wrong repository under `/next/`
- **Repro**: open `/next/repos/acme/widgets/settings/ci`.
- **Observed**: 400s for `GET /api/repos/acme%2Fwidgets/settings/settings`
  and `…/workflows`.
- **Fix**: a matcher redirects `/repos/<o>/<n>/settings[/<tab>]`
  (owner without `.`/`:`) to `/repos/<o>/<n>[/<tab>]`.
- **Verified**: `/settings/merge-queue` → `/next/repos/acme/widgets/merge-queue`,
  with no 4xx.

### 4. The selected tab sat off screen on a phone
- **Repro**: at 390 px open `/next/repos/acme/widgets/releases`.
- **Fix**: an `afterRenderEffect` scrolls the active tab into view
  whenever the tab changes.
- **Verified**: Releases at x 284–374, Branches at 281–374, CI at 110–239,
  with the page not scrolled vertically.

### 5. Release actions ran on the first click
- **Repro** (demo A): on `/next/repos/acme/widgets/releases` click
  Delete (or Promote RC); type `bad name` and Create release.
- **Fix**:
  - Promote RC, Merge back and Delete open the confirm dialog with the
    classic copy (`Promote 1.0.rc.1 to 1.0? This moves acme/widgets's 1.0
    branch to 1.0.rc.1.`, `Remove release 1.0? …`).
  - Cut RC and Promote RC are off with the classic reasons
    (`releaseActionReasons`), shown under the table and as the button title.
  - A refused name is reported beside its box with `aria-invalid=true`,
    and clears on the next edit or a clean create.
- **Verified** (demo B, 1280 light / 390 dark):
  - The dialog sits at x 355 w 570 (desktop) and x 16 w 358 (phone).
  - Cancel and Escape change nothing.
  - `Cut RC: 1.0.rc.1 is active: a new RC is cut once it is promoted.`
  - The inline error shows with no banner and clears on edit.
  - `walk-5632-1280` was created and survived a reload.

## Regression coverage
- `ui-ng/e2e/next-repo-import-releases.spec.js` (new, real demo with
  `-github-sim -qualification`) covers each of the five bugs.
- `ui-ng/src/app/repos/repo-detail-page.component.spec.ts`:
  - `importOutcomeText` and `releaseActionReasons`.
  - The release test now uses real statuses (`active`, not `open`) and
    asserts Cut RC off with its reason, and Promote asking before it calls.
- Ran: that spec, `next-repo-ci-mode-bypass.spec.js`,
  `next-settings-repos.spec.js`, `next-full-system.spec.js`,
  `next-walkthrough-daemon-rbac-repos.spec.js`, `next-qa-settings.spec.js`,
  and vitest for `src/app/repos`.

## Parity gaps left for follow-up (not bugs in what `/next/` has)
- No `/next/repos` list page or rail entry, no compare page, no per-release
  URL, no Advanced tab, no work ownership or qualification-plan editor,
  and no CI capabilities card.
- No base-gate warnings on Overview/Releases for `github`/`none`.
- `/next/` Settings → Repositories has no declarations or git-connection
  columns.
- No issue-import interval field; saving leaves the stored interval
  alone, so nothing is lost.
