# Walkthrough Report: Complete Angular UI at `/next/` (`2026-11-13.md` §1)

- **Plan section:** [`docs/test-plans/2026-11-13.md`](../test-plans/2026-11-13.md) §1 ("The complete Angular UI at `/next/`": PRs #1347, #1354, #1410, #1403, #1399, #1233, #1370, #1387)
- **Task:** [Task #5353](/tasks/5353) (origin [Task #4725](/tasks/4725))
- **Commit walked (`HEAD` before fixes):** `23005075b0a82ca3111ff2beb2af4831d4086fc2`
- **Date:** 2026-11-13 UTC
- **Outcome:** **8 defects found, reproduced deterministically, fixed in `ui-ng/`, and covered by Vitest + Playwright scenario regressions (`18/18` walkthrough checks passing, `43/43` Vitest suites / `377` tests passing, `40/40` `ui-ng/e2e` Playwright tests passing).**

---

## 1. Environment & Stubbed Boundaries

| Dimension | Part 1 (`walk_daemon.mjs`) | Part 2 (`walk_demo.mjs`) |
|---|---|---|
| Backend | Real `grain daemon -sandbox-port-base=0 -host-sandboxes` | `grain demo -github-sim -fake-ci -human-review -reconcile 2s` |
| Store driver | SQLite (`store/grain.db`, schema 146) | SQLite (throwaway store, schema 146) |
| Browser | Headless Chromium (`playwright-core` 1.62.1) | Headless Chromium (`playwright-core` 1.62.1) |
| Viewports & themes | `1440×900` desktop, `390×844` phone, light & dark | `1440×900` desktop, `390×844` phone, `2048×1200` wide, light & dark |
| Items covered | §1 Items **1, 2, 4 (CLI catalog), 5, 6** | §1 Items **3, 4, 7, 8, 9** |

### Boundaries Stubbed (Sandbox Isolation)

Only external boundaries unavailable inside the air-gapped sandbox VM were stubbed; Grain's HTTP server, Angular SPA, SQLite store, RBAC/CSRF middleware, `RepoComparer`, ETag cache, and CLI connections importer all ran unmodified:

1. **GitHub API & Git Smart HTTP (`docs/test-runs/2026-11-13-next-ui-complete/ghsim.go`):** Wraps in-tree `pkg/github/githubsim` + `git http-backend` on loopback (`127.0.0.1:4294` in `env.sh`, `127.0.0.1:0` in `ui-ng/e2e`), advertising 5 connected repositories (`acme/widgets`, `acme/api`, `acme/docs`, `acme/infra`, `acme/mobile`) to credential `ghp_walkthrough` while only `acme/widgets` and `acme/api` are configured on the instance via `POST /api/repos`. On `grain demo -github-sim`, real commits were pushed directly into the demo's bare `acme/widgets.git` repository so `GET /api/tasks/{id}/diff-preview` ran `daemon.GitHubRepoComparer` against real git trees.
2. **Model CLI Catalog & LLM Turns (`docs/test-runs/2026-11-13-next-ui-complete/fake-agy.py`):** Minimal `agy` CLI stub passed via `-agy-path` that prints 4 tab-separated catalog models on `agy models` (`claude-sonnet-4-6`, `gemini-2.5-pro`, `unconfigured-catalog-alpha`, `unconfigured-catalog-beta`), while `seed-models.yaml` configures only `[claude-sonnet-4-6, gemini-2.5-pro]`. For in-place Director shaping (§1 item 3), the Director's structured ````grain-draft```` reply was posted to the Director session's backing task via `POST /api/tasks/{id}/messages`.
3. **Google Cloud IAP (`tests/iap/iapsim.go`):** In-tree ES256 JWKS issuer + reverse proxy (`127.0.0.1:4295` / `127.0.0.1:4296`) signing real IAP JWT assertions for `alice@example.com` (`grain/operator`), `viewer@example.com` (`grain/viewer`), and `contributor@example.com` (`grain/contributor`).

### Reproducible Helpers

All helpers live under [`docs/test-runs/2026-11-13-next-ui-complete/`](./2026-11-13-next-ui-complete/):

- `ghsim.go` — 5-repository `githubsim` + `git http-backend` server (`//go:build ignore`).
- `fake-agy.py` — `agy models` CLI catalog stub advertising 2 configured + 2 unconfigured models.
- `env.sh` — Starts `ghsim`, `iapsim`, and `grain daemon -sandbox-port-base=0`, seeding tasks, schedules, suites, Grain CI runs, IAP principals, and a read-only personal token (`gma_...`).
- `walk_daemon.mjs` — Playwright walkthrough over the real SQLite daemon (Items 1, 2, 4 CLI catalog, 5, 6).
- `walk_demo.mjs` — Playwright walkthrough over `grain demo -github-sim -fake-ci` (Items 3, 4, 7, 8, 9).
- `run_all.sh` — Single-command runner executing both parts end-to-end (`./docs/test-runs/2026-11-13-next-ui-complete/run_all.sh`).

---

## 2. Item-by-Item Walkthrough & Verdicts

### Item 1 — Every rail destination over a real daemon, first-list parity, and deep-link reloads
- **Procedure:** Started `grain daemon -sandbox-port-base=0` (SQLite) via `env.sh`, seeded tasks across states (`proposed`, `queued`, `awaiting_reply`, adopted PR, `closed`), standing session, template/schedule/suite, and a Grain CI run on `acme/widgets`. Visited all 11 `/next/` destinations (`/next/inbox`, `/next/workstreams`, `/next/tasks`, `/next/agents`, `/next/sessions`, `/next/ci`, `/next/processes`, `/next/telemetry`, `/next/context`, `/next/fleet`, `/next/settings`), compared first-list rows with classic `/tasks`, `/processes`, `/ci`, and `/fleet`, and reloaded deep links `/next/tasks/<id>` and `/next/settings/repositories`.
- **Verdict:** **PASS.** Zero console errors and zero uncaught page errors across all 11 destinations; task rows, schedule rows (`Nightly widgets check`), CI run rows (`acme/widgets`), and fleet host status matched classic counterparts; `/next/tasks/2` and `/next/settings/repositories` landed on and reloaded into the exact view.

### Item 2 — Role gating on `/next/` & CSRF enforcement
- **Procedure:** Signed in through `iapsim` as `viewer@example.com` (`grain/viewer`) and `contributor@example.com` (`grain/contributor`), deep-linking directly to `/next/tasks/<proposalId>`, `/next/inbox` (at `1440×900` and `390×844`), and `/next/settings/tasks`. Tested mutating controls (`rail-new-task`, `phone-new-task`, `New` menu items, shortcut `c`, `Approve`, `Submit`, `Edit`, `Reply`, `qa-save-btn`), forced mutating `POST`s (`POST /api/tasks`, `POST /api/tasks/{id}/approve`, `POST /api/settings`), a read-only personal token (`tasks:read`, `settings:read`), and cross-origin `POST /api/tasks` with `Origin: https://evil.example`.
- **Before fix:** **FAIL (F1).** For `grain/viewer`, `rail-new-task` and `phone-new-task` remained enabled (`railNewDisabled=false, phoneNewDisabled=false`), pressing `c` opened `new-task-sheet` (`sheetOpenedForViewer=true`), `AppComponent` and `SessionsStore` did not hydrate `AccessService` from `GET /api/config` on direct deep links, and `TaskDetailComponent` rendered `act-reply` without checking `POST /api/tasks/{id}/comments`.
- **After fix:** **PASS.** All mutating controls are hidden or disabled with `AccessService.deniedReason(...)` tooltips for `grain/viewer`; `contributor@example.com` retains `New task` (`tasks:write`) while `Approve` (`tasks:admin`) stays hidden; forced `POST`s from viewer, contributor (on approve), personal token, and cross-origin `Origin: https://evil.example` all return HTTP `403`.

### Item 3 — Director drawer: open/close, cross-page context, focus trap/restore, and in-place task shaping
- **Procedure:** Over `grain demo -github-sim -fake-ci`, opened Director by top-bar trigger (`director-trigger`), `d`, and `Alt+D` (while focused inside `view-filter`), navigated across `/next/inbox` → `/next/tasks` → `/next/ci` verifying live `Context: Task #...` tracking, tested `Tab` cycling inside the compact modal drawer (`390×844`) and `Escape` focus restoration, and shaped a task in place via `act-edit` → `edit-shape-director` → `shaper-apply-save`, checking both `/next/task/<id>` and classic `/tasks/<id>`.
- **Before fix:** **FAIL (F2).** At compact width (`390×844`, `<= 1079px`), pressing `Tab` escaped `[data-testid="director-drawer"]` (`compactTrap=false`) because `DialogTrapDirective` included hidden controls (`.drawer-wide-btn` with `display: none` as first item, and `<input type="file" hidden>` inside `g-conversation` as last item).
- **After fix:** **PASS.** `DialogTrapDirective` filters out `[hidden]`, `[inert]`, `display: none`, and `visibility: hidden` elements; `Tab` stays strictly trapped inside the compact modal drawer and `Escape` restores focus to the opener; in-place shaping persists via `PATCH /api/tasks/{id}` and renders identically on `/next/task/<id>` and classic `/tasks/<id>`.

### Item 4 — Sessions canvas and model picker
- **Procedure:** Over the real daemon (`fake-agy.py` returning 4 CLI models vs 2 configured models in `GET /api/config`), inspected `chat-head-model` options. Over `grain demo`, switched the Director session model via `chat-head-model`, sent the next turn (`Control+Enter`), verified `session.model` and `task.model`, and at `390×844` opened both a long-token Markdown artifact (`canvas-tab-docs`) and a Memory document (`canvas-tab-memory`) in `g-session-canvas`.
- **Before fix:** **FAIL (F3 & F4).**
  1. `buildAgentModelOptions` listed all 4 CLI catalog models including `Unconfigured Alpha` and `Unconfigured Beta` (**F3**).
  2. At `390×844`, `.canvas-body` overflowed horizontally (`canvasBodyScrollW=477 > clientWidth=389`), `.sessions-split.has-canvas` squeezed `.chat-pane` to `260px` on a `390px` phone due to media-query specificity, compact fixed bounds missed the `52px`/`92px` bars, and `session-canvas.css` used raw `#ffffff` (**F4**).
- **After fix:** **PASS.** `chat-head-model` lists only configured models (`Deployment default`, `Claude Sonnet 4.6`, `Gemini 2.5 Pro`); changing the model updates the session and backing task for the next turn; at `390×844`, both Artifact and Memory tabs have zero horizontal overflow (`389 <= 389`, `docScrollW=390`, `chatPaneW=390`).

### Item 5 — Repo scoping (5 connected repos vs 2 configured instance repos)
- **Procedure:** With 5 repos advertised by `ghsim` (`acme/api`, `acme/docs`, `acme/infra`, `acme/mobile`, `acme/widgets`) and 2 configured via `POST /api/repos` (`acme/widgets`, `acme/api`), inspected operational repo pickers in New Task (`nt-repo`), CI (`ci-repo-pill`), and Processes → Schedules (`sched-repo-select`), then inspected Settings → Repositories (`select-reachable-repo`).
- **Before fix:** **FAIL (F5).** Settings → Repositories fetched `GET /api/deployment/reachable-repos` and discarded the repository names (`settingsReachable=null`), Schedules had no configured-repo picker (`schedRepo=null`), and `CIStore` read raw `targetRepos` without `configuredInstanceRepos(cfg)` allowlist scoping.
- **After fix:** **PASS.** Operational pickers across New Task, CI, and Schedules list only `acme/api` and `acme/widgets`, while Settings → Repositories (`select-reachable-repo`) lists all 5 connected `acme/*` repositories.

### Item 6 — Found in release (`v3.6.0`, malformed refusal under field, absent from classic)
- **Procedure:** Opened `new-task-sheet`, expanded `More options`, entered `not-a-semver` in `nt-found-in`, submitted, then entered `v3.6.0`, submitted, queried `GET /api/tasks/<id>` and SQLite `SELECT kind || ':' || target FROM task_link`, and inspected classic `/tasks` New Task dialog.
- **Before fix:** **FAIL (F6).** Submitting a malformed `foundIn` rendered no error under `nt-found-in` (`hasUnderFieldError=false, foundInAriaInvalid=null`) and falsely marked `nt-title` as `aria-invalid="true"`.
- **After fix:** **PASS.** Malformed values are refused client-side (and mapped on server 400s) with `role="alert"` `[data-testid="nt-found-in-error"]` directly under `nt-found-in` (`aria-invalid="true"` on `nt-found-in`, `null` on `nt-title`); `v3.6.0` creates row `found-in:v3.6.0` in SQLite `task_link`; classic New Task has no `Found in release` input.

### Item 7 — Inbox diff preview (Submit, proposal with branch, truncated huge diff, no-branch task)
- **Procedure:** Over `grain demo -github-sim -fake-ci`, pushed real git commits to bare `acme/widgets.git` for (a) a seeded `awaiting_submit` decision, (b) a `proposed` task with a 1-file commit on its task branch, and (c) a `proposed` task with a 20-file commit (`totalHunks=20 > DefaultDiffPreviewHunks`), alongside (d) a `proposed` task with no pushed branch. Selected each row in `/next/inbox`.
- **Before fix:** **FAIL (F7).** Proposals were excluded from `wantsDiff`, so neither (b) nor (c) showed a diff (`proposalShowsDiff=false`), and `diff-truncated` rendered no link when `sel.externalLink` was absent (`hugeHasLink=false`).
- **After fix:** **PASS.** Submit decisions and repository proposals with pushed branches render `inspector-diff`; truncated diffs without an external PR URL render `See the full diff` linking to `/task/<id>?from=inbox&tab=changes`; tasks with no pushed branch receive `404` → `null` and render no empty diff pane.

### Item 8 — Conditional polling (`If-None-Match` & `304 Not Modified` over 60 seconds)
- **Procedure:** Opened `/next/tasks`, classic `/tasks`, `/next/inbox`, and classic `/inbox` in parallel signed-in Chromium contexts against `grain demo -github-sim -fake-ci`, waited 1 s after initial load, and recorded every non-SSE `GET /api/*` request, `If-None-Match` header, status code, and response body byte count for **60 seconds**, then verified cached list content remained rendered.
- **Measurements (60 s idle window):**

| View | Poll GETs (60 s) | With `If-None-Match` | `304 Not Modified` | `200 OK` | Response Body Bytes | Byte Reduction vs Classic |
|---|---:|---:|---:|---:|---:|---:|
| `/next/tasks` | 52 | 41 | 41 | 11 | **50,438 B** | **−96.1%** |
| `/tasks` (classic) | 108 | 20 | 20 | 88 | 1,285,826 B | — |
| `/next/inbox` | 32 | 21 | 21 | 11 | **50,438 B** | **−96.1%** |
| `/inbox` (classic) | 120 | 20 | 20 | 100 | 1,285,862 B | — |

- **Verdict:** **PASS.** Both `/next/tasks` and `/next/inbox` send `If-None-Match`, receive `304 Not Modified` on unchanged polls, retain their rendered rows from `GrainApi`'s ETag cache, and cut polling bandwidth by **96.1%** (~1.24 MB saved per minute per tab).

### Item 9 — Visual & Design-Book Pass (`1440×900` and `390×844`, light and dark)
- **Procedure:** Captured full-matrix screenshots (`PAGES=/next/,/next/tasks,/next/sessions,/next/processes,/next/ci,/next/fleet,/next/settings`, `1440×900` and `390×844`, light and dark, plus `contact-sheet.png` before and after fixes) and audited all 11 rail destinations against `design/design/ui-design-book.design.md` (token discipline, GM3 elevation/surface hierarchy, single `<h1>`, zero native `<select>`, no horizontal scroll, WCAG contrast via `axe-core`).
- **Before fix:** **FAIL (F8 & F4).** `/next/tasks` and `/next/agents` rendered two `<h1>` elements (`<h1 class="view-headline">` + `<h1 id="task-title">` inside embedded `<g-task-detail [pane]="true">`), and `session-canvas.css` used raw `#ffffff`.
- **After fix:** **PASS.** Every `/next/` destination has strictly one `<h1>`, zero native `<select>` elements, zero horizontal overflow at `1440×900` and `390×844`, distinct light/dark GM3 surface tokens, zero raw hex in modified stylesheets, and zero serious/critical `axe-core` contrast or ARIA violations (`next-full-system.spec.js` green across desktop/phone × light/dark).

---

## 3. Summary of Findings & Fixes

| ID | Plan Item | Defect | Fix & Regression Coverage |
|---|---|---|---|
| **F1** | §1.2 | `rail-new-task`, `phone-new-task`, `New` menu items, shortcut `c`, and `act-reply` lacked RBAC gating for `grain/viewer`; `AppComponent` and `SessionsStore` did not hydrate `AccessService` on direct deep links. | Gated shell creation controls and `act-reply` on `AccessService.can(...)` with GM3 disabled tokens and `deniedReason` tooltips; hydrated `AccessService` on boot (`app.component.ts`, `sessions.store.ts`, `task-detail.component.ts`, `styles.css`). Covered by `app.component.spec.ts`, `task-page.component.spec.ts`, and `next-walkthrough-daemon-rbac-repos.spec.js`. |
| **F2** | §1.3 | `DialogTrapDirective` matched hidden controls (`.drawer-wide-btn` `display: none` and `<input type="file" hidden>`), allowing `Tab` to escape compact modal `DirectorDrawerComponent`; `d`/`Alt+D` did not preserve the active element opener. | Added `isFocusableVisible` check in `dialog-trap.directive.ts` and active-element opener tracking in `app.component.ts`. Covered by `next-walkthrough-director-sessions-diff.spec.js`. |
| **F3** | §1.4 | `buildAgentModelOptions` appended all `GET /api/agent-models` CLI catalog entries ahead of `cfg.models`, exposing unconfigured models in Sessions. | Scoped `buildAgentModelOptions` (`session-artifacts.ts`) to `configModels` (plus `currentModel`), using `agentModels` strictly for label/effort enrichment. Covered by `session-artifacts.spec.ts` and `next-walkthrough-daemon-rbac-repos.spec.js`. |
| **F4** | §1.4, §1.9 | At `390×844`, `.canvas-body` overflowed horizontally (`477px > 389px`), `.sessions-split.has-canvas` squeezed `.chat-pane` to `260px`, compact overlay bounds missed `52px`/`92px` bars, and `.canvas-iframe` used raw `#ffffff`. | Fixed responsive grid specificity, compact fixed bounds, `overflow-wrap: anywhere` containment, and `--md-sys-color-surface-container-lowest` token (`sessions-page.component.css`, `session-canvas.css`). Covered by `next-walkthrough-director-sessions-diff.spec.js`. |
| **F5** | §1.5 | Settings → Repositories discarded `reachable-repos` names; Schedules lacked a configured-repo picker; `CIStore` bypassed `configuredInstanceRepos(cfg)` allowlist scoping. | Added `select-reachable-repo` in `settings-code.component.ts`, `sched-repo-select` in `processes-page.component.ts`, and `configuredInstanceRepos` scoping in `ci.store.ts` / `ci-page.component.ts`. Covered by `next-walkthrough-daemon-rbac-repos.spec.js`. |
| **F6** | §1.6 | Malformed `Found in release` (`foundIn`) had no validation message under `nt-found-in` and falsely marked `nt-title` `aria-invalid="true"`. | Added release-tag validation in `new-task-form.ts` (`/^v?\d+\.\d+\.\d+(?:-rc\.\d+)?$/`), inline `nt-found-in-error` alert + focus management in `new-task-sheet.component.ts`, and error border token in `new-task-sheet.component.css`. Covered by `new-task-form.spec.ts` and `next-walkthrough-daemon-rbac-repos.spec.js`. |
| **F7** | §1.7 | `wantsDiff` excluded `"proposal"` entries, and `diff-truncated` rendered no link when `sel.externalLink` was null. | Enabled `wantsDiff` for repository-scoped proposals (`inbox.store.ts`) and added fallback `[routerLink]` (`See the full diff`) to the task's Changes tab (`inbox-page.component.ts`). Covered by `inbox-page.component.spec.ts` and `next-walkthrough-director-sessions-diff.spec.js`. |
| **F8** | §1.9 | Embedded `<g-task-detail [pane]="true">` rendered `<h1 id="task-title">` alongside the host page's `<h1>`, creating duplicate `<h1>` elements on `/next/tasks` and `/next/agents`. | Rendered `<h2 id="task-title">` when `pane()` is `true` and `<h1 id="task-title">` on standalone `/next/task/:id` (`task-detail.component.ts`). Covered by `task-page.component.spec.ts`, `walk_demo.mjs`, and `next-walkthrough-director-sessions-diff.spec.js`. |

---

## 4. Three-Angle Review Mandate Verification (`ui-design-book.design.md` §11)

1. **Design angle:** Verified GM3 surface/outline tokens (`--md-sys-color-*`), zero raw hex values in touched CSS, zero native `<select>` elements (`<g-pill-select>` used for `select-reachable-repo` and `sched-repo-select`), single `<h1>` per route, crisp disabled FAB states without `opacity: .35`, and initial bundle size `449.59 kB < 450.00 kB` with **0 build warnings**.
2. **Functional angle:** Verified keyboard navigation (`c`, `d`, `Alt+D`, `Escape`, compact `Tab` trap), ARIA attributes (`aria-invalid`, `aria-describedby`, `role="alert"`), deep-link reloads, SQLite `task_link` persistence, and ETag `304` caching.
3. **Adversarial angle:** Verified viewer/contributor/personal-token forced mutating `POST`s return `403`, cross-origin `POST`s (`Origin: https://evil.example`) return `403`, long unbroken URLs/identifiers inside `390×844` Canvas wrap without horizontal scroll, and unpushed proposal branches (`404`) leave no empty diff container.

---

## 5. CI repair (merge-queue run, attempt 3)

PR #1479's first `ui-ng e2e` run failed in the new `next-walkthrough-daemon-rbac-repos.spec.js`: the spec opened the `/next/ci` repository menu and read its `innerText` once, before `CIStore.loadConfig()` had returned, so the menu held only "All repositories". This was a race in the new test, not a product defect: the menu is a computed signal and fills in as soon as `GET /api/config` lands. It did not fail on the base branch, because the spec is new in this PR. The CI, Schedules, Settings → Repositories and Sessions model menu checks now use auto-retrying `expect(locator).toContainText(...)` / `not.toContainText(...)` matchers. Both new specs passed 6/6 under `--repeat-each=3 --workers=6`.
