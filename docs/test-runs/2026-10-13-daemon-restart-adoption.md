# A daemon restart adopts the run, which opens its pull request (2026-10-13)

Task 3282, stage 2c of
[`non-disruptive-upgrade.md`](../non-disruptive-upgrade.md): the upgrade
protocol's flush and adopt (piece 6), and the acceptance test. This
records what was run in the task's sandbox, and what was not.

## What was run

Environment: the grain task sandbox (Debian, docker 29.9.0), branch
`grain/eu/task-3282`.

1. **The acceptance test against a real daemon and real docker.**
   `GRAIN_RUNHOST_DOCKER=1 go test -run
   TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest -v ./cmd/grain/`.
   - Two `daemon.Run`s, one after the other, over one data directory.
   - `-run-host-image` is an image built from `debian:bookworm-slim` with
     this checkout's `grain` at `/usr/local/bin/grain`.
   - The agent is a stub claude CLI in the shared credential directory.
     It writes one assistant line, waits for a file the test writes after
     the restart, then writes a second line, one tool call and a success
     result.
   - GitHub is a `githubsim` over a bare repository. The test pushes the
     task's branch while the run is held open, because the stub has no
     sandbox tools to push with.

   The first attempt failed with:

   ```
   the adopted run ended "interrupted" (the daemon driving this run was restarted, and the one
   that adopted it cannot read a run host's spool with its framework (*daemon.BackendFramework);
   finished as interrupted and re-queued without a strike)
   ```

   So stage 2b's adoption had never resumed a run in a real daemon. The
   connection pool wraps every framework in `daemon.BackendFramework`,
   which was not an `agent.ResultParser`. The wrapper now delegates
   `ParseResult` to the framework it wraps and releases its connection
   lease, and `ResumeAdoptedRun` looks through it (`Unwrap`) before
   deciding. After that fix the test passed:

   ```
   runhost: daemon stopping; leaving run host …-runhost-run-runhost-restart-1-… (run runhost-restart-1) running at cursor 193
   orchestrator: run runhost-restart-1: left running in its run host for the next daemon to adopt
   orchestrator: adopted run host …-runhost-run-runhost-restart-1-… for run runhost-restart-1 at cursor 193 after a 0s gap
   grain daemon: run-host adoption: 1 adopted, 0 lost, 0 unclaimed run host(s) freed
   orchestrator: resuming adopted run runhost-restart-1 (task runhost-restart) from cursor 193; 0s of restart credited
   --- PASS: TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest (9.58s)
   ```

   What it checks:
   - The run host was still listed after the first daemon returned, and
     its row was still live.
   - No pull request existed before the restart. The second daemon then
     opened exactly one, for the task's branch.
   - The finished run is the same row, with outcome `succeeded`.
   - Its `StartedAt` is unchanged. The adopted run's deadline is
     `StartedAt` plus its budget (`newAdoptedRunClock`), so it was not
     reset.
   - The stored transcript holds the line written before the restart
     and the line written after it exactly once each, and in order.
   - No run host is left afterwards.

   The "0s gap" is accurate here: the cursor was persisted at the
   detach, and the second daemon started within the same second.
2. **The flush.** `go test -race -run Flush ./pkg/runhost/` and `go test
   -run 'Flush|Hold|Restart|Queue' ./pkg/upgrade/` both pass.
   - `Host.Flush` detaches a tailing run and persists its cursor at
     exactly what the tail had written. A tail that starts mid-flush
     detaches at once.
   - A second Host attached from that cursor joins the transcript with
     no hole.
   - `Resume` lets this daemon tail again.
   - The upgrader flushes after staging and before `RestartCmd`. It
     resumes if the restart leaves the process running, and flushes
     nothing when staging fails.
3. **The report.** `go test -run UpgradeCheck ./pkg/orchestrator/`
   passes. The post-upgrade check's body reports adopted and lost runs
   with their reasons, and the count of freed run hosts. It leaves out an
   adoption pass older than the upgrade.

## What was not run

- **The hand run on a real deployment** (a real agent on a subscription
  login, daemon restarted mid-task). A task sandbox has neither a
  deployment nor a subscription login, so it could not be done here.
  It is filed as a follow-up.
- **A real upgrade end to end.** The acceptance test restarts the daemon
  by stopping it, which takes the stop's detach path (`Host.Detach`).
  The upgrade's own flush path (`Host.Flush` before `RestartCmd`) is
  covered by the unit tests in 2 above, but no test drives a real
  upgrade into a run host. `tests/container`'s image suites would be the
  place for that. They need the release image and a systemd unit to
  restart, which the "run host" job does not have.
- **Kubernetes.** The flush is not wired on the Kubernetes path (see
  `upgrade.Config.Flush`).
