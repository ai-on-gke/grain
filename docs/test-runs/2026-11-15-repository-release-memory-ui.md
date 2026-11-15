# 2026-11-15 — Repository, merge queue, compare and release UI walkthrough (task 5888)

## Environment
- **Repository**: `ai-on-gke/grain`, branch `grain/53549e42/task-5888-walk-through-repository-merge`.
- **Commit tested**: `5a9bce62c` (`main` at the start of the run; build stamp `v0.0.0-20261115040402-5a9bce62c102`). All fixes below were re-verified against a rebuilt `bin/grain` and `grain demo` binary embedding the updated `ui-ng/` production bundle.
- **Build**: `make build` (`ui/` Vite bundle into `pkg/ui/static`, `ui-ng/` Angular production bundle into `pkg/ui/static-next`, and `ui-hub/`), plus `CGO_ENABLED=0 go build -tags grain_e2e ./cmd/grain` for loopback-only `-github-sim` seeding routes and `go build ./tests/iap/iapsim.go` for verified-identity RBAC checks.
- **Browser**: Headless Chromium via `@playwright/test` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `timezoneId: UTC`), driven through visible controls (rail links, tabs, pill selects, switches, text inputs, buttons, confirmation dialogs, drag handles, keyboard navigation, reload, browser Back/Forward) across `2048×1080`, `1440×900`, `1280×900`, and `390×844` in light and dark themes.
- **Gemini**: `/home/debian/.gemini-api-key` is not present in this sandbox, so `LOOK=1` was unavailable; visual verification combined `ui/e2e/screenshot.mjs` contact sheets (`before` and `after`) with DOM bounding-box geometry, scroll-overflow checks, and computed Material 3 surface/banner RGB token checks across all four viewports and both themes.
- **Real `grain demo` servers** (each over its own throwaway SQLite store):

| Instance | Address | Build | Flags | Purpose |
| --- | --- | --- | --- | --- |
| Demo A | `127.0.0.1:5888` | base (`5a9bce62c`) | `-github-sim -reconcile=1s -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -mechanical-port -qualification -sensitive-hold -fork-pull-request` | Full interactive walkthrough and bug reproductions |
| Demo B | `127.0.0.1:5889` | base (`5a9bce62c`) | `-external-config-repo=https://github.com/acme/grain-config.git -github-sim -fake-ci -qualification` | Config-repo-locked read-only repository settings, qualification-plan notice, and config-repo removal refusal |
| IAP Demo | `127.0.0.1:5890` behind `iapsim` (`:5891` JWKS, `:5892` proxy) | base (`5a9bce62c`) | `-ui-require-iap -ui-iap-audience=… -ui-iap-issuer=https://iap.test.local -ui-iap-keys-url=http://127.0.0.1:5891/jwks -ui-allowed-principals=user:alice@example.com -github-sim` | Real `grain/viewer` (`bob@example.com`) role-restricted repository settings check |
| Demo C | `127.0.0.1:5895` | fixed branch | `-github-sim -reconcile=1s -fake-ci -base-branch-ci -merge-queue-states -qualification` | Re-verification of fixes, 32-combination layout/theme audit, and `after` screenshots |

## Stubs and external-boundary helpers
Grain's own HTTP API, classic React UI, and Angular `/next/` UI ran unmocked throughout the walkthrough:
- **GitHub boundary**: `-github-sim` over `/tmp/grain-demo-*/githubsim/acme/widgets.git` served branches, compare diffs, issue listing/import, release branches, candidate cuts, and promotions.
- **Git & GitHub-sim seeding** (performed from a scratch clone outside the workspace):
  - Pushed `feature/walk-5888` (`WALK5888.md`), `fix/#5888-hash` (`walk5888.go`), and `.grain/settings.yml` (`prompt-extension`, `port-branches: [main, candidate]`, `port-types: { main->candidate: [fix, revert] }`) to `githubsim/acme/widgets.git`.
  - Seeded two open GitHub issues labelled `walk-5888` (`#8001`, `#8002`) via loopback `POST /api/e2e/github-sim/repos/acme/widgets/issues`.
- **Identity boundary**: `tests/iap/iapsim.go` signed real ES256 IAP assertions for `alice@example.com` (`grain/operator`) and `bob@example.com` (`grain/viewer`). For static screenshot contact sheets (`ui/e2e/screenshot.mjs --access viewer`) and one Playwright test, `GET /api/config`'s `access` block was overlaid with `grain/viewer` as documented in `docs/ui-screenshots.md`.
- **Add-repo in-flight check**: delayed one `POST /api/repos/plan` request by `250 ms` via Playwright `route.continue()` solely to sample `Add repo`'s `disabled` state mid-flight without altering the payload.

## Which UI each step ran against
Per `ui-ng/src/app/shell/nav.ts` and `ui-ng/src/app/app.routes.ts`, `/next/` serves `/next/repos/<owner>/<name>[/<tab>]`, `/next/settings/repositories`, `/next/tasks`, and `/next/task/<id>`. It does not yet have a standalone `/next/repos` list page (no rail destination; `/next/repos` redirects to `/next/inbox`), a branch-compare route, an `Advanced` repository tab, CI capability verdicts, base-gate warnings on Overview/Releases, overlap re-check cards, or the work-ownership and qualification-plan editors. Every migrated surface was walked on `/next/` first, and classic-only surfaces were walked on `/`:

| Area | Walked on `/next/` | Walked on classic `/` (no `/next/` surface yet) |
| --- | --- | --- |
| Repos list (rail, direct URL, counts, search/sort, long names, Add repo lock & phone layout, keyboard & pointer reorder) | Verified `/next/repos` falls back to `/next/inbox` | `/repos` |
| Repo row → task count → filtered task list (case-insensitive) & Back | `/next/repos/acme/widgets` → `View tasks in acme/widgets` (`/next/tasks?repo=…`, `acme/widgets` and `ACME/Widgets` both match 9 rows) | `/repos/acme/widgets` → `23 tasks filed here` (`/?f=repo:any:acme/widgets` and `ACME/Widgets`), browser Back/Forward |
| File task from repo & removed-repo task counts (including closed work) | Verified filed task on `/next/task/33` across reload | `/repos/acme/gadgets` → `new task` (`/new` preselecting `acme/gadgets`); after closing task `#33` and removing `acme/gadgets`, `/repos/acme/gadgets` still reported `proposed 2 · waiting 2 · closed 3 — 7 tasks still target this repo, which is no longer on the target list` |
| Settings → Repositories | `/next/settings/repositories` (base branch, CI mode, bypass switch, Settings/Releases/Remove, Add repository, Check stranded tasks) | `/settings/repositories` (also shows declarations and git-connection columns) |
| Repo tabs, URLs, classic `/settings/<tab>` & `/releases/<rel>` links, reload, Back/Forward, phone tab scroll | `/next/repos/acme/widgets[/<tab>]` | `/repos/acme/widgets/settings/<tab>` for `advanced` |
| Overview | Clone URL, Git connection, lifecycle state & refresh, issue importer switches/fields/counts | Summary cards, overlap re-check card (`MergeBatchPanel`), base-gate warnings, recent CI runs |
| CI | CI mode (`grain`, `github`, `none`), Watch default branch, workflow add/edit/save/remove | CI capabilities (`release` server methods & capability policy verdicts judged for `acme/widgets`) |
| Merge queue | `Merge on green` (no ordering claim), `Require branch up to date`, `Base-branch repairs bypass human review gates` confirmation | Same switches plus `Unfixable checks`, base-gate warnings on Overview/Releases linking to CI Mode, and `MergeBatchPanel` on `/repos/acme/widgets` and `/tasks/5` |
| Agents | Sandbox setup command, standing prompt extension, default capabilities | Upstream repository, read-only deployment + `.grain/settings.yml` prompt-extension layers, issue-import interval |
| Branches | Branch list (`main`, `release/1.0`, etc.) and `Create branch` (`walk/5888-branch`) | Branch compare picker |
| Advanced | — | Declaration syncing (`.grain/settings.yml`), trigger files (`-sensitive-hold`), extra push refs, read-only `Ports` table (`main -> candidate: fix, revert`), and `Remove` |
| Compare branches | Verified `/next/repos/acme/widgets/compare/…` no longer misroutes as repo `compare` | `/repos/acme/widgets/compare/main...feature/walk-5888`, `main...fix/%235888-hash` (reload), and `main...nosuch-branch-5888` (404 explanation) |
| Releases | Release workflow & base/post-merge jobs, release secrets, pending trunk release decisions, release trains (`1.0`, `2.0`), `Cut RC`/`Promote RC` disabled reasons, `Promote`/`Merge back`/`Delete` confirmations, inline refused release name, unknown release URL fallback | Work ownership (`Standing work on this deployment` + per-item overrides across schedules and the release) and qualification-plan editor (unsaved-edit guard + config-repo read-only notice) |

## Walkthrough steps and outcomes

### 1. Repos list, long-name layout, reordering, task counts, filing, and removed repositories
- **Counts & chip order (`/repos`)**: `acme/widgets` rendered `blocked 1` first ahead of `proposed 3 · waiting 5 · running 2 · asked 5 · merging 4 · deferred 1 · 23 tasks`.
- **Validation, in-flight lock, and responsive width**: submitting `not a repo` returned a `400` inline validation message (`repo must be owner/name…`). Adding the 72-character `acme-observability-platform/an-extraordinarily-long-repository-name-5888` locked `Add repo` (`disabled === true`) while in flight, rendered with zero horizontal overflow at `1280×900` (`scrollWidth === 1280`), and kept `Add repo` on screen at phone width `390×844` (`x = 302.7`, `width = 71.3`, `right = 374.0`, `scrollWidth === 390`).
- **Search, sort, and reorder**: filtering for `zzz-no-match` showed `No repos match your search.`; `gadg` narrowed to `acme/gadgets`. Focusing `Move <repo>` and pressing `Enter` exposed `Move here, to the end of the list`, persisted the new order across reload, and dragging `Move acme/widgets` above `Move acme/gadgets` with pointer events reordered the rows cleanly.
- **Task count navigation & filing**: clicking `acme/widgets`, returning via Back, going Forward, and following `23 tasks filed here` opened `/?f=repo:any:acme/widgets`; case-insensitive `ACME/Widgets` matched on both classic `/` and `/next/tasks?repo=ACME/Widgets`. Clicking `new task` on `/repos/acme/gadgets` preselected `acme/gadgets` on `/new`, filed task `#33`, and persisted across reload on `/tasks/33` and `/next/task/33`. After closing `#33` (`POST /api/tasks/33/close`) and removing `acme/gadgets`, `/repos/acme/gadgets` still counted all remaining open and closed tasks (`closed 3`, `7 tasks still target this repo, which is no longer on the target list`).

### 2. Repository tabs, URLs, Settings → Repositories, Overview, CI, Agents, Branches, and Advanced
- **Tabs & deep links (`/next/`)**: clicking Overview, CI & Workflows, Merge Queue, Agents & Grants, Branches, and Releases updated the URL (`/next/repos/acme/widgets/<tab>`, bare path for Overview), survived reload and Back/Forward, redirected classic `/next/repos/acme/widgets/settings/merge-queue` to `/next/repos/acme/widgets/merge-queue`, and scrolled the active `Releases` tab into view on a `390×844` phone (`x = 283.6..374.0`).
- **Issue importer (`/next/`)**: enabling issue import with label `walk-5888` and clicking `Import issues now` reported `Read 2 open issues on acme/widgets: imported 2, 0 already imported.`; a second pass reported `Read 2 open issues on acme/widgets: imported 0, 2 already imported.`; setting source `acme/nosuch-source-5888` reported `Could not read the open issues on acme/nosuch-source-5888: … GitHub answered 404: Not Found` without claiming `Read 0`. The queue-at-once switch dynamically named the source repository in its trust warning.
- **CI, Agents, Branches, and Advanced**:
  - On `/next/repos/acme/widgets/ci`, adding `.grain/workflows/smoke.yml` (`runs-on: ubuntu-latest`), saving, and reloading persisted the workflow in the workflow picker; `runs-on: linux` was properly rejected (`400`) with the runner compatibility message. Classic `/repos/acme/widgets/settings/ci` rendered the built-in `release` server's methods and capability policy verdicts for `acme/widgets`.
  - On `/next/repos/acme/widgets/agents`, saving `make smoke-deps` and `Check accessibility first.` persisted across reload. Classic `/repos/acme/widgets/settings/agents` also displayed the upstream fork-PR repository field, the read-only `.grain/settings.yml` layer (`Keep widget fixtures deterministic and verify both themes.`) beside the operator layer, and the issue-import interval, and prompted before discarding unsaved edits on tab change.
  - On `/next/repos/acme/widgets/branches`, creating `walk/5888-branch` from `main` cut the branch via the reconciler and persisted across reload (`branch-row-walk/5888-branch`).
  - On classic `/repos/acme/widgets/settings/advanced`, declaration syncing reported `.grain/settings.yml`, trigger files from `-sensitive-hold`, the read-only `Ports` table (`main -> candidate: fix, revert`), and `Remove` (with stranding summary on Demo A, and disabled with the external config-repo explanation on Demo B).

### 3. Merge queue, base-gate warnings, and overlap re-check panel
- **Merge queue controls**: `Merge on green` on `/next/repos/acme/widgets/merge-queue` (`Land approved pull requests automatically once checks succeed.`) and `Merge when green and clean` on classic make no ordering claim. Turning `Base-branch repairs bypass human review gates` **on** opened the confirmation dialog (`Let base-branch repairs merge without a human?`) on both `/next/repos/acme/widgets/merge-queue` and `/next/settings/repositories` (and on both classic surfaces), whereas turning it **off** saved immediately without prompting. Classic also exposed `Unfixable checks`.
- **Base-gate warnings (classic)**: switching CI mode to `none` and `github` (and toggling work ownership) updated the Overview and Releases base-gate warnings immediately without a page reload, linked straight back to CI Mode, and cleared once restored to `grain`.
- **Overlap re-check panel (`MergeBatchPanel`, classic)**: inspected live against Demo A's `merge_batch` row on both `/repos/acme/widgets` (`merge re-checks · 1 change on main · testing · 25m of 90m · show the run · grain-merge-queue/66128ce3/recheck/5/main at abcdef012345 · Rotate the Gemini signing key · #101`) and `/tasks/5` (`merge re-check` card above `MERGE GATES` with the same standing chip, elapsed-vs-90m max age, CI run link, and PR `#101` link).

### 4. Compare branches and Releases
- **Compare branches (classic)**: `/repos/acme/widgets/compare/main...feature/walk-5888` rendered the `WALK5888.md` diff; `/repos/acme/widgets/compare/main...fix/%235888-hash` (slash + URL-encoded `#`) rendered `walk5888.go` and survived reload; `/repos/acme/widgets/compare/main...nosuch-branch-5888` explained the `404` (`GitHub has nothing to compare…`).
- **Releases (`/next/` and classic)**:
  - On `/next/repos/acme/widgets/releases`, `Cut RC` on `1.0` was disabled with `Cut RC: 1.0.rc.1 is active: a new RC is cut once it is promoted.`; `Promote RC`, `Merge back`, and `Delete` each opened `g-confirm-dialog` before acting; entering `bad release name!` set `aria-invalid="true"` and reported the error inline beside the input (`pageBannerCount === 0`), clearing on the next keystroke; creating `2.0` provisioned `2.0.latest` and `2.0.rc.1` and survived reload.
  - On classic `/repos/acme/widgets/settings/releases` and `/repos/acme/widgets/releases`, toggled `Standing work on this deployment`, inspected per-item overrides across schedules and the release, verified the qualification-plan editor's unsaved-edit leave prompt (`You have unsaved changes here. Leave and discard them?`) and Demo B's config-repo read-only notice, and confirmed `/repos/acme/widgets/releases/nosuch-5888` fell back to `2.0` with its in-place explanation.

### 5. Config-repo-locked and role-restricted states, console, and network audit
- **Config-repo-locked (Demo B)**: `/next/repos/acme/widgets` displayed `repo-readonly-banner` and disabled config/settings writes while keeping operational controls role-gated; `/next/settings/repositories` disabled `Add repository` and `Remove`.
- **Console & network**: `0` uncaught `pageerror` exceptions across all walkthrough scripts and all Playwright projects (`desktop`, `phone`, `dark`). Every `4xx` response after the fixes below came from a deliberate negative test case (`not a repo`, invalid capability, `runs-on: linux`, `bad release name!`, `acme/nosuch-source-5888`, `nosuch-branch-5888`).

## Bugs found and fixed in `ui-ng/`

### 1. Refused repository setting banner stayed on screen after a clean save
- **Repro** (Demo A): open `/next/repos/acme/widgets/agents`, enter `invalid-cap-name-5888#nosuch` in `Default capabilities`, click `Save agent settings` (`400` sets `repo-error-banner`), then clear the invalid capability and click `Save agent settings` (`200`).
- **Observed**: the snackbar said `Saved repository agent settings`, while the red `repo-error-banner` stayed on screen still claiming the capability was refused. Every other save/mutation handler on `RepoDetailPageComponent` and several handlers on `SettingsCodeComponent` had the same defect.
- **Fix**: clear `this.error.set(null)` on clean completion across all repository save/action methods (and on tab switch), and call `this.state.clearError()` on clean completion across `SettingsCodeComponent`'s repository and Git connection actions. Also sync trimmed `issueImportLabel` and `issueImportRepo` back into signals on save so trailing whitespace never leaves a false dirty state.

### 2. Role-restricted viewers saw disabled repository controls with no explanation
- **Repro** (IAP Demo behind `iapsim` as `bob@example.com` with `grain/viewer`): `/next/settings/repositories` rendered `settings-role-readonly` (`Your role grain/viewer does not hold settings:write…`), whereas `/next/repos/acme/widgets` disabled every setting and Save button with zero `.s-banner.readonly` explanation.
- **Fix**: added `@else if (ready() && !can("PUT /api/repos/{owner}/{name}/settings"))` on `RepoDetailPageComponent` rendering `repo-role-readonly` with `access.deniedReason("PUT /api/repos/{owner}/{name}/settings", repoSlug())`.

### 3. Unsaved workflow edits on zero-workflow repositories (plus repo config and release workflow edits) were dropped without asking
- **Repro** (Demo A): open `/next/repos/<new-repo>/ci` (where `workflows` is `[]`), type YAML into `Declared workflow (.grain/workflows/ci.yml)`, and click `Overview`. Because `onEditWorkflowText` returned early when `selectedWorkflowPath` was not yet in `workflows()`, `unsavedSections()` stayed `[]` and the workflow text vanished without confirmation. Editing `Clone URL` on Overview or `Release workflow & base-branch CI jobs` on Releases likewise switched tabs without asking.
- **Fix**: added `stagedWorkflows()` (matching `saveWorkflows()`'s staged payload) plus `configBaseline` and `releaseSettingsBaseline` to `unsavedSections()` and `revertUnsaved()`. Also cleared banners and reloaded core repo config when navigating between two different repositories in place.

### 4. `/next/settings/repositories` Remove ignored `targetReposStranded` and left deleted repositories in `SettingsStateService`
- **Repro** (Demo A):
  1. On `/next/settings/repositories`, click `Remove` on `acme/widgets` (25+ tasks). `startRemoveRepo` read `plan?.tasks` and `plan?.schedules` instead of `POST /api/repos/plan`'s `targetReposStranded` array, showing no stranding warning (and omitting the empty-allowlist warning when removing the sole repository).
  2. Remove `acme/gadgets`, click `Git` in the Settings drawer, then click `Repositories`: `acme/gadgets` reappeared as a ghost row because `startRemoveRepo` never called `this.state.loadCore()`.
- **Fix**: read `targetReposStranded[].summary` in both `startRemoveRepo` and `checkReposPlan`, include the last-repository unrestricting warning when `remaining.length === 0`, guard async plan updates against dialog dismissal races (`this.confirmReq() === initialReq`), and `await this.state.loadCore()` after `DELETE /api/repos/{owner}/{name}`.

### 5. Classic per-release and compare URLs under `/next/` matched `:host/:owner/:name/:tab` and fired `400`s
- **Repro** (Demo A): open `/next/repos/acme/widgets/releases/1.0` or `/next/repos/acme/widgets/compare/main...release`. Unguarded `repos/:host/:owner/:name[:/:tab]` routes matched `host="acme"`, `owner="widgets"`, `name="releases"` (or `"compare"`), set `h1` to `acme/widgets/releases`, and fired three `400` requests against `acme%2Fwidgets/releases/...`.
- **Fix**: added `repoReleaseMatcher` in `ui-ng/src/app/app.routes.ts` routing `/repos/[<host>/]<owner>/<name>/releases/<release>` to `RepoDetailPageComponent`'s `Releases` tab (selecting the named release, or falling back to the newest release with an in-place `stale-release-banner` explanation when missing), extended `classicRepoSettingsMatcher` to host-prefixed repos, and guarded host repo routes on `/[.:]/.test(s[1].path)`.

## Reviews and regression coverage
- **Design, Functional, and Adversarial reviews**:
  - *Design*: verified across `2048×1080`, `1440×900`, `1280×844`, and `390×844` in light and dark (`32/32` checks with `scrollDiff === 0`, `h1Count === 1`, exact Material 3 token colours on `repo-role-readonly` and `stale-release-banner`, and `16 px` phone margins). Inspected `before` and `after` `contact-sheet.png` sets.
  - *Functional*: drove all five fixed flows end to end over a real `grain demo` (`-github-sim -qualification`).
  - *Adversarial*: caught and fixed three edge cases during review — (a) `startRemoveRepo`'s async `POST /api/repos/plan` overwriting a subsequently opened confirmation dialog after `Escape`, (b) in-place route parameter changes across repositories keeping stale `cloneUrl`/`error` state, and (c) untrimmed `issueImportLabel`/`issueImportRepo` leaving a phantom dirty baseline after save.
- **Automated tests run**:
  - Vitest (`cd ui-ng && npm test`): `49` files, `462` tests passed (including `4` new tests in `repo-detail-page.component.spec.ts` and `1` new test in `settings-page.component.spec.ts`).
  - Lint, format & production budget (`cd ui-ng && npm run format:check && npm run lint && npm run build`): `0` warnings, initial bundle `448.40 kB` (`< 450 kB` budget).
  - Playwright `/next/` (`ui-ng/e2e/next-repo-walkthrough-2026-11-15.spec.ts` across `desktop`, `phone`, `dark` — `10/10` passed; plus `next-full-system.spec.js`, `next-repo-import-releases.spec.js`, `next-repo-ci-mode-bypass.spec.js`, and `next-settings-repos.spec.js` — all passed).

## Remaining `/next/` migration parity gaps (tracked by the ongoing Angular migration, not defects in shipped `/next/` controls)
- Standalone `/next/repos` list destination (search/sort/reorder), `/next/repos/<owner>/<name>/compare/<base>...<head>`, and the `Advanced` tab (declarations, triggers, push-ref warnings, paired-branch `Ports` table).
- CI capability policy verdicts, `Unfixable checks`, overlap re-check (`MergeBatchPanel`), base-gate warnings on Overview/Releases, upstream fork-PR repository, issue-import interval, work ownership, and the qualification-plan editor.
