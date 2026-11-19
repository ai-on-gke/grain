# 2026-11-19 §8 Walkthrough: Command-Backed Forge Cut-Body Handling, Actions Run-Log Zip Fallback, and Pipeline Steward Cold-Start Wake

Walkthrough of `docs/test-plans/2026-11-19.md` §8 (PRs #1880, #1831, plus #1882's `CommandForgeTransport` draining fix, walked against `main` including #1913's retry changes), executed for [Task #7324](/tasks/7324) alongside [Task #7303](/tasks/7303) (`73ccc0a47`).

## Environment & Commit Walked

- **Base commit walked:** `1a449c7d5` (`origin/main`, 2026-11-19)
- **Test plan source:** `docs/test-plans/2026-11-19.md` from [Task #7303](/tasks/7303) (`73ccc0a47`, PR #1889)
- **Binary built from `HEAD`:** `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` (plus thin walkthrough wrapper `grain_forgecli.go` registering `pkg/gitproxy/gitproxytest`'s `forgecli` `ForgeViaCommand` source through `gitproxy.RegisterAmbientSource`)
- **Daemon mode:** real `grain daemon -sandbox-port-base=0 -host-sandboxes -ui-addr 127.0.0.1:4280`, tested both direct on host and relayed through a live `grain hostd` unix socket offering `host-git` (`grain capability host-git`)
- **Database backends:**
  - SQLite `3.40.1`
  - PostgreSQL `15.20` (`Debian 15.20-0+deb12u1`)
- **Browser / Playwright:** Chromium `151.0.7922.34` at `1440×900` (light & dark) and `390×844` (light)
- **Harness scripts:** [`docs/test-runs/2026-11-19-forge-logs-and-pipeline-steward/`](2026-11-19-forge-logs-and-pipeline-steward/)
  - `run_walkthrough.sh` — end-to-end driver across Parts 1–3 on SQLite and PostgreSQL
  - `grain_forgecli.go` (`//go:build ignore`) — core `grain` with `gitproxytest.Install()` registered
  - `mock_forgecli.py` — controllable mock `forgecli` command-backed forge CLI
  - `probe_commandforge.go` (`//go:build ignore`) — direct & `hostd`/`host-git` probe for `304`/`204`/`HEAD`, `text/html` cut, and signed-URL redaction
  - `mock_actions.go` (`//go:build ignore`) — local HTTP mock GitHub Actions + signed-redirect blob + git-http-backend server on `127.0.0.1:4284`
  - `walk_next_ci.mjs` — Playwright verification of `/next/ci`

## Outcome Summary

| # | Area / Scenario | SQLite | PostgreSQL | Initial Result | Post-Fix Result |
|---|---|---|---|---|---|
| 1A | Command-backed forge 200 cut-body + signed URL on stderr (direct & `hostd`/`host-git`) | Pass | Pass | Pass (`ErrForgeBodyCut`, zero secret leak, `?[redacted]`) | Pass |
| 1A-bis | Command-backed forge **pre-header failure** + signed URL on stderr (direct & `hostd`) | **Fail → Pass** | **Fail → Pass** | **F1:** `PREHEADER_SECRET_SIG` leaked into HTTP 502 API response | **Fixed & Verified** |
| 1B | Command-backed forge 200 `text/html` partial non-login HTML cut short | Pass | Pass | Pass (`ErrForgeBodyCut`, not truncated 200 HTML) | Pass |
| 1C | `304 Not Modified`, `204 No Content`, `HEAD` draining + 60 s timeout (direct & `hostd`) | Pass | Pass | Pass (clean exit markers, no `context canceled` or hang) | Pass |
| 1D | Cut-body triggers run-log zip fallback across `host-git` (`test/unit` step order) | Pass | Pass | Pass (HTTP 200 with ordered steps from zip) | Pass |
| 2A | Single-download multi-job zip fallback (`build`, `test/unit`, `lint`) via sandbox `grain ci log` & `/next/ci` | Pass | Pass | Pass (`/actions/runs/42/logs` hit once, `test_unit/` in step order, Playwright clean at 1440px & 390px) | Pass |
| 2B | `in_progress` job returning empty 200 skips run-zip download | Pass | Pass | Pass (HTTP 502, `/actions/runs/43/logs` hits = 0) | Pass |
| 2C | 100 MiB run zip (20 × 5 MiB steps) shared 64 MiB cap | **Overshoot → Pass** | **Overshoot → Pass** | **F2:** stopped at `67,108,865` B (`64 MiB + 1`) when steps lack `\n` | **Fixed (`67,108,864` B exact)** |
| 2D | Dual failure (empty+404, empty+empty) error text & `actionsLiveTTL` negative cache | Pass (+**F3**) | Pass (+**F3**) | Pass (`1 → 1 → 2` hits across 16 s TTL); **F3** found via audit on `invalidateRun` & non-zip 200 body | **Fixed & Verified** |
| 3A | Cold-start pipeline steward wake on open `pipeline.not-publishing:acme/widgets@main` | Pass | Pass | Pass (wakes, files standing task, subscribes `pipeline.*`, advances cadence) | Pass |
| 3B | Cold-start pipeline steward with ONLY `pipeline.steward-regression` open | Pass | Pass | Pass (quiet drop recorded, 0 tasks, `lastRunAt: null`, no self-wake loop) | Pass |

---

## Findings & Fixes

### F1: Signed URL query leaked on pre-header command-backed forge failures (`pkg/gitproxy/commandforge.go`)

- **Repro (before fix):** Configure a command-backed forge CLI (`forgecli`) that follows a signed log redirect URL and exits 1 *before* printing an HTTP status line (e.g., dial timeout or TLS reset against the blob store), writing `Get "https://pipelines.example.com/logs/42?sig=PREHEADER_SECRET_SIG&se=2099": dial tcp: i/o timeout` to stderr. Request `GET /api/ci/runs/gha:depot.code.forge.test:acme:widgets:102/log?job=build` against the live daemon (both direct and via `grain hostd` / `host-git`).
- **Observed before fix:** HTTP `502` returned verbatim:
  ```json
  {"error":"reading job \"build\"'s log from the forge: Get \"https://depot.code.forge.test\": `forgecli api --method GET repos/acme/widgets/actions/jobs/1021/logs` failed: Get \"https://pipelines.example.com/logs/42?sig=PREHEADER_SECRET_SIG\u0026se=2099\": dial tcp: i/o timeout"}
  ```
- **Root cause:** PR #1880 (`f748a0b83`) applied `redactURLQueries(...)` in `commandForgeRun.wait()` and `commandForgeRun.cut()`, but left `commandForgeRun.failure()` passing raw `said` to `authfix.Wrap(...)` and `shortMessage(...)`. Because `pkg/github.redactURLError` only strips `(*url.Error).URL`, the signed URL embedded inside the wrapped stderr string reached the agent via `apierror.Expose(err)`.
- **Related relay parity (`pkg/gitproxy/hostgit.go`):** `HostGitClient.forgeDownload` / `hostGitServeForgeDownload` wrapped a cut-body download error without converting `bodyCutMarker` back into `&forgeBodyCutError`, unlike `HostGitClient.forge` / `hostGitServeForge`.
- **Fix:**
  - `pkg/gitproxy/commandforge.go`: redact `said` with `redactURLQueries(said)` in `commandForgeRun.failure()` before `authfix.Wrap` and `shortMessage`.
  - `pkg/gitproxy/hostgit.go`: pass `ErrForgeBodyCut` directly from `hostGitServeForgeDownload` and reconstruct `&forgeBodyCutError` in `HostGitClient.forgeDownload`.
- **Observed after fix (real daemon, SQLite direct/hostd & PostgreSQL hostd):**
  ```json
  {"error":"reading job \"build\"'s log from the forge: Get \"https://depot.code.forge.test\": `forgecli api --method GET repos/acme/widgets/actions/jobs/1021/logs` failed: Get \"https://pipelines.example.com/logs/42?[redacted]\": dial tcp: i/o timeout"}
  ```
- **Regressions added:** `TestCommandForgePreHeaderFailureRedactsSignedURLs` (direct + `hostd`), `TestCommandForge204AndHeadDrainCleanly`, `TestCommandForgeDownloadCutShortThroughHostd` in `pkg/gitproxy/commandforge_test.go`.

### F2: `JobLogFromRunZip` overshot the 64 MiB shared step-file cap by 1 byte (`pkg/github/actions.go`)

- **Repro (before fix):** Serve a run-log zip for run 44 whose job `heavy` has 20 step files (`heavy/1_step.txt`..`heavy/20_step.txt`) each inflating to 5 MiB of `'A'` without a trailing newline (100 MiB uncompressed total). Request `GET /api/ci/runs/gha:acme:widgets:44/log?job=heavy`.
- **Observed before fix:** HTTP `200` with `"bytes": 67108865` (`64 MiB + 1`), because `out.WriteByte('\n')` executed unconditionally after `io.LimitReader(rc, left)` filled `out` to `maxZippedJobLog` (`67,108,864`).
- **Fix (`pkg/github/actions.go`):** Guard newline insertion with `&& out.Len() < maxZippedJobLog`.
- **Observed after fix (real daemon, SQLite & PostgreSQL):** HTTP `200`, `"bytes": 67108864` (exact `64 MiB`), `"truncated": true`, `"logLen": 262144` (`MaxActionsLogBytes`), no OOM.
- **Regression added:** `TestJobLogFromRunZipSharedStepCap` (both `no_trailing_newline` and `trailing_newline` variants, 20 × 5 MiB steps) in `pkg/github/actions_test.go`.

### F3: Negative run-log cache not cleared on re-run and bypassed on non-zip 200 bodies (`pkg/ui/ci_actions.go`)

- **Audit finding:**
  1. `actionsCache.invalidateRun` deleted `run|...`, `artifacts|...`, and `log|...` entries on a re-run, but did not delete `runlog-failed|<repo>|<runID>`, leaving a stale negative cache entry for up to `actionsLiveTTL` after a re-run.
  2. When `/actions/runs/{run}/logs` returned HTTP `200` with a non-zip body (e.g., an HTML error page causing `zip.ErrFormat`), `actionsJobLogFromRun` did not record `runlog-failed|...`, causing sibling job log reads to re-download the same broken payload.
- **Fix (`pkg/ui/ci_actions.go`):** Delete `runlog-failed|<repo>|<runID>` in `invalidateRun`, and cache `failedKey` for `actionsLiveTTL` when `errors.Is(err, zip.ErrFormat)` (still leaving valid zips that merely lack one job uncached as run-wide failures).
- **Regression added:** Extended `TestCIRunLogFallsBackToTheRunsLogZip` in `pkg/ui/ci_actions_test.go` covering corrupt non-zip negative caching and `RerunActionsRun` cache invalidation.

---

## Detailed Walkthrough Evidence

### Part 1: Command-Backed Forge Cut-Body, Redaction, and Bodyless Draining

Executed across three live configurations:
1. SQLite + direct bare-host `grain-forgecli daemon`
2. SQLite + live `grain hostd` unix socket (`host-git` allowlist entry running `grain-forgecli capability host-git`)
3. PostgreSQL 15.20 + live `grain hostd` unix socket (`host-git`)

Verified in all three configurations:
- **Cut-body 200 + signed URL (`Case 1A`, run 101):** `GET /api/ci/runs/gha:depot.code.forge.test:acme:widgets:101/log?job=build` returned HTTP `502` containing both job-log and run-zip `ErrForgeBodyCut` messages (`printed HTTP 200 and its headers, then exited 1 before the body ended`), with `https://pipelines.example.com/logs/42?[redacted]` and `https://pipelines.example.com/runzip/42?[redacted]`. Zero occurrences of `SUPER_SECRET_SIG` in API response, `daemon.log`, or `hostd.log`.
- **Pre-header failure + signed URL (`Case 1A-bis`, run 102):** After F1 fix, returned HTTP `502` with `https://pipelines.example.com/logs/42?[redacted]` and zero occurrences of `PREHEADER_SECRET_SIG`.
- **Cut-body 200 `text/html` partial non-login HTML (`Case 1B`, run 103):** Returned HTTP `502` with `ErrForgeBodyCut` (`https://pipelines.example.com/html/42?[redacted]: unexpected EOF`) rather than handing on a truncated HTTP `200` HTML page.
- **Bodyless draining (`Case 1C`):** `probe-commandforge` confirmed `304 Not Modified` (conditional `If-None-Match` ETag revalidation via `github.RealTransport`), `204 No Content`, and `HEAD` all drained cleanly, waited for `forgecli` to write its post-sleep clean-exit marker, logged zero `context canceled` lines, and confirmed `ForgeTransportOn` uses the 60 s `forgeAPITimeout`.
- **Cut-body → zip fallback across `host-git` (`Case 1D`, run 104):** When job log returned cut-body `ErrForgeBodyCut` and run zip returned a valid zip, `GET /api/ci/runs/gha:depot.code.forge.test:acme:widgets:104/log?job=test%2Funit` returned HTTP `200` with `"forgecli unit setup ok\nforgecli unit run ok\n"`.

### Part 2: GitHub Actions Run-Log Zip Fallback (`grain ci log`, API, and `/next/ci`)

Executed against stock `/home/debian/bin/grain daemon -sandbox-port-base=0 -github-host 127.0.0.1:4284 -github-insecure-http` over both SQLite and PostgreSQL:
- **Single-download multi-job caching via real host-sandbox `grain ci log` (Run 42):**
  - Filed and dispatched an approved task granted `https://grain.dev/capabilities/api#ci-read={"repo":"acme/widgets"}` executing:
    - `grain ci log gha:acme:widgets:42 --job build` → `build step ok\nartifact built\n`
    - `grain ci log gha:acme:widgets:42 --job test/unit` → `setup env ok\ndb ready\nFAIL TestWidget (0.01s)\nexpected 42, got 0\n` (underscored `test_unit/` folder assembled in numeric step order `1_setup.txt` before `2_run.txt`)
    - `grain ci log gha:acme:widgets:42 --job lint` → `lint clean\n0 issues\n`
  - Mock server counters confirmed `/repos/acme/widgets/actions/runs/42/logs` = **1** and `/_blob/run/42.zip` = **1** across all three job reads (and zero subsequent forge hits when Playwright loaded all three jobs on `/next/ci`).
- **Playwright `/next/ci` verification (SQLite & PostgreSQL):**
  - Opened `/next/ci`, filtered `Repo: acme/widgets` + `GitHub Actions`, selected Run 42 (`CI fallback`), loaded and verified `build` (701), `test/unit` (702, confirming `setup env ok` precedes `FAIL TestWidget`), and `lint` (703).
  - Checked `1440×900` light & dark and `390×844` light: zero horizontal overflow (`scrollWidth <= clientWidth`), zero `pageerror` events, zero unexpected console errors.
  - Selected Run 45 (`CI dual fail`) and clicked `Load GitHub job log`: verified `[data-testid="ci-action-error"]` rendered both failure reasons with zero secret leaks.
- **In-progress job (Run 43):** `GET /api/ci/runs/gha:acme:widgets:43/log?job=build` returned HTTP `502` naming empty 200 from `mock-blobstore`, with `/repos/acme/widgets/actions/runs/43/logs` hit count = **0**.
- **64 MiB shared step-file cap (Run 44):** Served 20 × 5 MiB compressed step files (100 MiB uncompressed); after F2 fix, `GET /api/ci/runs/gha:acme:widgets:44/log?job=heavy` returned HTTP `200`, `bytes: 67108864`, `truncated: true`, `logLen: 262144`.
- **Dual-failure error & negative TTL (Run 45):**
  1. First read (`build`, `empty_and_404`): HTTP `502` naming both the empty job log (HTTP 200 from `127.0.0.1:4284`, redirected to `localhost:4284`, `server mock-blobstore`) and `run 45's whole log could not stand in for it: HTTP 404 from 127.0.0.1:4284`. `/actions/runs/45/logs` hits = **1**.
  2. Rapid second read (`lint`): HTTP `502` served from `runlog-failed|acme/widgets|45`; `/actions/runs/45/logs` hits remained **1**.
  3. After sleeping 16 s (past `actionsLiveTTL = 15s`) and switching run 45 to `empty_and_empty`: HTTP `502` naming both empty 200 responses with `run 45's whole log came back with no bytes at all too`; `/actions/runs/45/logs` hits incremented to **2**. Zero `JOB_BLOB_SECRET` / `RUN_BLOB_SECRET` leaks in any response.

### Part 3: Pipeline Steward Cold-Start Wake (SQLite & PostgreSQL)

Executed against fresh stores on stock `/home/debian/bin/grain daemon -sandbox-port-base=0` with a green `base_branch_ci` row (`acme/widgets` `main` = `success`), zero existing pipeline-steward tasks, and zero notification subscriptions:
- **Open `pipeline.not-publishing:acme/widgets@main` (both SQLite and PostgreSQL):**
  - Triggered due schedule tick on `system:pipeline-steward`.
  - Daemon logged `orchestrator: waking the pipeline steward: pipeline notification pipeline.not-publishing:acme/widgets@main is open`.
  - Filed standing task `#2` (`templateId: "system:pipeline-steward"`), persisted `2|pipeline.*` in `task_notification_subscription`, recorded `lastRunAt`, and advanced `nextRunAt` by 6 hours.
- **Only `pipeline.steward-regression` open (both SQLite and PostgreSQL):**
  - Triggered due schedule tick on `system:pipeline-steward`.
  - Daemon logged `orchestrator: schedule system:pipeline-steward skipped ... nothing for the pipeline steward to do ... firing is dropped: its cadence moved on to the next occurrence`.
  - Created 0 steward tasks, persisted 0 subscriptions, kept `lastRunAt: null`, recorded `system:pipeline-steward|quiet|dropped` in `scheduled_firing_skip`, and advanced `nextRunAt` by 6 hours (no self-wake loop).
- **Regression added:** `TestPipelineStewardSafetyWakeInitializesNotificationSubscriberPostgres` in `pkg/orchestrator/pipelinesteward_internal_test.go` (run with `GRAIN_REQUIRE_POSTGRES=1`).

## Test Verification

- `GRAIN_REQUIRE_POSTGRES=1 go test -race -count=1 ./pkg/gitproxy ./pkg/github`
- `GRAIN_REQUIRE_POSTGRES=1 go test -race -count=1 ./pkg/ui -run 'TestCI|TestActions|TestAngularRouteTableInSync'`
- `GRAIN_REQUIRE_POSTGRES=1 go test -race -count=1 ./pkg/orchestrator -run 'TestPipelineSteward|TestAQuietQueue|TestEachKindOfTrouble|TestWakePipelineSteward|TestSystemSchedules|TestSteward'`
- `go test -count=1 ./tests/hygiene/... ./pkg/ports/...`
- `go test -count=1 ./tests/security -run TestRouteAuthorizationMatrix`
- `go vet ./pkg/gitproxy ./pkg/github ./pkg/ui ./pkg/orchestrator`
- `./docs/test-runs/2026-11-19-forge-logs-and-pipeline-steward/run_walkthrough.sh` (all 3 parts on SQLite and PostgreSQL, exit 0)
