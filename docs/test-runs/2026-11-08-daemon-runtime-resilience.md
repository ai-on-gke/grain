# Walkthrough Report: Daemon Runtime Resilience — Secret Manager `ErrGoneUpstream`, Non-Blocking `hostexec` & System-Schedule Grace

- **Test Plan Reference**: `docs/test-plans/2026-11-08.md` §6
- **Walkthrough Task**: [Task #3641](/tasks/3641) (filed by [Task #3495](/tasks/3495))
- **Date**: 2026-11-08
- **Target Branch / Base Commit**: `main` @ `8893b413`
- **Feature PRs Exercised**: #906 (`ErrGoneUpstream` redaction resilience), #912 (raw `.jsonl` transcript upload retry + `1h` system-schedule grace), #915 (`grain-agentd` placement refusal + `"the host agent refused:"`), #921 (stale-while-revalidate `hostexec` background probes)

## Executive Summary

Executed all three live runtime walkthroughs in **§6** of `docs/test-plans/2026-11-08.md` end-to-end against real compiled `grain daemon`, `grain host`, and `grain-agentd` processes, controllable local Secret Manager / GCS / GitHub HTTP servers, `SIGSTOP`ped unix-domain sockets, `faketime` wall-clock manipulation (`AT_SYSINFO_EHDR` vDSO strip + narrow `SECCOMP_RET_TRACE` realtime filter for Go's runtime), and headless Playwright Chromium.

Every feature behavior landed in PRs #906, #912, #915, and #921 worked on its covered paths, **and** live end-to-end execution uncovered **three real scoped bugs** on adjacent production paths, all reproduced deterministically, fixed in this branch with unit/e2e regressions, and re-verified end-to-end (`exit=0` across all three harnesses):

1. **Bug #1 (Security / Redaction Fail-Open Fallback — `pkg/orchestrator/run.go` + `pkg/daemon/transcripts.go`)**: When `-object-store` was configured and `TranscriptObjectStore.FlushRunEnd` failed closed because Secret Manager returned `503`/`403` (`resolveRedactor` error) or GCS returned `503` (`PutBytes` error), `FlushRunEnd` retained raw `.jsonl` on disk and returned `savedNarrative = false` — causing `orchestrator.RunDispatch` (`pkg/orchestrator/run.go:5194`) to fall back to `store.SetRunTranscript(finishCtx, runID, transcript)`, writing the **unredacted** narrative (scrubbed only with `SandboxEnvRedactor`, **not** `secretStore`'s known values) into SQLite `task_run.transcript`! Because `ui.Client.AttemptTranscript` checks `c.Store.RunTranscript` before `ArchivedTranscripts`, `GET /api/tasks/{id}/attempts/{n}/transcript` served raw Secret Manager secret values in plaintext both during the `503`/`403` outage and permanently after recovery.
   - **Fix**: Never fall back to `store.SetRunTranscript` when `cfg.TranscriptSink != nil`; instead, `TranscriptObjectStore.FlushRunEnd` stages `<runID>.md` (`0600` atomic write) alongside `<runID>` in `transcripts.unflushed/` whenever redaction or upload fails, and `retryUnflushed` redacts and uploads both `<runID>.jsonl` and `<runID>.md` once Secret Manager and the object store recover.
2. **Bug #2 (`GET /api/capability-servers/host-entries` Infinite Hang, Socket Leak & Same-Mtime Inode Swap — `pkg/daemon/capability_servers_hostd.go`, `pkg/hostd/client.go`, `pkg/daemon/hostexec.go`)**:
   - `placedStatusRegistry` embedded `*servers.Registry` without overriding `HostCapabilityEntries`, so `GET /api/capability-servers/host-entries` called `Hostd.Offers(hostd.ForDisplay(r.Context()))` directly with **no timeout** and **bypassing `queryHostdOffers`**. Against a `SIGSTOP`ped `grain host`, every load of `/settings/capabilities` hung indefinitely **and** leaked a blocked server goroutine + open unix socket fd even after HTTP client disconnect because `hostd.Client.oneShot` and `Offers` only called `conn.SetDeadline` when `ctx.Deadline()` was set and never interrupted `readFrame` on `ctx.Done()`.
   - `hostdTargetState` in `pkg/daemon/hostexec.go` tracked `socketModUnixNs` without comparing file inode (`os.SameFile`), missing same-mtime socket replacements.
   - **Fix**: Routed both co-located `placedStatusRegistry.HostCapabilityEntries` and placed `hostCapabilityEntries` through `queryHostdOffers(..., hostdOffersTimeout)`, bounded `hostAgentsOnly` and `probe` with `hostdOffersTimeout`, registered `context.AfterFunc` in `hostd.Client.oneShot` and `Offers` to wake blocked reads immediately on context cancellation, and added `os.SameFile` inode comparison to `hostdTargetState.sameAs`. Under 8-client × 40s `SIGSTOP` load, `/api/capability-servers/host-entries` dropped from infinite hang to **`p50=0.57ms, p95=0.96ms, p99=1.50ms, max=13.24ms`** with zero leaked sockets.
3. **Bug #3 (Pre-Placed Capability Server on `grain-agentd` Leaked `"grain hostd"` & Wrong `/etc/grain/hostd.yaml` Advice — `pkg/capability/protocol/client.go`)**: Saving a `run: host` capability server placed on a disconnected host is intentionally allowed (`pkg/ui/capability_servers.go:958-963`), as is loading one from `grain/config`. When `grain-agentd` (`Hello.AgentsOnly == true`) connected and `Registry.Sync` attached the pre-placed server via `protocol.startHostd`, `startHostd` checked `Offers` without checking `Hello.AgentsOnly` and returned `capability server walk-echo is set to run: host, but grain hostd's allowlist (/etc/grain/hostd.yaml) offers no capability entry "walk-echo": add an entry ...` — leaking `"grain hostd"` in `/settings/capabilities` and advising an allowlist edit that `grain-agentd` refuses.
   - **Fix**: Updated `startHostd` in `pkg/capability/protocol/client.go` to check `opts.Hostd.Hello(ctx)` (`hello.AgentsOnly`) when `!offered`, reporting `runs agent CLIs only (grain-agentd) and its allowlist may not name capabilities: place the server on a host enrolled with \`grain host\`, or run it in the container`, and replaced `"grain hostd's allowlist"` with `"the host agent's allowlist"` on full hosts.

---

## Reproducible Harness Scripts

All helper harnesses are committed with Apache-2.0 headers under `docs/test-runs/2026-11-08-daemon-runtime-resilience/`:

- [`fake_upstreams.go`](2026-11-08-daemon-runtime-resilience/fake_upstreams.go) — Controllable local HTTP stub implementing GCP Secret Manager (`create`, `addVersion`, `:access`, out-of-band `404` delete, `400 FAILED_PRECONDITION` `DESTROYED` state, and dynamic `503`/`403`/`200` status injection), GCS JSON + XML object store (`upload/storage/v1/b/.../o`, dynamic `PUT` status injection, and raw-secret leak detector across every attempted upload payload), and minimal GitHub REST endpoints.
- [`walk-secrets-transcripts.sh`](2026-11-08-daemon-runtime-resilience/walk-secrets-transcripts.sh) — End-to-end §6.1 walkthrough against a live `grain daemon -secrets-backend=secretmanager -object-store=gs://...`.
- [`walk_hostexec_probe.go`](2026-11-08-daemon-runtime-resilience/walk_hostexec_probe.go) & [`walk-hostexec.sh`](2026-11-08-daemon-runtime-resilience/walk-hostexec.sh) — End-to-end §6.2 8-client × 40s concurrent poller with 50ms `/proc/<daemon-pid>/fd` + `/proc/net/unix` socket telemetry, `SIGSTOP` / `SIGCONT`, and socket inode replacement checks.
- [`faketime_go_exec.c`](2026-11-08-daemon-runtime-resilience/faketime_go_exec.c), [`check_agentd_ui.mjs`](2026-11-08-daemon-runtime-resilience/check_agentd_ui.mjs), & [`walk-schedules-agentd.sh`](2026-11-08-daemon-runtime-resilience/walk-schedules-agentd.sh) — End-to-end §6.3 walkthrough booting `grain daemon` at `2026-11-09 02:59:30 UTC` (`faketime` + vDSO strip + realtime `SECCOMP_RET_TRACE`), crossing `03:00..03:05 UTC`, restarting at `03:50:00 UTC`, and enrolling a live `grain-agentd` host tested via CLI, REST, direct unix socket, and headless Playwright Chromium.

---

## §6.1 Live `-secrets-backend=secretmanager` `ErrGoneUpstream` Redaction, Fail-Closed `503`/`403` & Transcript Retry

### Methodology & Observed Results

1. **Setup & Secret Seeding**:
   - Started `fake_upstreams` with `--leak-needle=SUPER_SECRET_ACTIVE_VALUE_99887766` and booted `grain daemon -sandbox-port-base=0 -secrets-backend=secretmanager -secret-manager-project=test-proj -instance-name=s6 -object-store=gs://grain-s6-bucket/instances/walk-s6`.
   - Stored three secrets via `grain secrets set`: `active-token`, `deleted-upstream`, and `destroyed-version`.
2. **Out-of-Band `404` Delete & `400 FAILED_PRECONDITION` (`DESTROYED`)**:
   - Deleted `projects/test-proj/secrets/grain-s6-deleted-upstream` (`404 NOT_FOUND`) and marked all versions of `projects/test-proj/secrets/grain-s6-destroyed-version` `DESTROYED` (`400 FAILED_PRECONDITION: Secret Version [...] is in DESTROYED state.`).
   - `grain secrets list` output:
     - `stdout` annotated both gone keys cleanly:
       - `deleted-upstream: value (missing in Secret Manager)`
       - `destroyed-version: value (missing in Secret Manager)`
     - `stderr` printed the exact reconciliation hint:
       `grain secrets: 2 catalogued key(s) are gone from Secret Manager and left out of redaction; run \`grain secrets delete <secret> <key>\` to drop each, or set it again`
   - Ran `grain secrets delete deleted-upstream value`: exited `0` (`deleted deleted-upstream/value`), reducing the remaining gone annotation count to `1` (`destroyed-version`).
3. **Task 1 (`ErrGoneUpstream` Resilience)**:
   - Dispatched Task `#2` (`run 2-1`) whose scripted agent printed the raw value of `active-token` into both its live `.jsonl` event stream and its final narrative transcript while `destroyed-version` remained in `DESTROYED` state.
   - Daemon logged once:
     `grain secrets: destroyed-version/value is in the catalogue but secrets: catalogued key is gone from Secret Manager: ... (run \`grain secrets delete destroyed-version value\` to drop it from the catalogue, or set it again); leaving it out of redaction until it is reconciled`
   - Both `instances/walk-s6/transcripts/2-1.jsonl` and `instances/walk-s6/transcripts/2-1.md` uploaded cleanly to the object store with every occurrence of `SUPER_SECRET_ACTIVE_VALUE_99887766` replaced by `[redacted: secret active-token/value]` (`leakCount = 0`).
4. **Tasks 2 & 3 (Fail-Closed `503` / `403`, Raw Retention & Post-Fix Recovery)**:
   - Waited 31s past the 30s Secret Manager value cache TTL, flipped `active-token` to `HTTP 503` in Secret Manager and `PUT` to `HTTP 503` in the object store, and dispatched Task `#3` (`run 3-1`):
     - **0** object-store `PUT` attempts occurred while Secret Manager returned `503` (`leakCount = 0`).
     - Live file `state/transcripts/3-1` was unlinked by the orchestrator, while `state/transcripts.unflushed/3-1` (`600B`, `0600`) **and** `state/transcripts.unflushed/3-1.md` (`229B`, `0600`) were retained on disk.
     - Post-fix verification confirmed SQLite `task_run.transcript` length was `0` (`NULL`) and `GET /api/tasks/3/attempts/1/transcript` leaked **zero** secret bytes during the outage.
   - Switched Secret Manager to `HTTP 403 PERMISSION_DENIED` and object store to `HTTP 200` across a 6s periodic sync window: periodic retry continued to fail closed (`0` uploads, `leakCount = 0`, unflushed files preserved).
   - Switched Secret Manager back to `HTTP 200` and object store to `HTTP 503`, dispatched Task `#4` (`run 4-1`), verified `4-1` and `4-1.md` were retained alongside `3-1` and `3-1.md`, then restored the object store to `HTTP 200`:
     - Within one 2s periodic sync tick, `retryUnflushed` redacted and uploaded `3-1.jsonl`, `3-1.md`, `4-1.jsonl`, and `4-1.md` (`leakCount = 0`), unlinked all four staged files from `transcripts.unflushed/`, and `GET /api/tasks/3/attempts/1/transcript` served the redacted `[redacted: secret active-token/value]` narrative cleanly.

---

## §6.2 Stale-While-Revalidate `hostexec` Background Probes (`SIGSTOP`, `SIGCONT` & Socket Inode Swap)

### Methodology & Route Mapping

- Started a real `grain host` process (`pid 30037`) bound to `/tmp/walk-s6-hostexec/hostd.sock` (`inode 36199`) offering CLI `agy` and capability `walk-echo`, connected to a live `grain daemon` (`pid 30052`) with `-hostd-socket` and `-upgrade-src-dir` / `-upgrade-install-path` enabled.
- Verified healthy baseline (`upgrade.enabled = true`, `hostExecutor.hostd.reachable = true`, `entries = ["walk-echo"]`) across UI health/settings/upgrade routes:
  - `GET /api/upgrade` (Upgrade status)
  - `GET /api/sandboxes` & `GET /api/settings/cli-run` (backing `/system/health`)
  - `GET /api/settings` & `GET /api/capability-servers/host-entries` (backing `/settings` and `/settings/capabilities`)
- Sent `SIGSTOP` (`kill -STOP`) to `grain host` so its unix socket remained bound with an unread kernel accept queue, ran one cold request (`3,013ms` `hostdOffersTimeout = 3s`) to record the initial unreachable state, and then ran **8 concurrent clients** polling all 5 endpoints every `1s` across **40 seconds** (`1,600` total requests, spanning two `15s` `hostdUnreachableTTL` expirations) while sampling `/proc/<daemon-pid>/fd` + `/proc/net/unix` every `50ms`.

### Post-Fix Latency & Probe Coalescing Telemetry (8 Concurrent Clients × 40s under `SIGSTOP`)

| Endpoint | Touches `hostexec`? | Requests | Non-200 | p50 | p95 | p99 | Max | Requirement |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `GET /api/upgrade` | Yes (`ResolveCLIRuns`) | 320 | 0 | `1.98ms` | `2.96ms` | `4.39ms` | `17.37ms` | `<200ms` (**PASS**) |
| `GET /api/settings/cli-run` | Yes (`HostExecutorStatus` + `ResolveCLIRuns`) | 320 | 0 | `1.34ms` | `2.33ms` | `6.32ms` | `18.22ms` | `<200ms` (**PASS**) |
| `GET /api/sandboxes` | Yes (`HostExecutorStatus` + 100ms CPU sample) | 320 | 0 | `104.15ms` | `107.56ms` | `112.59ms` | `123.32ms` | `<200ms` (**PASS**) |
| `GET /api/capability-servers/host-entries` | Yes (`HostCapabilityEntries`) | 320 | 0 | `0.57ms` | `0.96ms` | `1.50ms` | `13.24ms` | `<200ms` (**PASS** after Bug #2 fix) |
| `GET /api/settings` | No (2×100ms `/proc/stat` CPU samples) | 320 | 0 | `204.69ms` | `207.60ms` | `797.38ms` | `996.85ms` | Zero `3.00s` `hostdOffersTimeout` stalls (**PASS**) |

- **Background Probe Coalescing (`/proc/<daemon-pid>/fd` + `/proc/net/unix` timeline)**:
  - `t = 0.15s`: `in_flight_probes = 0`, `cumulative_backlog = 1` (initial cold probe completed)
  - `t = 15.00s` → `t = 18.00s`: `in_flight_probes = 1`, `cumulative_backlog = 2` (single detached background probe after first 15s TTL expiry; all 8 concurrent callers served stale failure immediately)
  - `t = 33.00s` → `t = 36.00s`: `in_flight_probes = 1`, `cumulative_backlog = 3` (single detached background probe after second 15s TTL expiry)
  - **Max simultaneous in-flight background probes**: **`1`** (`max_backlog_step = 1`, `total_refresh_dials = 2`, `leaked_sockets = 0`).
- **`SIGCONT` & Socket Inode Replacement Recovery**:
  - Sent `SIGCONT` (`kill -CONT`) to `grain host`: once the active `15s` stale-failure TTL window elapsed (`12.69s`), the background refresh probe succeeded and `hostExecutor.hostd.reachable` flipped back to `true` (`120ms` poll).
  - Stopped and killed `grain host`, unlinked `hostd.sock`, and started a fresh `grain host` binding a new socket inode (`36199` → `36393`): `os.SameFile` target-state invalidation bypassed the 15s unreachable TTL and recovered **immediately on poll #1** (`118ms`).

---

## §6.3 `02:59:30 UTC` `faketime` System-Schedule 1-Hour Grace & `grain-agentd` Placement Diagnostics

### Part A: `2026-11-09 02:59:30 UTC` Startup (`MaxWorkers: 1`) & `03:50:00 UTC` Restart Preservation

- Because Go's Linux `x86_64` runtime reads wall time via kernel `[vdso]` (`AT_SYSINFO_EHDR`) and raw `SYSCALL 228` (`SYS_clock_gettime`), `faketime_go_exec.c` strips `AT_SYSINFO_EHDR` on `execve` (`PTRACE_EVENT_EXEC`) and installs a narrow BPF `SECCOMP_RET_TRACE` rule only for `CLOCK_REALTIME` / `CLOCK_REALTIME_COARSE` / `gettimeofday` / `time` (leaving `CLOCK_MONOTONIC` untouched so Go timers and tickers run smoothly), delegating realtime reads to `libfaketimeMT.so.1` (`FAKETIME="@2026-11-09 02:59:30 x10"`).
- Booted a fresh `grain daemon` (`-max-workers 1 -poll-interval 1s`) on an empty SQLite database at `2026-11-09 02:59:30 UTC` and created two user schedules (`sched-1` daily at `03:00 UTC`, `sched-2` daily at `03:02 UTC`; note user `everyNMinutes` schedules enforce `MinEveryNMinutes = 10`). Advanced across `03:00:00 UTC` .. `03:05:30 UTC`:

| Schedule ID | Kind | Seeded / Post-Window `next_run_at` (UTC) | `last_run_at` (UTC) | Firings in `03:00..03:05 UTC` |
| --- | --- | --- | --- | --- |
| `system:infra-review` | `daily` (`03:00`) | `2026-11-10 03:00:00` | *(empty)* | **0** (held past `+1h` grace) |
| `system:memory-agent` | `daily` (`03:00`) | `2026-11-10 03:00:00` | *(empty)* | **0** (held past `+1h` grace) |
| `system:pipeline-steward` | `everyNMinutes` (`360m`) | `2026-11-09 08:59:33.43` | *(empty)* | **0** (`+6h >= +1h` grace) |
| `system:release-notes` | `weekly` (`Mon 03:00`) | `2026-11-16 03:00:00` | *(empty)* | **0** (held past `+1h` grace; `2026-11-09` is Monday!) |
| `system:steward-audit` | `weekly` (`Mon 03:00`) | `2026-11-16 03:00:00` | *(empty)* | **0** (held past `+1h` grace; `2026-11-09` is Monday!) |
| `sched-1` (user) | `daily` (`03:00`) | `2026-11-10 03:00:00` | `2026-11-09 03:00:04.25` | **1** (`run 2-1` → `succeeded`) |
| `sched-2` (user) | `daily` (`03:02`) | `2026-11-10 03:02:00` | `2026-11-09 03:02:02.35` | **1** (`run 3-1` → `succeeded`) |

- **Restart Preservation at `03:50:00 UTC`**: Set `system:memory-agent`'s persisted `next_run_at` to `2026-11-09 04:10:00 UTC` (only 20 minutes ahead, inside a hypothetical `+1h` window if re-seeded) and restarted `grain daemon` at `FAKETIME="@2026-11-09 03:50:00"` on the existing SQLite DB. Across 3 reconcile cycles, all five `system:*` `next_run_at` values — including `2026-11-09 04:10:00 +0000 UTC` — were **preserved byte-for-byte**.

### Part B: Live `grain-agentd` (`agents-only`) Capability-Server Placement Diagnostics

1. **CLI Allowlist Validation (`grain-agentd -check`)**:
   - Rejected a `capabilities:` block with:
     `grain-agentd: .../bad-agentd.yaml names capabilities (walk-echo): grain-agentd runs agent CLIs only; remove them, or run \`grain host\`, which serves capabilities too`
2. **Live Enrollment & Direct Socket Refusal**:
   - Generated invite via `grain host invite`, connected real `grain-agentd` (`-join ghe_... -name agentd-walk`), and accepted pending enrollment.
   - Calling `hostd.Client.Run` for `KindCapability` `"walk-echo"` against `agentd-walk`'s live socket returned:
     `the host agent refused: capability "walk-echo" is not on this host's allowlist` (zero `hostd` substrings).
3. **REST, Pre-Placed Runtime Attach & Playwright Chromium UI (`/settings/hostds` & `/settings/capabilities`)**:
   - `PUT /api/capability-servers` & `POST /api/capability-servers/test` (`run: "host", hostd: "agentd-walk"`) both returned HTTP `400`:
     `run: host on host agentd-walk: agentd-walk runs agent CLIs only (grain-agentd), and its allowlist may not name capabilities, so it cannot run capability server "walk-echo"; place the server on a host enrolled with \`grain host\`, or run it in the container`
   - Pre-placed server `https://walk.example/cap/preplaced` (saved while `agentd-walk` was offline, then reconnected) surfaced in `GET /api/capability-servers` and on `/settings/capabilities` detail view (verified in headless Playwright Chromium at `1440×900`):
     `capability server https://walk.example/cap/preplaced: capability server walk-echo is set to run: host, but this host runs agent CLIs only (grain-agentd) and its allowlist may not name capabilities: place the server on a host enrolled with \`grain host\`, or run it in the container`
   - Driving `+ Add server` → `Run location: host (through the host agent)` → `Host: agentd-walk` → `Test` & `Add server` in Playwright displayed the `grain-agentd` / `enrolled with \`grain host\`` message cleanly with **`0px` horizontal overflow** and **zero standalone `hostd` leaks** on both `/settings/hostds` and `/settings/capabilities`.
   - `go test ./pkg/cli/... -run TestEveryCommandsHelpSaysHostNotHostd` passed.

---

## Bugs Fixed & Regression Tests Added

| Bug | Files Changed | Regression Test |
| --- | --- | --- |
| **Bug #1**: Fail-open SQLite `SetRunTranscript` fallback on `FlushRunEnd` failure leaked unredacted Secret Manager secrets; `.md` narratives were not staged/retried alongside `.jsonl` | `pkg/orchestrator/run.go`, `pkg/daemon/transcripts.go` | `TestRunDispatchWithFailedTranscriptFlushNeverLeaksSecretsToSQLiteAndRetriesNarrative` (`pkg/daemon/transcripts_unflushed_test.go`) |
| **Bug #2**: `GET /api/capability-servers/host-entries` bypassed `queryHostdOffers` with no timeout, `hostd.Client` leaked blocked sockets on context cancel without deadline, and `hostdTargetState` missed same-mtime socket inode swaps | `pkg/daemon/capability_servers_hostd.go`, `pkg/daemon/hostexec.go`, `pkg/hostd/client.go` | `TestHostCapabilityEntriesUsesStaleWhileRevalidateAndDetectsSameMtimeInodeReplacement` (`pkg/daemon/clirun_hostd_test.go`), `TestClientOffersAndHelloUnblockOnContextCancelWithoutDeadline` (`pkg/hostd/server_linux_test.go`) |
| **Bug #3**: Pre-placed `run: host` server on `grain-agentd` leaked `"grain hostd's allowlist"` and advised editing `/etc/grain/hostd.yaml` instead of checking `Hello.AgentsOnly` | `pkg/capability/protocol/client.go` | `TestAgentsOnlyHostRefusesPreplacedCapabilityServerWithoutHostdLeak`, `TestHostCapabilityServerIsRefusedUnlessHostdOffersIt` (`pkg/capability/protocol/hostd_test.go`) |
