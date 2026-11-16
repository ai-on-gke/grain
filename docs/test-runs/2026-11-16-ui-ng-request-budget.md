# `/next/` Per-Page HTTP Request Budget — Before & After (2026-11-16)

## Methodology

- **Measurement tool:** `ui-ng/e2e/measure-requests.mjs` + `ui-ng/e2e/request-harness.mjs` (CDP `Network.requestWillBeSent` + `Zone.prototype` async call-stack preservation + production source-map attribution).
- **CI gate:** `ui-ng/e2e/next-request-budget.spec.ts` enforcing per-page ceilings from `ui-ng/e2e/request-budgets.ts`.
- **Seeded `grain demo` fixture:** `128` tasks (`86` open, `42` closed, including a 30-task workstream), `12` running agents with active runs and transcripts, `6+` open notifications, and a live `director` console session with an active run and streaming transcript.
- **Windows measured per page:** `300s` (`5m`) steady-state visible idle after initial load settles, plus `60s` (`1m`) scripted normal use, compared side-by-side against Classic UI (`/`).

## Steady-State Idle Summary (5 Minutes Idle, Single Visible Tab)

| Page | `/next/` BEFORE (req/min) | `/next/` BEFORE dups | `/next/` AFTER (req/min) | `/next/` AFTER dups | Classic `/` (req/min) | `/next/` AFTER ÷ Classic | Budget ceiling (req/min) |
|---|---:|---:|---:|---:|---:|---:|---:|
| Inbox (`/next/inbox`) | 24.60 | 8 | **4.60** | **0** | 120.00 | 0.04× | 8 |
| Tasks (`/next/tasks`) | 47.20 | 9 | **4.60** | **0** | 108.00 | 0.04× | 8 |
| Agents (`/next/agents`) | 32.60 | 9 | **6.60** | **0** | 108.00 | 0.06× | 8 |
| Workstreams (`/next/workstreams`) | 28.80 | 6 | **4.60** | **0** | 109.40 | 0.04× | 8 |
| Task detail (`/next/task/3?tab=runs`) | 56.60 | 10 | **6.80** | **0** | 139.80 | 0.05× | 10 |
| Sessions (`/next/sessions/director`) | 124.40 | 119 | **7.20** | **0** | 128.00 | 0.06× | 10 |
| Processes (`/next/processes`) | 54.60 | 10 | **6.00** | **0** | 188.80 | 0.03× | 8 |
| CI (`/next/ci`) | 49.00 | 10 | **5.80** | **0** | 123.80 | 0.05× | 8 |
| Repos (`/next/repos`) | 24.40 | 8 | **4.00** | **0** | 108.00 | 0.04× | 8 |
| Telemetry (`/next/telemetry`) | 24.60 | 7 | **4.00** | **0** | 108.00 | 0.04× | 8 |
| Context (`/next/context`) | 24.80 | 10 | **4.60** | **0** | 108.00 | 0.04× | 8 |
| Fleet (`/next/fleet`) | 24.80 | 10 | **4.60** | **0** | 121.80 | 0.04× | 8 |
| Settings (`/next/settings`) | 24.20 | 8 | **4.40** | **0** | 108.20 | 0.04× | 8 |

- **Hidden tab rate (`document.hidden === true`):** `0.00 req/min` across every `/next/` page (`GrainEvents` disconnects SSE, `TaskFeedStore` and `LiveRefreshService` suspend all polls and transcript watches until `visibilitychange`).

---

## After Optimization — Full Per-Route & Initiator Breakdown

Seeded against `grain demo -as alice@example.com -github-sim -git-proxy -observability`: **128 tasks** (86 open, 42 closed), **12 running agents** with transcripts, **6+ open notifications**, and **live `director` session** with active run + streaming transcript.

## Summary by Page

| Page | `/next/` initial load | `/next/` idle (5m idle) | `/next/` idle req/min | Classic `/` idle (5m idle) | Classic `/` idle req/min | Ratio (`/next/` ÷ `/`) | `/next/` active (1m active) req/min | Classic `/` active (1m active) req/min |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Inbox (`/next/inbox`) | 73 | 23 | **4.60** | 600 | 120.00 | 0.04× | 16.00 | 120.00 |
| Tasks (`/next/tasks`) | 75 | 23 | **4.60** | 540 | 108.00 | 0.04× | 6.00 | 108.00 |
| Agents (`/next/agents`) | 73 | 33 | **6.60** | 540 | 108.00 | 0.06× | 8.00 | 108.00 |
| Workstreams (`/next/workstreams`) | 71 | 23 | **4.60** | 547 | 109.40 | 0.04× | 7.00 | 108.00 |
| Task detail (live run + transcript) (`/next/task/3?tab=runs`) | 74 | 34 | **6.80** | 699 | 139.80 | 0.05× | 8.00 | 140.00 |
| Sessions (live session + transcript) (`/next/sessions/director`) | 81 | 36 | **7.20** | 640 | 128.00 | 0.06× | 13.00 | 128.00 |
| Processes (`/next/processes`) | 81 | 30 | **6.00** | 944 | 188.80 | 0.03× | 16.00 | 188.00 |
| CI (`/next/ci`) | 72 | 29 | **5.80** | 619 | 123.80 | 0.05× | 4.00 | 124.00 |
| Repos (`/next/repos`) | 74 | 20 | **4.00** | 540 | 108.00 | 0.04× | 9.00 | 144.00 |
| Telemetry (`/next/telemetry`) | 76 | 20 | **4.00** | 540 | 108.00 | 0.04× | 5.00 | 108.00 |
| Context (`/next/context`) | 72 | 23 | **4.60** | 540 | 108.00 | 0.04× | 4.00 | 108.00 |
| Fleet (`/next/fleet`) | 73 | 23 | **4.60** | 609 | 121.80 | 0.04× | 10.00 | 127.00 |
| Settings (`/next/settings`) | 74 | 22 | **4.40** | 541 | 108.20 | 0.04× | 14.00 | 108.00 |

## Angular UI (`/next/`) — Per-Page Breakdown

### Inbox (`/next/inbox`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 3 | 0.60 | `/next/chunk-7VOUCBAI.js (1), /next/chunk-WG44JDET.js (1)` | 0 |
| **Total** | **23** | **4.60** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 3 | 3.00 | `services/task-feed.service.ts:218 (setTimeout) (2), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1)` | 1 |
| `GET /api/tasks/{id}` | 3 | 3.00 | `inbox/inbox-page.component.ts:362 (ɵɵlistener)` | 0 |
| `GET /api/repos/{owner}/{name}/github-branches` | 2 | 2.00 | `inbox/inbox-page.component.ts:362 (ɵɵlistener) (1), chunk-SWWQG3LR.js:1 (1)` | 0 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 2 | 2.00 | `chunk-SWWQG3LR.js:1 (1), services/live-refresh.service.ts:249 (setTimeout) (1)` | 0 |
| `GET /next/*.js (bundle)` | 2 | 2.00 | `app.component.ts:1297 (then) (1), shell/snackbar.component.ts:68 (Qt) (1)` | 0 |
| `GET /api/agent-models` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/console-sessions` | 1 | 1.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/tasks/summaries` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| **Total** | **16** | **16.00** | — | **1** |

### Tasks (`/next/tasks`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 3 | 0.60 | `/next/chunk-7VOUCBAI.js (1), /next/chunk-WG44JDET.js (1)` | 0 |
| **Total** | **23** | **4.60** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 4 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 2 | 2.00 | `tasks/tasks-page.component.ts:1401 (Object.hostBindings)` | 0 |
| **Total** | **6** | **6.00** | — | **0** |

### Agents (`/next/agents`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/sandboxes` | 10 | 2.00 | `services/live-refresh.service.ts:130 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 3 | 0.60 | `/next/chunk-7VOUCBAI.js (1), /next/chunk-WG44JDET.js (1)` | 0 |
| **Total** | **33** | **6.60** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 4 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/sandboxes` | 2 | 2.00 | `services/live-refresh.service.ts:130 (setTimeout)` | 0 |
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 2 | 2.00 | `agents/agents-page.component.ts:561 (ɵɵlistener)` | 0 |
| **Total** | **8** | **8.00** | — | **0** |

### Workstreams (`/next/workstreams`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 3 | 0.60 | `/next/chunk-7VOUCBAI.js (1), /next/chunk-WG44JDET.js (1)` | 0 |
| **Total** | **23** | **4.60** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 5 | 5.00 | `services/task-feed.service.ts:218 (setTimeout) (3), workstreams/workstreams-page.component.ts:569 (ɵɵlistener) (1)` | 1 |
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 1 | 1.00 | `workstreams/workstreams-page.component.ts:569 (ɵɵlistener)` | 0 |
| `GET /api/tasks/summaries` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| **Total** | **7** | **7.00** | — | **1** |

### Task detail (live run + transcript) (`/next/task/3?tab=runs`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 11 | 2.20 | `services/live-refresh.service.ts:249 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 3 | 0.60 | `/next/chunk-7VOUCBAI.js (1), /next/chunk-WG44JDET.js (1)` | 0 |
| **Total** | **34** | **6.80** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 4 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 4 | 4.00 | `services/live-refresh.service.ts:249 (setTimeout)` | 0 |
| **Total** | **8** | **8.00** | — | **0** |

### Sessions (live session + transcript) (`/next/sessions/director`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout) (19), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1)` | 0 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 11 | 2.20 | `services/live-refresh.service.ts:249 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 3 | 0.60 | `/next/chunk-7VOUCBAI.js (1), /next/chunk-WG44JDET.js (1)` | 0 |
| `GET /api/console-sessions` | 1 | 0.20 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 1 | 0.20 | `chunk-SWWQG3LR.js:1` | 0 |
| **Total** | **36** | **7.20** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 4 | 4.00 | `services/task-feed.service.ts:218 (setTimeout) (3), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 4 | 4.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (3), chunk-SWWQG3LR.js:1 (1)` | 2 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 4 | 4.00 | `services/live-refresh.service.ts:249 (setTimeout)` | 0 |
| `GET /api/console-sessions` | 1 | 1.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl)` | 0 |
| **Total** | **13** | **13.00** | — | **2** |

### Processes (`/next/processes`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/schedules` | 4 | 0.80 | `services/live-refresh.service.ts:130 (setTimeout)` | 0 |
| `GET /api/suite-runs` | 4 | 0.80 | `services/live-refresh.service.ts:130 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 2 | 0.40 | `/next/chunk-WG44JDET.js (1), /next/chunk-YTM2K3WN.js (1)` | 0 |
| **Total** | **30** | **6.00** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/repos/{owner}/{name}/declarations` | 4 | 4.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/tasks` | 4 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/declarations` | 1 | 1.00 | `shared/tip.directive.ts:85 (ɵɵlistener)` | 0 |
| `GET /api/schedules` | 1 | 1.00 | `shared/tip.directive.ts:85 (ɵɵlistener)` | 0 |
| `GET /api/schedules/{id}/composition` | 1 | 1.00 | `shared/tip.directive.ts:85 (Object.hostBindings)` | 0 |
| `GET /api/suite-runs` | 1 | 1.00 | `shared/tip.directive.ts:85 (ɵɵlistener)` | 0 |
| `GET /api/suites` | 1 | 1.00 | `shared/tip.directive.ts:85 (ɵɵlistener)` | 0 |
| `GET /api/suites/suite-1/composition` | 1 | 1.00 | `shared/tip.directive.ts:85 (Object.hostBindings)` | 0 |
| `GET /api/templates` | 1 | 1.00 | `shared/tip.directive.ts:85 (ɵɵlistener)` | 0 |
| `GET /api/templates/{id}/composition` | 1 | 1.00 | `shared/tip.directive.ts:85 (ɵɵlistener)` | 0 |
| **Total** | **16** | **16.00** | — | **0** |

### CI (`/next/ci`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/ci/faults` | 3 | 0.60 | `services/live-refresh.service.ts:130 (setTimeout)` | 0 |
| `GET /api/ci/runs` | 3 | 0.60 | `services/live-refresh.service.ts:130 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 3 | 0.60 | `/next/chunk-JDC34K4A.js (1), /next/chunk-WG44JDET.js (1)` | 0 |
| **Total** | **29** | **5.80** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 4 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| **Total** | **4** | **4.00** | — | **0** |

### Repos (`/next/repos`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| **Total** | **20** | **4.00** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 3 | 3.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `repos/repo-detail-page.component.ts:2771 (then)` | 0 |
| `GET /api/deployment/git-connections` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/deployment/reachable-repos` | 1 | 1.00 | `shared/tip.directive.ts:85 (ɵɵlistener)` | 0 |
| `GET /api/deployment/repos/acme/widgets/config` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/repos/{owner}/{name}/lifecycle` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/repos/{owner}/{name}/settings` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| **Total** | **9** | **9.00** | — | **0** |

### Telemetry (`/next/telemetry`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| **Total** | **20** | **4.00** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 4 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/metrics` | 1 | 1.00 | `telemetry/telemetry-page.component.ts:137 (ɵɵlistener)` | 0 |
| **Total** | **5** | **5.00** | — | **0** |

### Context (`/next/context`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 3 | 0.60 | `/next/chunk-JDC34K4A.js (1), /next/chunk-WG44JDET.js (1)` | 0 |
| **Total** | **23** | **4.60** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 4 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| **Total** | **4** | **4.00** | — | **0** |

### Fleet (`/next/fleet`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 3 | 0.60 | `/next/chunk-7VOUCBAI.js (1), /next/chunk-WG44JDET.js (1)` | 0 |
| **Total** | **23** | **4.60** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 4 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /api/actions-cache` | 1 | 1.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener)` | 0 |
| `GET /api/logs` | 1 | 1.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener)` | 0 |
| `GET /api/logs/{source}` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/package-cache` | 1 | 1.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener)` | 0 |
| `GET /api/pause` | 1 | 1.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener)` | 0 |
| `GET /api/pipeline-pause` | 1 | 1.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener)` | 0 |
| **Total** | **10** | **10.00** | — | **0** |

### Settings (`/next/settings`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 4.00 | `services/task-feed.service.ts:218 (setTimeout)` | 0 |
| `GET /next/*.js (bundle)` | 2 | 0.40 | `/next/chunk-JDC34K4A.js (1), /next/chunk-7VOUCBAI.js (1)` | 0 |
| **Total** | **22** | **4.40** | — | **0** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 5 | 5.00 | `services/task-feed.service.ts:218 (setTimeout) (3), shared/tip.directive.ts:85 (ɵɵlistener) (1)` | 2 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/boards` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/deployment` | 1 | 1.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl)` | 0 |
| `GET /api/deployment/git-connections` | 1 | 1.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl)` | 0 |
| `GET /api/github-tokens` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/settings` | 1 | 1.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl)` | 0 |
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 1 | 1.00 | `shared/tip.directive.ts:85 (ɵɵlistener)` | 0 |
| `GET /api/tasks/summaries` | 1 | 1.00 | `chunk-SWWQG3LR.js:1` | 0 |
| `GET /api/usage` | 1 | 1.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl)` | 0 |
| **Total** | **14** | **14.00** | — | **2** |

## Classic UI (`/`) — Per-Page Breakdown

### Inbox (`/inbox`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/local-script-approvals` | 60 | 12.00 | `ui/src/components/InboxPage.jsx:102 (api)` | 59 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **600** | **120.00** | — | **78** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/local-script-approvals` | 12 | 12.00 | `ui/src/components/InboxPage.jsx:102 (api)` | 11 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **120** | **120.00** | — | **14** |

### Tasks (`/tasks`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **540** | **108.00** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |

### Agents (`/tasks?state=running`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **540** | **108.00** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |

### Workstreams (`/tasks`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 101 | 20.20 | `ui/src/App.jsx:1314 (api)` | 1 |
| `GET /api/config` | 101 | 20.20 | `ui/src/App.jsx:1616 (api)` | 1 |
| `GET /api/console-sessions` | 101 | 20.20 | `ui/src/App.jsx:1388 (api)` | 1 |
| `GET /api/tasks` | 101 | 20.20 | `ui/src/App.jsx:1151 (api)` | 1 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 2 | 0.40 | `HTML parser` | 0 |
| `GET /api/capability-methods` | 1 | 0.20 | `ui/src/components/capabilityMethods.js:61 (api)` | 0 |
| **Total** | **547** | **109.40** | — | **23** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |

### Task detail (live run + transcript) (`/tasks/3`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/ci/runs` | 99 | 19.80 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 99 | 19.80 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 99 | 19.80 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/tasks` | 99 | 19.80 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 99 | 19.80 | `ui/src/App.jsx:2890 (api)` | 0 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 60 | 12.00 | `ui/src/useAttemptTranscript.js:55 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /api/capability-methods` | 1 | 0.20 | `ui/src/components/capabilityMethods.js:61 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/github-branches` | 1 | 0.20 | `ui/src/branchRefs.js:56 (api)` | 0 |
| `GET /api/tasks/{id}/tree` | 1 | 0.20 | `ui/src/components/detail/WorkTree.jsx:79 (api)` | 0 |
| `POST /api/capability-grants/check` | 1 | 0.20 | `ui/src/components/capabilityMethods.js:95 (api)` | 0 |
| **Total** | **699** | **139.80** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 20 | 20.00 | `ui/src/App.jsx:2890 (api)` | 0 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 12 | 12.00 | `ui/src/useAttemptTranscript.js:55 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **140** | **140.00** | — | **3** |

### Sessions (live session + transcript) (`/sessions/director`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 100 | 20.00 | `ui/src/App.jsx:1464 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **640** | **128.00** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 20 | 20.00 | `ui/src/App.jsx:1464 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **128** | **128.00** | — | **3** |

### Processes (`/processes`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/schedules` | 101 | 20.20 | `ui/src/App.jsx:1250 (api)` | 1 |
| `GET /api/suite-runs` | 101 | 20.20 | `ui/src/App.jsx:1292 (api)` | 1 |
| `GET /api/suites` | 101 | 20.20 | `ui/src/App.jsx:1278 (api)` | 1 |
| `GET /api/templates` | 101 | 20.20 | `ui/src/App.jsx:1264 (api)` | 1 |
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **944** | **188.80** | — | **23** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/schedules` | 20 | 20.00 | `ui/src/App.jsx:1250 (api)` | 0 |
| `GET /api/suite-runs` | 20 | 20.00 | `ui/src/App.jsx:1292 (api)` | 0 |
| `GET /api/suites` | 20 | 20.00 | `ui/src/App.jsx:1278 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/templates` | 20 | 20.00 | `ui/src/App.jsx:1264 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **188** | **188.00** | — | **3** |

### CI (`/ci`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 160 | 32.00 | `ui/src/App.jsx:1314 (api) (100), ui/src/components/CIPage.jsx:1938 (api) (60)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/ci/cache` | 10 | 2.00 | `ui/src/components/CIPage.jsx:1979 (api)` | 9 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/sandboxes` | 9 | 1.80 | `ui/src/components/StaleSchedulesAlert.jsx:59 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **619** | **123.80** | — | **28** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 32 | 32.00 | `ui/src/App.jsx:1314 (api) (20), ui/src/components/CIPage.jsx:1938 (api) (12)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/ci/cache` | 2 | 2.00 | `ui/src/components/CIPage.jsx:1979 (api)` | 1 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/sandboxes` | 2 | 2.00 | `ui/src/components/StaleSchedulesAlert.jsx:59 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **124** | **124.00** | — | **4** |

### Repos (`/repos`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **540** | **108.00** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 32 | 32.00 | `ui/src/App.jsx:1314 (api) (20), ui/src/components/RepoRuns.jsx:59 (api) (12)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/merge-batches` | 12 | 12.00 | `ui/src/components/MergeBatchPanel.jsx:284 (api)` | 11 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/capability-methods` | 2 | 2.00 | `ui/src/components/capabilityMethods.js:61 (api)` | 0 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/branches` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:635 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/capabilities` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:652 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/declarations` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:689 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/prompt-extension` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:664 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/release` | 1 | 1.00 | `ui/src/components/RepoReleaseWorkflowSettings.jsx:141 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/require-branch-up-to-date` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:697 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/setup-command` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:679 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/unfixable-checks` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:705 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/work-ownership` | 1 | 1.00 | `ui/src/components/RepoWorkOwnership.jsx:74 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/workflows` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:713 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **144** | **144.00** | — | **14** |

### Telemetry (`/metrics`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **540** | **108.00** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |

### Context (`/settings/memory`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **540** | **108.00** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |

### Fleet (`/system`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/sandboxes` | 40 | 8.00 | `ui/src/components/system/HealthCountersPanel.jsx:43 (api) (30), ui/src/components/StaleSchedulesAlert.jsx:59 (api) (10)` | 0 |
| `GET /api/metrics` | 29 | 5.80 | `ui/src/components/system/GitHubBudgetPanel.jsx:49 (api)` | 28 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **609** | **121.80** | — | **47** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/sandboxes` | 7 | 7.00 | `ui/src/components/system/HealthCountersPanel.jsx:43 (api) (4), ui/src/components/SandboxHealthPage.jsx:1634 (api) (2)` | 0 |
| `GET /api/metrics` | 4 | 4.00 | `ui/src/components/system/GitHubBudgetPanel.jsx:49 (api)` | 3 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/host/top` | 2 | 2.00 | `ui/src/components/TopPage.jsx:72 (api)` | 0 |
| `GET /api/logs/{source}` | 2 | 2.00 | `ui/src/components/LogsPage.jsx:104 (api)` | 0 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/deployment/reachable-repos` | 1 | 1.00 | `ui/src/components/system/HealthTab.jsx:68 (api)` | 0 |
| `GET /api/logs` | 1 | 1.00 | `ui/src/components/LogsPage.jsx:85 (api)` | 0 |
| `GET /api/settings` | 1 | 1.00 | `ui/src/components/system/HealthTab.jsx:61 (api)` | 0 |
| `GET /api/settings/cli-run` | 1 | 1.00 | `ui/src/components/system/HealthTab.jsx:64 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **127** | **127.00** | — | **6** |

### Settings (`/settings`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /api/usage` | 1 | 0.20 | `ui/src/components/settings/UsageCard.jsx:77 (api)` | 0 |
| **Total** | **541** | **108.20** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |


---

## Baseline (Before Optimization) — Full Per-Route & Initiator Breakdown

Seeded against `grain demo -as alice@example.com -github-sim -git-proxy -observability`: **128 tasks** (86 open, 42 closed), **12 running agents** with transcripts, **6+ open notifications**, and **live `director` session** with active run + streaming transcript.

## Summary by Page

| Page | `/next/` initial load | `/next/` idle (5m idle) | `/next/` idle req/min | Classic `/` idle (5m idle) | Classic `/` idle req/min | Ratio (`/next/` ÷ `/`) | `/next/` active (1m active) req/min | Classic `/` active (1m active) req/min |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Inbox (`/next/inbox`) | 82 | 123 | **24.60** | 604 | 120.80 | 0.20× | 48.00 | 120.00 |
| Tasks (`/next/tasks`) | 78 | 236 | **47.20** | 543 | 108.60 | 0.43× | 60.99 | 108.00 |
| Agents (`/next/agents`) | 79 | 163 | **32.60** | 543 | 108.60 | 0.30× | 45.00 | 108.00 |
| Workstreams (`/next/workstreams`) | 78 | 144 | **28.80** | 547 | 109.40 | 0.26× | 40.00 | 108.00 |
| Task detail (live run + transcript) (`/next/task/3?tab=runs`) | 80 | 283 | **56.60** | 702 | 140.40 | 0.40× | 57.00 | 140.00 |
| Sessions (live session + transcript) (`/next/sessions/director`) | 82 | 622 | **124.40** | 643 | 128.60 | 0.97× | 136.99 | 128.00 |
| Processes (`/next/processes`) | 87 | 273 | **54.60** | 937 | 187.40 | 0.29× | 64.99 | 187.99 |
| CI (`/next/ci`) | 78 | 245 | **49.00** | 621 | 124.20 | 0.39× | 49.00 | 124.00 |
| Repos (`/next/repos`) | 78 | 122 | **24.40** | 542 | 108.40 | 0.23× | 32.00 | 144.00 |
| Telemetry (`/next/telemetry`) | 79 | 123 | **24.60** | 542 | 108.40 | 0.23× | 27.00 | 108.00 |
| Context (`/next/context`) | 78 | 124 | **24.80** | 543 | 108.60 | 0.23× | 25.00 | 108.00 |
| Fleet (`/next/fleet`) | 79 | 124 | **24.80** | 614 | 122.80 | 0.20× | 35.00 | 127.00 |
| Settings (`/next/settings`) | 81 | 121 | **24.20** | 549 | 109.80 | 0.22× | 54.00 | 108.00 |

## Angular UI (`/next/`) — Per-Page Breakdown

### Inbox (`/next/inbox`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 103 | 20.60 | `services/task-feed.service.ts:204 (setTimeout) (97), inbox/inbox.store.ts:388 (setInterval) (5)` | 8 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 4 | 0.80 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **123** | **24.60** | — | **8** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 24 | 24.00 | `services/task-feed.service.ts:204 (setTimeout) (20), sessions/sessions.store.ts:233 (setInterval) (2)` | 5 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 5 | 5.00 | `sessions/sessions.store.ts:527 (setInterval) (4), chunk-7HNVSDKS.js:1 (1)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 4 | 4.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/console-sessions` | 3 | 3.00 | `sessions/sessions.store.ts:233 (setInterval) (2), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1)` | 0 |
| `GET /api/tasks/{id}` | 3 | 3.00 | `inbox/inbox-page.component.ts:362 (ɵɵlistener)` | 0 |
| `GET /api/config` | 2 | 2.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1), app.component.ts:1331 (setInterval) (1)` | 1 |
| `GET /api/repos/{owner}/{name}/github-branches` | 2 | 2.00 | `inbox/inbox-page.component.ts:362 (ɵɵlistener) (1), chunk-7HNVSDKS.js:1 (1)` | 0 |
| `GET /api/agent-models` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 1.00 | `shell/snackbar.component.ts:68 (jt)` | 0 |
| **Total** | **48** | **48.00** | — | **6** |

### Tasks (`/next/tasks`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 104 | 20.80 | `services/task-feed.service.ts:204 (setTimeout) (98), inbox/inbox.store.ts:388 (setInterval) (5)` | 9 |
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 99 | 19.80 | `task/task.store.ts:124 (setInterval)` | 0 |
| `GET /api/base-branch-gates` | 6 | 1.20 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/boards` | 6 | 1.20 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 4 | 0.80 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/tasks/summaries` | 1 | 0.20 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **236** | **47.20** | — | **9** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 22 | 22.00 | `task/task.store.ts:124 (setInterval) (20), tasks/tasks-page.component.ts:1401 (Object.hostBindings) (2)` | 1 |
| `GET /api/tasks` | 21 | 21.00 | `services/task-feed.service.ts:204 (setTimeout) (20), inbox/inbox.store.ts:388 (setInterval) (1)` | 2 |
| `GET /api/base-branch-gates` | 5 | 5.00 | `chunk-7HNVSDKS.js:1` | 4 |
| `GET /api/boards` | 5 | 5.00 | `chunk-7HNVSDKS.js:1` | 4 |
| `GET /api/config` | 5 | 5.00 | `tasks/tasks-page.component.ts:1401 (Object.hostBindings) (2), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (2)` | 4 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **61** | **60.99** | — | **15** |

### Agents (`/next/agents`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 103 | 20.60 | `services/task-feed.service.ts:204 (setTimeout) (98), inbox/inbox.store.ts:388 (setInterval) (5)` | 9 |
| `GET /api/sandboxes` | 20 | 4.00 | `agents/agents.store.ts:165 (setInterval)` | 0 |
| `GET /api/tasks/summaries` | 20 | 4.00 | `agents/agents.store.ts:165 (setInterval)` | 0 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 4 | 0.80 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **163** | **32.60** | — | **9** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 21 | 21.00 | `services/task-feed.service.ts:204 (setTimeout) (20), inbox/inbox.store.ts:388 (setInterval) (1)` | 2 |
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 9 | 9.00 | `task/task.store.ts:124 (setInterval) (7), agents/agents-page.component.ts:561 (ɵɵlistener) (2)` | 0 |
| `GET /api/config` | 4 | 4.00 | `agents/agents-page.component.ts:561 (ɵɵlistener) (2), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1)` | 3 |
| `GET /api/sandboxes` | 4 | 4.00 | `agents/agents.store.ts:165 (setInterval)` | 0 |
| `GET /api/tasks/summaries` | 4 | 4.00 | `agents/agents.store.ts:165 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **45** | **45.00** | — | **5** |

### Workstreams (`/next/workstreams`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 103 | 20.60 | `services/task-feed.service.ts:204 (setTimeout) (98), inbox/inbox.store.ts:388 (setInterval) (5)` | 6 |
| `GET /api/tasks?state=closed` | 20 | 4.00 | `workstreams/workstreams.store.ts:272 (setTimeout)` | 0 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 5 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **144** | **28.80** | — | **6** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 22 | 22.00 | `services/task-feed.service.ts:204 (setTimeout) (19), workstreams/workstreams-page.component.ts:569 (ɵɵlistener) (1)` | 3 |
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 8 | 8.00 | `task/task.store.ts:124 (setInterval) (7), workstreams/workstreams-page.component.ts:569 (ɵɵlistener) (1)` | 0 |
| `GET /api/config` | 3 | 3.00 | `workstreams/workstreams-page.component.ts:582 (ɵɵlistener) (1), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1)` | 2 |
| `GET /api/tasks?state=closed` | 3 | 3.00 | `workstreams/workstreams.store.ts:272 (setTimeout)` | 0 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/tasks/summaries` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **40** | **40.00** | — | **5** |

### Task detail (live run + transcript) (`/next/task/3?tab=runs`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 103 | 20.60 | `services/task-feed.service.ts:204 (setTimeout) (98), inbox/inbox.store.ts:388 (setInterval) (5)` | 10 |
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 99 | 19.80 | `task/task.store.ts:124 (setInterval)` | 0 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 60 | 12.00 | `task/task.store.ts:338 (setInterval)` | 0 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 5 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **283** | **56.60** | — | **10** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 21 | 21.00 | `services/task-feed.service.ts:204 (setTimeout) (20), inbox/inbox.store.ts:388 (setInterval) (1)` | 2 |
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 20 | 20.00 | `task/task.store.ts:124 (setInterval)` | 0 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 12 | 12.00 | `task/task.store.ts:338 (setInterval)` | 0 |
| `GET /api/config` | 1 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **57** | **57.00** | — | **2** |

### Sessions (live session + transcript) (`/next/sessions/director`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 203 | 40.60 | `sessions/sessions.store.ts:233 (setInterval) (99), services/task-feed.service.ts:204 (setTimeout) (98)` | 116 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 198 | 39.60 | `sessions/sessions.store.ts:527 (setInterval) (197), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 101 | 20.20 | `chunk-7HNVSDKS.js:1` | 2 |
| `GET /api/console-sessions` | 100 | 20.00 | `sessions/sessions.store.ts:233 (setInterval) (99), chunk-7HNVSDKS.js:1 (1)` | 1 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 4 | 0.80 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **622** | **124.40** | — | **119** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 43 | 43.00 | `sessions/sessions.store.ts:233 (setInterval) (20), services/task-feed.service.ts:204 (setTimeout) (20)` | 42 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 40 | 40.00 | `sessions/sessions.store.ts:527 (setInterval)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 26 | 26.00 | `chunk-7HNVSDKS.js:1` | 3 |
| `GET /api/console-sessions` | 22 | 22.00 | `sessions/sessions.store.ts:233 (setInterval) (20), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (2)` | 2 |
| `GET /api/config` | 3 | 3.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (2), app.component.ts:1331 (setInterval) (1)` | 2 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **137** | **136.99** | — | **49** |

### Processes (`/next/processes`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 103 | 20.60 | `services/task-feed.service.ts:204 (setTimeout) (98), inbox/inbox.store.ts:388 (setInterval) (5)` | 10 |
| `GET /api/declarations` | 30 | 6.00 | `processes/processes.store.ts:164 (setInterval)` | 0 |
| `GET /api/schedules` | 30 | 6.00 | `processes/processes.store.ts:164 (setInterval)` | 0 |
| `GET /api/suite-runs` | 30 | 6.00 | `processes/processes.store.ts:164 (setInterval)` | 0 |
| `GET /api/suites` | 30 | 6.00 | `processes/processes.store.ts:164 (setInterval)` | 0 |
| `GET /api/templates` | 30 | 6.00 | `processes/processes.store.ts:164 (setInterval)` | 0 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 4 | 0.80 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **273** | **54.60** | — | **10** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 21 | 21.00 | `services/task-feed.service.ts:204 (setTimeout) (20), inbox/inbox.store.ts:388 (setInterval) (1)` | 2 |
| `GET /api/declarations` | 6 | 6.00 | `processes/processes.store.ts:164 (setInterval) (5), shared/tip.directive.ts:85 (ɵɵlistener) (1)` | 0 |
| `GET /api/schedules` | 6 | 6.00 | `processes/processes.store.ts:164 (setInterval) (5), shared/tip.directive.ts:85 (ɵɵlistener) (1)` | 0 |
| `GET /api/suite-runs` | 6 | 6.00 | `processes/processes.store.ts:164 (setInterval) (5), shared/tip.directive.ts:85 (ɵɵlistener) (1)` | 0 |
| `GET /api/suites` | 6 | 6.00 | `processes/processes.store.ts:164 (setInterval) (5), shared/tip.directive.ts:85 (ɵɵlistener) (1)` | 0 |
| `GET /api/templates` | 6 | 6.00 | `processes/processes.store.ts:164 (setInterval) (5), shared/tip.directive.ts:85 (ɵɵlistener) (1)` | 0 |
| `GET /api/config` | 4 | 4.00 | `shared/tip.directive.ts:85 (ɵɵlistener) (2), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1)` | 3 |
| `GET /api/repos/{owner}/{name}/declarations` | 4 | 4.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/schedules/{id}/composition` | 1 | 1.00 | `shared/tip.directive.ts:85 (Object.hostBindings)` | 0 |
| `GET /api/suites/suite-1/composition` | 1 | 1.00 | `shared/tip.directive.ts:85 (Object.hostBindings)` | 0 |
| `GET /api/templates/{id}/composition` | 1 | 1.00 | `shared/tip.directive.ts:85 (ɵɵlistener)` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **65** | **64.99** | — | **5** |

### CI (`/next/ci`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 104 | 20.80 | `services/task-feed.service.ts:204 (setTimeout) (99), inbox/inbox.store.ts:388 (setInterval) (5)` | 10 |
| `GET /api/ci/faults` | 60 | 12.00 | `ci/ci.store.ts:112 (setInterval)` | 0 |
| `GET /api/ci/runs` | 60 | 12.00 | `ci/ci.store.ts:112 (setInterval)` | 0 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 5 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **245** | **49.00** | — | **10** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 21 | 21.00 | `services/task-feed.service.ts:204 (setTimeout) (20), inbox/inbox.store.ts:388 (setInterval) (1)` | 2 |
| `GET /api/ci/faults` | 12 | 12.00 | `ci/ci.store.ts:112 (setInterval)` | 0 |
| `GET /api/ci/runs` | 12 | 12.00 | `ci/ci.store.ts:112 (setInterval)` | 0 |
| `GET /api/config` | 1 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **49** | **49.00** | — | **2** |

### Repos (`/next/repos`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 103 | 20.60 | `services/task-feed.service.ts:204 (setTimeout) (98), inbox/inbox.store.ts:388 (setInterval) (5)` | 5 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 4 | 0.80 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **122** | **24.40** | — | **5** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 20 | 20.00 | `services/task-feed.service.ts:204 (setTimeout) (19), inbox/inbox.store.ts:388 (setInterval) (1)` | 2 |
| `GET /api/config` | 3 | 3.00 | `chunk-7HNVSDKS.js:1 (1), node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1)` | 2 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `repos/repo-detail-page.component.ts:2771 (then)` | 0 |
| `GET /api/deployment/git-connections` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/deployment/reachable-repos` | 1 | 1.00 | `shared/tip.directive.ts:85 (ɵɵlistener)` | 0 |
| `GET /api/deployment/repos/acme/widgets/config` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/repos/{owner}/{name}/lifecycle` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/repos/{owner}/{name}/settings` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **32** | **32.00** | — | **4** |

### Telemetry (`/next/telemetry`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 103 | 20.60 | `services/task-feed.service.ts:204 (setTimeout) (98), inbox/inbox.store.ts:388 (setInterval) (5)` | 5 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 4 | 0.80 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **123** | **24.60** | — | **5** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 21 | 21.00 | `services/task-feed.service.ts:204 (setTimeout) (20), inbox/inbox.store.ts:388 (setInterval) (1)` | 2 |
| `GET /api/config` | 2 | 2.00 | `telemetry/telemetry-page.component.ts:137 (ɵɵlistener) (1), app.component.ts:1331 (setInterval) (1)` | 1 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/metrics` | 1 | 1.00 | `telemetry/telemetry-page.component.ts:137 (ɵɵlistener)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **27** | **27.00** | — | **3** |

### Context (`/next/context`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 104 | 20.80 | `services/task-feed.service.ts:204 (setTimeout) (99), inbox/inbox.store.ts:388 (setInterval) (5)` | 10 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 4 | 0.80 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **124** | **24.80** | — | **10** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 21 | 21.00 | `services/task-feed.service.ts:204 (setTimeout) (20), inbox/inbox.store.ts:388 (setInterval) (1)` | 2 |
| `GET /api/config` | 1 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **25** | **25.00** | — | **2** |

### Fleet (`/next/fleet`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 103 | 20.60 | `services/task-feed.service.ts:204 (setTimeout) (98), inbox/inbox.store.ts:388 (setInterval) (5)` | 5 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 5 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /next/*.js (bundle)` | 1 | 0.20 | `/next/chunk-H7U22YSK.js` | 0 |
| **Total** | **124** | **24.80** | — | **5** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 21 | 21.00 | `services/task-feed.service.ts:204 (setTimeout) (20), inbox/inbox.store.ts:388 (setInterval) (1)` | 2 |
| `GET /api/config` | 5 | 5.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener) (4), app.component.ts:1331 (setInterval) (1)` | 4 |
| `GET /api/actions-cache` | 1 | 1.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener)` | 0 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/logs` | 1 | 1.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener)` | 0 |
| `GET /api/logs/{source}` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/package-cache` | 1 | 1.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener)` | 0 |
| `GET /api/pause` | 1 | 1.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener)` | 0 |
| `GET /api/pipeline-pause` | 1 | 1.00 | `fleet/fleet-page.component.ts:191 (ɵɵlistener)` | 0 |
| `GET /api/usage` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **35** | **35.00** | — | **6** |

### Settings (`/next/settings`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 103 | 20.60 | `services/task-feed.service.ts:204 (setTimeout) (98), inbox/inbox.store.ts:388 (setInterval) (5)` | 8 |
| `GET /api/config` | 5 | 1.00 | `app.component.ts:1331 (setInterval)` | 0 |
| `GET /api/local-script-approvals` | 5 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 5 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/usage` | 3 | 0.60 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **121** | **24.20** | — | **8** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/tasks` | 22 | 22.00 | `services/task-feed.service.ts:204 (setTimeout) (19), shared/tip.directive.ts:85 (Object.hostBindings) (1)` | 3 |
| `GET /api/tasks/{id}?comments_limit=100&attempts_limit=20` | 15 | 15.00 | `task/task.store.ts:124 (setInterval) (14), shared/tip.directive.ts:85 (Object.hostBindings) (1)` | 0 |
| `GET /api/config` | 4 | 4.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (3), app.component.ts:1331 (setInterval) (1)` | 3 |
| `GET /api/base-branch-gates` | 2 | 2.00 | `chunk-7HNVSDKS.js:1` | 1 |
| `GET /api/boards` | 2 | 2.00 | `chunk-7HNVSDKS.js:1` | 1 |
| `GET /api/usage` | 2 | 2.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl) (1), chunk-7HNVSDKS.js:1 (1)` | 0 |
| `GET /api/deployment` | 1 | 1.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl)` | 0 |
| `GET /api/deployment/git-connections` | 1 | 1.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl)` | 0 |
| `GET /api/github-tokens` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| `GET /api/local-script-approvals` | 1 | 1.00 | `inbox/inbox.store.ts:475 (then)` | 0 |
| `GET /api/notifications?open=1` | 1 | 1.00 | `inbox/inbox.store.ts:388 (setInterval)` | 0 |
| `GET /api/settings` | 1 | 1.00 | `node_modules/@angular/common/fesm2022/module-JS82OH2B.mjs:1617 (n.fetchImpl)` | 0 |
| `GET /api/tasks/summaries` | 1 | 1.00 | `chunk-7HNVSDKS.js:1` | 0 |
| **Total** | **54** | **54.00** | — | **8** |

## Classic UI (`/`) — Per-Page Breakdown

### Inbox (`/inbox`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/local-script-approvals` | 60 | 12.00 | `ui/src/components/InboxPage.jsx:102 (api)` | 59 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 2 | 0.40 | `ui/src/components/InboxPage.jsx:1669 (api)` | 0 |
| `GET /* (asset)` | 1 | 0.20 | `HTML parser` | 0 |
| `GET /api/capability-methods` | 1 | 0.20 | `ui/src/components/capabilityMethods.js:61 (api)` | 0 |
| **Total** | **604** | **120.80** | — | **78** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/local-script-approvals` | 12 | 12.00 | `ui/src/components/InboxPage.jsx:102 (api)` | 11 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **120** | **120.00** | — | **14** |

### Tasks (`/tasks`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 2 | 0.40 | `HTML parser` | 0 |
| `GET /api/capability-methods` | 1 | 0.20 | `ui/src/components/capabilityMethods.js:61 (api)` | 0 |
| **Total** | **543** | **108.60** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |

### Agents (`/tasks?state=running`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 2 | 0.40 | `HTML parser` | 0 |
| `GET /api/capability-methods` | 1 | 0.20 | `ui/src/components/capabilityMethods.js:61 (api)` | 0 |
| **Total** | **543** | **108.60** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |

### Workstreams (`/tasks`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 101 | 20.20 | `ui/src/App.jsx:1314 (api)` | 1 |
| `GET /api/config` | 101 | 20.20 | `ui/src/App.jsx:1616 (api)` | 1 |
| `GET /api/console-sessions` | 101 | 20.20 | `ui/src/App.jsx:1388 (api)` | 1 |
| `GET /api/tasks` | 101 | 20.20 | `ui/src/App.jsx:1151 (api)` | 1 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 2 | 0.40 | `HTML parser` | 0 |
| `GET /api/capability-methods` | 1 | 0.20 | `ui/src/components/capabilityMethods.js:61 (api)` | 0 |
| **Total** | **547** | **109.40** | — | **23** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |

### Task detail (live run + transcript) (`/tasks/3`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 100 | 20.00 | `ui/src/App.jsx:2890 (api)` | 6 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 60 | 12.00 | `ui/src/useAttemptTranscript.js:55 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 1 | 0.20 | `HTML parser` | 0 |
| `GET /api/capability-methods` | 1 | 0.20 | `ui/src/components/capabilityMethods.js:61 (api)` | 0 |
| **Total** | **702** | **140.40** | — | **25** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 20 | 20.00 | `ui/src/App.jsx:2890 (api)` | 0 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 12 | 12.00 | `ui/src/useAttemptTranscript.js:55 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **140** | **140.00** | — | **3** |

### Sessions (live session + transcript) (`/sessions/director`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 100 | 20.00 | `ui/src/App.jsx:1464 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 2 | 0.40 | `HTML parser` | 0 |
| `GET /api/tasks/{id}/tree` | 1 | 0.20 | `ui/src/components/detail/WorkTree.jsx:79 (api)` | 0 |
| **Total** | **643** | **128.60** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/tasks/{id}?comments_limit=50&attempts_limit=20` | 20 | 20.00 | `ui/src/App.jsx:1464 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **128** | **128.00** | — | **3** |

### Processes (`/processes`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/schedules` | 99 | 19.80 | `ui/src/App.jsx:1250 (api)` | 0 |
| `GET /api/suite-runs` | 99 | 19.80 | `ui/src/App.jsx:1292 (api)` | 0 |
| `GET /api/suites` | 99 | 19.80 | `ui/src/App.jsx:1278 (api)` | 0 |
| `GET /api/templates` | 99 | 19.80 | `ui/src/App.jsx:1264 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 1 | 0.20 | `HTML parser` | 0 |
| **Total** | **937** | **187.40** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/schedules` | 20 | 20.00 | `ui/src/App.jsx:1250 (api)` | 0 |
| `GET /api/suite-runs` | 20 | 20.00 | `ui/src/App.jsx:1292 (api)` | 0 |
| `GET /api/suites` | 20 | 20.00 | `ui/src/App.jsx:1278 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/templates` | 20 | 20.00 | `ui/src/App.jsx:1264 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **188** | **187.99** | — | **3** |

### CI (`/ci`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 160 | 32.00 | `ui/src/App.jsx:1314 (api) (100), ui/src/components/CIPage.jsx:1938 (api) (60)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/ci/cache` | 10 | 2.00 | `ui/src/components/CIPage.jsx:1979 (api)` | 9 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/sandboxes` | 10 | 2.00 | `ui/src/components/StaleSchedulesAlert.jsx:59 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 1 | 0.20 | `HTML parser` | 0 |
| **Total** | **621** | **124.20** | — | **28** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 32 | 32.00 | `ui/src/App.jsx:1314 (api) (20), ui/src/components/CIPage.jsx:1938 (api) (12)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/ci/cache` | 2 | 2.00 | `ui/src/components/CIPage.jsx:1979 (api)` | 1 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/sandboxes` | 2 | 2.00 | `ui/src/components/StaleSchedulesAlert.jsx:59 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **124** | **124.00** | — | **4** |

### Repos (`/repos`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 1 | 0.20 | `HTML parser` | 0 |
| `GET /api/deployment/reachable-repos` | 1 | 0.20 | `ui/src/components/RepoList.jsx:323 (api)` | 0 |
| **Total** | **542** | **108.40** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 32 | 32.00 | `ui/src/App.jsx:1314 (api) (20), ui/src/components/RepoRuns.jsx:59 (api) (12)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/merge-batches` | 12 | 12.00 | `ui/src/components/MergeBatchPanel.jsx:284 (api)` | 11 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/capability-methods` | 2 | 2.00 | `ui/src/components/capabilityMethods.js:61 (api)` | 0 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/branches` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:635 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/capabilities` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:652 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/declarations` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:689 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/prompt-extension` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:664 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/release` | 1 | 1.00 | `ui/src/components/RepoReleaseWorkflowSettings.jsx:141 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/require-branch-up-to-date` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:697 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/setup-command` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:679 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/unfixable-checks` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:705 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/work-ownership` | 1 | 1.00 | `ui/src/components/RepoWorkOwnership.jsx:74 (api)` | 0 |
| `GET /api/repos/{owner}/{name}/workflows` | 1 | 1.00 | `ui/src/components/RepoPage.jsx:713 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **144** | **144.00** | — | **14** |

### Telemetry (`/metrics`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/ci/runs` | 99 | 19.80 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 99 | 19.80 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 99 | 19.80 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/tasks` | 99 | 19.80 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 1 | 0.20 | `HTML parser` | 0 |
| `GET /api/metrics` | 1 | 0.20 | `ui/src/components/ReportBoard.jsx:239 (api)` | 0 |
| `GET /api/release-notes` | 1 | 0.20 | `ui/src/components/ReleaseNotesPanel.jsx:95 (api)` | 0 |
| `GET /api/release-notes/2026-W46` | 1 | 0.20 | `ui/src/components/ReleaseNotesPanel.jsx:128 (api)` | 0 |
| `GET /api/release-notes/stats` | 1 | 0.20 | `ui/src/components/ReleaseNotesPanel.jsx:119 (api)` | 0 |
| `GET /api/release-notes/versions` | 1 | 0.20 | `ui/src/components/ReleaseVersions.jsx:57 (api)` | 0 |
| **Total** | **542** | **108.40** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |

### Context (`/settings/memory`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 1 | 0.20 | `HTML parser` | 0 |
| `GET /api/memory` | 1 | 0.20 | `ui/src/components/MemoryPanel.jsx:131 (api)` | 0 |
| `GET /api/memory/documents` | 1 | 0.20 | `ui/src/components/MemoryPanel.jsx:132 (api)` | 0 |
| **Total** | **543** | **108.60** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |

### Fleet (`/system`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 100 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 100 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 100 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 100 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/sandboxes` | 40 | 8.00 | `ui/src/components/system/HealthCountersPanel.jsx:43 (api) (30), ui/src/components/StaleSchedulesAlert.jsx:59 (api) (10)` | 0 |
| `GET /api/metrics` | 30 | 6.00 | `ui/src/components/system/GitHubBudgetPanel.jsx:49 (api)` | 29 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 1 | 0.20 | `HTML parser` | 0 |
| `GET /api/deployment/reachable-repos` | 1 | 0.20 | `ui/src/components/system/HealthTab.jsx:68 (api)` | 0 |
| `GET /api/settings` | 1 | 0.20 | `ui/src/components/system/HealthTab.jsx:61 (api)` | 0 |
| `GET /api/settings/cli-run` | 1 | 0.20 | `ui/src/components/system/HealthTab.jsx:64 (api)` | 0 |
| **Total** | **614** | **122.80** | — | **48** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/sandboxes` | 7 | 7.00 | `ui/src/components/system/HealthCountersPanel.jsx:43 (api) (4), ui/src/components/SandboxHealthPage.jsx:1634 (api) (2)` | 0 |
| `GET /api/metrics` | 4 | 4.00 | `ui/src/components/system/GitHubBudgetPanel.jsx:49 (api)` | 3 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/host/top` | 2 | 2.00 | `ui/src/components/TopPage.jsx:72 (api)` | 0 |
| `GET /api/logs/{source}` | 2 | 2.00 | `ui/src/components/LogsPage.jsx:104 (api)` | 0 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/deployment/reachable-repos` | 1 | 1.00 | `ui/src/components/system/HealthTab.jsx:68 (api)` | 0 |
| `GET /api/logs` | 1 | 1.00 | `ui/src/components/LogsPage.jsx:85 (api)` | 0 |
| `GET /api/settings` | 1 | 1.00 | `ui/src/components/system/HealthTab.jsx:61 (api)` | 0 |
| `GET /api/settings/cli-run` | 1 | 1.00 | `ui/src/components/system/HealthTab.jsx:64 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **127** | **127.00** | — | **6** |

### Settings (`/settings`)

**Steady-state idle (5m idle):**

| Route pattern | Count (5m idle) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 101 | 20.20 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 101 | 20.20 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 101 | 20.20 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/tasks` | 101 | 20.20 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/restart-watch` | 100 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 20 | 4.00 | `ui/src/App.jsx:3859 (api)` | 19 |
| `GET /api/notifications?open=1` | 10 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 5 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 5 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| `GET /* (asset)` | 1 | 0.20 | `HTML parser` | 0 |
| `GET /api/deployment` | 1 | 0.20 | `ui/src/useDeployment.js:43 (api)` | 0 |
| `GET /api/settings` | 1 | 0.20 | `ui/src/components/settings/ModelsSection.jsx:77 (api)` | 0 |
| `GET /api/settings/cli-run` | 1 | 0.20 | `ui/src/components/settings/ModelsSection.jsx:78 (api)` | 0 |
| `GET /api/usage` | 1 | 0.20 | `ui/src/components/settings/UsageCard.jsx:77 (api)` | 0 |
| **Total** | **549** | **109.80** | — | **19** |

**Scripted normal use (1m active):**

| Route pattern | Count (1m active) | Req / min | Component / service (initiator) | Duplicates |
|---|---:|---:|---|---:|
| `GET /api/ci/runs` | 20 | 20.00 | `ui/src/App.jsx:1314 (api)` | 0 |
| `GET /api/config` | 20 | 20.00 | `ui/src/App.jsx:1616 (api)` | 0 |
| `GET /api/console-sessions` | 20 | 20.00 | `ui/src/App.jsx:1388 (api)` | 0 |
| `GET /api/restart-watch` | 20 | 20.00 | `ui/src/App.jsx:3006 (api)` | 0 |
| `GET /api/tasks` | 20 | 20.00 | `ui/src/App.jsx:1151 (api)` | 0 |
| `GET /api/persistent-sandboxes` | 4 | 4.00 | `ui/src/App.jsx:3859 (api)` | 3 |
| `GET /api/notifications?open=1` | 2 | 2.00 | `ui/src/notifications.js:50 (api)` | 0 |
| `GET /api/base-branch-gates` | 1 | 1.00 | `ui/src/App.jsx:1325 (api)` | 0 |
| `GET /api/tasks?state=closed` | 1 | 1.00 | `ui/src/closedTasks.js:289 (api)` | 0 |
| **Total** | **108** | **108.00** | — | **3** |
