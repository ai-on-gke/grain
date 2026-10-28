# CI base-branch workflow gate and auto-retry state: walkthrough

**Date**: 2026-10-01
**Task**: grain/task-1411
**Plan**: §2 of `docs/test-plans/2026-10-01.md`. The task says that document lands with task 1353, on branch `grain/task-1353`. Neither the file nor that branch was reachable from this sandbox: the remote has no `grain/task-1353`, and `refs/pull/1353/head` is a different change (`grain/task-53`) with no such file. This run follows the scope as the task itself states it, and every bullet there is covered below.
**Code under test**: `main` at `9e265140f`. The tree contains `d42feecca` (GateOnBase), `193c90a03` (retry shown as running), `68a6faaf8` (quarantine is not a regex), `ad4f00910`, `880590a06` and `8a2a054a7`.

## Environment

| | |
|---|---|
| OS | Debian 12.15, kernel 6.1.0-53-amd64, x86_64 |
| Go | go1.26.6 linux/amd64 |
| Node | v20.19.0; `@playwright/test` 1.62.1, Chromium from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| git | 2.39.5 |
| Stores | SQLite (modernc, `pkg/model/sqlite`); PostgreSQL 15 (Debian `postgresql` package, installed for this run) for the store tests |
| Frontend | `cd ui && npm ci && npm run build` → `pkg/ui/static` |

## How the environment was built (and what is stubbed)

Plain `grain demo` has no CI engine (`cmd/grain/demo_observability.go` says so). `grain daemon` reads workflows by cloning from GitHub (`ciWorkflowSource`), and its checks run through the reconciler. Both need GitHub, so the run used a small harness instead, `harness.go.txt` in [`2026-10-01-ci-base-gate-retry-state/`](2026-10-01-ci-base-gate-retry-state/). It is built with `go build -o /tmp/walkbin ./_walk` from a copy at `_walk/main.go`. The harness wires the same production pieces the daemon wires:

- `ui.NewServer` serving the built frontend and the real `/api/ci/*` and `/api/tasks/*` handlers;
- `model.Store` on SQLite in `/tmp/walk/data/store`, which survives restarts;
- `ci.Service`, with `ci.NewStoredRunStore` (calling `RecoverInterrupted` on start, as `buildCIRunStore` does), `ci.Engine`, and `ci.OpenDirCache`;
- `orchestrator.NewHostSandboxes` for the jobs, so steps really run as shell commands on the host. Each sandbox gets a `git clone` of the bare repository, checked out at the run's commit.

**Stubs, which replace only the GitHub boundary:**

1. *Workflow source.* A `ci.Source` that shallow-clones a branch of a local bare repository (`/tmp/walk/widgets.git`) and calls `ci.LoadDir`. This replaces `ciWorkflowSource`, which clones from GitHub.
2. *Merge batch.* `orchestrator.startMergeBatch` needs GitHub to build its speculative ref. `/walk/mergebatch` sends `ci.Service.Start` the request that function builds, field for field (`Event: pull_request`, `RequestedBy: merge-batch:…`, `GateOnBase: true`, `BaseRef: main`).
3. *wait_for_checks.* This is the real `mcp.NewGrainCIWaitTools` handler. Its `GrainCIRunner` is a copy of `cmd/grain`'s `daemonGrainCI` over `ui.HTTPClient`, with shared faults answered empty.

The four tasks `walk-x`, `walk-y`, `walk-z` and `walk-r` are rows in the store with base `main`, branch `walk/<b>` and a `grain-ci` grant. They are the tasks `wait_for_checks` and `POST /api/ci/runs {"taskId":…}` start runs for.

`setup.sh` builds the fixture repository:

| branch | `.grain/workflows/ci.yml` (`on: [push, pull_request]`) |
|---|---|
| `main` | jobs `a`, `b` |
| `walk/x` | job `a` only (drops `b`) |
| `walk/y` | `.grain/workflows/` deleted |
| `walk/z` | jobs `a`, `b`, `c` |
| `walk/r` | `a` (`retry: true`) fails if `/tmp/walk/markers/a-first-attempt` is missing (and creates it), otherwise sleeps 40 s and passes; `b` |
| `walk/q` | `.grain/quarantine.yml` quarantines `test_name: "TestFoo.*"`; job `a` prints `--- FAIL: TestFooBar` and exits 1 |
| `walk/q2` | same, but quarantines `TestFooBar` exactly (positive control) |

Commands:

```sh
sh docs/test-runs/2026-10-01-ci-base-gate-retry-state/setup.sh
mkdir _walk && cp docs/test-runs/2026-10-01-ci-base-gate-retry-state/harness.go.txt _walk/main.go
go build -o /tmp/walkbin ./_walk
/tmp/walkbin -data /tmp/walk/data -bare /tmp/walk/widgets.git &      # serves 127.0.0.1:18080
/tmp/walkbin wait -task walk-x            # the real wait_for_checks tool, full suite
/tmp/walkbin wait -task walk-x -jobs a    # narrowed
```

## Results

### Base-branch gate

| # | Step | Expected | Observed | |
|---|---|---|---|---|
| 1 | `wait_for_checks` on `walk-x` (full suite) | workflow-definition failure naming `ci.yml:b` | Returned after 0 s, `failure`, no jobs: `ci: walk/x does not run 1 job(s) its base main requires for push on walk/x: .grain/workflows/ci.yml:b. A branch cannot remove checks from its own merge gate; …` | PASS |
| 2 | `wait_for_checks` on `walk-y` | failure naming `ci.yml:a` and `ci.yml:b` | `… does not run 2 job(s) its base main requires …: .grain/workflows/ci.yml:a, .grain/workflows/ci.yml:b …` | PASS |
| 3 | `wait_for_checks` on `walk-z` | pass, the extra job `c` allowed | `success`: jobs a, b, c all success | PASS |
| 4 | `wait_for_checks(jobs=a)` on `walk-x` | not gated | `success`, job `a` ran, plus the "selective run" reminder | PASS |
| 5 | Merge-batch-shaped run of `walk/x` (`/walk/mergebatch?ref=walk/x`) | refused | `failure`, 0 jobs, `… requires for pull_request on walk/x: .grain/workflows/ci.yml:b …` | PASS |
| 6 | `POST /api/ci/runs` `{"taskId":"walk-x","GateOnBase":false,"BaseRef":"walk/x"}` and again with `gateOnBase`/`baseRef`/`gate_on_base`/`base_ref` | no effect | 202 `failure`, same gate error both times (the fields are `json:"-"` and task runs set them on the server) | PASS |
| 7 | `POST /api/ci/runs` `{"repo":"acme/widgets","ref":"walk/x","GateOnBase":true,"BaseRef":"main"}` (no task) | the fields are ignored; the run is ungated and is not a task verdict | 202, an ordinary run of job `a` (success) | as designed |
| 8 | Team of a refused run | the requesting team | **Run filed under no team, read back as "default"**. See finding F1 | FIXED |

### Auto-retry shown as running

Each attempt below used a fresh commit on `walk/r` (a trailing comment), so the job cache could not answer, and an empty marker directory.

| # | Step | Expected | Observed | |
|---|---|---|---|---|
| 9 | `GET /api/ci/runs/{id}` about 8 s in (first attempt failed, retry running) | job `a`: no conclusion, `retried`, `retryCount` 1, reason | `a None retried=True retryCount=1 reason=step "fail the first attempt only": first attempt: failing on purpose`; run conclusion empty | PASS |
| 10 | Playwright on `/ci`: open the run through its repo link (`walk-retry.cjs.txt`) | "retry 1/1" chip and "first attempt failed" note, nothing drawn as finished | `chip: retry 1/1`; `note: first attempt failed: step "fail the first attempt only": first attempt: failing on purpose`; the run row reads `running … 2 jobs: 1 success, 1 running`; no "all jobs completed". Screenshot: `retry-ci-page.png` | PASS |
| 11 | `wait_for_checks` on `walk-r` | does not return on the first attempt's failure; prints the Retry line | `[returned after 45s]` (8 s of failure plus 40 s of retry), `success`, then `Job a: success in 40s` / `Retry 1/1 in a fresh sandbox -- first attempt failed: step "fail the first attempt only": first attempt: failing on purpose`. Same result on all five runs | PASS |
| 12 | Restart the server after the run finished (SQLite) | `RetryCount`/`RetryReason` persist | Before and after restart: `a 'success' retried=True retryCount=1 reason=…` | PASS |
| 13 | Restart in the middle of the retry (SQLite) | run recovered as interrupted, retry state kept | `recovered 1 interrupted run(s)`; run `cancelled` ("this run was interrupted …"); job `a` still `retried=True retryCount=1 reason=…` (conclusion left empty, as `RecoverInterrupted` intends; see N2) | PASS |
| 14 | Postgres | the same round trip | `go test ./pkg/model/postgres ./pkg/model/sqlite -run CIRun -count=1 -v` against an embedded PostgreSQL 15 (`IsAvailable` true after installing the Debian package). `TestACIRunComesBackAsItWasWritten`, which writes and reads `RetryCount`/`RetryReason`, and the other CI-run store tests passed, including `TestRecoveringInterruptedCIRunsStampsThemAndLeavesTheirJobsReadable` | PASS (store level only; the harness itself was not run on Postgres) |

### Quarantine `test_name` with regex metacharacters

| # | Step | Expected | Observed | |
|---|---|---|---|---|
| 15 | `walk/q`: rule `TestFoo.*`, failing test `TestFooBar` | not quarantined (it would be under the old regex) | job `a` `failure`, run `failure` | PASS |
| 16 | `walk/q2`: rule `TestFooBar`, same failure (control) | quarantined, so the job passes | job `a` `success`, run `success` | PASS (but see F2: the record no longer says it was quarantined) |

`test_name` is still read as a **glob** (`filepath.Match`), as `68a6faaf8` says on purpose. So `TestFoo.*` does match a test literally named `TestFoo.` followed by anything, e.g. `TestFoo.x`, through the `*`. Go test names cannot contain `.`, so in practice the rule matches only literally. Anyone expecting no wildcards at all should know `*`, `?` and `[...]` remain live.

## Findings

### F1: a run refused before it plans loses the requesting team (fixed here)

`Service.recordWorkflowFailure` (`pkg/ci/service.go`) built the run without `Team`. Base-gate refusals go through that path, and so do workflow parse failures. The run store fills an empty team with "default", so a non-default team's branch that drops a required job got a red verdict that its own team-narrowed CI page and `list_ci_runs` (`RunFilter.Team`) never show.

*Reproduction (deterministic):* `go test ./pkg/ci -run TestBaseGateRefusalKeepsTheRequestingTeam`. Before the fix:

```
basegate_test.go:181: refused run's team = "", want "infra"
basegate_test.go:185: team infra's runs = [], want the refused run …
```

*Fix:* `recordWorkflowFailure` now sets `Team: req.Team`, as `Start` already does for a run that plans. Regression test: `TestBaseGateRefusalKeepsTheRequestingTeam` in `pkg/ci/basegate_test.go`. `go test ./pkg/ci` passes.

### F2: the stored run store drops a job's quarantine/flake outcome (fixed in grain/task-1512)

`193c90a03` fixed `RetryCount`/`RetryReason` not surviving a write to the store. Several sibling fields of `ci.JobRun` still don't survive it: `Quarantined`, `QuarantinedTests`, `Flake`, `Status` (`"passed (retry)"`, `"passed (quarantined)"`), `Warning` and `FailureSummary`. `storedRun` (`pkg/ci/runstore_stored.go`) never copies them into `model.CIJob`, and `ci_job` has no columns for them.

*Reproduction:* on `walk/q2` the job is green only because of quarantine (step 16), but `GET /api/ci/runs/{id}` for it reads `{'jobId': 'a', …, 'conclusion': 'success'}` with no `quarantined`, `quarantinedTests`, `status` or `warning`. On `walk/r` the retried job reads back with `retried` but no `flake`/`status`.

*Impact:* a quarantined pass is indistinguishable from a real pass once stored. `query_test_history` (`ci.QueryTestHistory`) reads `job.Flake` and `job.FailureSummary` off stored runs, so on a deployment with a store it cannot count flakes from that history.

*Why not fixed here:* it needs new `ci_job` columns in both SQLite and Postgres (one of them structured, for `FailureSummary`), store tests in both, and a decision on how much of the failure summary to keep. That is more than a scoped fix in a walkthrough, so it was proposed as a follow-up.

*Fixed afterwards* (grain/task-1512): `ci_job` now has `flake`, `quarantined`, `quarantined_tests`, `status`, `warning` and `failure_summary` columns, and `storedRun`/`runOf` copy all six. `FailureSummary` is kept as JSON bounded to `ci.MaxStoredFailureSummary` (16KiB, failing tests kept first). Round-trip tests are in `pkg/model/sqlite` and `pkg/model/postgres`, and `TestAStoredRunKeepsHowItsJobsPassed` in `pkg/ci` checks a quarantined job and a retried job after a restart, plus `QueryTestHistory` counting the flake. One gap remains in the engine: a job that passes by quarantine never gets a `FailureSummary`, so `query_test_history` still sees its quarantined tests only through `QuarantinedTests`.

## Notes (not bugs)

- **N1.** The gate's error says `… for push on walk/x` for a task run and `… for pull_request on walk/x` for a merge batch. That is correct (it names the event), but the branch appears twice in the sentence.
- **N2.** After a restart mid-retry, the run is `cancelled`/interrupted while job `a` keeps an empty conclusion. `RecoverInterrupted` stamps the run, not its jobs, on purpose ("its jobs were never concluded"). The UI shows the run's interrupted state.
- **N3.** The task's CI observation (`GET /api/tasks/walk-y` → `ci`) held only `runId`/`commit`/`startedAt`. The conclusion is written there by the orchestrator's reconciler, which the harness doesn't run. That part wasn't exercised.

## Skipped and why

- **Real merge queue and the reconciler's `startRequiredCIRun`/`retryRequiredCIRun`.** Both need GitHub (a pull request and a speculative ref). The merge batch was exercised with the exact `ci.Request` that `startMergeBatch` sends (step 5). That those call sites set `GateOnBase` is covered by `pkg/orchestrator/ci_test.go`, which `d42feecca` changed.
- **The harness on Postgres.** Postgres was exercised at the store level only (step 14), through the repo's own embedded-Postgres tests. That is where the `retry_count`/`retry_reason` columns are written and read.
- **`grain demo` / `grain daemon` binaries as such.** Neither can host real CI execution without GitHub (see above). The harness uses the same server, store, service, engine and sandbox types those commands do.
