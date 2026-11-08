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
