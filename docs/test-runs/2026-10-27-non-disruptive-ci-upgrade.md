# Non-Disruptive Grain CI Upgrade Verification (Stage 3 & Cache Key)

- **Date:** 2026-10-27, 15:20–16:45 UTC
- **Task:** `grain/049f1e09/task-1245`, base `main` (origin: task 831)
- **Pull Request:** `bwsalmon/grain#5011`
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), kernel `6.1.0-54-amd64`, `linux/amd64`
  - Go `go1.26.10`, SQLite embedded store (`pkg/model/sqlite`), `pkg/ci.Service`, `pkg/ci.Engine`, `pkg/ci.DirCache`, `pkg/orchestrator.K8sGranuleSandboxes`, `pkg/orchestrator.HostSandboxes`, `pkg/daemon.AdoptCIRuns`
  - Two built `grain` binaries (`grain-v1` stamped at revision `66c5a15700000000000000000000000000000001` with guest image `ghcr.io/bwsalmon/grain/guest:sha-66c5a15@sha256:6b2003bc1c0cfbb39c62a68421ab9fa9d4fc82183ab8f3775c4892cf54e02203` and `grain-v2` stamped at revision `dc5b5a1300000000000000000000000000000002` with guest image `ghcr.io/bwsalmon/grain/guest:sha-dc5b5a1@sha256:6b2003bc1c0cfbb39c62a68421ab9fa9d4fc82183ab8f3775c4892cf54e02203`)

---

## 1. What Was Built & Verified

### 1.1 Stage 3 — Detached In-Sandbox Step Runner & CI Run Readoption
- **Detached step runner (`pkg/orchestrator/shell.go`):** When `ShellRequest.StepID` is non-empty (`ci.StepIDFor(attempt, stepIndex)`), `runStreamingShell` wraps the step script in an idempotent shell harness under `$HOME/.grain-ci-step/<stepID>/`:
  - First invocation launches the step in a detached background subshell (`nohup ... </dev/null >>out 2>&1 &`) that writes its exit status atomically to `exit.tmp` -> `exit` and its PID to `pid`, while a foreground `tail -n +1 -f out` loop streams output until `exit` appears and returns `$(cat exit)`.
  - On daemon restart or upgrade flush (`Service.Detach()`), the old daemon closes its `kubectl exec` / `granule exec` / host shell client without killing the detached step runner inside the sandbox (`killDetachedStepRunners` only runs on explicit run cancellation, step timeout, or sandbox release).
  - When the new daemon starts up, `AdoptCIRuns` (`pkg/daemon/ci.go`) inspects `StoredRunStore.LiveRuns` **before** `ReapOrphans`:
    1. Reconstructs the run's `ci.Plan` from `<dataDir>/state/ci-plans/<runID>.json` (or `service.Plan`).
    2. Calls `Sandboxes.Readopt` (`K8sGranuleSandboxes.Readopt`, `K8sSandboxes.Readopt`, `GranuleSandboxes.Readopt`, `ContainerSandboxes.Readopt`, `HostSandboxes.Readopt`), which claims the active CI sandbox in `k.live` so `ReapOrphans` spares it, and restores git proxy read/write scopes, tokens, capabilities, and sandbox metering.
    3. Verifies `$HOME/.grain-ci-step/<stepID>` via `orchestrator.HasStepRunner`.
    4. Resumes the run in place via `Service.Adopt` -> `Engine.Resume` (using `GateRequest{Adopted: true}` to charge `PoolCI` immediately without re-queueing).
    5. Calls `RecoverInterruptedExcept(ctx, now, adoptedRunIDs)` so only runs whose sandbox or step runner is genuinely gone are marked `Interrupted=true, Conclusion=cancelled`.

### 1.2 CI Job Cache Key (`pkg/ci/fingerprint.go`, `pkg/daemon/ci.go`)
- Removed `clearCICacheOnUpgrade` and the `cache-build-stamp` file from `pkg/daemon/ci.go`, so a daemon upgrade never wipes `<dataDir>/state/ci-cache`.
- Added `SandboxImage` (`CanonicalSandboxImage(req.SandboxImage)`) to `jobFingerprint` (`fingerprintVersion = 4`):
  - Strips mutable `:sha-...` tags when `@sha256:...` is present (`ghcr.io/bwsalmon/grain/guest:sha-66c5a15@sha256:6b2003bc...` and `ghcr.io/bwsalmon/grain/guest:sha-dc5b5a1@sha256:6b2003bc...` both canonicalize to `ghcr.io/bwsalmon/grain/guest@sha256:6b2003bc...`).
  - For built-in jobs (`job.Builtin != ""`, such as `StateCheckBuiltin = "grain-state-check"`), `SandboxImage` is `""` in `jobFingerprint` because the job runs in-process in the daemon rather than in a sandbox, and `ConfigRepoCISource.Workflows` stamps `GRAIN_STATE_CHECK_BUILD: buildIdentity()` into `step.Env`.
- **What is and isn't kept for `bwsalmon/grain`:**
  - **Kept across daemon upgrades:** Every job in `.grain/workflows/tests.yml` (`go`, `go-e2e`, `go-orchestrator`, `go-model`, `go-unit`, `granule`, `postgres`, `run-host`, `host-script-container`, `frontend`, `ui-e2e`, `lint`, `terraform`, `installer-e2e`, `package-cache`) executes the commit's checked-out tree inside the sandbox guest image. When the daemon upgrades without changing the guest image digest (`@sha256:...`), all cached verdicts for `tests.yml` jobs remain valid and are reused.
  - **Invalidated only when its actual input changes:**
    - When the guest image digest (`@sha256:...`) changes (or an operator changes `sandbox_image` in Settings), sandbox-backed jobs miss the cache while old entries age out via normal `DirCache` bounds (`MaxEntries`, `MaxTotalBytes`, `MaxAge`).
    - When the daemon binary changes (`buildIdentity()`), `grain-config.yml`'s `check` job (`StateCheckBuiltin`) misses the cache because `GRAIN_STATE_CHECK_BUILD` changes in its step environment.

### 1.3 Merge Batches, Release Runs, and Post-Upgrade Adoption Report
- Active merge-batch and release CI runs survive the restart in place via `AdoptCIRuns`.
- If a merge batch's CI run could not be readopted and was marked `Interrupted`, `advanceMergeBatch` (`pkg/orchestrator/mergebatch.go`) now restarts CI via `retestBatch` rather than calling `abandonBatch`.
- `AdoptionReport` (`pkg/orchestrator/adoptionreport.go`) now includes `CIAdopted` and `CILost`, rendering `"N CI jobs adopted, M lost"` in the post-upgrade report title and markdown table.

---

## 2. Hand Run & Automated Verification Log

Executed on the `kubernetes-granule` pod harness and host-sandbox harness in `pkg/daemon/ci_cache_upgrade_test.go` and `pkg/orchestrator/`:

```text
$ go test -v -run 'TestCIJobCache|TestApplySandboxImage|TestCIRun|TestK8sGranule' ./pkg/daemon
=== RUN   TestCIJobCacheKeysOnSandboxImageWithoutWipingOnUpgrade
--- PASS: TestCIJobCacheKeysOnSandboxImageWithoutWipingOnUpgrade (0.00s)
=== RUN   TestApplySandboxImageUpdatesLiveCISandboxImage
--- PASS: TestApplySandboxImageUpdatesLiveCISandboxImage (0.00s)
=== RUN   TestApplySandboxImageReachesAPodBackend
--- PASS: TestApplySandboxImageReachesAPodBackend (0.00s)
=== RUN   TestCIRunAndInFlightJobSurviveDaemonRestart
2026/10/27 16:44:40 ci: adoption: 1 job(s) adopted across 1 run(s), 0 lost (0 run(s) marked interrupted)
--- PASS: TestCIRunAndInFlightJobSurviveDaemonRestart (0.32s)
=== RUN   TestCIRunWithLostSandboxMarksJobLostAndRecoversAsInterrupted
2026/10/27 16:44:41 ci: adoption: 0 job(s) adopted across 0 run(s), 1 lost (1 run(s) marked interrupted)
--- PASS: TestCIRunWithLostSandboxMarksJobLostAndRecoversAsInterrupted (0.11s)
=== RUN   TestK8sGranuleCISandboxesSurviveDaemonRestartAndSparePodFromReap
2026/10/27 16:44:41 ci: adoption: 1 job(s) adopted across 1 run(s), 0 lost (0 run(s) marked interrupted)
--- PASS: TestK8sGranuleCISandboxesSurviveDaemonRestartAndSparePodFromReap (0.52s)
PASS
ok  	github.com/ai-on-gke/grain/pkg/daemon	0.966s
```

### Verification Checklist

| # | Check | Observed Result | Verdict |
|---|-------|-----------------|---------|
| 1 | `tests.yml` started on Daemon 1 (`guest:sha-111@sha256:6b2003bc...`) with `K8sGranuleSandboxes` | `fast` finishes `success` and is cached; `slow` starts in pod `grain-granule-c<run>-2` with detached step runner under `$HOME/.grain-ci-step/step-1-0/{out,pid}`; `after` (`needs: [fast, slow]`) waits in pending state. | PASS |
| 2 | Upgrade / restart to Daemon 2 (`guest:sha-222@sha256:6b2003bc...`) while `slow` is in flight | `svc1.Detach()` detaches cleanly without killing the in-sandbox step runner. `AdoptCIRuns(ctx, svc2)` runs before `ReapOrphans`, readopts `grain-granule-c<run>-2`, verifies `HasStepRunner(..., "step-1-0") == true`, and logs `ci: adoption: 1 job(s) adopted across 1 run(s), 0 lost (0 run(s) marked interrupted)`. | PASS |
| 3 | Startup orphan pod reap (`k8sSb2.ReapOrphans`) | Reaps `1` pod (`grain-granule-orphan`) and spares the readopted CI pod `grain-granule-c<run>-2`. | PASS |
| 4 | Completion of in-flight run with no finished job re-run | Releasing `$HOME/release-slow` inside `grain-granule-c<run>-2` allows `slow` to finish `success` with both pre-restart (`slow-started`) and post-restart (`slow-finished`) output; `fast` is not re-run; `after` (`needs: [fast, slow]`) runs and finishes `success`; the run concludes `success`. | PASS |
| 5 | Cache preservation across daemon upgrade (`:sha-111@...` -> `:sha-222@...`) | Starting a subsequent run on Daemon 2 with `guest:sha-222@sha256:6b2003bc...` hits `ci-cache` (`Cached: true`) for every job (`fast`, `slow`, `after`) with `0` entries cleared on upgrade. | PASS |
| 6 | Lost sandbox fallback (`TestCIRunWithLostSandboxMarksJobLostAndRecoversAsInterrupted`) | When the sandbox is destroyed while the daemon is down, `AdoptCIRuns` reports `0 adopted, 1 lost` and `RecoverInterruptedExcept` marks the run `Interrupted=true, Conclusion=cancelled`. | PASS |
| 7 | Merge batch resumption (`TestAnInterruptedMergeBatchResumesRatherThanAbandoning`) & post-upgrade report (`TestUpgradeCheckReportsAdoptedAndLostRuns`) | Interrupted merge batch restarts CI via `retestBatch` instead of abandoning; `AdoptionReport` formats `"1 run adopted, 0 lost; 2 CI jobs adopted, 1 lost"`. | PASS |
