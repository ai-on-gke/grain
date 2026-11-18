# Walkthrough: Task List Cost and `GET /api/tasks/summaries` (2026-11-16 §2)

- **Date:** 2026-11-17, 00:55–02:00 UTC.
- **Task:** [/tasks/6475](/tasks/6475) (filed by [/tasks/6330](/tasks/6330)),
  branch `grain/53549e42/task-6475-walkthrough-task-list-cost-and`, base `main`.
- **Plan section:** `docs/test-plans/2026-11-16.md` §2 ("Task list cost and the
  batch task summaries endpoint", plus §10 CI API token isolation on
  `GET /api/tasks/summaries`).
- **Base commit walked:** `a771a9973` ("Merge pull request #1705"). Every bug
  found was reproduced deterministically against `a771a9973`, fixed with
  regression tests, and re-verified on a rebuilt binary over both PostgreSQL
  and SQLite.
- **Feature commits under test:**
  - PR #1542 (`e32d05d66`, `4a50de3a8`, `43c3b0402`): batched child-table
    hydration in `hydrateTasks`, open-only `ListOpenTasks`, and the
    whole-list caching rule in `taskListCache`.
  - PR #1620 (`7bda5290d`, `dd537d292`, `58d07cd08`):
    `GET /api/tasks/summaries?ids=…` (≤50 unique IDs, silent identical omission
    of unreadable/missing IDs, `?repo=` rejection, bounded default limits,
    ETag/304) and `/next/` stores moved off per-task fan-out.

## Summary of Outcomes

| Plan Item | Area | PostgreSQL (base) | SQLite (base) | Post-Fix Status |
| --- | --- | --- | --- | --- |
| 1 | `GET /api/tasks` cost at 5,000 tasks (cold, 304, after-write, filtered, closed tab) | **PASS** (`0.91ms` 304, `88.35ms` rebuild) — **Bug 1 & Bug 2 found** | **PASS** (`0.75ms` 304, `139.93ms` rebuild) — **Bug 1 & Bug 2 found** | **PASS** (`0.89ms` / `0.95ms` list 304; 50-ID summaries 304 `391.56ms → 2.18ms` PG, `176.48ms → 2.08ms` SQLite) |
| 2 | Open-row parity vs `?fields=full` + close/reopen transition loop | **PASS** (266/266 exact match, 6/6 transitions) | **PASS** (266/266 exact match, 6/6 transitions) | **PASS** |
| 3 | `GET /api/tasks/summaries` `curl` & CI API token probes across all roles | **PASS** (0 leaks, identical omission, 400 validation, ETag invalidation) | **PASS** (0 leaks, identical omission, 400 validation, ETag invalidation) | **PASS** (plus warm-cache authz pinned) |
| 4 | Playwright request logging on `/next/inbox`, `/next/agents`, `/next/tasks` (incl. Workstreams) + failing-chunk injection | **PASS** (bounded chunks, cached rows survive 500, 0 page errors) — **Bug 3 found in `GrainApi.taskSummaries`** | **PASS** (bounded chunks, cached rows survive 500, 0 page errors) — **Bug 3 found** | **PASS** |
| 5 | Classic `/` parity spot-check | **PASS** (265/265 open work tasks match, 0 missing in DOM) | **PASS** (265/265 open work tasks match, 0 missing in DOM) | **PASS** |

Three real bugs were found, reproduced deterministically, fixed, and pinned
with regression tests:

1. **Bug 1 (`1e15e12b2`) — `taskListCache.list` ignored `dispatchConditionVersion`.**
   Filtered `GET /api/tasks?state=...` served stale `queued/ready` reasons after
   a dispatch condition changed without a DB write, and whenever
   `dispatchConditionVersion > 0`, alternating live `GET /api/tasks` polls with
   closed-tab or filtered reads wiped `taskListCache` on every request.
2. **Bug 2 (`f896b9e24`) — `GET /api/tasks/summaries` thrashed `taskDetailCache` on 50-ID batches and ran per-ID DB reads on warm polls.**
   `taskDetailCache` shared one 32-entry LRU across full panes and paged
   summaries, while `handleTaskSummaries` called `Store.GetTask` and
   `Store.ChangeToken` per ID before checking the cache. Quiet 50-ID 304 polls
   (the exact chunk size `/next/agents` and `/next/tasks` poll) rebuilt all 50
   tasks on every poll (**391.56ms p50 on PostgreSQL**, **176.48ms p50 on
   SQLite** — slower than cold misses!) and evicted open task panes. Splitting
   the LRU (32 unpaged panes + 256 paged summaries), caching `entry.repo` for
   per-caller visibility checks, and reading `ChangeToken` once per request
   brought 50-ID quiet 304 polls down to **2.18ms p50 on PostgreSQL (180×
   faster)** and **2.08ms p50 on SQLite (85× faster)**.
3. **Bug 3 (`d58a4095c`) — `GrainApi.taskSummaries` used `Promise.all` instead of `Promise.allSettled`.**
   When fetching >50 IDs in 50-ID chunks, one failing chunk discarded all
   fulfilled chunks and rejected the call instead of preserving succeeded
   chunks (unlike `loadTaskSummaryBatches`).

---

## Environment

- **Host:** Linux `6.1.0-55-amd64` (Debian 12 sandbox), `x86_64`, 7 CPUs, UTC.
- **Toolchains:**
  - Go `go1.26.10 linux/amd64`
  - PostgreSQL `15.20 (Debian 15.20-0+deb12u1)` (installed via `apt-get install -y postgresql`; tests run with `GRAIN_REQUIRE_POSTGRES=1`)
  - SQLite `3.40.1`
  - Node.js `v20.19.0`, npm `10.8.2`
  - Playwright `1.62.1` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, headless Chromium)
- **Build:**
  - Base build from `a771a9973`: `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` (followed by `cd ui-ng && npm ci` to restore `@playwright/test`).
  - Post-fix build from `d58a4095c`: `cd ui-ng && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain`.
- **Daemon flags (both backends):**
  - Shared: `-ui-addr 127.0.0.1:4290 -sandbox-port-base=0 -host-sandboxes -allow-unisolated-host-sandboxes -max-workers 1 -ci=false -as admin@example.com`
  - Verified IAP JWT auth: `-ui-iap-audience /projects/424242/global/backendServices/1 -ui-iap-issuer https://iap.example.com -ui-iap-keys-url http://127.0.0.1:4291/jwks` (backed by a local ES256 JWKS server `jwks_signer.go` on `127.0.0.1:4291`).
  - PostgreSQL run: `-data-dir /home/debian/walk/pg-data -sandbox-dir /home/debian/walk/pg-sandboxes -postgres-url postgres://grain:grain@127.0.0.1:5432/grain_listcost?sslmode=disable`
  - SQLite run: `-data-dir /home/debian/walk/listcost-sqlite/data -sandbox-dir /home/debian/walk/sqlite-sandboxes` (DB at `<data-dir>/store/grain.db`).
  - Immediately after startup, `POST /api/pipeline-pause` was called so the daemon would not dispatch seeded tasks against fake remotes, and `seed.go -start-live-runs` restored the 65 live `running` runs (since daemon startup `RecoverOrphanedRuns` reaps pre-seeded runs).

## Seeded Dataset & Walkthrough Helpers

All reusable helpers live under `docs/test-runs/2026-11-16-task-list-cost/`:

| Helper | Purpose |
| --- | --- |
| `seed.go` | Seeds 5,001 tasks into PostgreSQL or SQLite (`-postgres-url` or `-sqlite-dir`) plus `-start-live-runs` to reinstate the 65 live runs after daemon startup. |
| `jwks_signer.go` | Serves a local ES256 JWKS endpoint and writes signed IAP JWTs for `admin@example.com`, `limited@example.com`, and `ci-only@example.com`. |
| `walk_items1_2.py` | Measures cold, quiet-304, after-write, filtered, `?fields=full`, closed-tab, and 20-/50-ID summary latencies; diffs all open rows against `?fields=full`; runs the 6-task close/reopen transition loop. |
| `probe_summaries.sh` | Runs `curl` probes on `GET /api/tasks/summaries` across admin IAP, local bearer token, repo-scoped human IAP, CI-only human IAP, `gma_` external agent tokens, and acting agent (`X-Grain-Acting-Task: 5000`). |
| `probe_ciapi.go` | Exercises `GET /api/tasks/summaries` through a real `ciapi.Endpoint` + `ui.Server` against the seeded PostgreSQL or SQLite store using minted CI API tokens (§10). |
| `walk_playwright.mjs` | Drives headless Chromium across `/next/inbox`, `/next/agents`, `/next/tasks` (incl. `Submit & land` and `?group=workstream`), `/next/workstreams` (`/task:300`), injects chunk 500s via `page.route`, and spot-checks classic `/` parity. |

**Seeded 5,001-task distribution (realistic mix across `acme/widgets`, `acme/secret`, `acme/gadgets`, and repo-less tasks):**
- **266 open tasks:**
  - `queued`: 50 (including blocked-by-dependency chains e.g. task `23` blocked by `25`, base-branch repair tasks `31–35`, and multi-repo `task_write` targets)
  - `proposed`: 20
  - `running`: 65 (live runs + multiple attempts + comments + `task_link` workstream roots)
  - `deferred`: 20 (with `not_before` timestamps + deferral comments)
  - `awaiting_reply`: 36 (35 work tasks incl. rolled-over / ask-question states + 1 Director console session `5001`)
  - `awaiting_submit`: 65 (merge-queue / PR links, security holds, review links, multi-attempt histories)
  - `failed`: 10
- **4,735 closed tasks:** spanning merged/closed/cancelled work, including heavy child-table fixtures on tasks `4997` (`acme/widgets`), `4998` (`acme/secret`), and `4999` (no-repo), each with 25+ comments and 12+ attempts to test default limit clamping (`comments_limit=20`, `attempts_limit=10`), plus acting-agent task `5000` granted `api:tasks:read@acme/widgets`.
- **RBAC / scope sets:** `custom/widgets-read` (`tasks:read` scoped to `acme/widgets`), `custom/ci-only` (`ci:read` without `tasks:read`), bound to `limited@example.com` and `ci-only@example.com` respectively.

*(Note: On daemon restarts before `POST /api/pipeline-pause` landed, 2 queued tasks attempted dispatch against non-existent remotes and transitioned `queued → failed`, giving `queued=48, failed=12` while keeping total open tasks at 266 and open work tasks at 265.)*

---

## Item 1: `GET /api/tasks` Cost at 5,000 Tasks (PostgreSQL & SQLite)

### Measured HTTP Timings Against Live `grain daemon` (5,001 tasks: 266 open, 4,735 closed)

#### PostgreSQL 15.20

| Endpoint / Scenario | Base (`a771a9973`) | Post-Fix (`d58a4095c`) | Notes |
| --- | --- | --- | --- |
| Cold `GET /api/tasks` (266 open rows, ~345 KB) | `113.34ms` | `115.22ms` | Open-only `ListOpenTasks` + batched `hydrateTasks` |
| Quiet 304 `GET /api/tasks` (`If-None-Match`, n=10) | min `0.75ms`, **p50 `0.91ms`**, max `1.29ms` | min `0.78ms`, **p50 `0.89ms`**, max `1.15ms` | Sub-millisecond `ChangeToken` hit |
| After-write rebuild `GET /api/tasks` (n=5) | min `83.83ms`, **p50 `88.35ms`**, max `130.09ms` | min `87.33ms`, **p50 `93.14ms`**, max `100.64ms` | ~5.5× cheaper than `?fields=full` |
| After-write `?state=queued,proposed,running&limit=1` (n=5) | min `73.95ms`, **p50 `86.17ms`**, max `96.73ms` | min `83.88ms`, **p50 `86.74ms`**, max `115.32ms` | Open-only read (does not populate whole-list cache) |
| Cold `GET /api/tasks?fields=full` (5,001 rows, ~6.62 MB) | `521.02ms` | `485.59ms` | Whole-list hydration |
| After-write `GET /api/tasks?fields=full` (n=3) | min `456.76ms`, **p50 `502.23ms`**, max `600.01ms` | min `501.87ms`, **p50 `520.20ms`**, max `594.89ms` | Expected whole-DB rebuild |
| `GET /api/tasks?state=closed&cursor=&limit=50` (4,735 closed) | cold `347.91ms` / warm `44.35ms` | cold `477.04ms` / warm `34.12ms` | Warm hit served from whole-list cache |
| `GET /api/tasks/summaries` (20 IDs, `cl=1&al=1`) | cold `197.53ms` / **quiet 304 p50 `33.27ms`** (`32.37–37.46ms`) | cold `228.50ms` / **quiet 304 p50 `1.75ms`** (`1.41–2.27ms`) | **19× faster after Bug 2 fix** |
| `GET /api/tasks/summaries` (50 IDs, `cl=1&al=1`) | cold `227.77ms` / **quiet 304 p50 `391.56ms`** (`365.63–423.54ms`) | cold `370.46ms` / **quiet 304 p50 `2.18ms`** (`1.97–2.31ms`) | **180× faster after Bug 2 fix** (was 1.7× slower than cold!) |

#### SQLite 3.40.1

| Endpoint / Scenario | Base (`a771a9973`) | Post-Fix (`d58a4095c`) | Notes |
| --- | --- | --- | --- |
| Cold `GET /api/tasks` (266 open rows, ~345–358 KB) | `3.03ms` *(warmed by startup poll)* | `241.81ms` *(true cold)* | Open-only `ListOpenTasks` |
| Quiet 304 `GET /api/tasks` (`If-None-Match`, n=10) | min `0.64ms`, **p50 `0.75ms`**, max `1.02ms` | min `0.71ms`, **p50 `0.95ms`**, max `1.48ms` | Sub-millisecond `PRAGMA data_version` hit |
| After-write rebuild `GET /api/tasks` (n=5) | min `136.89ms`, **p50 `139.93ms`**, max `190.18ms` | min `148.33ms`, **p50 `195.84ms`**, max `224.83ms` | ~3–5× cheaper than `?fields=full` |
| After-write `?state=queued,proposed,running&limit=1` (n=5) | min `133.12ms`, **p50 `136.44ms`**, max `140.98ms` | min `136.70ms`, **p50 `149.47ms`**, max `189.31ms` | Open-only read |
| Cold `GET /api/tasks?fields=full` (5,001 rows, ~6.62 MB) | `907.10ms` | `935.09ms` | Whole-list hydration |
| After-write `GET /api/tasks?fields=full` (n=3) | min `591.47ms`, **p50 `680.71ms`**, max `710.63ms` | min `565.62ms`, **p50 `595.27ms`**, max `661.85ms` | Expected whole-DB rebuild |
| `GET /api/tasks?state=closed&cursor=&limit=50` (4,735 closed) | cold `642.50ms` / warm `15.77ms` | cold `718.53ms` / warm `24.43ms` | Warm hit served from whole-list cache |
| `GET /api/tasks/summaries` (20 IDs, `cl=1&al=1`) | cold `90.63ms` / **quiet 304 p50 `12.79ms`** (`11.42–17.41ms`) | cold `88.51ms` / **quiet 304 p50 `1.37ms`** (`1.16–2.74ms`) | **9.3× faster after Bug 2 fix** |
| `GET /api/tasks/summaries` (50 IDs, `cl=1&al=1`) | cold `135.37ms` / **quiet 304 p50 `176.48ms`** (`169.05–238.92ms`) | cold `123.61ms` / **quiet 304 p50 `2.08ms`** (`1.76–2.55ms`) | **85× faster after Bug 2 fix** (was 1.3× slower than cold!) |

Additionally, `GRAIN_REQUIRE_POSTGRES=1 go test -v -race ./pkg/ui -run TestTaskListOnPostgresIsCheapAtSixThousandTasks` passed (under `-race`):
- Cold build (6,000 tasks): `302.43ms`
- Quiet poll p50: `416.52µs`
- After-write rebuild p50: `185.76ms`
- `?state=...&limit=1` after-write p50: `169.46ms`

---

## Item 2: Open-Row Parity vs `?fields=full` & Close/Reopen Transitions

Ran `walk_items1_2.py` on both PostgreSQL and SQLite (before and after fixes):

1. **Initial parity diff:** Compared every open row returned by `GET /api/tasks`
   against the open subset of `GET /api/tasks?fields=full` across all fields
   (state, priority, reason, dependencies, blocked flag, paused object, PR/merge
   metadata, links, writes, capabilities, grants, timestamps).
   - **Result:** 266/266 rows matched 100% identically on both PostgreSQL and SQLite.
2. **Close/reopen transition loop:** Cycled 6 representative open tasks through
   `POST /api/tasks/{id}/close` followed by `POST /api/tasks/{id}/reopen`,
   re-verifying `GET /api/tasks` (open set), `GET /api/tasks?state=closed&cursor=&limit=50`
   (closed tab), and `GET /api/tasks?fields=full` after every transition:
   - Task `40` (queued), Task `10` (queued/proposed), Task `140` (deferred),
     Task `200` (awaiting_reply), Task `131` (awaiting_submit), and Task `25`
     (blocker of dependent Task `23`, confirming closing `25` unblocked `23`
     and reopening `25` re-blocked `23` consistently across open and full views).
   - **Result:** 100% PASS on both PostgreSQL and SQLite (before and after fixes).

---

## Item 3 (+ §10): `GET /api/tasks/summaries` Authorization, Validation, Omission & ETag Probes

Ran `probe_summaries.sh` (live `grain daemon` via `curl`) and `probe_ciapi.go`
(real `ciapi.Endpoint` + `ui.Server` against the seeded store) on both
PostgreSQL and SQLite (before and after fixes):

| Principal | Auth Mechanism | Mixed `4997,4998,4999,999999` | Foreign (`4998`) / No-repo (`4999`) / Missing (`999999`) Omission | Validation (50+dup / 51 / 14 malformed / `?repo=`) | ETag / 304 & Invalidation |
| --- | --- | --- | --- | --- | --- |
| `admin-iap` (`admin@example.com`) | Verified ES256 IAP JWT | 200: `['4997', '4998', '4999']`; default bounds `comments=20 (+older)`, `attempts=10` | N/A (unrestricted) | 50+dup=200, 51=400, 14 malformed=400, 3 `?repo=`=400 | ETag → 304 → comment on `4997` → 200 new ETag → 304 |
| `local-token` | Bearer `local-api-token` | 200: `['4997', '4998', '4999']` | N/A (unrestricted) | Same: all PASS | Same: all PASS |
| `limited-iap` (`limited@example.com`) | Verified ES256 IAP JWT (`custom/widgets-read`) | 200: `['4997']` only | Byte-identical `200 {"tasks":{}}` + `ETag: "932fc0054affdd5055de0f6d7c810af8"` | Same: all PASS | Same: all PASS |
| `gma-widgets` | Bearer `gma_…` (`custom/widgets-read`) | 200: `['4997']` only | Byte-identical `200 {"tasks":{}}` + `ETag: "932fc0054affdd5055de0f6d7c810af8"` | Same: all PASS | Same: all PASS |
| `acting-agent-5k` | `X-Grain-Acting-Task: 5000` (`api:tasks:read@acme/widgets`) | 200: `['4997']` only | Byte-identical `200 {"tasks":{}}` + `ETag: "932fc0054affdd5055de0f6d7c810af8"` | Same: all PASS | Same: all PASS |
| `ci-only-iap` (`ci-only@example.com`) | Verified ES256 IAP JWT (`custom/ci-only`) | **403 Forbidden** (`Cache-Control: no-store`) | Refused at gate | Refused at gate | Refused at gate |
| `gma-ci-only` | Bearer `gma_…` (`custom/ci-only`) | **403 Forbidden** (`Cache-Control: no-store`) | Refused at gate | Refused at gate | Refused at gate |
| `ci-api` (`acme/widgets`, `tasks:read`) | CI API bearer token via `ciapi.Endpoint` | 200: `['4997']` only | Byte-identical `200 {"tasks":{}}` + `ETag: "932fc0054affdd5055de0f6d7c810af8"` | 50+dup=200, 51=400, 7 malformed=400, foreign `?repo=`=403, own `?repo=`=200 | ETag unchanged on foreign `4998` write; changes on readable `4997` write |
| `ci-api` (`acme/widgets`, `ci:read`) | CI API bearer token via `ciapi.Endpoint` | **403 Forbidden** | Refused at gate | Refused at gate | Refused at gate |

**Malformed queries checked (all 400):** missing `ids`, empty `ids=`, whitespace/empty segments (`ids=,,`, `ids=1,,2`), invalid ID chars (`../1`, `1/2`, `foo bar`), negative/zero/non-numeric `comments_limit` & `attempts_limit`, `comments_before` / `attempts_before` rejected on batch summaries, and `51` distinct IDs.

**Observation on CI proxy (`ciapi.Route`):** `ciapi.Route` forwards the response `ETag` cleanly (and the ETag changes on readable writes while remaining unchanged on foreign writes), but does not forward the `If-None-Match` request header to the upstream handler (so conditional GETs through the CI proxy return `200` rather than `304`). Not a security issue; noted for completeness.

---

## Item 4 & Item 5: Playwright Request Logging, Chunk Failure Injection & Classic `/` Parity

Ran `walk_playwright.mjs` in headless Chromium against both PostgreSQL (base and
post-fix) and SQLite (base):

### Request Logging on `/next/` Views (265 open work tasks, 65 running, 65 awaiting submit)

| Route | Visible State | `GET /api/tasks/summaries` Calls | Single `GET /api/tasks/{id}` Calls | Page Errors |
| --- | --- | --- | --- | --- |
| `/next/inbox` | 140–142 queue rows | **2** (`20ids(cl=1,al=1):200`, `20ids(cl=1,al=1):200`) | **1** (auto-selected inspector item `#170`) | 0 |
| `/next/agents` | 65 running agent cards | **2** (`50ids(cl=1,al=3):200`, `15ids(cl=1,al=3):200`) | **0** | 0 |
| `/next/tasks` | 265 open work tasks (`Backlog`, `Needs approval 20`, `Running 65`, `Asked`, `Submit & land 65`, `Deferred 20`, `All 265`) | **2** (`50ids(cl=1,al=1):200`, `15ids(cl=1,al=1):200`) | **1** (auto-selected `#32`) | 0 |
| `/next/tasks?group=workstream` | Workstreams grouping active | **2** (`50ids(cl=1,al=1):200`, `15ids(cl=1,al=1):200`) | **1** (auto-selected `#32`) | 0 |
| `/next/workstreams` → `/next/workstreams/task:300` | 60 streams rendered; opening `task:300` hydrates root | **1** (`1ids(cl=1,al=1):200` for root `300`) | **1** (`#1`) | 0 |

Zero per-task fan-out across all `/next/` stores: 65 running agents hydrated in
2 batched calls (`50 + 15`) with 0 single-task calls; 65 awaiting-submit PRs
hydrated in 2 batched calls (`50 + 15`).

### Failing-Chunk Injection (`page.route` HTTP 500)

- **`/next/agents` cold chunk-2 500:** `okChunks=1, failedChunks=1`, all 65 running cards rendered, 0 page errors.
- **`/next/agents` warm poll 500:** `intercepted500=2`, visible running titles stayed `65 → 65`, 0 page errors.
- **`/next/tasks` (`Submit & land` tab) cold chunk-2 500:** `okChunks=1, failedChunks=1`, all 65 awaiting-submit rows stayed visible, 0 page errors.
- **`/next/inbox` warm summaries 500:** `intercepted500=1`, all `140–142` queue rows stayed visible, 0 page errors.

### Item 5: Classic `/` Parity Spot-Check

- Polled open work tasks on classic `/` vs `/next/tasks`: `265 == 265` (exact ID match).
- Rendered classic `/` DOM task links: `465` (`265` open + `200` closed page), `missingOpenInClassicDOM=0`, `pageErrors=0`.

---

## Bugs Found, Reproductions & Fixes

### Bug 1: `taskListCache.list` ignored `dispatchConditionVersion`

- **Where:** `pkg/ui/task_list_filter.go` (`taskListCache.list`) vs `pkg/ui/task_list_cache.go` (`taskListCache.response`).
- **Symptom & Root Cause:**
  `taskListCache.response` appended `:dc=<dispatchConditionVersion>` to its
  version token so dispatch-condition transitions (which do not write to the
  database) invalidate cached queued reasons. However, `taskListCache.list`
  (used by filtered `GET /api/tasks?state=...` and cursor-paged closed-tab
  reads) compared only the raw `Store.ChangeToken(ctx)`.
  This caused two defects:
  1. **Stale filtered responses:** Once the whole-list cache was primed (e.g.
     by `?fields=summary` or opening the Closed tab), activating or clearing a
     dispatch condition left `GET /api/tasks?state=queued` serving stale
     `Reason = "queued/ready"` until an unrelated DB write occurred.
  2. **Cache thrashing once any dispatch condition has ever fired:** Whenever
     `dispatchConditionVersion() > 0` (which persists for the daemon's lifetime
     after any condition fires), `response` stored `c.token = "<ct>:dc=<v>"`
     while `list` stored `c.token = "<ct>"`, so alternating live
     `GET /api/tasks` polls with closed-tab or filtered reads wiped
     `taskListCache` and forced a full DB rebuild on every request.
- **Deterministic Reproduction:**
  `TestTaskListCacheFilteredInvalidatesAndDoesNotThrashOnDispatchCondition` in
  `pkg/ui/task_list_cache_test.go`. Against `a771a9973` it failed immediately on
  both assertions: filtered `Reason` stayed `"queued/ready"` after `set.Block`,
  and 8 alternating live/closed-tab polls triggered **8 rebuilds** (want `0`).
- **Fix Commit:** `1e15e12b2` (`fix(ui): fold dispatchConditionVersion into filtered taskListCache token`).
  Extracted shared `(c *taskListCache) cacheToken(ctx, client)` used by both
  `response` and `list`.

### Bug 2: `GET /api/tasks/summaries` thrashed the 32-entry detail cache and queried the DB per ID on warm polls

- **Where:** `pkg/ui/task_detail_cache.go` and `pkg/ui/tasks.go` (`handleTaskSummaries`).
- **Symptom & Root Cause:**
  Measured directly in Item 1 on the 5,000-task database:
  - PostgreSQL quiet 304 on 20-ID summaries: **33.27ms p50** (vs `0.91ms` for `GET /api/tasks`).
  - PostgreSQL quiet 304 on 50-ID summaries: **391.56ms p50** — **1.7× slower than the 227.77ms cold miss!**
  - SQLite quiet 304 on 50-ID summaries: **176.48ms p50** — **1.3× slower than the 135.37ms cold miss!**
  Two bugs compounded:
  1. `taskDetailCache` kept a single 32-entry LRU shared between full unpaged
     panes and paged summaries, even though `GET /api/tasks/summaries` accepts
     up to **50 IDs** — the exact chunk size `/next/agents` and `/next/tasks`
     poll continuously. Every 50-ID poll evicted its own first 18 entries before
     finishing, guaranteeing 0% cache hit rate (50 full detail rebuilds on every
     quiet 304 poll) and evicting any open `/next/task/:id` pane.
  2. `handleTaskSummaries` called `s.tasks.Store.GetTask(ctx, id)` and
     `s.tasks.Store.ChangeToken(ctx)` inside the per-ID loop before checking the
     cache, adding 2N database round-trips even on a 100%-warm 20-ID batch.
- **Deterministic Reproduction:**
  - Live: `walk_items1_2.py` against `a771a9973` (`391.56ms` PG / `176.48ms` SQLite quiet 304 on 50 IDs).
  - Unit: `TestTaskSummariesFiftyIDQuietPollDoesNotThrashDetailCache` in
    `pkg/ui/task_detail_cache_test.go`. Against `a771a9973` it failed with
    `three quiet 50-id summary polls triggered 150 detail rebuilds, want 0`.
- **Fix Commit:** `f896b9e24` (`fix(ui): avoid detail cache thrash and per-id DB reads in GET /api/tasks/summaries`).
  - Split `taskDetailCache` into `entries` (max 32 full unpaged panes) and
    `pagedEntries` (max 256 compact paged summaries).
  - Stored `repo` (`detail.Repo == taskRepo(t)`) on `taskDetailCacheEntry` and
    added `tokenFor` + `hit` + `responseForWithToken` so `handleTaskSummaries`
    fetches `ChangeToken` once per request and serves warm hits after checking
    `readable(entry.repo)` with zero per-ID DB queries. Cold misses still call
    `Store.GetTask` + `readable` before building/caching so unauthorized callers
    never hydrate or read restricted tasks.
  - Extended `TestTaskSummariesAuthorizationAndOmission` to verify both cold
    and admin-warmed paged cache hits enforce identical per-caller repo scoping
    and identical foreign/no-repo/missing ETags.
  - Updated `TestTaskSummariesSkipsPerEntryFailureAndFailsOnStoreError` to call
    `store.Close()` before `db.Close()` so `ChangeToken` releases its dedicated
    connection (`model.Store.Close`) when simulating a closed store.
- **Post-Fix Live Measurement:**
  - PostgreSQL quiet 304: 20 IDs `33.27ms → 1.75ms` (**19× faster**), 50 IDs `391.56ms → 2.18ms` (**180× faster**).
  - SQLite quiet 304: 20 IDs `12.79ms → 1.37ms` (**9.3× faster**), 50 IDs `176.48ms → 2.08ms` (**85× faster**).

### Bug 3: `GrainApi.taskSummaries` discarded fulfilled chunks when another chunk failed

- **Where:** `ui-ng/src/app/api/grain-api.service.ts` (`GrainApi.taskSummaries`).
- **Symptom & Root Cause:**
  While `loadTaskSummaryBatches` (used by `agents`, `tasks`, and `workstreams`
  stores) used `Promise.allSettled` to keep partial successes across 50-ID
  chunks, `GrainApi.taskSummaries` (used by `InboxStore` and public API callers)
  used `Promise.all`. For any call spanning >50 IDs, a single failing chunk
  rejected the whole promise and threw away every task already returned by
  fulfilled chunks.
- **Deterministic Reproduction:**
  Vitest test `"taskSummaries preserves fulfilled chunks when another chunk fails and rejects if all chunks fail"`
  in `ui-ng/src/app/api/grain-api.service.spec.ts`. Against `a771a9973` it
  failed with `HttpErrorResponse: 500 Internal Server Error` instead of
  returning chunk 1's `p1` summary.
- **Fix Commit:** `d58a4095c` (`fix(ui-ng): preserve succeeded chunks when one fails in GrainApi.taskSummaries`).
  Switched `GrainApi.taskSummaries` to `Promise.allSettled`, merging all
  fulfilled chunks and re-throwing the first error only if zero chunks
  succeeded.

---

## Verification

- **Go unit, race & PostgreSQL tests:**
  - `GRAIN_REQUIRE_POSTGRES=1 go test -v -race ./pkg/ui -run 'TestTaskListCache|TestTaskDetail|TestTaskSummaries|TestTaskListOnPostgres|TestDispatchConditions'` — **PASS**
  - `go test ./pkg/ui -run TestAngularRouteTableInSync` — **PASS**
  - `go test ./tests/security -run 'TestRouteAuthorizationMatrix|TestHorizontal'` — **PASS**
  - `go test ./pkg/daemon -run TestAListedRepositorysCIAPITokenIsIsolatedEndToEnd` — **PASS**
  - `go test ./pkg/ports/...` — **PASS**
  - `go test ./tests/hygiene/...` — **PASS**
- **Frontend (`ui-ng`) checks:**
  - `npm run format:check` — **PASS**
  - `npm run lint` — **PASS**
  - `npm test` — **PASS** (70 test files, 690 tests)
  - `npm run build` — **PASS** (production bundle generated cleanly)
- **Live post-fix daemon runs:**
  - Rebuilt `/home/debian/bin/grain` from `d58a4095c` and re-ran `walk_items1_2.py`, `probe_summaries.sh`, and `probe_ciapi.go` on both SQLite and PostgreSQL, plus `walk_playwright.mjs` on PostgreSQL — all **PASS**.
