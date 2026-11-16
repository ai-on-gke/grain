# Test Run — Granule `ccdbc4b` Re-Vendor & Real-Docker Concurrent `vm delete` Settlement (2026-11-13 Afternoon §4)

Plan: [`docs/test-plans/2026-11-13-afternoon.md` §4](../test-plans/2026-11-13-afternoon.md) (auditing PR #1444 / commit `002c34723`, filed from [Task #5366](/tasks/5366)).
Walkthrough task: [Task #5486](/tasks/5486). Commit walked: `ab445d443` (`main` at 2026-11-14 09:11 UTC).
Harness and raw logs: [`2026-11-13-granule-docker-delete/`](2026-11-13-granule-docker-delete/).

| Item | Verdict |
|---|---|
| 1. Vendored tree integrity & `-race` suites (`pkg/granule`, `third_party/granule/pkg/dockervm`, `pkg/cli`) | **Pass** on `go.mod` / byte-identity & `pkg/granule` / `pkg/cli`. **Finding F2 (upstream `gke-labs/granule`)**: `TestDelete_RemovalInProgressBoundsWait/context_cancelled` (`25ms` timeout) fails under cold-cache parallel `-race` compilation because `dockervm.Docker.run` returns `*exec.ExitError` (`signal: killed`) without wrapping `ctx.Err()`. |
| 2a. Two concurrent `granule.Command.Delete` calls against the same VM on real `dockerd` | **Fail on `002c34723` (F1), fixed in `pkg/granule` & filed upstream.** Docker 29.9.3 `docker inspect` prints lowercase `error: no such object: <name>`, which `third_party/granule`'s case-sensitive `isNotFound` (`"No such object"`) rejects at the end of `waitRemoved`. **Pass** after `pkg/granule` fix (`0` errors, `0` leftover state files). |
| 2b. Direct `docker rm -f <container>` racing `granule.Command.Delete` (orphan-sweep vs run-release) | **Fail on `002c34723` (F1): orphaned `<name>.json` left in `stateDir`.** Fixed in `pkg/granule` & filed upstream; **Pass** after fix (`nil` return on both callers, container gone, `stateDir` empty). |
| 2c. Concurrent `granule.RemoveContainers` calls racing `docker rm -f` on real `dockerd` | **Pass** before and after fix; hardened `RemoveContainers` / `RemovalInProgress` for case-insensitive stderr matching and `ctx.Err()` propagation. |
| 3. Short `context.WithTimeout` (`80ms`) on `Command.Delete` during active removal, then uncancelled cleanup | **Fail on `errors.Is(err, context.DeadlineExceeded)` before fix (F3), fixed in `pkg/granule`.** Non-nil error wrapping `context.DeadlineExceeded` returned (no silent `nil`), saved state preserved while container still exists in `dockerd`, and follow-up uncancelled `Command.Delete` clears both container and saved state cleanly. |

---

## Environment

- **Sandbox VM:** Debian 12 (`Linux 6.1.0-54-amd64`), 7 vCPU, 30 GiB RAM.
- **Go toolchain:** `go1.26.10 linux/amd64` (module `toolchain go1.26.11`).
- **Docker Engine:** real local `dockerd` — Docker Engine Community `29.9.3` (API `1.56`), `containerd` `v2.4.2`, `runc` `1.5.3`.
- **Vendored `granule` binary:** built directly from `github.com/gke-labs/granule/cmd/granule` (`=> ./third_party/granule`, `v0.0.0-20261112151335-ccdbc4b77525`).
- **Container workload for real-Docker races:** locally built `FROM scratch` image (`grain-walkthrough-granule-delete:local`) running a static Go PID 1 binary that populates `10,000`–`20,000` 4 KiB files across 128 subdirectories in `overlay2` (`docker rm -f` holds `State.Status == "removing"` in `dockerd` for ~`0.4 s` on stopped containers and ~`2.0`–`3.5 s` on `SIGTERM`-ignoring running containers, with zero external registry pulls). All containers, temporary directories, and images were removed on exit (`docker ps -a` and `docker images` verified empty).

---

## Item 1 — Vendored Tree Integrity & Race Suites

1. **Module & sync verification:**
   - `go.mod` line 9 requires `github.com/gke-labs/granule v0.0.0-20261112151335-ccdbc4b77525` with `replace github.com/gke-labs/granule => ./third_party/granule` (line 62) and `tool github.com/gke-labs/granule/cmd/granule` (line 64).
   - Matches [`docs/granule-local-patches.md`](../granule-local-patches.md) (`ccdbc4b77525a7f78e10e36f1b30efe35194f4c1`, PRs `gke-labs/granule#255`–`#256`, zero local patches in `third_party/granule`).
   - `git status -- third_party/granule` remained completely clean throughout this run, and `go test ./tests/hygiene/... ./pkg/ports/... ./tests/deploy/...` all passed.

2. **Race suites:**
   - `go test -race -count=1 ./pkg/granule/...`: **PASS** (`18.99s` baseline; `34.65s` under `-race -shuffle=on -count=2` with the new unit and real-Docker regression tests included).
   - `(cd third_party/granule && go test -race -count=1 ./pkg/dockervm/... ./pkg/cli/...)`:
     - On the initial cold-cache invocation (while `go test -race` compiled `./pkg/cli/...` in parallel across CPUs), `pkg/dockervm` failed in **`TestDelete_RemovalInProgressBoundsWait/context_cancelled`** (**Finding F2** below):
       ```text
       --- FAIL: TestDelete_RemovalInProgressBoundsWait (0.06s)
           --- PASS: TestDelete_RemovalInProgressBoundsWait/timeout (0.03s)
           --- FAIL: TestDelete_RemovalInProgressBoundsWait/context_cancelled (0.03s)
               docker_test.go:433: Delete() error = removing VM container granule-vm-web: docker rm -f granule-vm-web: signal: killed: , want context.DeadlineExceeded
       ```
     - Re-running with a warm build cache passed cleanly (`pkg/dockervm` `1.964s`, `pkg/cli` `3.345s`), as did `-cpu=1 -count=30`.

---

## Findings & Root-Cause Analysis

### F1 (P1) — Real Docker 28+/29 `docker inspect` writes lowercase `error: no such object:`, breaking `granule` `ccdbc4b`'s `waitRemoved` and orphaning VM state files

**Symptom on `ab445d443` (`002c34723`):**
Every concurrent `granule.Command.Delete` call that hits an active container removal on Docker 29.9.3 fails at the exact moment the container finishes disappearing, and leaves `<stateDir>/<name>.json` behind when the winning remover was `docker rm -f` ([`out/before-fix.txt`](2026-11-13-granule-docker-delete/out/before-fix.txt)):

```text
granule: vm delete wt5486-2b-race: exit status 1: granule: removing VM container granule-vm-wt5486-2b-race: docker inspect -f {{.State.Running}} granule-vm-wt5486-2b-race: exit status 1: error: no such object: granule-vm-wt5486-2b-race
leftover stateDir files=[wt5486-2b-race.json]
```

Additionally, `granule vm status <missing>` and `granule vm list -status` (when any listed VM has no container) fail outright on Docker 29.9.3 with the same error instead of reporting `ContainerMissing`.

**Root cause:**
1. In `third_party/granule/pkg/dockervm/docker.go:104-110`:
   ```go
   func isNotFound(err error) bool {
       if err == nil {
           return false
       }
       msg := err.Error()
       return strings.Contains(msg, "No such container") || strings.Contains(msg, "No such object")
   }
   ```
2. Docker 29.9.3 prints capital `"Error response from daemon: No such container: ..."` on `docker stop` / `docker rm`, **but prints lowercase `error: no such object: <name>`** on `docker inspect` (the same CLI output change `pkg/runhost/docker.go:221` previously handled with `strings.ToLower`).
3. Both `third_party/granule/pkg/dockervm/testdata/fakedocker/main.go:124` and `pkg/granule/granule_test.go:464` (`writeFakeDockerRemoving`) mocked uppercase `"No such object: <name>"`, hiding the bug from CI in both repositories.
4. When `granule vm delete` loses a `docker rm -f` race inside `dockerd`, `isRemovalInProgress` enters `waitRemoved`, which polls `d.containerState` (`docker inspect -f {{.State.Running}} <pod>`). While `dockerd` is removing the container (`State.Status == "removing"`), `docker inspect` exits `0` (`false`) and `waitRemoved` keeps polling. As soon as `dockerd` finishes removing the container, the next `docker inspect` exits `1` with `error: no such object: <pod>`, `isNotFound` returns `false`, `waitRemoved` returns that error, and `deleteVM` (`third_party/granule/pkg/cli/vm.go:741`) aborts **before** `vmspec.Delete(stateDir, name)` (line 745).

**Resolution (upstream-first + `pkg/granule` bridge):**
- **`third_party/granule` left untouched** (byte-identical to `gke-labs/granule` `ccdbc4b`).
- **Upstream proposal filed via `propose_task` targeting `repo: "gke-labs/granule"`** to make `isNotFound` and `isRemovalInProgress` case-insensitive in `pkg/dockervm/docker.go`, wrap `ctx.Err()` in `Docker.run` (fixing F2), and update `fakedocker` to emit Docker 29's lowercase `error: no such object: <name>` — paired with a dependent `ai-on-gke/grain` re-vendor proposal (`reads: ["gke-labs/granule"]`).
- **Immediate bridge in `pkg/granule/granule.go` on this branch:** when `Command.Delete` (or `Command.Create`'s `"removing stale "` pre-clean) fails with `ctx.Err() == nil` and `docker inspect` + `no such object` (case-insensitive), `granule` has already waited out the container's removal in `waitRemoved`; `Command.Delete` / `Command.Create` re-invokes `granule vm` once so `granule` sees the container gone (`docker stop` -> `No such container`, `docker rm -f` -> exit `0`) and deletes `<stateDir>/<name>.json`. `RemoveContainers` and `RemovalInProgress` were also made case-insensitive.

### F2 (P2, upstream `gke-labs/granule`) — `dockervm.Docker.run` drops `ctx.Err()` when `ctx` expires during `docker stop` or `docker rm -f`

In `third_party/granule/pkg/dockervm/docker.go:88-97`, `exec.CommandContext(ctx, ...).Run()` kills the child `docker` CLI with `SIGKILL` when `ctx` expires and returns `*exec.ExitError` (`"signal: killed"`). `waitRemoved` explicitly checks `if ctx.Err() != nil { return ctx.Err() }`, but `Docker.run` does not — so any `Delete` cancelled during `docker stop` or `docker rm -f` (including `TestDelete_RemovalInProgressBoundsWait/context_cancelled`'s `25ms` timeout under parallel `-race` builds) fails `errors.Is(err, context.DeadlineExceeded)`. Included in the upstream `gke-labs/granule` task proposal.

### F3 (P2, `pkg/granule`) — `Command.vm` and `RemoveContainers` did not wrap `ctx.Err()` when `exec.CommandContext` killed a child on context expiry

In `pkg/granule/granule.go`, `Command.vm` wrapped only `cmd.Run()`'s `*exec.ExitError` (`signal: killed`) when `ctx` timed out or cancelled (`errors.Is(err, context.DeadlineExceeded) == false`), and in `RemoveContainers` an expired `ctx` after `docker rm -f` had printed `"No such container: <pod>-netns"` to stderr would fall through the stderr line loop and return `nil`. Fixed in `pkg/granule/granule.go` by checking `if ctx.Err() != nil` in both `Command.vm` and `RemoveContainers` and wrapping `%w (%w)` (`ctx.Err()`, `err`).

---

## Item 2 — Concurrent `vm delete` and `RemoveContainers` on Real `dockerd`

Driven by `go run docs/test-runs/2026-11-13-granule-docker-delete/walkthrough.go` ([`out/before-fix.txt`](2026-11-13-granule-docker-delete/out/before-fix.txt) vs [`out/after-fix.txt`](2026-11-13-granule-docker-delete/out/after-fix.txt)):

1. **Case `2a-simultaneous` (two concurrent `Command.Delete` calls launched together against `wt5486-2a-sim`, 15,000 overlay files):**
   - Both callers ran `docker stop -t 1 granule-vm-wt5486-2a-sim` (`rc=0`), then collided inside `dockerd` on `docker rm -f`: the losing caller received `Error response from daemon: removal of container granule-vm-wt5486-2a-sim is already in progress`, polled `docker inspect -f {{.State.Running}}` 5 times while `dockerd` unlinked the overlay, observed `error: no such object: granule-vm-wt5486-2a-sim` (`rc=1`), and completed state removal cleanly.
   - Outcomes after fix: `caller[0]` `dur=3.339s err=<nil>`, `caller[1]` `dur=3.696s err=<nil>`, `docker inspect` `rc=1` (`error: no such object: granule-vm-wt5486-2a-sim`), `stateDir` entries `[]` (`granule.Exists=false`).

2. **Case `2a-removing` (`Command.Delete` #2 launched once `dockerd` reported `State.Status == "removing"` on `wt5486-2a-rem`, 20,000 overlay files):**
   - Initiator `dur=641ms err=<nil>`, racer `dur=621ms err=<nil>`; passthrough log confirmed real `dockerd` emitted `removal of container granule-vm-wt5486-2a-rem is already in progress`, followed by 4 `docker inspect` polls at `rc=0` and the 5th at `rc=1` (`error: no such object:`), then clean state deletion (`stateDir` entries `[]`).

3. **Case `2b` (direct `/usr/bin/docker rm -f` racing `granule.Command.Delete` on `wt5486-2b-race`, 10,000 overlay files, `IGNORE_TERM=1`):**
   - Before fix: `docker rm -f` returned `<nil>` (`2.093s`), while `Command.Delete` failed (`exit status 1`) and orphaned `wt5486-2b-race.json` in `stateDir`.
   - After fix: `docker rm -f` `dur=2.092s err=<nil>`, `Command.Delete` `dur=424ms err=<nil>`, `docker inspect` `rc=1`, `stateDir` entries `[]` (`granule.Exists=false`).

4. **Case `2c` (4 concurrent `granule.RemoveContainers` calls racing real `docker rm -f` on stateless `wt5486-2c-rm`):**
   - While `docker rm -f` held `granule-vm-wt5486-2c-rm` in `status="removing"` (`dur=2.114s err=<nil>`), all 4 racing `RemoveContainers` calls hit `removal of container ... is already in progress` and returned `<nil>` in `51`–`80ms`. Post-settlement `docker inspect` confirmed `rc=1` (`error: no such object:`).

---

## Item 3 — Timeout / `ctx` Cancellation Error Contract (`Command.Delete`)

1. **Case `3a` (unshimmed real `dockerd` `removing` state on `wt5486-3a-timeout`, `context.WithTimeout` = `80ms`):**
   - `c.Delete(shortCtx, stateDir, "wt5486-3a-timeout")` returned in `81ms` with non-nil error:
     `granule: vm delete wt5486-3a-timeout: context deadline exceeded (signal: killed)` (`errors.Is(err, context.DeadlineExceeded) == true`).
   - Immediately after the timeout returned, real `docker inspect` confirmed the container was still in `status="removing"` (`rc=0`) and `granule.Exists(stateDir, "wt5486-3a-timeout") == true` (`wt5486-3a-timeout.json` preserved).
   - Follow-up uncancelled `c.Delete(ctx, stateDir, "wt5486-3a-timeout")` waited out the remaining `389ms` of teardown, returned `<nil>`, left `docker inspect` at `rc=1`, and cleared `stateDir` (`[]`).

2. **Case `3b` (thin passthrough `/usr/bin/docker` shim holding `inspect` during active removal on `wt5486-3b-shim`, `context.WithTimeout` = `80ms`):**
   - `c.Delete(shortCtx, ...)` returned in `82ms` with `granule: vm delete wt5486-3b-shim: context deadline exceeded (signal: killed)` (`isDeadlineExceeded=true`), preserving `wt5486-3b-shim.json`.
   - Releasing the `inspect` hold and calling `c.Delete(ctx, ...)` returned `<nil>`, removed the container (`docker inspect` `rc=1`), and left `stateDir` empty (`[]`).

---

## Regression Tests Added (`pkg/granule/granule_test.go`)

- `TestDeleteReportsARemovalThatNeverSettles`: strengthened to assert `errors.Is(err, context.DeadlineExceeded)`.
- `TestDeleteFinishesWhenDocker29InspectPrintsLowercaseNoSuchObject`: drives the compiled vendored `granule` binary against Docker 29's lowercase `error: no such object: <name>` `inspect` stderr and verifies `Command.Delete` returns `nil` and removes `<name>.json`.
- `TestCreateFinishesWhenDocker29StaleCleanupPrintsLowercaseNoSuchObject`: verifies `Command.Create` recovers when stale-container cleanup trips on lowercase `error: no such object:`.
- `TestRemoveContainersHandlesCaseInsensitiveErrorsAndCancellation`: verifies lowercase stderr handling and ensures an expired `ctx` returns `context.DeadlineExceeded` rather than `nil`.
- `TestRealDockerConcurrentDeleteSettlement`: runs against live `dockerd` whenever reachable (skipping cleanly when `docker info` is unavailable or `-short` is set), racing `docker rm -f`, short-timeout `Command.Delete`, uncancelled `Command.Delete`, two concurrent `Command.Delete` calls, and `RemoveContainers` on real containers.
