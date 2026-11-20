# 2026-11-18 — Repository, merge queue, compare and release UI walkthrough (task 7017)

## Environment
- **Repository**: `ai-on-gke/grain`, branch
  `grain/53549e42/task-7017-walk-through-repository-merge`.
- **Commit tested**: `f022b607f` (`main` at the start of the run; build
  stamp `v0.0.0-20261118040016-f022b607f174`). The fixes were re-verified
  on this branch's build (`ba9a8b577`).
- **Build**: `make build`, then `go build -tags grain_e2e ./cmd/grain` for
  the demo's loopback-only GitHub simulation. The fix build re-ran
  `npm run build` in `ui-ng/` before its `go build`.
- **Browser**: Playwright 1.62.1 (ui-ng's pinned version, after `npm ci`
  in `ui-ng/` -- `make build` installs only its production dependencies)
  with headless Chromium from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`
  and `timezoneId: UTC`. Throwaway node scripts outside the repository
  drove visible controls: rail links, tabs, switches, pill menus, text
  boxes, buttons, dialogs, drag handles, browser Back/Forward and reload.
  Viewports were 1280×900 and 390×844.
- **Gemini**: this sandbox has no `/home/debian/.gemini-api-key`, so
  nothing was Gemini-backed and `LOOK=1` was unavailable. Layout was
  checked with DOM geometry (bounding boxes, `scrollWidth`).
- **Servers**: each was a real `grain demo -open=false -access-code-file …`
  over its own throwaway embedded store, signed in through
  `POST /api/auth/access-code` as `alice@example.com`.

| Demo | Port | Build | Flags | Used for |
| --- | --- | --- | --- | --- |
| A | 7017 | base | `-github-sim -reconcile=1s -fake-ci -base-branch-ci -merge-queue-states -merge-rules -qualification -scheduled-workflows` | most of the walkthrough and the reproductions |
| B | 7018 | base | `-github-sim -qualification` | base-gate warnings (no `-base-branch-ci`, see below) |
| C | 7019 | fix | `-github-sim -reconcile=1s -qualification` | re-verifying the fixes |

## Stubs and external-boundary helpers
Nothing was stubbed. Grain's own API, the classic UI and the Angular UI
were never mocked or intercepted, and no `page.route` was used.
- **GitHub**: the in-process GitHub simulation (`-github-sim`) over a real
  bare repository stood in for GitHub: branches, compare, issue listing
  and import, release branches, and RC cut and promote.
- **Seeding**: `git push` into `grain-demo-*/githubsim/acme/widgets.git`
  of `feature/walk-7017` and `fix/#7017-hash` (one commit adding
  `WALK7017.md`).
- **Direct API calls** were used only to read persisted state back after
  a UI mutation, and to read `/api/merge-batches` and the base-branch
  gates.

## Which UI each step ran against
Since the 11-14 run, `/next/` has gained a Repos rail entry and list page
(`/next/repos`), an Advanced tab, work ownership, the qualification plan,
CI capabilities, the issue-import interval and the base-gate warnings.

| Step | UI |
| --- | --- |
| Repos list: rail, direct URL, counts, search, sort, long names, Add repo, reorder | `/next/repos` |
| Repo row → page → back; task count → filtered list → back | `/next/` |
| File a task from the repo page | `/next/` (bug 3) |
| Repo tabs, URL, reload, Back/Forward, classic `/settings/<tab>` address | `/next/repos/<o>/<n>/<tab>` |
| Overview: issue import, lifecycle; CI; Merge queue; Agents; Branches; Releases; Advanced | `/next/` |
| Settings → Repositories (base branch, declarations, CI mode, git connection, bypass) | `/next/settings/repositories` |
| Base-gate warnings (Overview, Releases) | `/next/` |
| Compare (`/repos/<o>/<n>/compare/<base>...<head>`) | classic (no `/next/` page: `/next/repos/…/compare/…` falls through to the Inbox) |
| Unknown release URL fallback | classic (no per-release URL in `/next/`) |
| Overlap re-check panel (repo page, task page) | classic (no `/next/` panel) |
| Upstream repository, Unfixable checks, prompt layers | classic only (not on `/next/`) |

## Steps and outcomes

### Repos list (`/next/repos`)
- The rail's **Repos** goes to `/next/repos`, and a reload of that address
  renders the same list. `acme/widgets` reads `Blocked 1 Proposed 3
  Waiting 4 Running 2 Asked 2 Ready to submit 2 Merging 3 Deferred 2 18
  open`, with Blocked first.
- **Add repo** for the 72-character
  `acme-observability-platform/an-extraordinarily-long-repository-name-7017`:
  a MutationObserver saw the button go `disabled` + `aria-busy=true` +
  `Adding…` while in flight, then back to `Add repo`. At 1280 px
  `scrollWidth` is 1280. At 390 px `scrollWidth` is 390, Add repo sits at
  x 289–374, and the Blocked chip wraps below the name, first among the
  chips (x 49–123).
- **Search**: `zzz-none` reads `No repos match “zzz-none”.` with *Clear the
  filter*. `WIDG` matches `acme/widgets`, so matching is case-insensitive.
  **Sort** by name writes `?sort=name`.
- **Reorder**: on a handle, Space, ArrowUp, Space announced `Picked up
  acme/widgets, position 3 of 3…` then `Dropped acme/widgets at position
  2`. A pointer drag moved the first row down. Tapping a handle picks it
  up (`aria-pressed=true`) and tapping it again drops it. The order
  survives a reload.
- A row's name opens `/next/repos/acme/widgets`, and Back returns to the
  list. **18 open** opens `/next/tasks?repo=acme%2Fwidgets` (All 24), and
  Back returns. `/next/tasks?repo=ACME/Widgets` lists the same 24 tasks.
- **Removed repository**: on classic, `/repos/acme/widgets` reads `21 tasks
  filed here` including `closed 2`. The `/next/` list counts open work
  only (`18 open`), by design.

### Repo page tabs (`/next/`)
- The tabs are Overview, CI & Workflows, Merge Queue, Agents & Grants,
  Branches, Releases and Advanced. Each sets `/next/repos/acme/widgets/<tab>`.
  A reload keeps `advanced`; Back goes to `releases` and Forward to
  `advanced`. The classic address `/next/repos/acme/widgets/settings/merge-queue`
  redirects to `/next/repos/acme/widgets/merge-queue`.

### Overview: issue import
- `Every (minutes)` = 0 is refused beside the box (`must be at least 1, or
  empty for the default`), and 15 saves and survives a reload.
- With source `acme/nosuch-7017`, **Import issues now** reads `Could not
  read the open issues on acme/nosuch-7017: … GitHub answered 404: Not
  Found`. With the source cleared it reads `Read 1 open issue on
  acme/widgets: imported 1, 0 already imported.`
- The queue-at-once switch carries the trust warning naming the source
  repository.
- **Unsaved label**: switching to CI asks `Discard unsaved changes? You
  have unsaved issue import changes on acme/widgets.` Cancel keeps the URL.

### CI, merge queue, base gates
- The CI mode pill offers Grain CI / GitHub Actions checks / No CI, and
  each choice was stored (`github`, `none`, `grain`).
- **Base-gate warnings** (demo B): with *Watch default branch* on,
  choosing GitHub or No CI puts the server's sentence (`Nothing is testing
  acme/widgets's default branch, and the merge queue is merging onto it
  anyway. …`) on Overview and Releases with no reload. **Go to CI Mode**
  lands on `/ci#repo-ci-mode`, and choosing Grain clears it.
  - On demo A (`-base-branch-ci`) no warning appears for GitHub or No CI,
    because the server warns only when the branch has no base-branch row
    (`pkg/ui/basebranchgate.go`, `row == nil && !ciEnabled`), and that
    flag had enrolled it.
  - The server's sentence says `"Use Grain CI"` where the pill reads
    `Grain CI (.grain/workflows)`.
  - The link's target leaves focus on `<body>`.
- **Work ownership**: turning *Standing work on this deployment* off and
  back on saved each time, and no warning appeared on either pane.
- **Merge queue**: *Merge on green* reads `Land approved pull requests
  automatically once checks succeed.`, with no ordering claim. *Require
  branch up to date* persisted across a reload. Turning the bypass on asks
  `Let base-branch repairs merge without a human?`; turning it off does
  not ask.
  - There is no **Unfixable checks** editor and no **overlap re-check
    panel** on `/next/`. The classic Overview shows `merge re-checks 1
    change on main testing 18m of 90m show the run` for the demo's seeded
    batch (`/api/merge-batches`: `running: true`, task 5, `maxAgeSeconds`
    5400).

### Agents & Grants
- Setup command `make deps-7017` and prompt `Prefer table-driven tests
  (7017).` saved and survived a reload.
- An unsaved setup command asks before the rail's Tasks link.
- **Default capability** `nosuch-cap-7017` is refused (`400 …
  defaultCapabilities: unknown capability nosuch-cap-7017`) in the page
  banner. **The banner stayed after the field was cleared and saved
  cleanly**: bug 1.
- There is no Upstream repository field and no read-only deployment or
  `.grain/settings.yml` prompt layers on `/next/`.

### Branches
- `walk/7017-branch` from `main` was created and listed.

### Releases
- `bad name` is refused beside its box (`aria-invalid=true`, no banner),
  and the error clears on the next edit. `walk-7017` was created and
  survived a reload, and the reconciler provisioned it and cut
  `walk-7017.rc.1`.
- Promote RC asks before running, and Escape leaves the release `active`.
- **With two releases, 1.0's Cut RC was enabled with no reason while
  `1.0.rc.1` was active, and its Promote dialog read `Promote 1.0's
  current candidate?`**: bug 2.
- The qualification plan's *Add item* followed by a tab switch asks
  `You have unsaved qualification plan changes`.

### Advanced, removal, Settings → Repositories
- Advanced shows declarations (`Read at a29b44270dab: 0 template(s) …`),
  `declares no path triggers`, `declares no templates that push beyond
  their own branch`, and Ports `No branches are paired`. The demo has no
  paired branches, so there is no Ports table.
- **Remove** on `acme/widgets` asks, naming `15 tasks still in play … 2
  schedules … (sched-2, sched-1) stop firing`. It was cancelled. Removing
  `acme/gadgets` (no tasks) asks with `Tasks already filed there are not
  affected`, then lands on `/next/repos`.
- `/next/settings/repositories` lists each repository with base branch,
  declarations (`Read` / `Not read`), CI mode, git connection (`Not
  listed`), bypass switch and Settings/Releases/Remove.

### File a task from the repo page
- On `/next/repos/acme/gadgets`, the rail's New task opened with
  Repository `acme/widgets`, and the task was stored against
  `acme/widgets`. The page itself had no New task: bug 3.

### Compare and unknown release (classic)
- `/repos/acme/widgets/compare/main...feature/walk-7017` and
  `…/main...fix/%237017-hash` show `WALK7017.md +1 -0`.
- `…/main...nosuch-7017` reads `GitHub has nothing to compare for
  main...nosuch-7017 in acme/widgets: one of the two b…` (404 from the
  compare route), and the page still renders after a reload.
- `/repos/acme/widgets/releases/nosuch-7017` reads `acme/widgets has no
  release named “nosuch-7017” -- showing walk-7017 instead. The link may
  be stale, or the release deleted; pick another below.`

### Console and network audit
- **Uncaught page errors**: 0.
- **Every 4xx was a deliberate negative case**: 400 for `bad name`, for
  the unknown capability and for the interval 0, and 404 for the unknown
  compare ref.

## Bugs found and fixed (all in `ui-ng/`; the classic pages were correct)

### 1. A refused setting's banner outlived the clean save
- **Repro** (demo A): on `/next/repos/acme/widgets/agents`, enter default
  capability `nosuch-cap-7017` and Save agent settings. The banner reads
  `defaultCapabilities: unknown capability nosuch-cap-7017`. Clear the
  field and Save again: the save succeeds and the banner stays.
- **Cause**: every handler set the page-wide `error` signal, and only
  Dismiss ever cleared it.
- **Fix**: `refuse(action, err)` records which action raised the banner,
  and `settle(action)` at the start of that action clears it. A refusal
  raised by a different setting is left alone.
- **Verified** (demo C): the banner clears on the clean save. A
  merge-on-green toggle leaves a capability refusal in place.

### 2. Release rows other than the selected one ignored their candidates
- **Repro** (demo A): with release `1.0` (`1.0.rc.1` active), create
  release `walk-7017` and reload. `walk-7017` is selected. `1.0`'s Cut RC
  is enabled with no title, and its Promote dialog reads `Promote 1.0's
  current candidate?`.
- **Cause**: only the selected release's candidates were fetched, and
  `releaseActionReasons` was told the rest had not loaded.
- **Fix**: candidates are read for every release into `candidatesOf`, and
  each row is judged by its own.
- **Verified** (demo C): `cut-rc-1.0` is disabled with title `1.0.rc.1 is
  active: a new RC is cut once it is promoted.`, and Promote asks
  `Promote 1.0.rc.1 to 1.0?`.

### 3. No way to file a task from the repo page
- **Repro** (demo A or C): add `acme/gadgets`, open its page, and use the
  rail's New task. Repository reads `acme/widgets`, and the task is filed
  there.
- **Fix**: a **New task** button beside *View tasks in …* opens the sheet
  with the page's repository preselected (`CreateTaskService.open({repo})`),
  as the classic page's "new task" does.
- **Verified** (e2e): the task is stored with `repo: acme/gadgets`.

## Regression coverage
- `ui-ng/e2e/next-repo-banner-releases-new-task.spec.js` (new, real demo
  with `-github-sim -qualification -reconcile=1s`) covers all three bugs.
- Ran:
  - that spec;
  - `next-repo-import-releases`, `next-repo-base-gate-warning`,
    `next-repo-ci-mode-bypass`, `next-repo-ownership-plan-caps`,
    `next-repos-list`, `next-repo-advanced`, `next-settings-repos` and
    `next-full-system`;
  - vitest for `src/app/repos` (38 tests).

## Parity gaps left for follow-up (not bugs in what `/next/` has)
- No compare page and no per-release URL (with its unknown-release
  fallback).
- No overlap re-check panel on the repo page or task page.
- No Unfixable checks editor, no Upstream repository field, and no
  read-only prompt layers.
- No Overview summary and no recent CI runs.
