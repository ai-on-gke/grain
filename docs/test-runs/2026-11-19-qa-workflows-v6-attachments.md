# Walkthrough Report: QA Workflows v6 Consolidation, Specification Attachment Hydration, and `/next/` Classifier Chips (`docs/test-plans/2026-11-19.md` §2)

- **Date:** 2026-11-19 / 2026-11-20 UTC
- **Task:** [Task #7318](/tasks/7318) (proposed by [Task #7303](/tasks/7303))
- **Audited PRs:** #1869 (`a114fc485`), #1879 (`dfe42ede8`)
- **Base commit walked:** `1a449c7d513ee831d3723425c05c4f33f5142a5b` (`origin/main`)
- **Prior test-run overlap check:** `git log origin/main -- docs/test-runs/` confirmed no prior reports for `2026-11-17.md` §1/§2 or `2026-11-19.md` §2 had landed on `origin/main`; this run focuses specifically on the behavior introduced in PRs #1869 and #1879.

## Environment

- **OS / Toolchain:** Debian Linux (`amd64`), `go1.26.10`, Node `v20.19.0`, `jq 1.6`
- **Databases tested:**
  - SQLite `3.40.1`
  - PostgreSQL `15.20` (`GRAIN_REQUIRE_POSTGRES=1`, real `grain daemon -postgres-url ...`)
- **Browser:** Playwright Chromium `151.0.7922.34` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`)
- **Viewports & themes:** `1440×900` (desktop) and `390×844` (phone), both `light` and `dark`
- **Daemon harness:** [`docs/test-runs/2026-11-19-qa-workflows-v6-attachments/`](2026-11-19-qa-workflows-v6-attachments/) (`env.sh`, `ghsim.go`, `fake-agy.py`, `run_walkthrough.sh`, `walk_ui.mjs`) running a real `grain daemon -sandbox-port-base=0` against an auth-enforcing `ghsim` (`401 Unauthorized` on unauthenticated git HTTP) and a scriptable fake `agy` CLI capturing both isolated `NoTools` (S0/S2/S4) and main agent prompts.

## Summary of Findings & Fixes

Three real defects were discovered during live-daemon testing and fixed with regression coverage in this branch:

1. **P1 (UI) — `/next/settings/tasks` (`SettingsWorkComponent`) locked out `Quality flow package` (`pill-qa-flow`) and `PR rollout (%)` (`input-qa-rollout-pct`) once any task ran under QA:**
   - **Root cause:** `SettingsWorkComponent.loadRunningExp()` (`ui-ng/src/app/settings/settings-work.component.ts`) queried `GET /api/qa/experiments?state=running&limit=1` and assigned `res?.experiments?.[0]` to `runningExp` without filtering `legacy: true`. In `pkg/ui/qa_experiment_api.go`, when no real `qa_experiment` is running, `handleListQAExperiments` synthesizes a read-only `legacy: true` summary (`id: "legacy-e<epoch>"`, `state: "running"`) from `task_qa_assignment` rows for the active epoch. The `@if (runningExp())` branch then hid both `pill-qa-flow` and `input-qa-rollout-pct` behind `"Running: Legacy Epoch 1 (...) · Manage"`.
   - **Deterministic reproduction:** Enable QA (`qaEnabled: true`, `qaFlowId: "standard-workflow"`, `qaRolloutPct: 100`) on a live daemon, dispatch one task (populating `task_qa_assignment`), then open `/next/settings/tasks`. Before the fix, `pill-qa-flow` and `input-qa-rollout-pct` vanished completely; `qa-running-experiment-notice` rendered `"Running: Legacy Epoch 1 (golden-v1-minimal) (Control 0% / Treatment 100%) · Manage"`.
   - **Fix:** Query `state=running&limit=5` and select `(res?.experiments ?? []).find((e) => !e.legacy) ?? null` in `ui-ng/src/app/settings/settings-work.component.ts`.
   - **Regression tests:** Added unit tests in `ui-ng/src/app/settings/settings-page.component.spec.ts` (verifying `legacy: true` leaves controls editable while a real running experiment hides them) and a Playwright scenario in `ui-ng/e2e/next-qa-settings.spec.js` (verifying `pill-qa-flow` stays visible and saves all three canonical slugs when `GET /api/qa/experiments` returns `legacy-e1`).

2. **P2 (Backend) — Pending `TaskQASummaries` preview ignored explicit task `QAFlowRef` when global QA was enabled:**
   - **Root cause:** In `pkg/model/qa.go` (`TaskQASummaries`), pending tasks checked `if !qaCfg.Enabled && hasExplicitFlow` before overriding `effFlow = strings.TrimSpace(t.QAFlowRef)`. However, `resolveOperatorFlowBinding` (`pkg/qa/resolve/resolve.go`) resolves task-level `QAFlowRef` ahead of global `qaCfg.FlowID` whenever global QA is enabled. As a result, a proposed/queued task pinned to `qa_flow_ref = "gates-only"` previewed `"standard-workflow"` / `"Standard"` on `GET /api/tasks` and `GET /api/tasks/{id}` when global QA defaulted to `"standard-workflow"`.
   - **Deterministic reproduction:** Enable global QA with `qaFlowId: "standard-workflow"`, create a proposed task with `qa_flow_ref = "gates-only"`, and call `GET /api/tasks/{id}`. Before the fix, `qaSummary.flowId` returned `"standard-workflow"`; after the fix, it returns `"gates-only"` (`"Gates only"`).
   - **Fix:** Changed `if !qaCfg.Enabled && hasExplicitFlow` to `if hasExplicitFlow` in `pkg/model/qa.go`.
   - **Regression tests:** Added assertions in `pkg/model/qa_test.go` (SQLite) and `pkg/model/postgres/qa_store_test.go` (PostgreSQL) covering both canonical (`gates-only`) and legacy (`golden-v1-minimal`) pinned `QAFlowRef` values while global QA is enabled.

3. **P2 (Backend) — S3 check runs computed lower `complexity_tier` than S0/S0b/S2/S5 because `persistS3ReportToStore` omitted `assign.Spec`:**
   - **Root cause:** `persistS3ReportToStore` (`pkg/orchestrator/qa_gate.go`) called `extractCandidatePathsWithSpec(task, nil, "", specAtt.Text)`, whereas S0 (`qa_phases.go`), S1/S0b/S2 (`qa_phases.go`), and S5 (`qa_gate.go`) all pass the resolved `*qa.FlowSpec` (which includes `spec.Context.ExtraPaths`). On a real task touching two packages (`pkg/ui/qa_api.go` and `pkg/orchestrator/engine.go`), S0/S0b/S2/S5 recorded `complexity_tier = "medium"` while all S3/S3.5 check runs recorded `complexity_tier = "low"`.
   - **Fix:** Passed `assign.Spec` (when `assign != nil`) into `extractCandidatePathsWithSpec` in `pkg/orchestrator/qa_gate.go`. Verified on both SQLite and PostgreSQL that S3 and S5 check runs now agree (`medium`).

*(Note: No non-test files under `pkg/qa/**` were modified, so `QAEngineRev` remains `7` and `pkg/qa/rev_test.go` passes unchanged.)*

## Walkthrough Results

### 1. Real-daemon backend scenarios (SQLite & PostgreSQL)

Executed via:
```bash
STORE=sqlite   docs/test-runs/2026-11-19-qa-workflows-v6-attachments/run_walkthrough.sh
STORE=postgres docs/test-runs/2026-11-19-qa-workflows-v6-attachments/run_walkthrough.sh
```

| # | Scenario | SQLite | PostgreSQL | Evidence / Notes |
|---|---|---|---|---|
| 0 | Canonical 3-workflow catalog on fresh boot | PASS | PASS | `GET /api/qa/flows`, `GET /api/qa/flow-versions` (`builtIn == true`), and `GET /api/settings` (`qaFlowIds`) return strictly `standard-workflow,fast-baseline,gates-only`. |
| 1a | Attachment-driven complexity escalation (brief 1-line body + rich `.md` + binary `.png` + oversized 4-byte UTF-8 `.txt` straddling 131,072 & 131,076 B) | PASS | PASS | Pre-dispatch `GET /api/tasks` and `GET /api/tasks/{id}` both reported `pending: true, complexityTier: "high"`. Dedicated oversized-UTF-8-only task (#3) also escalated cleanly to `high` on both SQLite `BLOB` and PostgreSQL `BYTEA` (`SUBSTR(content, 1, ?)` + `TrimIncompleteTrailingUTF8Bytes` + `TruncateValidUTF8`), with zero `U+FFFD` replacement runes and zero leaked PNG sentinel bytes. |
| 1b | S0 skip on authored spec vs S0 fire without authored spec | PASS | PASS | Root task #2 skipped S0 (`0` `qa:intent` check runs, `0` `s0-intent` invocations). Contrast inline task #5 (no authored spec) fired S0 (`1` `qa:intent` check run), confirming `RunIsolatedPrompt` was live. |
| 1c | Root `propose_dag` → child `slice_plan` step-down → Console Session / Director root `propose_dag` preservation | PASS | PASS | Root task #2 recorded S0b `{"effectiveMode":"propose_dag","isChildSubtask":false}` and agent prompt carried `## Workstream Subtask Decomposition (S0b — propose_dag)`. Proposed child task #4 stepped down to `{"effectiveMode":"slice_plan","isChildSubtask":true}` despite high complexity. Console Session (`POST /api/console-sessions`) filed root task (#7 SQLite / #8 Postgres) with `SourceSession` attribution and preserved `{"effectiveMode":"propose_dag","isChildSubtask":false}`. |
| 1d | Epoch-scoped monotonic `UpdateQAAssignmentComplexityTier` | PASS | PASS | Seeded `low` assignment in epoch 1 upgraded `low → high` on rich spec dispatch, and did **not** downgrade when shrunk to a typo fix and reopened in the same epoch. Separate epoch isolation verified in unit + Postgres tests. |
| 2 | Ephemeral `gitproxy` shallow mirror (`NewQAMirrorResolver`) against 401-enforcing forge | PASS | PASS | Unauthenticated `GET /acme/widgets.git/info/refs` returned HTTP 401. Mirror task (#9 SQLite / #10 Postgres) pushed to `grain-qa-stage/<id>`, triggered controller S3/S4/S5 checks via ephemeral `qa-mirror-*` `gitproxy` session (`audit.log` logged `info/refs` & `git-upload-pack` `forwarded: 200` with `qa-mirror-0aefdf9d3d8d` / `qa-mirror-353d4061b157`), recorded `S3=7, S4=1, S5=1`, promoted PR `acme/widgets#9000`, and kept `S3` & `S5` `complexityTier=medium`. Zero 401s, zero plaintext `ghp_walkthrough` leaks in `daemon.log`/`audit.log`/`qa-repos`/QA rows, and decrypting `secrets/sandbox-tokens.json` via `secrets.Decrypt` confirmed `{}` (`has_qa_mirror=false`, zero dangling tokens). |
| 3 | `pathTokenRe` extraction boundaries on real spec attachment | PASS | PASS | Task S1 `candidatePaths` extracted `pkg/ui/qa_api.go`, `.github/workflows/tests.yml`, and `.grain/settings.yml` (plus flow `extra-paths` `design/design`, `docs`), while rejecting `https://docs.example.com/pkg/foo.go`, `config.yaml.bak`, `pkg/ignored_backup.go.bak`, and `./attachments/design.md`. |
| 4 | Legacy `qaFlowId` alias compatibility across DB restart (all 10 retired `v1`–`v4` slugs + repo `qaFlowRef` + pinned task `qa_flow_ref`) | PASS | PASS | Seeded `golden-v1-minimal`, `golden-v2`, `golden-v2-checks-only`, `golden-v2-skill-only`, `golden-v3`, `golden-v3-enforced`, `engineering-workflow-v4`, `golden-v4-composable`, `deterministic-s3-only`, and `repo-custom` in `task_qa_assignment`, plus legacy `grain_config.qa` (`engineering-workflow-v4`, `golden-v4-composable`, `golden-v3-enforced`) and `repo_config.qa_flow_ref` (`golden-v2`). After daemon restart on `HEAD`: `GET /api/tasks` and `GET /api/tasks/{id}` matched on canonical `flowId`/`flowLabel`, `GET /api/settings` kept catalog at the 3 canonical flows, `PUT /api/settings` accepted legacy rollout updates and canonical migrations cleanly, and pending pinned task (`qa_flow_ref = "gates-only"`) previewed `gates-only`. |

### 2. Browser walkthrough (`/next/` at 1440×900 & 390×844, light & dark)

Executed via `walk_ui.mjs` against the live PostgreSQL daemon (with tasks spanning `standard-workflow`, `fast-baseline`, `gates-only`, `control`, `T3 Review`, `T1`, `T2 Held`, `High`/`Med`/`Low` complexity, and all 8 stages S0–S5) and via Playwright over `grain demo`:

- **Batch `GET /api/tasks` vs `GET /api/tasks/{id}` `qaSummary` parity:** 18/18 tasks matched identically (17 with `qaSummary`).
- **`/next/inbox` (1440 & 390, light & dark):** 16 `qaWorkflowChip` badges rendered cleanly (`Standard · T3 Review`, `Standard · High`, `Fast · Med`, `Standard · Med`, `Gates only · T2 Held`, `Control`), zero clipping, zero horizontal overflow.
- **`/next/tasks?tab=all` (1440 & 390, light & dark):** 16 `qaWorkflowChip` badges spanning all 4 labels (`Standard`, `Fast`, `Gates only`, `Control`), zero clipping, zero horizontal overflow.
- **`/next/workstreams` & `/next/workstreams/2` (1440 & 390, light & dark):** Workstream cards and detail rendered `qaWorkflowChip` badges cleanly without overflow.
- **`/next/task/9` Overview & Changes and CI tab (1440 & 390, light & dark):** Header `task-qa-chip` rendered `Standard · T3 Review`; hovering all 8 stage tags (`S0`, `S0b`, `S1`, `S2`, `S3`, `S3.5`, `S4`, `S5`) displayed `#g-tip` tooltips strictly inside viewport bounds at both `1440×900` and `390×844`.
- **`/next/processes/quality` (1440 & 390, light & dark):** Catalog rendered strictly the 3 canonical workflows (`standard-workflow`, `fast-baseline`, `gates-only`). Legacy deep links (`?flow=golden-v2`, `golden-v1-minimal`, `engineering-workflow-v4`, `golden-v4-composable`, `deterministic-s3-only`) all resolved cleanly to their canonical workflow in the studio switcher with zero console/page errors.
- **`/next/settings/tasks` (Work & Agents → Tasks; `/next/settings/work` also verified error-free) (1440 & 390, light & dark):**
  - *Before fix:* Blocked by Bug #1 (`Running: Legacy Epoch 1 (golden-v1-minimal)` hid `pill-qa-flow`).
  - *After fix:* `pill-qa-flow` stayed visible even with live `task_qa_assignment` rows, displayed strictly the 3 canonical options (`Standard workflow · standard-workflow`, `Fast baseline · fast-baseline`, `Gates only · gates-only`), saved and persisted each slug across page reloads, and displayed `engineering-workflow-v4 (legacy)` cleanly when seeded with a legacy stored `qaFlowId`, migrating smoothly to `standard-workflow`.
- **Console / page / network hygiene:** Zero console errors, zero uncaught page errors, zero 5xx responses across all viewports and themes.

### 3. Automated test verification

- `GRAIN_REQUIRE_POSTGRES=1 go test -race -count=1 ./pkg/model/postgres -run TestQASpecAttachmentsMonotonicTierAndLegacySummariesOnPostgres` — **PASS**
- `GRAIN_REQUIRE_POSTGRES=1 go test -race -count=1 ./pkg/model -run 'TestQA|TestTaskSpec|TestTaskQA|TestCanonical|TestValidQA'` — **PASS**
- `go test -race -count=1 ./pkg/orchestrator -run 'TestQA|TestPre|TestPost'` — **PASS**
- `go test -count=1 ./pkg/qa/... ./pkg/ui -run 'TestQA|TestAngularRouteTableInSync' ./tests/hygiene/... ./pkg/ports/...` — **PASS**
- `cd ui-ng && npx vitest run src/app/settings/settings-page.component.spec.ts src/app/processes/qa-flows-panel.component.spec.ts src/app/shared/gates.spec.ts && npm run lint` — **PASS** (66/66 tests, 0 lint errors)
- `make fmt-frontend` — **PASS**
- `cd ui-ng && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright GRAIN_BIN=/home/debian/bin/grain npx playwright test e2e/next-qa-settings.spec.js e2e/next-qa-engine.spec.js e2e/next-qa-telemetry.spec.js e2e/next-qa-workflow-studio.spec.js --project=desktop` — **PASS** (10/10)
- `cd ui-ng && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright GRAIN_BIN=/home/debian/bin/grain npx playwright test e2e/next-full-system.spec.js` — **PASS** (5/5: desktop/phone × light/dark + contrast)

## Sandbox Image Note

`postgresql`, `postgresql-client`, and `sqlite3` were not pre-installed in the sandbox image and were installed via `sudo apt-get update && sudo apt-get install -y postgresql postgresql-client sqlite3` for live PostgreSQL/SQLite daemon and `GRAIN_REQUIRE_POSTGRES=1` test verification.
