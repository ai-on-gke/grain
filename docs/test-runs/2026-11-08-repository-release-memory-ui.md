# 2026-11-08 — Repository, Merge Queue, Compare, and Release UI Walkthrough (Task 3460)

## Environment and Build Tested

- **Repository**: `ai-on-gke/grain`
- **Commit tested**: `82a986f89ae8` (`v0.0.0-20261108094035-82a986f89ae8`), rebased cleanly onto `ad6d8b91` (`main`) before commit
- **Frontend build**: `cd ui && npm ci && npm run build` (real Vite production bundle embedded into the Go binary)
- **Server binary**: `go build -tags grain_e2e -o /tmp/grain-walk/grain ./cmd/grain`
- **Browser**: Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), exercised at desktop (`1280×900`) and phone (`390×844`) viewports across light and dark color schemes
- **Visual key**: `/home/debian/.gemini-api-key` was absent in this sandbox; responsive layout, truncation, chip priority, tab strip auto-scroll, and horizontal overflow were verified via DOM geometry (`getBoundingClientRect()`, `computedStyle`, and `document.documentElement.scrollWidth <= clientWidth`)

### Real `grain demo` Instances Used

All exercises ran against real `grain demo` servers over throwaway embedded SQLite stores and real `git-http-backend` bare repositories (`-github-sim`), keeping Grain's Go HTTP API and built React UI completely intact:

1. **Server A (primary full-feature deployment)**:
   `grain demo -open=false -github-sim -reconcile=500ms -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification -legacy-capability`
2. **Server B (minimal GitHub-sim deployment without pre-seeded CI/base-branch jobs)**:
   `grain demo -open=false -github-sim -reconcile=500ms`
3. **Server C (external config repository refusal mode)**:
   `grain demo -open=false -github-sim -qualification -external-config-repo=https://github.com/grain/config.git`
4. **Server D (unconfigured git connections mode)**:
   `grain demo -open=false -unconfigured`

### External-Boundary Stubs and Latency Shims Documented

Grain's own API and React UI were never mocked or replaced. Only the following narrow external/transport boundary hooks were used:

- **In-process GitHub simulation (`-github-sim`)**: Served real bare Git repositories (`acme/widgets.git`) over `git-http-backend` for branch listing, branch creation, `.grain/*` declaration commits (`main`), compare diffs (`feature/compare-3460`, `release/1.0`), release train provisioning (`2.0.latest`, `3.0.latest`), candidate cutting (`rc.1`, `rc.2`), promotion, and open GitHub issue import.
- **Transient request delay on `POST /api/repos` and `POST /api/repos/acme/widgets/import-issues`**: Added a 600–700 ms pass-through delay (`route.continue()`) once per endpoint solely to observe transient in-flight disabled button states (`Importing…`) against a fast local SQLite store.
- **Single-shot `500` fault injection on `GET /api/repos/acme/widgets/releases`**: Returned HTTP `500` once (`{"error":"simulated transient release store outage 3460"}`) to verify error banner surfacing and clean reload recovery when release loading fails, then removed the route handler immediately.

---

## Step-by-Step Walkthrough and Outcomes

### 1. Repository List (`/repos`), Counts, Search, Sort, Responsive Layout, and Reordering

- **Rail and direct URL entry**:
  - Clicked **Repos** in the left navigation rail (showing badge count `1` initially for `acme/widgets`, updating live to `3` after adding two repositories) to reach `/repos`, and reloaded `/repos` directly.
- **Unconfigured git notice and unrestricted-to-restricted allowlist warning (Server D)**:
  - Opened `/repos` on Server D (`-unconfigured`). Confirmed `NoGitConnectionNotice` rendered above the list pointing to `/settings/git`, and verified that adding the first target repository on an unrestricted deployment prompted confirmation warning about stranding off-list tasks before narrowing the allowlist.
- **Adding repositories & Clone URL conditional visibility**:
  - Focused `Repo to add`: confirmed dropdown options offered reachable repositories alongside `Other…`.
  - Typed `git.example.test/team/alpha` (off-default Git host) and confirmed the **Clone URL** input appeared automatically; cleared back to default host and confirmed **Clone URL** hid again.
  - Added `acme/gadgets` and an 88-character stress repository `acme/an-extremely-long-repository-name-to-exercise-truncation-and-phone-chip-layout-3460`. Both persisted across reload and updated the rail count immediately.
- **Per-state counts & Blocked chip priority**:
  - Inspected `acme/widgets` row on `/repos`: displayed `Blocked 1`, `Proposed 3`, `Waiting 2`, `Running 2`, `Asked 4`, `Merging 3`, `Deferred 1`, and `16 tasks`.
  - Switched viewport to phone width (`390×844`) in both light and dark modes (`scrollWidth === 390`, `clientWidth === 390`, `overflows: false`):
    - Confirmed **Add repo** stayed completely on screen (`left: 303px`, `right: 374px` within `390px`).
    - Confirmed the **Blocked 1** chip remained visible (`display: inline-flex`, `x: 241px`, `width: 72px`) while lower-priority per-state chips hid cleanly (`display: none`) on narrow screens.
    - Confirmed the 88-character repository name truncated with CSS ellipsis (`width: 144px`) without pushing row controls off screen.
- **Search and sort modes**:
  - Exercised case-insensitive search (`GADG` matched `acme/gadgets`; `no-such-repo-xyz` rendered the empty-filter notice cleanly).
  - Exercised all four sort modes: `Custom order` (drag handles enabled), `Name (A–Z)`, `Active first`, and `Most tasks` (confirming drag handles hid or disabled when not in custom order).
- **Pointer, keyboard, and touch reordering**:
  - **Keyboard**: Focused drag handle (`Move acme/gadgets`), pressed `Enter` to pick up, confirmed `Move here` drop targets rendered, pressed `Escape` to cancel without mutating order, then pressed `Enter` again and activated a `Move here` target — verified `PUT /api/repos/order` succeeded and focus returned to the moved repository's drag handle.
  - **Pointer drag**: Dragged repository row via `dragTo` on desktop (`1280×900`), reloaded `/repos`, and confirmed the new custom order persisted.
  - **Touch tap (`hasTouch: true`, `390×844`)**: Tapped a repository's drag handle on a phone-sized touch context and tapped `Move here` to reorder cleanly by touch.

### 2. Repo Navigation, Filtered Task List, and Task Filing Preselection

- **Row navigation and history**:
  - Clicked `acme/widgets` on `/repos` to open `/repos/acme/widgets`, clicked `← Repos` to return to `/repos`, and exercised browser `Back`/`Forward`.
  - On `/repos/acme/widgets`, clicked `16 tasks filed here` to land on `/?f=repo:any:acme/widgets` (confirming 16 matching task rows rendered), then used browser `Back` to return cleanly to `/repos/acme/widgets`.
- **Filing a task from a repository page**:
  - Opened `/repos/acme/gadgets` and clicked **New task**.
  - Verified `NewTaskOverlay` preselected `repo = "acme/gadgets"` (`Search repos…` and hidden `repo` field both populated with `acme/gadgets`).
  - Submitted task `#24`; verified `POST /api/tasks` carried `"repo":"acme/gadgets"`, the header summary updated to `1 task filed here`, and `/tasks/24` persisted `acme/gadgets` across reload.

### 3. Repository Settings Tabs, Deep Links, Phone Tab Strip, and `/settings/repositories`

- **All seven tabs & URL synchronization**:
  - Walked `Overview`, `CI`, `Merge queue`, `Agents`, `Branches`, `Releases`, and `Advanced`.
  - Confirmed clicking tabs updated `/repos/acme/widgets/settings/<tab>` (`Overview` at `/repos/acme/widgets`) via `history.replaceState` (`history.length` delta `0` across tab switches so `Back` exits the repo page cleanly instead of cycling through visited tabs), while direct URLs, reloads, legacy `#agents` hash links (upgraded to `/repos/acme/widgets/settings/agents`), and unknown tab slugs (`/repos/acme/widgets/settings/not-a-tab` falling back to `/repos/acme/widgets`) all worked cleanly.
  - At phone width (`390×844`), selected every tab including `Releases` and `Advanced`: confirmed the horizontal tab strip auto-scrolled each active tab fully into the viewport (`fullyVisible: true`, `scrollWidth === 390` with zero page overflow, including on the 88-character repository page).
- **Entry from `/settings/repositories`**:
  - Opened `/settings/repositories` (`scrollWidth === 390` on phone, no overflow). Verified each row displayed the repository's **base branch** (`main` → `/repos/acme/widgets/settings/releases`), **declarations** (`Read` / `Ignored` → `/repos/acme/widgets/settings/advanced`), **CI mode** (`Grain CI` → `/repos/acme/widgets/settings/ci`), and **git connection** (`Not listed` / connection name → `/settings/git`), plus browser `Back` back to `/settings/repositories`.

### 4. Tab-by-Tab Inspection, Editing, Validation, Leave Guards, and Persistence

- **Overview (`/repos/acme/widgets`)**:
  - Verified summary cards (`CI gate`, `Up to date before merge`, `Merge when green`, `Issue import`, `Base repair bypass`, `Branches`), the **Merge re-checks** overlap panel (`1 change on main`, `Testing`, elapsed `18m of 90m` progressing over time, branch/SHA `grain-merge-queue/acbbed22/recheck/5/main at abcdef012345`, task link `Rotate the Gemini signing key` → `/tasks/5` whose own task detail page also renders the matching re-check status, and `Show the run` opening run `#101` overlay with `Cancel run`), and the **CI runs** table (`RepoRuns`, `Start a run`).
- **CI (`/repos/acme/widgets/settings/ci`)**:
  - Toggled `Watch acme/widgets's default branch with grain CI even when grain has not merged into it` (`PUT /api/repos/acme/widgets/watch-default-branch`) and confirmed reload persistence.
  - Exercised **CI workflows**: confirmed incomplete new workflow rows stayed local without auto-submitting; saved invalid YAML (`name: [unclosed`) and verified `400` inline error surfacing while keeping the unsaved draft; verified clicking `← Repos` with an unsaved workflow edit triggered the confirmation dialog (dismissing stayed on `/repos/acme/widgets/settings/ci`, and switching tabs preserved the draft); fixed YAML for `.grain/workflows/lint-3460.yml`, saved (`200`), reloaded to confirm persistence, then tested **Remove** confirmation (dismiss kept the workflow; accept deleted it).
  - Inspected **CI capabilities / CI grants** (`availableCICapabilities`, inherited `defaultCapabilities`, and `deploymentDefaultCapabilities` judged for `acme/widgets`).
- **Agents (`/repos/acme/widgets/settings/agents`)**:
  - Inspected **Default capabilities** (`Example legacy capability` via `-legacy-capability`) and **Default grants** (`API preset: self-debug`, `grain › source`, `github › read-issues`).
  - Tested **Upstream pull-request repository**: invalid value refused with `400` and clean revert; valid `owner/name` saved and updated helper summary.
  - Verified all **three Prompt extension layers** (`Deployment layer: always cite test runs.`, `Repo layer: keep PRs focused (task 3460).`, and `.grain/settings.yml`'s read-only `Declared layer: run make check-docs before finishing.`) and **both Setup command layers** (operator `make setup-repo-3460` alongside read-only `.grain/settings.yml` `echo declared-setup-3460`).
  - Exercised **GitHub issue import**: confirmed `Import issues now` is disabled while issue import is off; enabled import; verified toggling `Queue imported tasks at once` surfaced its trust warning while preserving the configured label (`walk-1794133179035`); confirmed unsaved issue-import edits triggered the leave guard; observed `Importing…` disabled in-flight state; confirmed first click reported `Read 1 open issue on acme/widgets: imported 1, 0 already imported` and second click reported `Read 1 open issue on acme/widgets: imported 0, 1 already imported`; tested a nonexistent source repository (`acme/missing-source-3460`) and verified the `404` failure message rendered inline cleanly before restoring `acme/widgets`.
- **Branches (`/repos/acme/widgets/settings/branches`)**:
  - Verified invalid Git ref name (`bad..branch.lock`) was refused inline; added `feature/walk-3460` (`pending` badge) and confirmed duplicate branch note on re-adding.
- **Advanced (`/repos/acme/widgets/settings/advanced`)**:
  - Pushed multi-file broken `.grain/*` declarations (`unknown_field_xyz` in `.grain/templates/bad-one.yml` and invalid `on: push` in `.grain/triggers/bad-two.yml`) to `acme/widgets.git` and verified `Last read failed:` formatted each file's error on separate lines (`white-space: pre-line`).
  - Pushed clean `.grain/*` declarations (`9d16f7e44413`) with `.grain/templates/docs-check.yml` (`push:` extra refs), `.grain/triggers/docs-gate.yml`, and `.grain/settings.yml` (`prompt-extension`, `setup-command`, `port-branches: [main, release/1.0]`, and `port-types` including unpaired `main->nonexistent`), synced declarations, and verified:
    - `Read at 9d16f7e44413: 1 template(s), 0 suite(s), 0 schedule(s), 1 trigger(s)`
    - `Path triggers: .grain/triggers/docs-gate.yml`
    - `Tasks from .grain/templates/docs-check.yml may also push: refs/heads/docs-sync/*, refs/tags/docs-* (force)`
    - Read-only **Ports** table (`main->nonexistent` → `nothing (not a paired branch)`, `main->release/1.0` → `fix, docs`, `release/1.0->main` → `every merge`) and warning `Warning: port-types in .grain/settings.yml: main->nonexistent names nonexistent, which is not a paired branch (the paired branches are main, release/1.0), so the entry has no effect`.
  - Exercised **Remove repository**: verified `acme/widgets` (with 16+ active tasks) prompted stranding confirmation (dismissed to preserve `acme/widgets`), while `acme/transient-3460` (0 tasks) removed cleanly and navigated back to `/repos`.
  - On Server C (`-external-config-repo=https://github.com/grain/config.git`), verified `ConfigManagedNotice` rendered at the top of repo settings and disabled mutating controls with `aria-describedby` linking to the refusal banner (see also Follow-Up Finding below regarding `StateRepoRef` in `grain demo`).

### 5. Merge Queue Settings and CI Mode Switching

- **CI mode transitions (`Grain CI` ↔ `GitHub CI` ↔ `No CI`)**:
  - Switched `acme/widgets` across `Grain CI`, `GitHub CI`, and `No CI` (`PUT /api/repos/acme/widgets/ci-mode`), confirming Overview's `CI gate` summary card updated live without reload (`Grain CI`, `GitHub CI`, `No CI`) and phone viewport (`390×844`) had zero overflow (`scrollWidth === 390`).
- **Merge queue controls (`/repos/acme/widgets/settings/merge-queue` and `/settings/repositories`)**:
  - Toggled `Require branches to be up to date before merging into acme/widgets` and `Merge pull requests into acme/widgets when green and clean`; reloaded and verified persisted values.
  - Tested `Base-branch repair PRs in acme/widgets may merge without a human when their CI is green`:
    - Turning **ON** on `/repos/acme/widgets/settings/merge-queue` opened confirmation (`Let base-branch repair PRs in acme/widgets merge without a human when their CI is green? ...`): dismissing left the checkbox unchecked; accepting saved `true` and persisted across reload. Turning **OFF** asked **no** confirmation.
    - Repeated on `/settings/repositories`: turning **ON** opened the identical confirmation dialog (dismissed → stayed off; accepted → turned on), and turning **OFF** asked **no** confirmation.
  - Tested **Unfixable checks** (`Check that cannot pass`):
    - Confirmed **Add** button is disabled while input is blank.
    - Entered `grain CI`: refused inline without network mutation (`grainCICheckName` cannot be marked unfixable).
    - Added `e2e/flake-3460`: persisted across reload; entering `E2E/FLAKE-3460` was refused inline as a case-insensitive duplicate (`already on the list`).

### 6. Compare Branches (`/repos/acme/widgets/compare/<base>...<head>`)

- On `/repos/acme/widgets/settings/branches`, entered `Base branch = main` and `Head branch = feature/compare-3460` (pushed to `acme/widgets.git` with `WALK.md` and `walk.go`) and clicked **Compare**:
  - Navigated to `/repos/acme/widgets/compare/main...feature/compare-3460`, rendering `PullRequestPage` with `Side by side`, `Inline`, and `Raw` diff modes and both `WALK.md` (`+2 -0`) and `walk.go` (`+3 -0`).
  - Reloaded `/repos/acme/widgets/compare/main...feature/compare-3460` directly and confirmed diff persistence.
  - Tested slashed base ref `/repos/acme/widgets/compare/release/1.0...main` (parsed `release/1.0` base cleanly and rendered `.grain/settings.yml` diff).
  - Tested identical refs `/repos/acme/widgets/compare/main...main` (`There are no changes between these branches.`).
  - Tested unknown ref `/repos/acme/widgets/compare/main...unknown-ref-3460`: HTTP `404` surfaced cleanly as `GitHub has nothing to compare for main...unknown-ref-3460 in acme/widgets: one of the two branches does not exist...` with zero horizontal overflow at phone width (`390×844`, `scrollWidth === 390`).

### 7. Releases, Work Ownership, Qualification, and Exploratory Edge Cases

- **Work ownership (`/repos/acme/widgets/settings/releases`)**:
  - Toggled `Standing work on this deployment` OFF (`PUT` persisted across reload, updating caption to `Standing work on this deployment: off`), then restored ON.
  - Exercised per-item override buttons (`: off` → `Off, set here` chip; `: say nothing` → reset to repository default) and clicked `Turn off automatic releases` / `Turn on automatic releases` in `RepoReleaseWorkflowSettings`, confirming cross-component refresh (`refreshKey`) without reload.
  - Verified unsaved leave guard on `Jobs dispatched after every merge` (dismissing confirmation kept the operator on `/repos/acme/widgets/settings/releases`).
- **Releases & Release Trains (`/repos/acme/widgets/releases` and `/repos/acme/widgets/releases/<name>`)**:
  - Opened `/repos/acme/widgets/releases` from the repo page header **Releases** button and via direct URL.
  - **Release name validation**: Submitted `"bad name"` (`400`: invalid characters), `"-lead"` (`400`: must start with letter/digit), and `"1.0"` (`400`: `this repo already has an unmerged release with this name`); all surfaced inline without crashing.
  - **Seeded release `1.0` with qualification (`-qualification`)**:
    - Opened `/repos/acme/widgets/releases/1.0`: verified `Current candidate: 1.0.rc.1 -- active`, **Cut new RC** disabled with explicit reason `Cut new RC: 1.0.rc.1 is active: a new RC is cut once it is promoted.`, **Promote current RC** disabled while qualification was pending, qualification status `Pending approval`, and task link to `/tasks/23`.
    - Clicked **Approve all**: confirmed `Unapproved` badge cleared and qualification advanced to `Running`.
    - Edited `QualificationPlanEditor` (`Require approval before running qualification tasks`), confirmed unsaved leave guard fired when attempting to navigate away, saved, reloaded, and verified persistence.
  - **Full RC cut, promote, second RC, history, picker navigation, and deletion (`2.0` and `3.0`)**:
    - Created releases `2.0` and `3.0`; exercised the **Release** picker switching between releases and confirmed browser `Back` and `Forward` updated `/repos/acme/widgets/releases/<name>` cleanly.
    - On `3.0`, observed reconciler advance `3.0.rc.1` from `cutting` to `active`; clicked **Promote current RC** — dismissing `Promote 3.0.rc.1 to 3.0? This moves acme/widgets's 3.0 branch to 3.0.rc.1.` aborted promotion; accepting promoted `3.0.rc.1` (`3.0.rc.1 -- promoted`, disabling **Promote current RC** with `Promote current RC: 3.0.rc.1 is already promoted: a candidate is promoted once.` and enabling **Cut new RC**).
    - Clicked **Cut new RC** to cut `3.0.rc.2 -- active` and confirmed candidate **History** listed both `3.0.rc.2 -- active` and `3.0.rc.1 -- promoted`.
    - Exercised **Delete release**: dismissing `Remove release 2.0? This drops the release and its candidates from grain.` kept `/repos/acme/widgets/releases/2.0`; accepting deleted it and returned to `/repos/acme/widgets/releases`.
  - **Unknown release deep link**:
    - Opened `/repos/acme/widgets/releases/nosuch-3460`: redirected cleanly to `/repos/acme/widgets/releases` with warning `acme/widgets has no release named “nosuch-3460” -- showing 1.0 instead.`
- **Exploratory checks beyond `ui/e2e`**:
  - **Phone light/dark layout on `/repos/acme/widgets/releases/1.0`**: `scrollWidth === 390`, `clientWidth === 390`, `overflows: false` in both light and dark modes.
  - **Transient `500` API error & recovery**: Fault-injected `500` on `GET /api/repos/acme/widgets/releases` rendered `simulated transient release store outage 3460` cleanly with zero uncaught exceptions, and recovered immediately on reload once the fault cleared.
  - **Timezone formatting (`UTC` vs `Asia/Tokyo`)**: Updated deployment `timeZone` via `PUT /api/settings` from `UTC` to `Asia/Tokyo` and verified timestamps on `/repos/acme/widgets` (`RepoRuns` / `MergeBatchPanel`) re-rendered in the configured zone rather than hardcoding UTC.

---

## Console, Page Error, and Network Audit

- **Uncaught page errors (`pageerror`)**: **0** across all 5 Playwright walkthrough sessions and all 4 demo servers.
- **Unexpected failed requests / HTTP 5xx**: **0** (excluding the single intentional `500` fault injection on `/api/repos/acme/widgets/releases` and expected `400`/`404` responses from negative validation tests: invalid workflow YAML, nonexistent upstream/issue-import repo, unknown compare ref `main...unknown-ref-3460`, and invalid/duplicate release names).

---

## Bugs Found and Follow-Up Tasks

### Proposed Follow-Up (Out-of-Scope Demo Wiring)

- **`grain demo -external-config-repo` leaves `ui.Config.StateRepoRef` unset (`pkg/demo/demo.go`)**:
  - **Reproduction**: Start `grain demo -github-sim -external-config-repo=https://github.com/grain/config.git`, sign in, and inspect `GET /api/config` (or open `/repos/grain/config/settings/advanced`).
  - **Observed**: In `pkg/daemon/ui_server.go` + `pkg/daemon/statemanager.go`, a real `grain daemon` sets `StateRepoRef: settingsRepo`, which parses the external config remote into `owner/name` so `GET /api/config` populates `configRepo` (`"grain/config"`). In `pkg/demo/demo.go`, `-external-config-repo` wires `cfg.StateRepo = newExternalConfigRepo(...)` without setting `cfg.StateRepoRef`. Consequently, `GET /api/config` omits `configRepo` in demo mode, so `row.configRepo` is `false` on `/repos/grain/config/settings/advanced` (showing `grain/config is not one of this deployment's target repos.` instead of `This is the deployment's own configuration repository and cannot be removed here.`).
  - **Disposition**: Real daemon deployments already wire `StateRepoRef` properly; filed a focused follow-up task on `main` to wire `cfg.StateRepoRef` in `pkg/demo/demo.go` using shared remote-slug parsing and add a `pkg/demo` test.

---

# Second pass: task 3613

A second walk of the same area, by task 3613, against later code. It covers
the same brief and adds two bugs that the first pass missed or left for a
follow-up. Both are fixed on this branch.

## Environment

- **Commits tested**: the walk started on `8ee9b97e`. The run was suspended
  and resumed in a fresh sandbox, and the second half and every fix
  verification ran on `33a44581` (origin/main at 2026-11-08 12:44 UTC).
  The fixes are on top of that commit.
- **Sandbox**: Debian, Go and Node 20.19 from the image, Playwright
  Chromium headless shell 1234 (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
  Desktop runs used 1280×900. Phone runs used 390×844, including
  `hasTouch` for tap reordering. Light and dark were both checked.
- **No Gemini key**: `/home/debian/.gemini-api-key` is absent, so
  `LOOK=1` was not available. Layout was checked with DOM bounding boxes
  and `scrollWidth` assertions instead of by looking at the images.
- **Servers**: each one was a real `grain demo` built from the checkout
  (`make frontend` then `go build ./cmd/grain`). Each ran on a throwaway
  embedded SQLite store under its own `TMPDIR`, on `-addr=127.0.0.1:0`,
  with a generated `-access-code-file`. The browser signed in through
  `POST /api/auth/access-code` and got the real session cookie.
  - **A (full)**: `-github-sim -reconcile=500ms -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification -legacy-capability`
  - **B (gate)**: `-github-sim`. This is the one server where a GitHub CI
    Mode with a watched default branch gives an unjudged gate. Server A's
    `-base-branch-ci` judges the branch, so no gate appears there.
  - **C (external config)**: `-github-sim -qualification -external-config-repo=https://github.com/grain/config.git`
  - **D (unconfigured)**: `-unconfigured`
- **Stubs**: the demo already simulates GitHub (branches, compare, issues,
  repository contents) through `-github-sim`, so only two things were
  intercepted, both with Playwright `page.route`:
  1. `GET /api/repos/acme/widgets/releases` returned one `503`
     (`simulated release store outage 3613`), to check the error state
     and that a reload recovers. Every later call reached the real server.
  2. `POST …/import-issues` was held for 600 ms before reaching the real
     server, so the `Importing…` loading state could be seen.
  Declarations, branches and release state were real. They were written
  by `git push` into the simulated repository at
  `<demo dir>/githubsim/acme/widgets.git`.

## Steps and outcomes

All interaction went through visible controls in a real Chromium page.
The only exceptions are setup PUTs that write what the page's own switches
write, and they are marked as such below.

### Repos list (server A, then D)
- Server D: `/repos` shows the no-git-connection notice, which links to
  `/settings/git`.
- `/repos` opened from the rail and by direct URL with a reload. The busy
  row `acme/widgets` reads `blocked 1 proposed 3 waiting 2 running 2 asked 4
  merging 3 deferred 1 · 16 tasks`.
- `Clone URL` appears only for a repository on another host
  (`git.example.test/team/custom`) and goes away when the field is cleared.
- Added `acme/gadgets`, `acme/zebra` and an 86-character repository name.
  All three survived a reload.
- At 390 px, in light and in dark: `scrollWidth` is 390. `Add repo` sits at
  x=303–374 px. The `Blocked 1` chip is the first chip on the busy row and
  is fully visible (x=241–312 px). The long name is cut with an ellipsis
  at 144 px.
- Search (`GADGETS` gives 1 row, a nonsense term gives 0, and the clear
  button gives all 4 back). All four sorts were checked: `Name (A–Z)`,
  `Active first`, `Most tasks`, `Custom order`.
- Reordering by keyboard (`Enter`, `Escape` to cancel, `Enter`, then
  `Move here`), by pointer drag, which held after a reload, and by touch
  tap at 390×844. The custom order is kept in the browser
  (`localStorage`), so no request is made.

### Repo page, task count and filing (server A)
- Opened a row, came back with `← Repos`, then followed `16 tasks filed
  here` to the task list filtered to that repo (16 rows). Browser Back
  returned to the repo.
- `New task` from `/repos/acme/gadgets` had `acme/gadgets` preselected.
  Filing it created #24, and after a reload the page read `1 task filed
  here`.
- Remove on `acme/zebra`: dismissing the confirm kept the repo, and
  accepting removed it. `/repos/ACME/zebra` at 390 px then reads `2 tasks
  still target this repo, which is no longer on the target list`. A
  console session on the repo is not counted, and the page does not
  overflow.

### Tabs, URLs and Settings → Repositories
- All seven tabs follow `/repos/acme/widgets/settings/<tab>` and survive a
  reload. The old `#agents` hash is upgraded to the path form. At 390 px
  the selected tab scrolls fully into view for all seven tabs (checked
  after MUI's 300 ms scroll animation).
- With focus on the CI tab, `ArrowRight` moves focus to `Merge queue`, and
  `Enter` opens `/repos/acme/widgets/settings/merge-queue`.
- The `acme/widgets` row on Settings → Repositories reads `main | Read |
  Grain CI | Not listed`. Its `main` link opens `settings/releases` and
  Back/Forward works. The declarations link opens `settings/advanced` and
  the CI mode link opens `settings/ci`.

### Overview, CI, Merge queue (server A, gate on B)
- Overview summary: `CI gate Grain CI · Up to date before merge Not
  required · Merge when green Off · Issue import Off · Base repair bypass
  Off · Branches 0`. After issue import was set up it read `On, approved`.
- `Merge re-checks` panel: the standing chip reads `Testing` and the clock
  reads `5m of 90m`. `Show the run` opens run #101. The task link goes to
  `/tasks/5`, which shows the same re-check, and Back returns.
- CI tab:
  - `Watch default branch` survives a reload.
  - A new workflow with invalid YAML is refused with a 400 when the field
    loses focus, and the leave guard then asks (`You have unsaved changes
    here. Leave and discard them?`).
  - A valid workflow saves as `.grain/workflows/lint-3613.yml`, survives a
    reload, and is gone after Remove and a reload.
  - CI capability policy verdicts, judged per repo: a policy rule denying
    `https://capabilities.demo.grain.example/cloud#cloud-key` to CI runs
    on `acme/gadgets` was set with `PUT /api/capability-policy`. In the
    CI grants `Add grant…` list, that method is disabled with the
    policy's reason on `acme/gadgets` and enabled on `acme/widgets`. The
    policy was restored afterwards.
- CI Mode and base-gate warnings (server B, all through the radios):
  - With `Use GitHub CI` and the default branch watched, the header shows
    `Nothing is testing acme/widgets's default branch, and the merge queue
    is merging onto it anyway. grain CI is not enabled for this
    repository…`.
  - With `Use No CI`, the Overview reads `No CI — base branch not judged`.
  - On the Releases page, `Go to CI Mode` opens `settings/ci` with the
    radio in view.
  - Choosing grain CI clears the warning within seconds, with no reload.
- Merge queue tab:
  - `Require branches up to date` and `Merge when green and clean` both
    survive a reload.
  - `Base-branch repair PRs may merge without a human`: turning it on asks
    for confirmation, both on the Merge queue tab and on Settings →
    Repositories. Dismissing leaves it off and accepting turns it on.
    Turning it off asks nothing, in both places.
  - Unfixable checks: `Add` is disabled while the box is empty. `grain CI`
    is refused inline (`…judges one exact commit on the branch…`) without
    a write. An added check survives a reload, and the same name in
    different case is refused as `already declares`.

### Agents (server A)
- The deployment layer (`Deployment prompt layer: include regression
  tests.`, set through `PUT /api/settings`) and the `.grain/settings.yml`
  layers (`Declared prompt layer…`, `echo declared-setup-3613`) are shown
  read-only beside the editable boxes.
- Default capabilities: ticking `Example legacy capability` saved it, and
  the `A task filed against acme/widgets starts with: …` line named it
  before and after a reload.
- The prompt extension and setup command save when the field loses focus,
  and both survive a reload. Leaving by clicking also takes focus off the
  field, which saves it first, so no prompt appears. That is intended.
- GitHub issue import:
  - `Import issues now` is disabled while import is off.
  - The queue-at-once switch shows its trust warning (`Turn it on only if
    you trust everybody who can open an issue…`).
  - Typing in source, label and interval and then leaving makes the guard
    ask.
  - While the import is in flight the button reads `Importing…`.
  - Outcomes:
    - Empty label: `Read 0 open issues on acme/widgets: imported 0, 0
      already imported.`
    - Label `grain-agent`: `Read 1 open issue on acme/widgets: imported 1,
      0 already imported.`
    - Same import again: `…imported 0, 1 already imported.`
    - Missing source repo: `Read 0 open issues on
      acme/missing-upstream-3613: … GitHub answered 404: Not Found`.
  - Label and interval both survive a reload.

### Branches and Compare (server A)
- `Add branch` refuses `bad..ref.lock`. It adds `feature/managed-3613`,
  which survives a reload. Adding it again reads `feature/managed-3613 is
  already on this repo's list (pending); nothing new was added.`
- Picking `feature/compare-3613` from the Head branch menu, which lists
  the repository's own branches, opens
  `/repos/acme/widgets/compare/main...feature/compare-3613`. Both pushed
  files are in the diff, and it survives a reload.
- The direct link with a slashed base, `release/1.0...main`, works.
- An unknown head ref reports that GitHub has nothing to compare.
- The diff page fits 390 px.

### Advanced (server A)
- The push was valid: a template with `push:` refs, a gated path trigger
  with `template:`, a daily schedule, and `.grain/settings.yml` with
  `port-branches` and `port-types`. After it:
  - The status reads `Read at b73b589242ca: 1 template(s), 0 suite(s), 1
    schedule(s), 1 trigger(s)`.
  - The trigger file is listed, and the extra push refs
    `refs/heads/docs-sync/*` and `refs/tags/docs-*` (force) are shown.
  - The read-only `Port directions` table is drawn, with the warning
    `port-types in .grain/settings.yml: main->unpaired-branch names
    unpaired-branch, which is not a paired branch…`.
- A broken template was then pushed. The status reads `Last read failed:
  … field not_a_real_field not found…`, and the last good trigger stays
  listed.
- The demo polls a repository every 2 minutes
  (`declarationPollInterval`), so a push shows up on the next poll.
- Remove on `acme/widgets`: the confirm says `Remove acme/widgets from
  target repos? … Tasks already filed there are not affected.`
  Dismissing keeps the page.
- On server C, `/repos/grain/config/settings/advanced` shows no Remove
  and says `This is the deployment's own configuration repository and
  cannot be removed here.` (fix 2 below).

### Releases (server A)
- Work ownership:
  - The `Nightly widget audit` schedule, read from the declarations, can
    be set to `off`. It then shows `Off, set here`, and that survives a
    reload. `say nothing` clears it.
  - `Turn off automatic releases` presses the release item's own row, and
    turning them back on releases it.
  - An unsaved `postMergeDispatchJobs` edit makes the leave guard ask.
- `/repos/acme/widgets/releases/1.0`:
  - The 503 stub shows `Could not load …: simulated release store outage
    3613`, and a reload recovers.
  - The candidate is `1.0.rc.1`. `Cut new RC` is disabled with the reason
    `Cut new RC: 1.0.rc.1 is active: a new RC is cut once it is promoted.`
  - `Promote current RC` was enabled in this run, after qualification had
    started. In the first pass it was disabled with its reason while
    qualification was still pending approval.
  - The qualification task link goes to `/tasks/23`, and Back returns.
    `Approve all` moves qualification to running.
  - Editing the qualification plan makes the leave guard ask. Saving
    persists, checked by a reload.
- Creating releases:
  - `bad space` is refused with the full release-name rule.
  - `1.0` is refused with `this repo already has an unmerged release with
    this name`.
  - `2.0` is created, and its direct URL survives a reload.
- `Delete release`: dismissing its confirm keeps the release, and
  accepting removes it.
- `/releases/nonexistent-3613` says `acme/widgets has no release named
  “nonexistent-3613” -- showing 1.0 instead…`.
- 390 px fits in light and in dark.
- The full RC cut → promote → second RC cycle was walked in the first
  pass above. It was not repeated here.

## Console and network audit (second pass)
- Uncaught page errors: **0**.
- Console `Failed to load resource` entries: every one matches a
  deliberate negative case. The 400s are the invalid branch name and the
  invalid workflow YAML. The 404s are the unknown compare ref and the
  missing issue source. The 503 is the stub.
- One more: on server C, `GET /api/state-repo/files` answers 500. The
  demo's external config stub has no working tree
  (`errDemoConfigRepo`), and the page continues normally. This was noted
  and not changed, because it only affects the demo stub.
- Network failures other than aborted navigations: **0**.

## Bugs found and fixed

### 1. Standing work changes left the base-gate warning stale (`ui/src/components/RepoPage.jsx`)
- **Repro**: on `grain demo -github-sim`, run
  `PUT /api/repos/acme/widgets/ci-mode {"ciMode":"github"}` and
  `PUT …/watch-default-branch {"watchDefaultBranch":true}`. Open
  `/repos/acme/widgets/settings/releases` and switch off **Standing work
  on this deployment**.
- **Observed**: `GET /api/base-branch-gates` drops the gate straight away,
  because `baseCheckSwitchedOff` in `pkg/ui/basebranchgate.go` no longer
  judges the repo. The page never asked the shell to read the gates
  again, so the red header warning, the CI tab's dot and the Overview's
  `base branch not judged` all stayed until a reload or the shell's
  one-minute poll. Switching standing work back on did not bring the
  warning back either.
- **Fix**: `RepoWorkOwnership`'s `onSaved` now also calls
  `onRefreshGates()`, which is what the CI Mode and Watch default branch
  saves already did.
- **Coverage**:
  - Vitest: `re-reads the base-branch gates when standing work is
    switched off and on`. It fails on the base commit.
  - Playwright: `ui/e2e/repo-work-ownership-gate-refresh.spec.js`, on
    desktop and phone. The warning has to leave within 10 s with no
    reload and come back when standing work is switched on again. The
    spec also checks the page does not overflow and has no page errors.
    It fails on the base commit.

### 2. The demo with an external config repo did not identify its config repo (`pkg/demo`, `pkg/ui/teams.go`)
- **Repro**: run `grain demo -github-sim -external-config-repo=https://github.com/grain/config.git`
  and request `GET /api/config`.
- **Observed**: `configRepo` is missing, and `GET /api/deployment` gives
  `configRepo` as the remote URL. `/repos/grain/config/settings/advanced`
  reads `grain/config is not one of this deployment's target repos.`
  instead of refusing to remove the config repo. The first pass filed this
  as a follow-up; it was small and safe enough to fix here.
- **Fix**:
  - The demo now sets `ui.Config.StateRepoRef` from the remote, in
    owner/name form, the way the daemon wires `settingsRepo`.
  - `populateTeamReposFrom` now falls back to `repoSlugFromRemote(remote)`
    instead of the raw URL.
- **Coverage**:
  - Go: `TestExternalConfigRepoRef` in `pkg/demo`, and
    `TestExternalConfigRepoDeploymentNamesItsConfigRepoAsOwnerName` in
    `pkg/ui`. The second fails on the base commit.
  - Playwright: `repo-release-plan-config-repo.spec.js` now asserts
    `configRepo === "grain/config"` on both endpoints, plus the refusal
    copy with no Remove button, on desktop and phone. It fails on the
    base commit.
