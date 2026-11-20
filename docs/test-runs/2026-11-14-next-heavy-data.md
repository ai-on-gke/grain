# 2026-11-14 walkthrough: `/next/` heavy data, pause banners and `/api/config` ETags

Plan: [`docs/test-plans/2026-11-14.md`](../test-plans/2026-11-14.md) §3 (PR #1498 `b8eb7ddbd`, PR #1501 `ab445d443`). Filed by [Task #5629](/tasks/5629), run as [Task #5728](/tasks/5728).

## Environment

- **Commit walked:** `3cc8bc207` (`main` at the start of the run), built with `make frontend && go build -o /home/debian/bin/grain ./cmd/grain`.
- **Daemons:** real `grain daemon -sandbox-port-base=0 -host-sandboxes` over SQLite 3.40.1 and PostgreSQL 15.20 (Debian `postgresql` package, local cluster). For RBAC, the same daemon sat behind `tests/iap/iapsim.go`. For probe coalescing, its git host was the 2026-11-13 GitHub simulator (`docs/test-runs/2026-11-13-next-ui-complete/ghsim.go`) behind a logging, delaying proxy.
- **Browser:** Playwright 1.62.1 Chromium (headless) at 1440×900 and 390×844, in light and dark (`colorScheme` plus `grain.themeMode`). Go 1.26.10, Node 20.19.
- **Helpers** (all under [`2026-11-14-next-heavy-data/`](2026-11-14-next-heavy-data/)):
  - `pause.sh` runs the `/api/config` ETag walk (and `setup`/`second`/`extend`/`down` steps).
  - `banner.mjs` drives the browser banner across those steps.
  - `seed/main.go` writes 2,044 tasks into a stopped daemon's SQLite store (`-ws-last` files the heavy workstream last).
  - `walk.mjs` walks the seven `/next/` pages at both widths and themes. It records console errors and 4xx responses and checks the heavy Workstreams layout. `IAP_EMAIL` signs in through iapsim.
  - `slowproxy.py` and `probes.sh` set up the slow git host; `tabs.mjs` opens several Settings tabs at once.

## Item 1: pause countdown across `/api/config` 304s (pass)

The pauses are real usage-limit pauses. `pause.sh` writes the `usage_limit_pause` rows a run's `UsageLimitError` would persist, and the daemon restores them at startup (`orchestrator.Pause.Restore`). The daemon ran with `TZ=America/Los_Angeles` and the team zone set to `Asia/Tokyo` (`PUT /api/settings {"timeZone":"Asia/Tokyo"}`). Both stores gave identical results:

| Step | SQLite | PostgreSQL |
|---|---|---|
| One pause: `Cache-Control: no-store`, `Vary: Cookie` + `Vary: Authorization`, ETag set | ✓ | ✓ |
| `If-None-Match` 3 s later (`secondsRemaining` 5398.8 → 5395.7) | 304 | 304 |
| Second pause (codex, +30 m; `pauses` 1 → 2): old tag | 200, new tag | 200, new tag |
| Extension (claude +90 m → +150 m): old tag / new tag | 200 / 304 | 200 / 304 |
| End early (live `DELETE /api/pause` → `lifted: true`): old tag | 200, new tag, `agentPause: null` | same |

**Browser** (`banner.mjs`, SQLite, Chromium in `America/Los_Angeles`, page clock installed):

- **Initial:** "Model quota exceeded — claude development paused · resets 12:46 PM GMT+9 (in 1h 30m)". The label is in the team zone, not the browser's or the daemon's `TZ`.
- **+10 min on the page clock:** "in 1h 20m". The countdown did not restart from the stale `secondsRemaining`, because `parseResetMs` prefers the absolute `until`.
- **Second pause, one poll (61 s) later:** "development paused across 2 targets … Details (2)".
- **Extension, one poll later:** "resets 1:47 PM GMT+9 (in 2h 18m)".
- **Lift, one poll later:** banner gone. No console errors or 4xx.

**Observation (no bug):** `/next/` never sends `If-None-Match` to `/api/config`. `GrainApi.config()` is a plain `get` with a 2 s memo, not `getIfChanged`, so every 60 s shell poll fetches the full body (all five polls above were 200). The 304 path PR #1498 added is used by API clients, not by `/next/`. That is why the countdown is right either way: no `/next/` reader takes `secondsRemaining` over `until` (`dispatch-status.ts`, `fleet-page.component.ts`). Moving `config()` onto `getIfChanged` would save the body on each poll and is safe for the banner. It is listed as a follow-up, not done here.

## Item 2: batched direction roots vs the old walk (pass, one deliberate difference)

The old per-task walk (`2c9fff5d0`, `directionRootTaskID`) already chose the parent with `MIN(target)`, and it followed WorkOrigin, dangling parents and cycles exactly as the batched resolver does. A new Go test, `TestClusterStatsDirectionRootsMatchLegacyWalkOnHeavyData` (`pkg/model/cluster_stats_roots_test.go`), runs on **both** SQLite and PostgreSQL. Its oracle reimplements the old walk over `GetTask`, independent of the batched SQL. It seeds 2,044 tasks:

- a 250-deep proposed-by chain;
- a cycle `y-a → y-b → y-c → y-a` entered from `y-tail`;
- a dangling parent `g-missing`;
- `m-two` proposed by both `m-zeta` and `m-alpha`;
- a self link falling back to WorkOrigin, and WorkOrigin-only children;
- a 1,750-task random forest with some two-parent tasks.

Every direction's completed and spawned counts match the legacy walk. Spot checks: `d-0000` 250/249, `y-a` 4/3, `g-missing` 2/2 with an empty title, `m-alpha` 3/2, `m-zeta` 1/0, `o-root` 3/2.

**The deliberate difference:** the old rollup ranged over a Go map, so which member of a cycle became its root changed from call to call (from `y-tail` the old walk picks `y-b`, from `y-a` it picks `y-a`). The batched resolver walks in ascending task ID, so it is deterministic. The test pins that and checks two calls return the same direction order. `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/model -run TestClusterStats` passes, with both subtests run (sqlite 7 s, postgres 23 s).

## Item 3: `/next/` pages with heavy data in a browser

`walk.mjs` covered 7 pages × 2 widths × 2 themes: `/next/workstreams`, `/next/workstreams/task:100`, `/next/inbox`, `/next/tasks`, `/next/telemetry`, `/next/settings` and `/next/context`.

**Admin (access code), 2,044 seeded tasks, SQLite:** all 28 page views had no console errors, no 4xx and no sideways scroll.

- **Pills:** no pill overlaps another pill or the progress cell on any Workstreams row or card at 1440 or 390.
- **Repo badges:** the heavy row's badges read "widgets, api +2" (four repositories), capped at 2+N at both widths.
- **Goal:** the long multi-line goal is in the inspector (`ws-inspector` › `ws-goal`), never in `.ws-dhead`.
- **Fold:** "Expand all (45)" goes from 4 to 49 nodes and back via "Collapse done".
- **Escape:** opening the Director drawer over the expanded detail and pressing Escape once closes the drawer only. The tree stays expanded, the URL stays on the workstream and the inspector stays open.

**Bug (filed, not fixed here): Workstreams drops done members on busy deployments.** `WorkstreamsStore.loadClosedPages` reads at most `MAX_CLOSED_PAGES × CLOSED_PAGE_LIMIT = 4 × 100 = 400` closed tasks, newest-closed first. Once a deployment has closed more than 400 tasks in the 14-day Done window, a workstream whose subtasks closed earlier silently loses them. Reproduction:

1. `go run ./docs/test-runs/2026-11-14-next-heavy-data/seed/main.go -store <data-dir>/store` on a stopped daemon, without `-ws-last`, then start the daemon.
2. Open `/next/workstreams/task:100`. It shows 4 nodes (root plus 3 open) and no fold toggle, although 45 merged subtasks exist.
3. `GET /api/tasks?state=closed&closedSince=<now-14d>&limit=200` puts none of tasks 101–145 on the first page.

The same data seeded with `-ws-last` (subtasks closed newest) shows "45/48 done" and the fold. This deployment alone closes far more than 400 tasks a fortnight, so it is a real gap. The fix needs a member-scoped read (closed tasks under one root, or a raised cap the server can serve cheaply), so it is filed as a follow-up rather than patched here.

**Repo-scoped viewer** (iapsim `wv@example.com`; role `widgets/viewer` = `{"includes":["grain/viewer"],"repos":["acme/widgets"]}`, which gives 20 scopes, all limited to `acme/widgets`). This one was fixed in this change. Before the fix, every page logged 403s and their console errors:

| Page | 403 before the fix |
|---|---|
| every page (shell poll) | `GET /api/notifications?open=1&fields=list`, `GET /api/local-script-approvals` |
| `/next/settings` | also `/api/settings`, `/api/deployment`, `/api/agent-keys`, `/api/usage` |
| `/next/tasks` | also `/api/boards` |
| Director drawer | also `/api/agent-models`, `/api/console-sessions` |

- **Cause:** the server refuses a deployment-wide read naming no repository to a role that holds the scope only on some repositories (`authorizeRoleCall`). `/next/` gated these reads with `access.can(route)`, which is true when the scope is held on any repository. A plain `grain/viewer` gets 200 on the same routes.
- **Fix:** `AccessService.canGlobal(route)` (wrapping the existing `canGlobalScope`, already used for `/api/usage` in the shell), `repoLimited(route)` and `globalDeniedReason(route)`. They now gate `InboxStore` (notifications and approvals), `SettingsStateService` (settings, deployment), Settings → Models (usage; the agent-keys card it also gated was later removed on `main`), `TasksStore`/`TasksPage` (boards) and `SessionsStore` (agent models and the session list, skipped only when the role is repo-limited).
- **First-load race:** the inbox store's first load could fire before the role was known. It now reads `/api/config` first when `configLoaded()` is false (shared with the shell's own read).
- **Result:** after the fix all 28 repo-viewer page views are clean. (Settings → Models explained the hidden vault with `globalDeniedReason` until `main` removed the agent-keys card from that page; the merge took that removal and dropped the banner assertion from the e2e spec.)

**Deployment with no repositories** (fresh daemon, no `-repo`, `/api/config` `repos: null`): all 24 page views are clean (there are no workstreams, so no detail page).

## Item 4: probe coalescing and branch refresh (pass)

`probes.sh` put the GitHub simulator behind `slowproxy.py`, which holds `/user/repos`, `/installation/repositories` and `/repos/*/branches` for 3 s and logs every call. One credential (`bot`); reachability TTL is the default 2 min.

**API:**

- **Cold:** one `GET /api/deployment/reachable-repos` took 3.0 s and made one upstream `/user/repos`.
- **Four concurrent callers plus a fifth cancelled at 1 s** (`curl -m 1`): one upstream listing. The four others got 200 with 2 repos and `connections[0].ok`, so the cancelled caller did not poison the flight.
- **Warm:** 1.7 ms, no upstream call.

**Browser:** four `/next/settings/repositories` tabs opened at once, one closed after 1 s. One upstream listing, and each surviving tab got its `reachable-repos` 200, with no console errors.

**Branches** (`GET /api/repos/acme/widgets/github-branches`): the plain read stays cached after a new branch is pushed to the simulator's bare repo (`feat/second-new-branch` absent, 24 ms). `?refresh=1` made one upstream call (3.0 s) and listed the new branch, and later plain reads serve the refreshed list.

In `/next/` the New Task branch picker reads grain's own `GET /api/repos/{owner}/{name}/branches`, not `github-branches`. `?refresh=1` is reached through `BranchRefsService.hasBranch`, which forces a refresh, at most every 15 s, when a branch it is asked about is missing from the cached list (Inbox and the task Changes tab).

## Item 5: classic pages

Not needed: every surface in §3 has a `/next/` page, and nothing was changed in `ui/`.

## UI change: tests and reviews

- **Unit:** `npx vitest run` passes, 49 files and 458 tests (a new `AccessService` case covers `canGlobal`, `repoLimited` and `globalDeniedReason`).
- **e2e:** `npx playwright test` in `ui-ng/` passes 79/79, including `next-full-system.spec.js` and the new `ui-ng/e2e/next-repo-limited-viewer.spec.js`. On the pre-fix source the new spec fails with 29 deployment-wide requests (notifications, approvals, boards, …); with the fix it passes.
- **Screenshots:**
  - "before" and "after" contact sheets: Settings, Inbox, Tasks, Workstreams and Context as the repo-scoped viewer, at 1440/390 in light/dark;
  - an admin heavy-data sheet: Workstreams, the expanded workstream detail, Inbox, Tasks, Telemetry and Settings.
  - Nothing moves for an unlimited caller. For the repo viewer nothing visible changes now that `main` has removed the agent-keys card; the contact sheets were taken before that.
- **Contact sheets** (committed beside the report): [`before-contact-sheet.png`](2026-11-14-next-heavy-data/before-contact-sheet.png) and [`after-contact-sheet.png`](2026-11-14-next-heavy-data/after-contact-sheet.png) for the repo-scoped viewer, and [`admin-heavy-contact-sheet.png`](2026-11-14-next-heavy-data/admin-heavy-contact-sheet.png) for the admin heavy-data walk.
- **Design book:** §5 "Role-aware" now states the `canGlobal` rule.
- **Review mandate:** the author ran the three passes below; they were not independent reviewers.
  - **Design:** no token, spacing or layout change. The new banner text reuses `s-banner warn`. There is no new copy outside that sentence.
  - **Functional:** the steps and requests are recorded above, with a failing-then-passing Playwright scenario.
  - **Adversarial:**
    - an unlimited caller still gets every read (`access: null` means `canGlobal` is true);
    - a config read that fails on first load leaves the old behaviour;
    - a role lacking `sessions:read` altogether keeps the old unconditional sessions read, because the existing specs rely on it and the server answers it with its own reason.
    - **P2, filed with the follow-up:** a repo-limited viewer's Sessions page now shows an empty list rather than the old 403 error; it could say why.

## Follow-ups proposed

- Workstreams: read a workstream's closed members by root, not from a 400-task window.
- `/next/` `GET /api/config` polls: use `If-None-Match` (`getIfChanged`). Also, a repo-limited viewer's Sessions page could explain why it is empty.
