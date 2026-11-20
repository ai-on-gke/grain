# Repository, merge queue, and release UI walkthrough — 2026-11-19

## Environment
- **Repository**: `ai-on-gke/grain`, branch
  `grain/53549e42/task-7306-walk-through-repository-merge`.
- **Base commit tested**: `17339dd82` (`main` at start of run; build stamp
  `v3.14.0-dev.479+17339dd`). All fixes were re-verified on rebuilt
  `ui-ng` + `bin/grain` binaries over fresh `grain demo` stores.
- **Toolchain**: Go `go1.26.10 linux/amd64`, Node `v20.19.0`, npm `10.8.2`.
- **Build**: `cd ui && npm ci && npm run build`, `cd ui-ng && npm ci && npm run build`,
  `cd ui-hub && npm ci --omit=dev && npm run build`, `make grain`.
  After fixes: `cd ui-ng && npm run build && cd .. && make grain`.
- **Browser**: Playwright (`ui-ng/node_modules/playwright`) headless Chromium,
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `timezoneId: UTC`.
  Desktop (1280×900, 1440×900, 2048×1100) and phone (390×844, touch), light
  and dark themes.
- **Gemini**: no `/home/debian/.gemini-api-key` was present in this sandbox,
  so `LOOK=1` was unavailable. Layout and rendering were verified via DOM
  bounding boxes, horizontal overflow checks (`scrollWidth <= innerWidth`),
  axe-core accessibility scans, light/dark PNG byte diffs, and the
  `ui/e2e/screenshot.mjs` before/after contact sheets attached to the task.
- **Servers**: real `bin/grain demo -open=false -access-code-file …`
  processes over throwaway embedded SQLite stores, signed in via
  `POST /api/auth/access-code` as `alice@example.com` (and via `tests/iap/iapsim.go`
  as `viewer@example.com` for Demo C):

| Server | Port | Flags | Purpose |
| --- | --- | --- | --- |
| Demo A | `17306` | `-github-sim -fake-ci -scheduled-workflows -base-branch-ci -merge-rules -merge-queue-states -mechanical-port -qualification -secrets-store -reconcile=2s` | Full walkthrough of Repos, Settings → Repositories, all 7 repo tabs, merge queue, releases, declarations, ports, compare branches, capability policy verdicts |
| Demo B | `17307` | `-external-config-repo https://github.com/acme/grain-config.git -github-sim -fake-ci -qualification -secrets-store` | Config-repo-locked read-only UI and config-repo removal refusal |
| Demo C + `iapsim` | `17308` / proxy `17310` | `-ui-iap-audience … -ui-iap-issuer https://iap.test.local -ui-iap-keys-url http://127.0.0.1:17309/jwks -github-sim -fake-ci -qualification` | Real `grain/viewer` RBAC principal (`viewer@example.com`) read-only UI check |
| Verification demos | `17315`, `17316` | Rebuilt `bin/grain` with fixes | Re-verified every fix and DOM geometry at 2048/1280/390 px, plus before/after contact sheets |

## Stubs and external-boundary helpers
Grain's own HTTP API, Angular `/next/` UI, and classic React UI were never
mocked or intercepted during the walkthrough.
- **Simulated GitHub (`-github-sim`)**: backed by a real local bare git
  repository (`/tmp/grain-demo-*/githubsim/acme/widgets.git`) plus seeded
  upstream repository `acme-upstream/widgets` with an open issue.
- **Bare repo seeding (Demo A)**: pushed `.grain/settings.yml` (first with an
  invalid string `port-types` to verify declaration sync error reporting, then
  with valid map `port-types` including an unpaired branch typo `main->candidtae`
  and `prompt-extension`), `.grain/triggers/deploy.yml`,
  `.grain/templates/release-push.yml` (with `push:` extra push refs
  `refs/heads/qualification/*` and `refs/heads/main (force)`), and two feature
  branches (`feature/walk-7306` adding `WALK7306.md`, and `fix/#7306-hash`
  adding `HASH7306.md`) directly to the bare repo so Grain's real git and
  declaration pipelines processed them.
- **Simulated CI (`-fake-ci`)**: in-process fake CI provider.
- **Simulated IAP (`tests/iap/iapsim.go`)**: local ES256 JWKS server + proxy
  to authenticate a real `grain/viewer` session against `grain demo`.
- **Direct API calls**: used only to seed capability policy rules
  (`PUT /api/capability-policy`), close a task on a removed repo
  (`POST /api/tasks`, `POST /api/tasks/{id}/close`), register the viewer
  principal on Demo C (`POST /api/access/principals`), and inspect persisted
  state after UI mutations. (In the automated e2e spec
  `next-repo-walkthrough-regressions.spec.ts`, the viewer-role test patches
  `GET /api/config`'s `access` block, matching the established pattern in
  `next-repo-advanced.spec.ts`.)

## Which UI each step ran against
`ui-ng/src/app/shell/nav.ts` now includes `Repos` (`/next/repos`) and
`ui-ng/src/app/app.routes.ts` serves `/next/repos`, `/next/settings/repositories`,
and all 7 tabs of `/next/repos/<owner>/<name>[/<tab>]` (plus classic
`/settings/<tab>` redirects and now `/releases/<release>` direct URLs).
Steps not yet ported to `/next/` ran against the classic React UI at `/`:

| Coverage area | UI exercised | Notes |
| --- | --- | --- |
| Repos list (rail & direct URL, counts, search/sort, long names, Blocked chip first, Add repo in-flight lock & phone fit, pointer + keyboard + tap reorder) | `/next/repos` | Fixed Bug 4 (external-config-repo lock) and Bug 7 (tap Move up / Move down / Done controls) |
| Repo row → return to list → task count → filtered task list (case-insensitive `?repo=ACME/WIDGETS`) → Back | `/next/repos` → `/next/repos/acme/widgets` → `/next/tasks` | Verified case-insensitive matching (8 rows for both `acme/widgets` and `ACME/WIDGETS`) |
| Removed repository's page still counting remaining tasks (including closed work) | Classic `/repos/acme/removed-7306` + `/next/repos/acme/removed-7306` | Classic header displays `Closed 1 · 1 task still targets this repo, which is no longer on the target list`; `/next/` links to `/next/tasks?repo=acme/removed-7306` but has no task-count summary header yet (proposed follow-up) |
| File a task from repo page (repo preselection & persistence) | Classic `/repos/acme/widgets` → `/new` | `/next/repos/<o>/<n>` has no `New task` button yet (proposed follow-up); classic preselected `acme/widgets` and persisted the new task (visible on `/next/tasks?repo=acme/widgets`) |
| Repo page 7 tabs, URLs, classic `/settings/<tab>` redirect, phone active-tab scroll, reload & Back/Forward | `/next/repos/acme/widgets[/<tab>]` | All 7 tabs (`overview`, `ci`, `merge-queue`, `agents`, `branches`, `releases`, `advanced`) verified at 1280 px and 390 px |
| Settings → Repositories (base branch, declarations, CI mode, git connection, base repair bypass confirmation, Check stranded tasks) | `/next/settings/repositories` | All 7 columns present; bypass ON opens confirm dialog, OFF toggles without dialog |
| Overview: repo config, git connection, lifecycle refresh, issue importer (enable, queue-at-once + trust warning, source repo, label, interval, Import issues now counts & error on un-listable repo) | `/next/repos/acme/widgets` | Fixed Bug 6 (unsaved Clone URL guard) |
| Overview: summary strip, overlap re-check card, recent CI runs | Classic `/repos/acme/widgets` | Not yet ported to `/next/` Overview (proposed follow-up) |
| CI: CI mode, Watch default branch, workflows, CI capabilities (built-in `release` server methods + repo policy verdicts) | `/next/repos/acme/widgets/ci` | Fixed Bug 1 (refused setting banner failed to clear on clean save) |
| Merge queue: CI mode switching + work ownership base-gate warnings on Overview & Releases, Merge on green copy (no ordering claim), Require up to date, Base-branch repair bypass confirmation | `/next/repos/acme/widgets/merge-queue` + `acme/beta-7306` | Warnings appear/clear without reload and link to `#repo-ci-mode` |
| Merge queue: Unfixable checks & Overlap re-check panel on repo page and task page | Classic `/repos/acme/widgets/settings/merge-queue`, `/repos/acme/widgets`, `/tasks/5` | Not yet ported to `/next/` Merge queue or `/next/task/5` (proposed follow-up) |
| Agents: Setup command, Prompt extension, Default capabilities, unsaved-edit guard | `/next/repos/acme/widgets/agents` | Guard verified on tab switch and rail navigation |
| Agents: Upstream repository (fork-to-upstream PRs) and read-only deployment / `.grain/settings.yml` prompt extension layers | Classic `/repos/acme/widgets/settings/agents` | Not yet ported to `/next/` Agents tab (proposed follow-up) |
| Branches: list + Add branch | `/next/repos/acme/widgets/branches` | Created `release/walk-7306` from `main`, persisted across reload |
| Compare branches (`/repos/<o>/<n>/compare/<base>...<head>`, `/` & `#` refs, picker, direct link, reload, unknown ref) | Classic `/repos/acme/widgets/settings/branches` & `/repos/acme/widgets/compare/...` | No `/next/` compare page yet (proposed follow-up); verified `main...feature/walk-7306`, `main...fix/%237306-hash`, reload, and `main...no-such-ref-7306` |
| Releases: Work ownership, Release settings, Releases list, Cut RC / Promote RC & disabled reasons, confirmations, inline name refusal, Qualification plan & unsaved guard, direct & unknown release URLs | `/next/repos/acme/widgets/releases` + classic `/repos/acme/widgets/releases/<rel>` | Fixed Bug 2 (direct/unknown release URLs on `/next/`), Bug 3 (Work ownership config-repo lock), Bug 6 (unsaved Release settings guard) |
| Advanced: `.grain/` declarations (sync error + clean sync + warnings), Trigger files, Extra push refs, read-only Ports table, Remove repo (stranding warning, config-repo refusal, clean removal) | `/next/repos/acme/widgets/advanced`, `acme/beta-7306`, `acme/grain-config` | All 5 cards verified against real synced declarations and Demo B config repo |
| Config-repo-locked & role-restricted read-only UI | Demo B (`-external-config-repo`) & Demo C (`grain/viewer` via `iapsim`) | Fixed Bugs 3, 4, and 5 |

## Detailed walkthrough steps and outcomes

### 1. Repos list (`/next/repos`)
- Opened `/next/inbox`, clicked **Repos** in the rail → `/next/repos` (and tested direct URL).
- `acme/widgets` showed **Blocked 1** first, followed by Proposed 3, Waiting 4, Running 2, Asked 2, Ready to submit 2, Merging 4, Deferred 2, and `19 open`.
- Sort options confirmed: `Custom order`, `Name (A–Z)`, `Active first`, `Most tasks`.
- Search `zzz-none` rendered `No repos match “zzz-none”.` with a `Clear the filter` button; clearing restored all rows.
- Added `acme/beta-7306` and 70-character `acme-observability-platform/an-extraordinarily-long-repository-name-7306`:
  - `Add repo` button locked (`disabled=true`) while the `POST /api/repos` request was in flight.
  - Zero horizontal overflow at 1280 px (`scrollWidth === 1280`) and 390 px (`scrollWidth === 390`); at 390 px `Add repo` stayed on screen (`x=289..374`), and `Blocked 1` stayed first on `acme/widgets`.
- Reordered repos via pointer drag (`dragTo`) and keyboard (`Space` → `ArrowDown` → `Enter` / `Escape`), verified persistence across reload. Found touch/tap reorder gap (Bug 7, fixed).
- Opened `acme/widgets` row → `/next/repos/acme/widgets`, returned via `← Repos`. Followed `19 open` → `/next/tasks?repo=acme%2Fwidgets`, verified uppercase `/next/tasks?repo=ACME%2FWIDGETS` matched identically (8 rows), and navigated Back twice to `/next/repos`.

### 2. Settings → Repositories (`/next/settings/repositories`)
- Verified all 7 columns: `Repository`, `Base branch`, `Declarations`, `CI mode`, `Git connection`, `Base repair bypass`, `Manage`.
- `acme/widgets` row showed `main`, `Read`, `grain`, `Not listed`, bypass switch, `Settings`, `Releases`, `Remove`.
- Clicked `Check stranded tasks` → `All open tasks match active target repositories (0 stranded).`
- Toggled `Base repair bypass` ON → confirm dialog `Let base-branch repairs merge without a human?` appeared; confirmed → switched ON. Toggled OFF → no dialog, switched OFF cleanly.
- Clicked `acme/widgets` link → `/next/repos/acme/widgets`.

### 3. Repo page tabs, URLs, Overview, CI, Merge queue, Agents, Branches, Advanced
- Verified classic URL redirects `/next/repos/acme/widgets/settings/<tab>` → `/next/repos/acme/widgets[/<tab>]` for all 7 tabs with zero 4xx responses.
- At 390×844 phone width, clicked through every tab, reloaded on `advanced`, and tested Back (`releases`) and Forward (`advanced`); the active tab stayed scrolled inside `nav.repo-tabs` (`x=16..374`) on every step.
- **Overview**: refreshed lifecycle, enabled issue importer, toggled `Queue imported tasks at once` (verified trust hint updated dynamically to name `acme/nosuch-7306`), saved `acme/nosuch-7306` and ran `Import issues now` → reported `Could not read the open issues on acme/nosuch-7306: … GitHub answered 404: Not Found`. Switched source repo to `acme-upstream/widgets` (interval 15 min), saved, ran `Import issues now` → pass 1: `Read 1 open issue on acme-upstream/widgets: imported 1, 0 already imported.`, pass 2: `imported 0, 1 already imported.` Reloaded and confirmed persisted values.
- **CI**:
  - Added `.grain/workflows/bad.yml` with broken YAML → refused with error banner; fixed YAML and saved → **found Bug 1** (error banner stayed stuck after clean save; fixed).
  - Tested CI capabilities picker: added built-in `https://grain.dev/capabilities/release#registry-push={}` and set real capability policy rules (`PUT /api/capability-policy` denying `release#package-publish` on `acme/widgets` CI and restricting `release#registry-push` to `acme/other`) → verified live policy verdicts judged specifically for `acme/widgets` both on held/offered pills and inside the `Capability method` picker for all three built-in `release` server methods (`image-signing`, `package-publish`, `registry-push`).
- **Base-gate warnings & Merge queue**:
  - On `acme/beta-7306` (watched default branch, un-enrolled), switched CI mode `grain` → `github` → `none` → `grain`: warning banner appeared on Overview and Releases with no reload, cleared without reload when Work ownership standing work was toggled off and reappeared when toggled back on, linked directly to `/next/repos/acme/beta-7306/ci#repo-ci-mode` focusing `select-repo-ci-mode`, and cleared on `grain`.
  - On `acme/widgets` Merge queue: confirmed `Merge on green` copy (`Land approved pull requests automatically once checks succeed.`) makes no ordering claim; toggled `Merge on green`, `Require branch up to date`, and `Base-branch repairs bypass human review gates` (ON asked confirmation, OFF did not); reloaded and verified persisted values.
  - On classic `/repos/acme/widgets/settings/merge-queue`: verified `Merge when green and clean` copy (no ordering claim), added `cla/google` to `Checks no commit here can clear` and verified reload persistence.
  - On classic `/repos/acme/widgets` Overview and `/tasks/5`: verified the overlap re-check panel (`Merge re-checks · 1 change on main · Testing · 16m of 90m · Show the run · grain-merge-queue/78cfb30f/recheck/5/main at abcdef012345 · Rotate the Gemini signing key · #101` linking to `/tasks/5`).
- **Agents**:
  - Tested unsaved Setup command guard on tab switch and rail click (Cancel kept edit and URL), saved setup command and prompt extension, reloaded and verified persisted values.
  - On classic `/repos/acme/widgets/settings/agents`: verified `Upstream repository` field and read-only deployment + `.grain/settings.yml` (`Declared repo prompt layer from .grain/settings.yml…`) prompt extension layers.
- **Branches & Compare**:
  - Created branch `release/walk-7306` from `main` on `/next/repos/acme/widgets/branches`, reloaded and verified persistence.
  - On classic `/repos/acme/widgets/settings/branches`: picked `feature/walk-7306` and clicked `Compare` → `/repos/acme/widgets/compare/main...feature/walk-7306`, verified `WALK7306.md` diff and reload persistence.
  - Opened direct `/repos/acme/widgets/compare/main...fix/%237306-hash` → verified `HASH7306.md` diff (`#` branch name).
  - Opened `/repos/acme/widgets/compare/main...no-such-ref-7306` → verified friendly error (`GitHub has nothing to compare for main...no-such-ref-7306 in acme/widgets…`).
- **Advanced**:
  - First observed real declaration-sync failure banner (`Last read failed: … .grain/settings.yml: yaml: unmarshal errors … still running what 1b208d0b973a declared.`), then pushed valid YAML and observed clean sync at `a5190f6f71c5`: `1 template(s), 0 suite(s), 0 schedule(s), 1 trigger(s)`, both port-types warnings shown once in the declarations card, `.grain/triggers/deploy.yml` in Trigger files, `.grain/templates/release-push.yml` (`refs/heads/main (force), refs/heads/qualification/* (delete)`) in Extra push refs, and all 3 directions (`candidate→main`, `main→candidate`, `main→candidtae`) in the read-only Ports table.
  - Clicked `Remove` on `acme/widgets` → confirm dialog named stranded open tasks (15), schedules (`sched-2, sched-1`), and qualification plan; cancelled.
  - Clicked `Remove` on `acme/beta-7306` (0 open tasks) → removed cleanly and navigated to `/next/repos`.
  - On Demo B (`acme/grain-config`): confirmed `Remove` button is not rendered and description explains it is the deployment's own configuration repository.

### 4. Releases, Work ownership, Qualification plan
- **Work ownership**: toggled `Standing work on this deployment` OFF/ON and verified sync with `Enable release workflow`; toggled `sched-1` per-item override. Found Bug 3 on Demo B (`-external-config-repo`).
- **Release settings**: edited base-branch & post-merge jobs, found Bug 6 (no unsaved-edit guard on leave; fixed), saved cleanly.
- **Releases & RCs**: confirmed `Cut RC` disabled on `1.0` with reason `Cut RC: 1.0.rc.1 is active: a new RC is cut once it is promoted.`; tested invalid release name `bad name` → inline `aria-invalid` error beside input with zero top-level error banner, cleared immediately on edit; created `2.0`; approved qualification on `1.0`'s `rc#1`; tested `Promote RC` confirm dialog (Cancel left unchanged, Confirm moved candidate to `promoting` and updated both Cut/Promote reasons); deleted `2.0` via confirm dialog.
- **Qualification plan**: verified `repeat=0` inline validation (`Repeat must be a whole number of at least 1.`), verified unsaved-edit confirm dialog on leaving tab, and verified config-repo read-only notice on Demo B.
- **Direct & unknown release URLs**: on classic `/repos/acme/widgets/releases/does-not-exist`, confirmed fallback to `1.0` with in-place explanation. On `/next/repos/acme/widgets/releases/unknown-rel`, **found Bug 2** (misparsed `acme` as a git host and fired 400s; fixed).

### 5. Config-repo-locked (Demo B) & Role-restricted `grain/viewer` (Demo C)
- Demo B (`-external-config-repo`): Settings → Repositories, Overview config & issue import settings, CI mode & CI capabilities, Merge queue, Agents, Release settings, Qualification plan, Declarations, and Remove repo all rendered read-only with explanations; operational `Import issues now` remained enabled. Found Bugs 3 (Work ownership editable → 409) and 4 (`/next/repos` Add repo editable → 409); both fixed.
- Demo C (`grain/viewer` via `iapsim`): CI capabilities, Work ownership, Qualification plan, Declarations, and Remove repo disabled with role explanation; Overview, Merge queue, Agents, and Branches disabled **without** explanation (Bug 5; fixed).

## Bugs found and fixed in this task

1. **Refused repo setting error banner did not clear on clean save** (`ui-ng/src/app/repos/repo-detail-page.component.ts`)
   - *Repro*: Save invalid workflow YAML on `/next/repos/acme/widgets/ci` → error banner appears → fix YAML and click `Save workflow` → save succeeds (200) yet `[data-testid="repo-error-banner"]` remained on screen.
   - *Fix*: Cleared `this.error.set(null)` on every clean save/mutation across `RepoDetailPageComponent`.
2. **Direct/unknown release URLs misparsed `<owner>` as a git host** (`ui-ng/src/app/app.routes.ts`, `ui-ng/src/app/repos/repo-detail-page.component.ts`)
   - *Repro*: Open `/next/repos/acme/widgets/releases/unknown-rel` (or `/releases/1.0`) → matched unguarded `repos/:host/:owner/:name/:tab` with `host="acme"`, rendering heading `acme/widgets/releases` and firing three 400 API requests.
   - *Fix*: Unified repo detail routes under a single guarded `repoDetailMatcher` (preserving `RepoDetailPageComponent` reuse across tabs, release picks, and `replaceUrl` fallback, with `runGuardsAndResolvers: "paramsChange"` for browser Back leave-guarding), wired `:release` selection, and added the in-place `[data-testid="unknown-release-notice"]` banner when falling back to the newest release.
3. **Work ownership remained enabled on config-repo-managed deployments** (`ui-ng/src/app/repos/repo-work-ownership.component.ts`, `repo-detail-page.component.ts`)
   - *Repro*: On `-external-config-repo`, open `/next/repos/acme/widgets/releases` and click the Standing work switch → sent `PUT /api/repos/acme/widgets/work-ownership` and dumped an HTTP 409 diff.
   - *Fix*: Added `configRepoOnly` input to `RepoWorkOwnershipComponent`, passed `[configRepoOnly]="configRepoOnly()"`, disabled all controls, and showed `[data-testid="work-ownership-config-managed"]` with `aria-describedby`.
4. **`/next/repos` left Add repo enabled on config-repo-managed deployments** (`ui-ng/src/app/repos/repos-page.component.ts`)
   - *Repro*: On `-external-config-repo`, submit `Add repo` on `/next/repos` → failed with HTTP 409 `config_managed_externally`.
   - *Fix*: Gated `canAdd()` on `!configRepoOnly()` and rendered `[data-testid="repos-config-managed"]` explaining target repositories are managed in the config repository.
5. **Role-restricted viewers saw disabled repo controls with no explanation on Overview, Merge queue, Agents, and Branches** (`ui-ng/src/app/repos/repo-detail-page.component.ts`)
   - *Repro*: Sign in as `grain/viewer` (Demo C) and visit `/next/repos/acme/widgets`, `/merge-queue`, `/agents`, or `/branches` → controls were disabled with no reason shown.
   - *Fix*: Added `roleReadOnlyReason()` and rendered `[data-testid="repo-readonly-banner"]` with the denied reason whenever the user lacks `repos:write`.
6. **Unsaved Release settings and Overview Clone URL edits were discarded without asking** (`ui-ng/src/app/repos/repo-detail-page.component.ts`)
   - *Repro*: Edit `Base-branch CI jobs` on Releases or `Clone URL` on Overview and switch tabs → navigated immediately, silently dropping changes.
   - *Fix*: Added `cloneUrlBaseline` and `releaseBaseline` to `unsavedSections()` and `revertUnsaved()`.
7. **Tap/touch reorder affordance missing on `/next/repos`** (`ui-ng/src/app/repos/repos-page.component.ts`, `repos-page.css`)
   - *Repro*: At phone width (390 px, touch), tap the `⠿` handle on `/next/repos` → row entered picked state (`aria-pressed="true"`) with no on-screen buttons to move it without a hardware keyboard.
   - *Fix*: Added accessible `↑` (Move up), `↓` (Move down), and `Done` controls (`.repos-move-bar`, 28×28+ targets, M3 tokens, responsive wrap at phone width) while a row is picked.

## Regression coverage and verification
- **Unit tests**: Added tests in `ui-ng/src/app/repos/repos-page.component.spec.ts` and `ui-ng/src/app/repos/repo-detail-page.component.spec.ts` (`cd ui-ng && npm test`: 76 files, 833 tests passed; `npm run lint` and `npm run format:check` clean).
- **Playwright e2e**: Added `ui-ng/e2e/next-repo-walkthrough-regressions.spec.ts` and `ui-ng/e2e/next-repo-external-config-locks.spec.ts` (including `@phone`, `@dark`, axe-core, and bounding-box checks). Ran both plus `next-full-system.spec.js`, `next-repos-list.spec.js`, `next-repo-import-releases.spec.js`, `next-repo-ownership-plan-caps.spec.js`, `next-repo-base-gate-warning.spec.js`, `next-repo-ci-mode-bypass.spec.js`, `next-repo-advanced.spec.ts`, `next-repo-advanced-ports.spec.ts`, and `next-settings-repos.spec.js`: **55/55 passed** across desktop, phone, and dark.
- **Go hygiene & routes**: `go test ./tests/hygiene -run "TestCoreSaysNoGoogleTokens|TestNoNewClassicE2ESpecs"` and `go test ./pkg/ui -run TestAngularRouteTableInSync` passed.
- **Visual & geometry check**: Shot before and after contact sheets (`desktop` and `phone`, `light` and `dark`) for standard and `-external-config-repo` demos; verified 0 horizontal overflow at 2048/1280/390 px, all controls inside viewport, and light/dark renders distinct.

## Remaining `/next/` parity gaps (proposed follow-up tasks)
These classic features work today on `/` and have not yet been ported to `/next/`:
1. **Compare branches in `/next/`**: branch compare picker on Branches tab and `/next/repos/<owner>/<name>/compare/<base>...<head>` diff page (supporting `/` and `#` branch names, reload, and unknown ref handling).
2. **Merge queue overlap re-check panel & Unfixable checks in `/next/`**: port `MergeBatchPanel` (standing chip, elapsed time vs max age, CI run and task links) onto `/next/` Repo Overview/Merge queue and `/next/task/<id>`, plus the `Unfixable checks` editor on `/next/` Repo → Merge queue.
3. **Repo page Overview summary, recent CI runs, `New task` preselection, removed-repo closed task counts, and Agents upstream repo + prompt layers in `/next/`**: add a `New task` action preselecting the repository, task-count summary (including closed work on removed repos) & recent CI runs on Overview, and `Upstream repository` + read-only deployment/`.grain/settings.yml` prompt extension layers on Agents.
