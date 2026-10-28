# Walkthrough: upgrade drain window, `interrupted` outcome, client retry

- **Date:** 2026-10-12
- **Plan:** `docs/test-plans/2026-10-11.md` §7
- **Commit under test:** `b660114c6` (on `candidate`; branch cut from `candidate` at `c56e11751`)
- **Task:** 3143 (filed by the nightly test-plan task 2955)

## Environment

- Debian 12 sandbox, Go toolchain from the repo, Node `v20.19.0`, Playwright Chromium headless shell `151.0.7922.34`.
- A real `grain daemon`, built with `go build -tags grain_e2e ./cmd/grain` and with the frontend embedded
  (`cd ui && npm ci && npm run build`). The `grain_e2e` tag adds the local-directory sandbox backend that the
  browser suite already uses. No other sandbox backend can run here, since host sandboxes have been removed and
  there is no granule/container runtime.
- The daemon was started with:
  `-data-dir /tmp/wt/data -sandbox-dir /tmp/wt/sbx -ui-addr 127.0.0.1:8420 -agent-framework claude
  -claude-path /tmp/wt/claude-stub -claude-oauth-token-file /tmp/wt/claude-token -poll-interval 2s -max-workers 1`.
  Step 2 added `-upgrade-src-dir`, `-upgrade-install-path`, `-upgrade-restart-cmd` and `-upgrade-drain-window`.
- Readiness was checked with `GET /api/sandboxes`.
- Stopping the daemon meant `kill -TERM <pid>`. Restarting it meant running the same command line again over the same `-data-dir`.
- The CLI was the same binary (`grain list`, `grain create`, `grain comment`) on its default `http://127.0.0.1:8420`.
- The store was read directly with `sqlite3` on `data/store/grain.db` (`task_run`, `task_run_tool`, `task_observation`).

### Stubs

| Boundary | Stub | Why |
|---|---|---|
| Agent / model (claude CLI) | `/tmp/wt/claude-stub`: a shell script that logs its start, prints one `run_command` tool call in stream-json, sleeps `$STUB_SLEEP` seconds (600 by default; 40 in the window-0 case), then prints a `result` | The only stub the plan allows. It holds a run open until the daemon kills it. |
| Upgrade build (`make container-build`, a docker build on a real host) | `-upgrade-src-dir` is a git clone of a local bare repo whose `Makefile`'s `container-build` copies an already-built grain binary to `bin/grain` (`bin/` is gitignored) | No docker build inside the sandbox. Checkout, install, health check, schema step and restart all ran for real. |
| `systemctl restart grain-daemon` | `-upgrade-restart-cmd /tmp/wt/restart.sh`: a detached script that SIGTERMs the daemon, waits for it to exit, and starts the installed binary with the same flags | No systemd unit in the sandbox |

Nothing in grain's own API, store or UI was mocked.

## Step 1: SIGTERM under a live run, then restart

1. `grain create -approve -no-repo -title "long run for drain walkthrough"` filed task 2. The daemon dispatched run `2-1` at 01:12:33, and the stub started.
2. Sent `kill -TERM` at 01:12:55.09. The daemon exited 0.5 s later. Its journal:
   ```
   01:12:55 grain daemon: shutting down; waiting up to 4m30s for 1 run(s) to release their sandboxes: [2-1]
   01:12:55 orchestrator: run 2-1: recording what it did with its tools: context canceled
   01:12:55 orchestrator: run 2-1 (task 2): orchestrator: run 2-1: interrupted by daemon shutdown: context canceled
   orchestrator: finishing run 2-1: context canceled
   01:12:55 grain daemon: every run in flight at shutdown has finished
   ```
3. The store after the stop:
   - `task_run 2-1`: outcome `interrupted`, finished at 01:12:55.09, detail `interrupted by daemon shutdown or upgrade; re-queued for next dispatch; the run made 1 tool call(s) [run_command x1] …`
   - `task_streak`: no row for task 2, so the streak is 0. `GET /api/tasks/2` carries no `failedAttempts`.
   - The transcript was empty, and there were 0 `task_run_tool` rows even though the run made one tool call. This is **finding 2**.
4. The restart at 01:13:46 dispatched run `2-2` at 01:13:46, the same second the daemon came up. The interrupted run imposed no retry backoff. The task resumed.
5. Two more SIGTERM/restart cycles (`2-2`, then `2-3`) ended the same way. After three interrupted runs the streak was still 0. Run `2-3` was stopped by a daemon built with the fix for finding 2, and kept its transcript (36 bytes) and its `task_run_tool` row. Its journal had no `finishing run` error.
6. Playwright opened `/tasks/2` with `X-Grain-Admin-Mode: on`. Results:
   - The Timeline shows "Interrupted (daemon restarted)" on both finished attempts. Each sits in an `<li class="timeline-item timeline-item-attempt">` whose marker is `<span class="badge badge-closed">`. Attempt #2 reads "Attempt #2 · Interrupted (daemon restarted)".
   - There is no "consecutive failed attempt" banner on the page.

**Result:** pass. The outcome is `interrupted` (not `failed`), the failure streak and backoff are unchanged, the badge shows, and the task resumes. The transcript loss is finding 2, which is fixed here.

## Step 2: scheduled upgrade under a drain window

### `-upgrade-drain-window 30s`, with a run active

| Time (UTC) | Event |
|---|---|
| 01:19:38.29 | run `2-4` dispatched (stub sleeps 600 s) |
| 01:19:47.85 | `POST /api/upgrade {"branch":"candidate","afterTasksFinish":true}` returned 202, `scheduled.busyRuns: 1` |
| 01:19:48 | `orchestrator: holding the queue until this deployment is idle, then upgrading to candidate (1 run still to finish)` |
| 01:19:48 to 01:20:18 | Waiting. Queue held, no dispatch, run `2-4` still live. |
| 01:20:18 | `orchestrator: upgrade drain window 30s elapsed (1 run still to finish); proceeding with upgrade to candidate` |
| 01:20:18 | `orchestrator: this deployment is idle -- starting the upgrade to candidate` (wrong, see **finding 3**) |
| 01:20:18 | upgrade steps 1–5 (checkout, build, install, health check, schema "unchanged: both builds are at schema 17") succeeded in 231 ms. Step 6 ran `restart.sh` |
| 01:20:18.55 | the restart SIGTERMed the old daemon. `run 2-4: interrupted by daemon shutdown` |
| 01:20:18.75 | the old daemon exited. The new one was ready 1 s later |
| 01:20:18 | new daemon: `an upgrade held the queue over this restart -- the pipeline is picking work up again` and `filed task 3 to check the upgrade to candidate (auto)` |
| 01:20:18.98 | run `2-5` dispatched, so the interrupted task resumed |

The upgrade started 30.2 s after it was scheduled. Run `2-4` ended `interrupted` with its transcript kept. The streak stayed 0.

### `-upgrade-drain-window 0`, with runs that finish (stub sleeps 40 s)

| Time (UTC) | Event |
|---|---|
| 01:20:55.94 | runs `2-6` and `3-2` dispatched |
| 01:21:00.37 | upgrade scheduled (202) |
| 01:21:01 | `holding the queue until this deployment is idle, then upgrading to candidate (2 runs still to finish)` |
| 01:21:01 to 01:21:35 | Waiting. No "drain window elapsed" line, so it did not proceed early. |
| 01:21:35.97 / 01:21:35.99 | `2-6` and `3-2` finished on their own, both `succeeded` |
| 01:21:37 | next tick: `this deployment is idle -- starting the upgrade to candidate` |

That first attempt failed at checkout ("`/tmp/wt/src` has uncommitted changes"). The cause was my build stub leaving an untracked `bin/grain`, not grain. I added `bin/` to the stub repo's `.gitignore` and scheduled again at 01:22:05.45. The deployment was idle by then, so the upgrade started in the same tick and succeeded in 323 ms. `restart.sh` ran at 01:22:06.29, the old daemon exited at 01:22:06.50, and the new one was ready about 1 s later.

**Result:** pass. A 30 s window waits 30 s and then proceeds, and the run it cuts short is recorded as `interrupted` and resumes. A window of 0 waits as long as the runs take and proceeds the tick after they finish.

## Step 3: CLI reads and one POST across a daemon restart

`/tmp/wt/step3.sh` does the following:
- runs `grain list` every 0.3 s for 25 s;
- SIGTERMs the daemon at t≈3 s;
- once the daemon has exited, sends one `grain comment 4 "exactly-once-<tag>"`;
- starts the daemon again 4 s later;
- counts the comments with that body.

| CLI | Daemon down | Reads | POST | Comment count |
|---|---|---|---|---|
| fixed (this branch) | 01:24:22.37 → 01:24:26.38 (ready ≈01:24:27) | every read rc=0. The one in flight over the gap took 7716 ms (retried), the rest ≈25–30 ms | rc=0 after 7778 ms | **1** |
| `candidate` as shipped | 01:24:52.81 → 01:24:56.81 | every read rc=0, one took 7962 ms | rc=0 after 7380 ms | **1** |

Over a plain restart on loopback, both clients apply the POST exactly once. A refused connection means no request was sent, and the daemon's UI server shuts down gracefully (`httpSrv.Shutdown`), so nothing it had read was lost.

That result does not hold when a proxy sits in front of the daemon (the relay or IAP), or when a request is dropped after the daemon read it. Under the shipped client a single POST is then applied up to ten times. This is **finding 1**, with a deterministic reproduction below.

**Result:** reads recover via retry. The POST was applied exactly once in the restart that was run. The client's general policy duplicates POSTs (finding 1), which is fixed here.

## Findings

### 1. The CLI client resent non-idempotent requests, duplicating them (bug, fixed)

`pkg/ui/httpclient.go` (`HTTPClient.do`) retried every method, up to 10 attempts, after any of these:
- a transport error: EOF, connection reset, or any `net.Error`;
- a 502, 503 or 504 response.

A POST the daemon had read and applied, followed by a dropped connection, was sent again. So was one a gateway answered with 502/504 after forwarding it.

**Reproduction (deterministic):** `TestHTTPClientDoesNotResendAPOSTTheServerMayHaveApplied` in `pkg/ui/httpclient_test.go`. Its `httptest` server reads the request body, counts it, then hijacks and closes the connection without answering. One `AddComment` call against `candidate` as shipped:
```
--- FAIL: TestHTTPClientDoesNotResendAPOSTTheServerMayHaveApplied (19.32s)
    httpclient_test.go:655: the server received the POST 10 times, want exactly 1
--- FAIL: TestHTTPClientDoesNotResendAPOSTOnAGatewayError (58.15s)
    status 502: the server received the POST 10 times, want exactly 1
    status 503: the server received the POST 10 times, want exactly 1
    status 504: the server received the POST 10 times, want exactly 1
```

**Fix:**
- GET, HEAD and OPTIONS keep the full retry policy.
- Any other method is retried only after a failed dial (`*net.OpError` with `Op == "dial"`), the one failure that proves the request never reached a server. That is the daemon-restart case step 3 exercised, and it still lands the comment once.
- PUT and DELETE are deliberately treated as non-idempotent: grain's handlers make no promise to answer a repeat the way they answered the first.

**Regression tests (`pkg/ui/httpclient_test.go`):**
- `TestHTTPClientDoesNotResendAPOSTTheServerMayHaveApplied`
- `TestHTTPClientDoesNotResendAPOSTOnAGatewayError`
- `TestHTTPClientRetriesAGETOverADroppedConnection`
- `TestHTTPClientRetriesAPOSTWhileTheDaemonIsNotListening`

### 2. An interrupted run lost its transcript and tool census (bug, fixed)

b660114c6 detached `FinishRun` and `SetSuspendedSession` from the daemon's cancelled context, but not the writes after them. Those still ran on the cancelled `ctx`:
- `SetRunTranscript`
- `RecordRunTelemetry`
- the refusal-burst `SetRunOutcome`
- `revokeAll` (lease revocation)

On SIGTERM the interrupted run therefore had an empty transcript and no `task_run_tool` rows. RunDispatch also returned `finishing run …: context canceled` for a row it had finished, which is the line in the step 1 journal above.

**Reproduction:** `TestRunDispatchInterruptedByShutdownKeepsItsRecord` in `pkg/orchestrator/run_test.go` cancels RunDispatch's ctx under a live agent. Against the shipped code:
```
run_test.go: RunDispatch err = … interrupted by daemon shutdown: context canceled
             orchestrator: finishing run r1: context canceled, want no failure to finish the run's own row
run_test.go: RunTranscript = ("", true, <nil>), want the interrupted run's transcript kept
run_test.go: tool census for r1 = [], want the interrupted run's one tool call recorded
```

**Fix:** all four now use the same bounded, detached `finishCtx` (`runCleanupTimeout`, 2 min) as `FinishRun`. Verified on the real daemon: run `2-3` and run `2-4` (stopped by the upgrade's restart) both kept their transcript and census.

### 3. The journal called the deployment "idle" when the drain window ran out (fixed)

When the window elapsed, `startScheduledUpgrade` logged `this deployment is idle -- starting the upgrade`. That line comes right after `drain window 30s elapsed (1 run still to finish)` and is wrong. It now gets the reason from its caller: `the 30s drain window has elapsed with 1 run still to finish -- starting the upgrade to candidate …`. The same goes for the "could not be started" line. I also updated `scheduledUpgradeWait`'s "deliberately no deadline" comment to point at the window.

### Observations (no change)

- The interrupted badge uses the `closed` (neutral) class, and its label is "Interrupted (daemon restarted)". It is the same label whether a plain SIGTERM or an upgrade caused the stop. That matches `model.InterruptedDetail`, so no change.
- While the queue is held for a scheduled upgrade, no new run starts. In the 30 s case the stub started exactly once until the restart, and the e2e test asserts this.
- A row `RecoverOrphanedRuns` finishes as `orphaned` (a daemon killed hard) does not revoke its leases either. That predates this change and is unaffected by it. It is mentioned only for completeness.

## Durable tests added

- **`tests/e2e/upgrade_drain_interrupted_test.go`:** runs the real `daemon.Run` (the code `grain daemon` runs) over a persistent data directory, through the CLI's `ui.HTTPClient`. Only the claude CLI is stubbed. "SIGTERM" is cancelling Run's ctx, which is exactly what `signal.NotifyContext` does in `cmd/grain/daemon.go`.
  - `TestADaemonRestartInterruptsTheRunAndTheTaskResumes` checks:
    - the run ends `interrupted` with its transcript kept;
    - the streak and backoff stay 0 and `failedAttempts` stays 0;
    - a read and a comment sent while the daemon is down both succeed after the restart, and the comment lands exactly once;
    - the task runs again.
  - `TestAScheduledUpgradeStopsWaitingWhenItsDrainWindowRunsOut` (window 3 s) checks that the restart comes no earlier than the window, no second run starts while the queue is held, and the cut-short run is `interrupted`.
  - `TestAScheduledUpgradeWithNoDrainWindowWaitsForTheRunToFinish` (window 0) checks that the restart comes after the run finished on its own.
  - All three take about 15 s together.
- `pkg/ui/httpclient_test.go`: the four client tests listed under finding 1.
- `pkg/orchestrator/run_test.go`: `TestRunDispatchInterruptedByShutdownKeepsItsRecord`.

## CI verdict

grain CI, full required suite (`wait_for_checks` with no job filter), runs against the head commit of
`grain/eu/task-3143`, the commit that adds this report. That verdict is on pull request bwsalmon/grain#3506 and in
task 3143's closing note. A report cannot name the result of the commit it sits in. Locally, before pushing:
- `go test ./pkg/ui -run TestHTTPClient`, the drain/dispatch subset of `./pkg/orchestrator`, and the three new
  `./tests/e2e` tests all pass;
- the two regression tests fail against `candidate` as shipped, with the output quoted above.
