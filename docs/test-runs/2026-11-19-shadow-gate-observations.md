# Walkthrough: Shadow Gate Observations Across All 13 Built-In Families (`docs/test-plans/2026-11-19.md` §1)

**Plan:** `docs/test-plans/2026-11-19.md` §1 (filed alongside [Task #7303](/tasks/7303); PRs #1824, #1816, #1825, #1830, #1875, #1885, plus #1905 and `c561225f2` on `HEAD`).
**Task:** [Task #7317](/tasks/7317)
**Pre-flight check:** `git log origin/main -- docs/test-runs/` confirmed no prior reports had landed on `main` for `2026-11-17.md` §4 or `2026-11-18.md` §2 (PR #1813 review/approval observations). On `HEAD`, `pkg/gates/registry.go` pins `Implementation = "builtin-gate-observer-v10"` (incorporating `c561225f2` report/receipt scoping on `implement` tasks and PR #1905 read-only cached observation reads). This walkthrough exercised all 13 built-in families end to end on real `grain daemon` instances backed by both SQLite and PostgreSQL, plus real Playwright Chromium against `/next/`.

## Summary

| Area | SQLite 3.40.1 | PostgreSQL 15.20 | Notes |
| --- | --- | --- | --- |
| Additive indexes (`task_qa_check_run_head`, `task_qa_finding_run`, `github_ci_job_repo_commit`, `gate_health_workflow_order`, `gate_outcome_source_at`) | Pass | Pass | Verified in `sqlite_master` and `pg_indexes` (`public`) |
| **Scenario A:** Live CI checks (`checks` v1: multi-workflow same-named jobs, rerun same-attempt vs new-attempt delayed jobs & missing timestamps, same-head rename fence, default grant stability, 500 error rejection & recovery, local review/approval refresh without rewinding CI) | Pass (11/11) | Pass (11/11) | Optimistic fence also observed catching live 500ms reconciler writes cleanly |
| **Scenario B:** Outcome predictions (`delivery`, `integration`, `children`, `acknowledgment`) | Pass (10/10) | **Defect F2 found & fixed** → Pass (10/10) | **F2:** PostgreSQL `TIMESTAMPTZ` microsecond truncation caused `processResultFrom` to drop `gate_outcome_source(resource='finish')` on ~99.9% of runs |
| **Scenario C:** Health & qualification (`base_health`, `nightly_health`, `publishing_health`, `pipeline_health`, `qa_stage`, `release_qualification`), `state-repo` export exclusion, strict noninterference & `304 Not Modified` | **Defect F1 found & fixed** → Pass (12/12) | Pass (12/12) | **F1:** `capturePipelineHealth` failed scanning `MAX(finished_at)` / `MIN(started_at)` scalar subqueries into `sql.NullTime` on `modernc.org/sqlite` |
| **Scenario D:** Unsafe shadow history injection & fresh-event recovery (`detail`, paged `?comments_limit=1&attempts_limit=1`, `/api/tasks/summaries`) | Pass (4/4) | Pass (4/4) | 3 injected unsafe rows withheld under `unknown_invalid_history: 3`, zero sentinel leakage, fresh comment recovers safe sample |
| **Scenario E:** Scoped human viewer (`bob@example.com` via ES256 Hub JWT) & scoped external-agent API token (`tasks:read` on `acme/widgets`) | Pass (7/7) | Pass (7/7) | Cross-repo history withheld (`gate_observation_diagnostics: null`) with zero `secret-repo` leakage; `checks` omitted without `ci:read`; health bindings denied (`unknown`) |
| **Scenario F:** Canonical JSON Schema validation (`contracts.schema.json` + `api.schema.json` + `validate.semantic` + `validate_api.semantic`) | Pass (38 live views + 44 history samples, **13/13** gate types) | Pass (38 live views + 44 history samples, **13/13** gate types) | Strict shadow invariants (`mode=observation_only`, `authority=legacy`, all effect flags `false`, empty `work`/`actions`) held on every sample |
| **Browser (`/next/`):** `/next/task/<id>` across 6 task shapes × 1440×900 & 390×844 × light & dark, scoped viewer browser check, and `/next/agents` `390x844` `PageDown`+`Control+Home` (`235dd2ec4`) | **Defect F3 found & fixed** → Pass (1,511/1,511 checks) | — | **F3:** `<g-task-observations>` hardcoded `<h3>`/`<h4>` beneath standalone `<h1>`, violating `ui-design-book.design.md` §8 and axe-core `heading-order` on `/next/task/<id>` |

## Environment & Build Tested

- **Base commit walked:** `1a449c7d513ee831d3723425c05c4f33f5142a5b` (`origin/main`), rebuilt with the three fixes on branch `grain/53549e42/task-7317-walkthrough-shadow-gate`.
- **Toolchain & runtimes:**
  - Go: `go1.26.10 linux/amd64`
  - Node.js: `v20.19.0`
  - SQLite CLI: `3.40.1` (daemon embedded driver: `modernc.org/sqlite`)
  - PostgreSQL: `15.20 (Debian 15.20-0+deb12u1)` (`github.com/jackc/pgx/v5 v5.11.0`)
  - Python: `3.11.2`, `jsonschema 4.10.3`
  - Browser: Playwright Chromium `151.0.7922.34` (`chromium-1234`, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at `1440×900` (desktop) and `390×844` (mobile), light and dark themes.
- **Packages installed in sandbox (not pre-baked in stock image):** `postgresql`, `postgresql-client`, `sqlite3`, `python3-jsonschema`.
- **Build commands:** `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` (followed by `cd ui-ng && npm ci` to restore `@playwright/test` and `@axe-core/playwright`, which `make frontend`'s `npm ci --omit=dev` omits).
- **Live harness:**
  - Real `grain daemon -host-sandboxes -sandbox-port-base=0 -as alice@example.com -repo acme/widgets -reconcile=500ms` with `GRAIN_GATE_OBSERVATION_TASKS=1..19` and `GRAIN_GATE_OBSERVATION_CHILDREN='{"6":[{"tasks":["5"],"predicate":"success"},{"tasks":["7"],"predicate":"closed"}]}'`, pointing `-github-api-url` and `-git-base-url` at an in-process dual-repo (`acme/widgets` + `acme/secret-repo`) GitHub + `git-http-backend` simulator that also served an ES256 JWKS (`/.well-known/jwks.json`) for `-hub-url` JWT verification (`alice@example.com` operator, `bob@example.com` scoped `widgets-viewer`), plus a fake Antigravity CLI script capable of pushing real git commits/PRs, posting reports, calling `nothing_needed` (with oversized >8192-char and empty bodies), and holding a long-description running task for `/next/agents`.

## Defects Found & Fixed

### F1 — SQLite `pipeline_health` fails scanning `MAX(finished_at)` / `MIN(started_at)` scalar subqueries into `sql.NullTime`

- **Symptom & reproduction:** On SQLite, querying `GET /api/tasks/<pipeline-goal-task>` while any non-closed task in the store had a finished or live `task_run` row caused both `pipeline-legacy` and `pipeline-current` bindings to fail with `status: "error"`, `code: "unavailable"`, `disposition: "rejected"`. Direct store call returned:
  ```
  sql: Scan error on column index 8, name "(SELECT MAX(`r`.`finished_at`) FROM `task_run` AS `r` WHERE `r`.`task_id` = `s`.`task_id`)": unsupported Scan, storing driver.Value type string into type *time.Time
  ```
- **Root cause:** In `pkg/model/gate_health_observations.go` (`capturePipelineHealth`), columns 8 and 9 are scalar aggregate subqueries (`SELECT MAX(r.finished_at) ...` and `SELECT MIN(r.started_at) ... WHERE r.finished_at IS NULL`). Because SQLite reports no declared column affinity (`""`) for aggregate expressions, `modernc.org/sqlite` returns Go `string` values (e.g. `"2026-11-20 00:12:13.170298152 +0000 UTC"`) rather than `time.Time`, which `sql.NullTime.Scan` rejects. Existing unit tests in `pkg/model/gateobservationtest/health.go` never inserted a `task_run` row, so both subqueries always returned `NULL`.
- **Fix:** Scan both scalar-subquery columns into `any` and parse them via a fail-closed helper `scanSubqueryTime` backed by the existing `parseScannedTime` (`pkg/model/external_ref.go`), which handles `nil`, `time.Time` (PostgreSQL), `string`, and `[]byte` (SQLite) and fails closed on non-nil unparseable values.
- **Files & regression:** `pkg/model/gate_health_observations.go`, `pkg/model/gateobservationtest/health.go` (`pipeline-health-scans-finished-and-live-run-timestamps`, verifying both `MAX(finished_at)` preventing false queued stalls and `MIN(started_at)` driving live-run stall detection on SQLite and PostgreSQL).

### F2 — PostgreSQL `TIMESTAMPTZ` microsecond truncation silently drops `gate_outcome_source(resource='finish')`

- **Symptom & reproduction:** On live PostgreSQL 15.20, **every single settled task** (`id_ci`, `id_unmerged`, `id_nn_big`, `id_nn_empty`, `id_child`, `id_qa`) was missing its `resource='finish'` row in `gate_outcome_source`. As a result, `delivery-change` on an open PR task returned `status: "unknown"` with `"Settled change task lacks current authenticated finish provenance."` instead of `"met"`.
- **Root cause:** In `pkg/orchestrator/gate_outcome_sources.go` (`processResultFrom`), `processResultFromLegacy` persists `o.CompletedAt = &now` using Go's nanosecond-precision `time.Now()`. PostgreSQL `TIMESTAMPTZ` (`pgx/v5/pgtype/timestamptz.go`) truncates downward to microsecond precision (`int64(ts.Time.Nanosecond())/1000`). Immediately afterward, line 41 read `o` back from the store and checked `o.CompletedAt.Before(now)`. Whenever `now.Nanosecond()%1000 != 0` (~99.9% of real runs), the microsecond-truncated readback was strictly before `now`, causing `processResultFrom` to return early without calling `RecordGateOutcomeSource(..., Resource: "finish")`. Existing tests used zero-nanosecond timestamps (`time.Date(..., 0, time.UTC)`) or ran only on SQLite (which preserves all 9 nanosecond digits in text).
- **Fix:** Compare at microsecond precision (`o.CompletedAt.Truncate(time.Microsecond).Before(now.Truncate(time.Microsecond))`, matching the existing `Truncate(time.Microsecond)` guard in `pkg/model/gate_observations.go:137` and `pkg/model/gate_outcomes.go:69`) and record `At: *o.CompletedAt` so persisted SQL and JSON timestamps match on both engines.
- **Files & regression:** `pkg/orchestrator/gate_outcome_sources.go`, `pkg/orchestrator/gate_outcomes_test.go` (`TestShadowFinishProvenanceWithSubMicrosecondTimestamp` on both SQLite and real PostgreSQL via `postgrestest.Open`), `pkg/orchestrator/orchestrator_test.go` (`postgrestest.Shutdown()` in `TestMain`).

### F3 — `<g-task-observations>` violates heading order (`<h1>` → `<h3>`) on standalone `/next/task/<id>`

- **Symptom & reproduction:** Running axe-core against `[data-testid="gate-observations"]` on standalone `/next/task/<id>` reported a `heading-order` violation on every task, viewport (`1440×900` and `390×844`), and theme (`light` and `dark`): `<h3>Built-in gates · Observation only · Shadow predictions</h3>` appeared directly below the standalone page's `<h1>` task title with no intervening `<h2>`. Existing `next-gate-live-qualification.spec.ts` tested only the split-pane route (`/next/tasks/<id>`) and checked only `serious`/`critical` axe rules (`heading-order` is `moderate`).
- **Root cause:** Unlike `<g-task-gates-bar>` immediately below it in `ui-ng/src/app/task/task-detail.component.ts` (which receives `[pane]="pane()"` per `design/design/ui-design-book.design.md` §8), `<g-task-observations>` had no `pane` input and hardcoded `<h3>`/`<h4>`.
- **Fix:** Added `readonly pane = input<boolean>(false)` to `TaskObservationsComponent`, passed `[pane]="pane()"` from `TaskDetailComponent`, and changed the headings to `<h2 [attr.aria-level]="pane() ? 3 : null">` and `<h3 [attr.aria-level]="pane() ? 4 : null">` while keeping identical visual typography (`font-weight: 600; font-size: 14px; line-height: 20px; margin: 0 0 8px;`).
- **Files & regressions:** `ui-ng/src/app/task/task-observations.component.ts`, `ui-ng/src/app/task/task-detail.component.ts`, `ui-ng/src/app/task/task-observations.component.spec.ts` (unit test for standalone `h2`/`h3` and pane `aria-level="3"`/`"4"`), `ui-ng/e2e/next-gate-live-qualification.spec.ts` (Playwright axe `heading-order` regression across both `/next/task/<id>` and `/next/tasks/<id>`).

## Detailed Walkthrough Procedures & Outcomes

### Scenario A — Live CI Checks (`checks` v1) + Review/Approval Refresh

Ran identically on live SQLite and PostgreSQL daemons against a real pushed PR (`id_ci`, paused after PR creation so the orchestrator did not push conflicting commits during synthetic check failures):

1. **A1 (Multi-workflow same-named job `test`):** Workflow `CI A` (`test=success`, suite `101`) alongside `CI B` (`test=failure`, suite `102`) on the same head SHA. Both current and legacy `checks` bindings returned `unmet` (`"Current-head CI has a failing required verdict."`); the passing sibling never masked the failing job.
2. **A2a / A2b (Same-attempt rerun of failed job):** On a new commit, workflow `CI A` `run_attempt=2` had historical job `lint` (`started_at` before attempt 2's `run_started_at`) and re-run job `test` (`success`). Without a recorded `github_ci_job` row for `lint`, current `checks` returned `unknown` (`"Unreplaced historical job lacks exact attempt proof."`). After recording the exact attempt-1 `github_ci_job` row for `lint`, current `checks` retained the unreplaced verdict and returned `met`.
3. **A2c (Missing timestamp on attempt 2):** Missing `started_at` on `test` during `run_attempt=2` returned `unknown` (`"Job membership in the latest workflow attempt is unproved."`).
4. **A2d (New workflow attempt with delayed job creation):** Newer workflow run `2002` (suite `202`) had no check runs yet while older run `2001` (suite `201`) had green jobs; current `checks` returned `unknown` (`"Latest successful workflow has no proved current-attempt job checks."`).
5. **A3 (Same-head check rename fencing):** Recorded `github_ci_job` named `test` (`failure`, check run `501`) while remote check run `501` reported renamed `test-renamed` (`success`); current `checks` fenced against stale/contradictory evidence and returned `unknown` (`"Complete remote checks disagree with recorded exact-head CI evidence."`).
6. **A4 (Default task grant change does not flip CI provider):** On a clean green head (`deps: ['checks/policy', 'checks/github']`), toggled deployment default capabilities to include `grain#ci`. Existing task remained `met` on `checks/github` without flipping providers.
7. **A5 (External CI read error & clean recovery):** Configured GitHub sim to return HTTP 500 for check runs. `checks` returned `status: "error"`, `disposition: "rejected"`, `accepted_at: null`, and direct DB audit confirmed `badAccepted: 0` across all `error` shadow history rows. Restoring HTTP 200 checks recovered cleanly to `status: "met"`, `disposition: "accepted"`, non-null `accepted_at`.
8. **A6 (Local refresh without rewinding accepted external CI evidence):** Linked a review task (`LinkReviewTask`) onto `id_ci` and re-read `GET /api/tasks/<id_ci>`. Local `review` (4 bindings) and `approval` (1 binding) observations refreshed while `checks` stayed `met` with non-regressing `accepted_at`.
9. **Live concurrency check:** During Scenarios A2–A4, the live 500ms daemon reconciler concurrently updated `github_ci_job`, and the optimistic collection fence caught the concurrent write (`"Recorded CI evidence changed during collection."`) cleanly without corrupting accepted state.

### Scenario B — Outcome Predictions (`delivery`, `integration`, `children`, `acknowledgment`)

1. **B1a / B1b (PR open → PR closed unmerged):** Real PR open gave `delivery-change: met`, `pr_merged: unmet`. Closing the PR unmerged auto-closed the task while keeping `delivery-change: met` visible and `pr_merged: unmet` (never falsely satisfied).
2. **B1c (Reopen + run-now clears settlement without shadow resurrection):** Reopening and invoking `POST /api/tasks/<id>/run-now` cleared `completedAt` and `closedAt` and moved `delivery-change` to `pending`, with no resurrection from diagnostic history.
3. **B3a / B3b (`nothing_needed` oversized >8192-char report and absent report):** Both `nothing_needed` runs (one with an oversized 9 KB comment and one with no report body) returned `delivery-nothing_needed: met` and `legacy_nothing_needed_ack: met`, while `receipt` (`explicit_receipt`) stayed `unknown`.
4. **B2a / B2b (Implement report delivery + parent `children`):** Report-only `implement` child task (`id=7`) closed by verified human returned `report: met` and `receipt: met`. Parent task (`id=6`) with `GRAIN_GATE_OBSERVATION_CHILDREN` (`5: success`, `7: closed`) returned `met` on both `children` bindings.
5. **B2c / B2d (Reopen + reply clears settlement and invalidates stale child success):** Reopening both children via `POST /api/tasks/<id>/comments {"reopen": true}` cleared `completedAt`/`closedAt`, moved child `report`/`receipt` off `met`, and immediately invalidated both parent `children` bindings (`success: pending`, `closed: pending`).

### Scenario C — Health & Qualification (`base_health`, `nightly_health`, `publishing_health`, `pipeline_health`, `qa_stage`, `release_qualification`)

1. **C1a / C1b (`base_health` & `publishing_health`):** On exact base tip, `base-legacy: met`, `base-current: met`, `publishing-legacy: met`, and `publishing-current: unknown` (explicitly `unknown` when publication cannot be proved). Updating `gate_health_base_source` to a stale tip immediately moved `base-current` to `unknown` (`"Current profile current_tip_v1: the recorded verdict is for an older base tip."`).
2. **C2 (`nightly_health`):** With a verified workflow run on `main` (`nightly.yml`), both `nightly-legacy` and `nightly-current` returned `met`.
3. **C3 (`pipeline_health`):** After fixing F1, both `pipeline-legacy` and `pipeline-current` evaluated cleanly with `disposition: "accepted"` and non-error statuses on both SQLite and PostgreSQL.
4. **C4a–C4d (`qa_stage` exact-head pinning & evaluator-error rejection):** Seeded `golden-v3` S3/custom-stage check runs on the PR's exact head → `qa-s3: met` (`qa-s4: unknown`, `qa-s5: unmet`). Pushing a new commit invalidated `qa-s3` (`unknown`, 11 observed blockers). Recording a `verdict: "error"` check run on head 2 produced `status: "error"`, `disposition: "rejected"`, `accepted_at: null`, and `badAccepted: 0` in DB. Pushing head 3 with passing runs recovered cleanly to `qa-s3: met`, `disposition: "accepted"`.
5. **C5 (`release_qualification`):** Linked task to a repo-declared `release-qualification` suite run for `v0.1.0-rc.1` (`model.RepoDeclaredID(model.DeclaredSuite, repo, model.ReleaseQualificationSuiteStem)`). All 8 predicates rendered (`provenance: met`; `breaking_change: unknown`, `publication_readiness: unknown`, and remaining unproved predicates explicitly `unknown` with missing tag history).
6. **C6 (`state-repo` export exclusion):** Confirmed all 5 local diagnostic tables (`gate_health_base_source`, `gate_health_workflow_source`, `gate_outcome_source`, `gate_observation_shadow`, `gate_observation_shadow_history`) were populated (`counts > 0`), ran `POST /api/state/export`, and confirmed all 25 exported files in `data/state-repo/tables/` excluded every local gate diagnostic table (`leaked=[]`).
7. **C7 (Strict noninterference & read-only `304`):** Snapshotted 10 authoritative tables (`task`, `task_state`, `task_run`, `task_comment`, `task_event`, `task_goal`, `task_link`, `merge_batch`, `base_branch_ci`, `notification`) before and after `GET /api/tasks/<id>`, conditional `If-None-Match` (`304 Not Modified`), and `GET /api/tasks/summaries`: zero mutations (`diff_tables={}`) on both SQLite and PostgreSQL.

### Scenario D — Unsafe History Injection & Fresh-Event Recovery

Injected 3 unsafe JSON variants directly into `gate_observation_shadow_history` on SQLite and PostgreSQL:
1. `mode: "enforced"`, `authority: "builtins"`, `automatic_cutover: true`, with `human_text: "PRIVATE-SENTINEL-LEAK-HISTORY"`;
2. `bindings[0].required: true` with non-empty `results[0].result.actions`;
3. Unregistered gate type `"bogus_gate"`.

Verified across all three API read surfaces (`GET /api/tasks/<id>`, `GET /api/tasks/<id>?comments_limit=1&attempts_limit=1`, `GET /api/tasks/summaries?ids=<id>`):
- All 3 unsafe samples were withheld and counted under `counts.unknown_invalid_history == 3`.
- `"PRIVATE-SENTINEL-LEAK-HISTORY"` never appeared in any HTTP response body.
- Current `gate_observations` stayed intact and every visible retained history sample remained strictly `mode: "observation_only"`, `authority: "legacy"`.
- Posting a fresh comment cleanly recorded a new safe sample (`equivalent_milestones: 2`, `unknown_invalid_history: 3`) with zero sentinel leakage.

### Scenario E — Scoped Viewer & Scoped Agent API Ceilings

Moved a task that initially observed a `base-green` goal on `acme/secret-repo` over to `acme/widgets`:
- Verified operator (`alice@example.com`) retained both history generations including `acme/secret-repo`.
- Verified both scoped human viewer (`bob@example.com`, ES256 Hub JWT, role `widgets-viewer` with `tasks:read` on `acme/widgets`) and scoped external-agent API token (`walk-scoped-agent`, `tasks:read` on `acme/widgets`):
  - Current `gate_observations` remained visible on `acme/widgets`, while `gate_observation_diagnostics` was withheld (`null`) with zero `secret-repo` leakage in the observation/diagnostic payload.
  - On `id_ci`, `checks` bindings were omitted without `ci:read` while all 18 non-check bindings rendered normally.
  - On `id_base`, all 4 `base_health`/`publishing_health` bindings returned `unknown` (denied) with `gate_observation_diagnostics: null`.

### Scenario F — Canonical Schema Validation

Validated all exported live API views and retained history samples against `design/spec/gates/contracts.schema.json`, `design/spec/gates/api.schema.json`, `validate.semantic`, and `validate_api.semantic` (with remote `$ref` resolution forbidden):
- **SQLite:** 38 live current views + 44 retained history samples passed; **13/13** built-in gate types observed (`acknowledgment`, `approval`, `base_health`, `checks`, `children`, `delivery`, `integration`, `nightly_health`, `pipeline_health`, `publishing_health`, `qa_stage`, `release_qualification`, `review`).
- **PostgreSQL:** 38 live current views + 44 retained history samples passed; **13/13** built-in gate types observed.
- Repo spec validator `python3 design/spec/gates/validate.py` also passed (29 API operations/tools, 61 examples, 13 gate types).

### Browser Walkthrough (`/next/`) & Narrow Scroll Fix (`235dd2ec4`)

Drove real Playwright Chromium `151.0.7922.34` against the rebuilt live SQLite daemon (zero classic pages visited):
- **6 task pages** (`checks+review+approval`, `children`, `base+publishing health`, `qa_stage`, `release_qualification`, `withheld unsafe history`) × **2 widths** (`1440×900`, `390×844`) × **2 themes** (`light`, `dark`):
  - 1,511/1,511 assertions passed after fixing F3:
  - Agent guidance (`agent_text`) appeared exactly once per binding (never duplicated).
  - Exact-head labels (`head <sha>`) appeared at most once per dependency/evidence row (never duplicated).
  - Predicted waits rendered solely as inert text (`"Predicted wait only: ... No operational wait is installed."`) with `0` interactive controls (`button,a,input,select,textarea,[role=button]`).
  - Withheld-history notice rendered clearly on the injected task with zero sentinel text.
  - Zero horizontal overflow (`scrollWidth <= clientWidth`), zero console errors, zero non-GET `/api/` mutations, and **0 axe-core violations** (including `heading-order`).
- **Scoped viewer browser check (`bob@example.com`):** On `/next/task/<cross-repo-id>` at `1440×900`, the observation card displayed `"Diagnostic history unavailable."` with zero mention of `secret-repo`.
- **Narrow `/next/agents` Details scroll (`235dd2ec4`):** At `390×844` in both light and dark themes, opened a live running long-description task from `/next/agents` via keyboard `Enter`, switched to `Details`, focused `.task-page`, and pressed `PageDown` immediately followed by `Control+Home`: `scrollTop` moved `0 → 630 → 0` cleanly in both themes.

## Automated Verification Suites Run

- `python3 design/spec/gates/validate.py` — pass (29 API operations, 61 examples, 13 gate types).
- `go vet ./pkg/model/... ./pkg/orchestrator/... ./pkg/gates/... ./pkg/gateobserve/...` — pass.
- `GRAIN_REQUIRE_POSTGRES=1 go test -count=1 -race ./pkg/model/sqlite ./pkg/model/postgres -run TestGateObservation` — pass (both engines).
- `GRAIN_REQUIRE_POSTGRES=1 go test -count=1 -race ./pkg/orchestrator -run TestShadow` — pass (both engines).
- `go test -count=1 ./pkg/gates/... ./pkg/gateobserve/... ./pkg/ui -run TestGate` — pass.
- `go test -count=1 ./tests/hygiene/... ./pkg/ports/...` — pass.
- `make fmt-frontend` — pass.
- `cd ui-ng && npx vitest run src/app/task/task-observations.component.spec.ts` — pass (24/24).
- `cd ui-ng && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test next-gate-observations.spec.js next-gate-check-observations.spec.ts next-gate-live-qualification.spec.ts next-agent-details-scroll.spec.ts` — pass.
- `cd ui-ng && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test next-full-system.spec.js` — pass (5/5: desktop light/dark, phone light/dark, colour contrast).
