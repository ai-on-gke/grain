# 2026-11-16 — Repository, merge queue, compare and release UI walkthrough (task 6333)

## Environment
- **Repository**: `ai-on-gke/grain`, branch
  `grain/53549e42/task-6333-walk-through-repository-merge`.
- **Commit tested**: `feb34cb22` (`main` at the start of the run). The
  fixes below were verified on this branch's builds (`03ad70162`, then
  `5735f27f0`).
- **Build**: `make frontend` (the Vite `ui/` bundle and the Angular `ui-ng/`
  bundle), then `go build -tags grain_e2e ./cmd/grain` for the demo's
  loopback-only GitHub simulation. Fix builds re-ran
  `ng build --configuration production` in `ui-ng/` before the `go build`.
- **Browser**: Playwright 1.62.1 headless Chromium,
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `timezoneId: UTC`,
  Node 20.19. Throwaway node scripts outside the repository drove visible
  controls: rail links, tab buttons, switches, pill menus, text boxes,
  buttons, dialogs, browser Back/Forward and reload. Viewports were
  1280×900 and 390×844 (with `hasTouch`/`isMobile` for the tap reorder),
  in light and dark.
- **Gemini**: this sandbox has no `/home/debian/.gemini-api-key`, so
  nothing was Gemini-backed and `LOOK=1` was unavailable. The file reader
  here shows PNGs as bytes. Layout was therefore checked with DOM geometry
  (bounding boxes, `scrollWidth`) and axe, alongside the
  `ui/e2e/screenshot.mjs` contact sheets attached to the task.
- **Servers**: each a real `grain demo -open=false -access-code-file …`
  over its own throwaway embedded store, signed in through
  `POST /api/auth/access-code` as `alice@example.com`.

| Demo | Port | Flags | Used for |
| --- | --- | --- | --- |
| A | 6333 | `-github-sim -reconcile=1s -fake-ci -base-branch-ci -merge-rules -merge-queue-states -qualification -scheduled-workflows` | the walkthrough, every reproduction, and (restarted on each fix build) the re-verification |
| B | 6334 | `-github-sim -qualification -external-config-repo https://example.com/acme/grain-config.git` | config-repo-locked rendering |

## Stubs and external-boundary helpers
Grain's own API, the React UI and the Angular UI were never mocked or
replaced.
- **GitHub**: the in-process GitHub simulation (`-github-sim`), over a
  real bare repository, stood in for GitHub. It served branches, compare,
  issue listing and import, release branches, and RC cut and promote.
- **Seeding**: `git push` into `grain-demo-*/githubsim/acme/widgets.git`
  of `feature/walk-6333` and `fix/#6333-hash` (one commit adding
  `WALK6333.md`).
- **One `page.route`**: in the Add repo step, every non-GET `/api/**`
  request was held for 1.5 s and then **continued to the real server**
  (`route.continue()`), so the in-flight lock could be observed. No
  response was fabricated.
- **Direct API calls** were used only to read persisted state back after
  a UI mutation, and in the e2e spec to set up and tear down repositories.

## Which UI each step ran against
`ui-ng/src/app/shell/nav.ts` now has a Repos entry and `app.routes.ts` has
`/repos`, `/repos/<o>/<n>[/<tab>]` and a redirect from the classic
`/repos/<o>/<n>/settings/<tab>`. There is no compare route and no
per-release route.

| Step | UI |
| --- | --- |
| Repos list (counts, search/sort, long names, Add repo, reorder) | `/next/repos` |
| Repo row → task count → filtered task list | `/next/` |
| File a task from the repo page | `/next/` (New task added in this run) |
| Repo page tabs, URL, reload, Back/Forward, phone | `/next/repos/<o>/<n>/<tab>` |
| Settings → Repositories | `/next/settings/repositories` |
| Overview: config, issue import, lifecycle | `/next/` |
| CI: mode, Watch default branch, workflows, CI capabilities | `/next/` |
| Merge queue switches, bypass confirmation, overlap re-check | `/next/` (re-check panel added in this run) |
| Agents: setup, prompt, default capabilities | `/next/` |
| Branches: Add branch | `/next/` |
| Releases: ownership, release settings, list, Cut/Promote, create | `/next/` |
| Advanced: declarations, trigger files, push refs, ports, Remove | `/next/` |
| Compare (`/repos/<o>/<n>/compare/<base>...<head>`) | classic — **no `/next/` page** |
| Per-release URL, unknown release fallback | classic — **no `/next/` page** |
| Overlap re-check on the task page | `/next/task/5` (added in this run), cross-checked on classic `/tasks/5` |

## Steps and outcomes

### Repos list (`/next/repos`)
- **Rail**: Inbox → Repos opens `/next/repos`. The heading reads `Repos 1`.
  The `acme/widgets` row reads `Blocked 1` first, then Proposed 3,
  Waiting 4, Running 2, Asked 2, Ready to submit 2, Merging 3, Deferred 2,
  `18 open`.
- **Add repo**: added
  `acme-observability-platform/an-extraordinarily-long-repository-name-6333`
  and `acme/gadgets-6333`. While the POST was held, the button read
  `Adding…` and was disabled, and so was the input. After the add the page
  said `Added acme/gadgets-6333`.
- **Long name**: `scrollWidth` stayed 1280 at desktop. On the busy
  `acme/widgets` row the Blocked chip is the first `[data-chip]`, and it
  sits fully inside the row (x 286–361 of 101–1251).
- **Phone (390)**: Add repo sits at x 289–374, on screen; `scrollWidth` 390.
- **Search**: `zzz-none` reads `No repos match “zzz-none”.` with
  `Clear the filter`. `GADGET` (upper case) matches `acme/gadgets-6333`.
- **Sort**: offers Custom order, Name (A–Z), Active first and Most tasks.
- **Keyboard reorder**: on the `acme/gadgets-6333` handle, Space, Home,
  Space moved it to position 1 and announced `Dropped acme/gadgets-6333
  at position 1`. The order survived a reload.
- **Pointer drag**: dropping `acme/widgets` on the upper half of the first
  row moved it up one place. A drop on the lower half of the row just
  above is a no-op by design, since it lands where the row already is.
- **Tap reorder**: tapping one handle picked that row up. Tapping a second
  handle only picked *that* row up instead, so a finger had no way to put
  a row down anywhere else: **bug 2**.

### Repo row → task list
- The row name opens `/next/repos/acme/widgets`; Back returns to
  `/next/repos`.
- `18 open` opens `/next/tasks?repo=acme%2Fwidgets` with an
  `acme/widgets ×` chip. Back returns to the list.
  `/next/tasks?repo=ACME/Widgets` lists the same rows (case-insensitive).
- **But the tab counts ignored the filter.** `?repo=acme/gadgets-6333`
  (a repo with one closed task) and `?repo=nosuch/zzz` both read
  `Backlog 4 … Submit & land 11 … All 24`, the same as no filter, over an
  empty backlog: **bug 4**.
- **Removed repository**: filed a task into `acme/gadgets-6333` and closed
  it, then removed the repo from Advanced.
  - The `/next/` list drops the repo, since it counts open work only.
  - The `/next/` repo page showed **no count at all**, where classic
    `/repos/acme/gadgets-6333` says `1 task still targets this repo, which
    is no longer on the target list`: **gap 5**, fixed in this run.
  - `/next/tasks?repo=acme/gadgets-6333&tab=closed` lists `#28`.

### File a task from the repo page
- The `/next/` repo page had no New task. Filing into a repository meant
  opening the global sheet and choosing the repository again: **gap 3**,
  fixed in this run.
- **After the fix**: New task on `/next/repos/acme/gadgets-6333` opens the
  sheet with Repository `acme/gadgets-6333`. Create and start filed task
  #28, and `GET /api/tasks` reads it back with `repo: acme/gadgets-6333`.

### Repo page tabs and addresses
- **Tabs and URLs**: Overview `/next/repos/acme/widgets`, then `/ci`,
  `/merge-queue`, `/agents`, `/branches`, `/releases`, `/advanced`.
- **Reload and history**: reload keeps Advanced. Back goes to Releases and
  Forward to Advanced.
- **Classic address**: `/next/repos/acme/widgets/settings/merge-queue`
  redirects to `/next/repos/acme/widgets/merge-queue`. `/next/` keeps the
  tab at `/repos/<o>/<n>/<tab>`, without the classic `/settings/` segment.
- **Unknown tab**: `/next/repos/acme/widgets/nosuch-tab` shows Overview.
- **Phone**: covered by `next-repo-import-releases.spec.js`, which passed.

### Settings → Repositories (`/next/settings/repositories`)
- **Columns**: each row shows base branch `main`, declarations
  (`Read`/`Not read`), CI mode, git connection (`Not listed`), the bypass
  switch, and Settings/Releases/Remove.
- **Bypass switch**: turning it on asks `Let base-branch repairs merge
  without a human? …`. Turn on stores it, and it is still on after a
  reload. Turning it off asks nothing.
- **Links**: Releases opens `/next/repos/acme/widgets/releases`.
- **Check stranded tasks** runs. **Remove** on `acme/widgets` asks
  `Remove repository acme/widgets? Warning: This will strand 16 tasks or
  schedules. …`, and Cancel leaves it.

### Overview
- **Repository config & Git connection**, **Issue importer & lifecycle
  refresh**: with source `acme/nosuch-6333`, label `grain` and interval
  15, switching tab first asks `Discard unsaved changes? You have unsaved
  issue import changes …`. Cancel stays on the page. Save stores
  `{enabled, repo: acme/nosuch-6333, label: grain, intervalSeconds: 900}`.
- **Import issues now** (source not listable) reads `Could not read the
  open issues on acme/nosuch-6333: … GitHub answered 404: Not Found`.
- **Import issues now** (source cleared) reads `Read 1 open issue on
  acme/widgets: imported 1, 0 already imported.`
- **Queue imported tasks at once** shows its trust warning naming
  `acme/widgets`. Turned on and saved, it is still on after a reload.
- **Gaps**: Overview has no summary, recent CI runs or overlap re-check
  card (classic shows recent CI runs). The re-check is now on Merge Queue.

### CI
- **CI mode**: GitHub, No CI and Grain CI each stored (`ciMode`
  `github`/`none`/`grain`).
- **Base-gate warning on `acme/gadgets-6333`** (never enrolled, Watch
  default branch on):
  - Under GitHub and No CI it shows on Overview and Releases: `Nothing is
    testing acme/gadgets-6333's default branch, and the merge queue is
    merging onto it anyway. …`, with `Go to CI Mode`.
  - The link opens `/ci#repo-ci-mode` with the CI mode pill focused.
  - It clears when the mode goes back to Grain CI, with no reload.
- **On `acme/widgets`** (already enrolled by base-branch CI under grain
  mode), switching to No CI raises no warning. The server's
  `baseBranchGates` reads the existing row as judged (`pkg/ui/basebranchgate.go`,
  `row == nil` is the only "not enabled" case). Noted, not changed: this
  is the server's reading, not the UI's.
- **Work ownership**: turning `Standing work on this deployment` off on
  `acme/widgets` stored it and raised no gate warning, for the same
  reason. It was turned back on.
- **CI capabilities** lists GitHub sandbox, Registry push, Package publish
  and Image signing (the `release` server's methods), plus `+ Add API
  scope or method grant`.
- **Declared workflow** has an editor, and the unsaved-workflow guard is
  covered by `next-repo-import-releases.spec.js`.

### Merge queue
- **Copy**: `Merge on green — Land approved pull requests automatically
  once checks succeed.` makes no ordering claim.
- **Merge on green** was turned on and was still on after a reload.
  `Require branch up to date` toggles and persists.
- **Bypass**: turning it on asks `Let base-branch repair PRs in
  acme/widgets merge without a human when their CI is green? …`.
  - Escape leaves it off, both in the UI and stored.
  - Turn on stores `true`.
  - Turning it off asks nothing and stores `false`.
- **Unfixable checks** has no `/next/` control (`unfixableChecks` is in
  the settings response): **gap**, proposed below.
- **Overlap re-check**: the demo seeds one
  (`GET /api/merge-batches` → `acme/widgets` `main`, task 5, `running`,
  `maxAgeSeconds 5400`).
  - Classic `/tasks/5` shows `merge re-check … testing … 20m of 90m …
    show the run`.
  - `/next/` showed it nowhere: **gap 6**, fixed in this run.
- **After the fix**: the Merge Queue tab's `Merge re-checks` card reads
  `acme/widgets → main | Testing | 5m of 90m | Show the run |
  grain-merge-queue/…/recheck/5/main at abcdef012345 | #5 Rotate the
  Gemini signing key · PR #101`.
  - `Show the run` opens `/next/ci/runs/demo-recheck-1`, and `#5` opens
    `/next/task/5`.
  - `/next/task/5` shows `Merge re-check` above its gates; task 2 (in no
    re-check) shows none.

### Agents
- **Leave guard**: typing a setup command and clicking rail Tasks asks
  `Discard unsaved changes? You have unsaved agent settings changes on
  acme/widgets.` Cancel stays on the page.
- **Refused default capability**: saving with
  `not-a-real-capability-6333` shows the banner `defaultCapabilities:
  unknown capability not-a-real-capability-6333`. The setup command and
  prompt still saved, and both were still there after a reload.
- **The banner stayed** after the field was cleared and saved cleanly
  (`PUT …/capabilities` 200): **bug 1**.
- **Gaps**: no Upstream repository field, and none of the read-only
  deployment or `.grain/settings.yml` prompt layers (classic has both).
  Proposed below.

### Branches
- `walk/6333-branch` from `main` was created and listed (head `—`, pending
  on the simulation).

### Releases (`/next/`)
- **Work ownership**: the repository default and each item (release,
  base-ci, post-merge, ports, triage, sched-1, sched-2) offers On / Off /
  Say nothing.
- **Release actions**: Cut RC on `1.0` is off and says why, both under
  the table and as the button's title: `1.0.rc.1 is active: a new RC is
  cut once it is promoted.`
- **Refused name**: `bad name` is reported beside the box (`invalid
  release name: …`) and clears on the next edit.
- **Create**: `walk-6333` was created and is still listed after a reload.
  It came with `walk-6333.rc.1` active, so its Cut RC is off.
- **Promote RC** asks `Promote walk-6333.rc.1 to walk-6333? This moves
  acme/widgets's walk-6333 branch to walk-6333.rc.1.`, and Escape cancels.
- **Inspect RCs** lists the candidates and their qualification. It does
  not change the URL, since `/next/` has no per-release address.

### Compare and release URLs (classic only)
- **Pickers**: on `/repos/acme/widgets/settings/branches` the Head branch
  field lists the repository's own branches from `GET …/github-branches`
  (`1.0.latest`, `1.0.rc.1`, `candidate`, `feature/walk-6333`,
  `fix/#6333-hash`, `main`, `release/1.0`). Typing `walk` narrows it to
  `feature/walk-6333`. Compare with `fix/#6333-hash` opens
  `/repos/acme/widgets/compare/main...fix/%236333-hash`, showing
  `WALK6333.md +1 -0`, and Back returns.
- **Direct links**: `compare/main...feature/walk-6333` shows the same diff
  on a direct link. `compare/main...nosuch-6333` reads `GitHub has nothing
  to compare for main...nosuch-6333 in acme/widgets: …` (404), both on
  first load and after a reload.
- `/repos/acme/widgets/releases/1.0` shows `1.0.rc.1 -- active`, its
  qualification `pending approval`, and the reasons.
- `/repos/acme/widgets/releases/nosuch-6333` reads `acme/widgets has no
  release named “nosuch-6333” -- showing 1.0 instead. The link may be
  stale, or the release deleted; pick another below.`

### Advanced
- Shows declarations (`Read at 4bfcf6cb9974: 0 template(s), …`), `acme/widgets
  declares no path triggers.`, `… no templates that push beyond their own
  branch.`, and `No branches are paired — operator: none; declared in
  .grain/settings.yml: none.` There are no paired branches, so there is
  no Ports table.
- **Remove** asks `Remove acme/widgets from target repos? … 15 tasks still
  in play there are parked …, 2 schedules … stop firing … and its
  release-qualification plan no longer runs.` Cancel leaves it.
- **Remove** on `acme/gadgets-6333` removed it.

### Config-repo-locked rendering (demo B)
- The repo page shows `Repository configuration is managed in the config
  repository; operational actions (branches, issue import, and releases)
  remain available according to your role.`
  - Setup command, prompt extension, default capabilities and their Save
    are disabled, as are all three Merge Queue switches and the CI mode
    pill.
  - The qualification plan reads `… is managed in the config repository
    (tables/qualification_config.json, …); edit it there.`
- Settings → Repositories reads `Read-only … Managed in config repo …
  (tables/repo_config.json)`, with its bypass switch disabled.

### Console and network audit
- **Uncaught page errors**: 0. **Angular `ERROR`s**: 0.
- **Every 4xx** was a deliberate negative case:
  - 400 for `bad name` (POST releases) and for the unknown capability
    (PUT capabilities);
  - 404 for the unknown compare ref;
  - 401 once, from a walk script that sent the wrong access code.
- `net::ERR_ABORTED` on `/api/config` and `/api/tasks` came from
  navigating away mid-request.

## Bugs and gaps fixed (all in `ui-ng/`)

### 1. A refused repo setting's banner outlived the fix
- **Repro** (demo A): `/next/repos/acme/widgets/agents`, Default
  capabilities `not-a-real-capability-6333`, Save. Clear the field, Save.
- **Observed**: the banner stayed until Dismiss.
- **Fix**: every refusal records which save raised it (`fail(from, err)`),
  and that save's clean success clears it (`saved(from)`). This covers the
  repo config, git connection, declarations, issue import, CI mode, Watch,
  workflows, release settings, the three merge-queue switches and agent
  settings. A different section's refusal stays up.
- **Verified**: the banner cleared on the clean save. It stayed up across
  a Require-up-to-date toggle and cleared when agent settings saved.

### 2. A finger could not reorder repos
- **Repro**: on `/next/repos` (touch, 390 px), tap one handle, then
  another.
- **Fix**: with a row held, tapping another row's handle puts the held row
  in that row's place, as the classic list's Move here does. Focus follows
  the moved row. A visible hint shows while a row is held (`Moving <repo>:
  tap another repo's handle to put it there, or its own handle to leave it
  where it is.`). Tapping the held row's own handle still drops it in
  place.
- **Verified** (touch context): `acme/widgets` moved from 3rd to 1st and
  announced `acme/widgets moved to position 1 of 3`; the order survived a
  reload. The hint sits at x 16–374 at 390 px.

### 3. No New task on the repo page
- **Fix**: New task beside `View tasks in <repo>` opens the sheet with the
  repository chosen. It is gated on `POST /api/tasks` or
  `/api/tasks/from-template`, as the rail's is.
- **Verified**: as above (task #28, `repo: acme/gadgets-6333`). At
  1280 px the button sits at x 996–1080.

### 4. Task tab counts ignored the repo filter
- **Repro**: open `/next/tasks?repo=nosuch/zzz`.
- **Observed**: Backlog 4 over an empty list.
- **Fix**: `tabCounts` narrows the store's counts the way `rows()` narrows
  the list (repo, case-insensitive, and the text filter). A narrowed
  Closed count shows only once every closed page is loaded, since closed
  tasks are paged from the server.
- **Verified**: `?repo=nosuch/zzz` reads Backlog 0, and `?repo=ACME/Widgets`
  equals `?repo=acme/widgets`.

### 5. A removed repository's page said nothing of what was left there
- **Fix**: the repo page reads `GET /api/tasks?repo=<r>&limit=1&cursor=`
  for its `total` (closed included). It shows `N tasks filed here, closed
  ones included.`, or `N tasks still target this repo, which is no longer
  on the target list.` once the repo is off a non-empty target list. The
  count links to `/tasks?repo=<r>`.
- **Verified**: `acme/widgets` reads `21 tasks filed here, closed ones
  included.` A removed repo with one task reads `1 task still targets this
  repo, …`, and its link opens `/next/tasks?repo=acme%2Fgone-6333`.

### 6. The merge queue's overlap re-checks showed nowhere on /next/
- **Fix**: `g-merge-recheck-panel`
  (`ui-ng/src/app/repos/merge-recheck-panel.component.ts`), ported from
  the classic `MergeBatchPanel.jsx`. It shows the standing chip (Testing /
  Passed / Failed / Abandoned, with the classic tooltips), elapsed time
  against the max age, ref and commit, the CI run link and the member
  task links. It refreshes every 10 s.
- **Where**: on the Merge Queue tab, with the classic empty-state sentence
  when nothing runs, and above the task page's gates for a task in a
  re-check, rendering nothing otherwise.
- **Verified**: as above. At 1280 the panel sits at x 125–1055;
  `scrollWidth` stays at the viewport width. axe found no serious or
  critical violations at desktop, phone and dark.

## Regression coverage
- `ui-ng/e2e/next-repo-walkthrough-fixes.spec.js` (new, real demo with
  `-github-sim -qualification`) covers each of the six. Tests tagged
  `@phone`/`@dark` also run in those projects.
- `ui-ng/src/app/repos/merge-recheck-panel.component.spec.ts`: `batchAge`
  and `batchStanding`.
- **Ran, all passing**:
  - that spec (11 tests across projects);
  - `next-repos-list`, `next-repo-import-releases`, `next-full-system`,
    `next-settings-repos`, `next-repo-ci-mode-bypass`,
    `next-repo-base-gate-warning`, `next-repo-advanced`,
    `next-repo-ownership-plan-caps`, `next-tasks-batch-actions`,
    `next-saved-boards`, `next-task-interactions` and
    `next-task-lifecycle-regressions`;
  - vitest for `src/app/repos` and `src/app/tasks` (102 tests).

## Not exercised
- Role-restricted (non-admin) rendering: the demo signs everybody in
  through the access code as an admin. The config-repo lock was walked
  instead.
- Release trains, the qualification plan editor's unsaved-edit guard and
  Merge back were not re-walked. `next-repo-ownership-plan-caps.spec.js`
  covers the plan guard and passed.
- Whether the classic repo page keeps a stale banner the same way
  (bug 1) was not checked. Classic reports agent-settings refusals through
  its own snackbar and form state.

## Gaps left for follow-up (not bugs in what `/next/` has)
- No `/next/` compare page or per-release URL (with the unknown-release
  fallback).
- No Unfixable checks control on Merge Queue.
- No Upstream repository field and no read-only prompt layers on Agents.
- Overview has no summary or recent CI runs.
- Server: an already-enrolled base branch raises no gate warning after
  its CI mode goes to github/none.
