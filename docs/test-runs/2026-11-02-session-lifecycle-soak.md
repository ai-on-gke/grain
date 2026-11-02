# Session lifecycle soak on a real run host — 2026-11-02

Plan: [`docs/test-plans/2026-11-02.md`](../test-plans/2026-11-02.md) §8. Task 2450, filed by task 2399. It confirms, together on one live daemon, the session fixes from the 11-01 walkthroughs: PRs #394, #407, #377, #370, #369, #351, #355, #399/#359 and #357.

## Environment

- **Tree:** `origin/main` at `8726f7f7`. `git log --oneline origin/main -- pkg/orchestrator pkg/daemon` showed no sibling §4/§8 fix newer than the PRs under test, so the work started from `main` as it stood.
- **Two soaks:**
  - **Soak 1** ran on `main` with only the harness added, 05:52:16 → about 06:46. It was cut off at about 54 minutes, when the grain daemon running this task was restarted and the sandbox went with it. Its findings are F1–F3.
  - **Soak 2** ran the full 60 minutes on this branch, with the F1 and F2 fixes and the harness fix, 06:50:26 → 07:50:27.
  - Soak 1's numbers below come from that run's notes. Its log went with the sandbox.
- **Daemon:** a real `daemon.Run`, the code `grain daemon` runs, started in-process by the soak harness `TestSessionLifecycleSoak` (`pkg/cli/daemon_session_soak_test.go`, added here). The setup is the same as `docs/test-runs/2026-11-01-warm-sandbox-adoption.md`:
  - one data dir (`/var/tmp/soak`), SQLite;
  - the UI/API on a free loopback port;
  - every sandbox-facing port ephemeral, as `-sandbox-port-base=0` gives (the git proxy took a new port on each start);
  - `MaxWorkers 2`, poll interval 1 s.
  - A restart cancels the daemon's context, which is what its SIGTERM handler does.
- **Run host:** Docker (`GRAIN_RUNHOST_DOCKER=1`, `debian:bookworm-slim` plus this checkout's `grain`). Every agent exec is its own container, `<deployment>-runhost-run-<run>-<n>`. Sandboxes are host directories under `-sandbox-dir`, on the `LocalSandboxes` backend the `run host` CI job's restart tests use.
- **Agent:** a **stub `claude` CLI**, because the sandbox has no agent credentials. Each exec:
  - logs its argv, stdin and `--mcp-config`;
  - records `ls attachments` at the start and end of the turn;
  - sleeps 5 s, so messages can land mid-turn;
  - answers with a resumable session.
  - It can also start the run's own `grain mcpserver` from its `--mcp-config` and call `ask_question` or `wait_for_checks` over stdio. Inside the run host that fails (F3).
- **GitHub:** the in-repo `githubsim` serving bare `acme/widgets` and `acme/docs`.
- **Sessions:**
  - the director (task `1`), with `writes: [acme/widgets, acme/docs]` declared in `grain_config.console_sessions`;
  - `soak-chat`, an interactive chat on `acme/widgets` with `keep_running`.
- **Load:** a director message every ~32 s. Every 3rd carries a file attached between turns, and every 4th is followed 2 s later by a mid-turn message with a file. The chat runs back to back, at about 6 runs a minute.
- **Tools:** `sqlite3` (apt), `docker`, `curl`.

### The soak script

```
GRAIN_RUNHOST_DOCKER=1 GRAIN_SOAK_DURATION=60m GRAIN_SOAK_DATA_DIR=/var/tmp/soak GRAIN_SOAK_LOG=/var/tmp/soak.log \
  go test -count=1 -run TestSessionLifecycleSoak -timeout 90m -v ./pkg/cli/
```

The harness skips unless both variables are set, so CI never runs it. Its phases are scheduled in minutes of a 60 m soak and scale with `GRAIN_SOAK_DURATION`:

| min | phase |
|---|---|
| 2 | a person detaches `api:deployment:read` from the director task (`ui.Client.SetCapability`) |
| 8–12 | the director is left idle; at 8.5 the session idle TTL is cut 30 m → 1 m; at 12 it is put back |
| 18 | daemon restart |
| 25 | question asked over a person's comment |
| 32 | `wait_for_checks` from the chat, then a daemon restart 10 s into it |
| 40 | daemon stopped, the director's `acme/docs` write removed from the state repo, daemon started |

Every loop it logs a `SNAP` line with each task's state, latest run and sandbox, `docker ps -a`, and `GET /api/sandboxes`. It also logs `TASK` lines (writes, grants) and `WAITLEDGER` lines (`task_run_wait_ledger`).

In soak 1, the stub's `sed` assumed spaced JSON, but the real `--mcp-config` is compact, so the question phase did not fire on schedule. It was re-driven at 06:23–06:24 by a host-side watcher:
1. wait for the stub's `asking-1` marker;
2. post the person's comment over `POST /api/tasks/1/comments`;
3. call `ask_question` through `grain mcpserver` on the host, using the live run's own `--mcp-config` args and `GRAIN_LOCAL_API_TOKEN_FILE`.

The parsing and the marker are committed in `test(cli): make the soak stub's MCP calls parse the real --mcp-config`.

## Soak 1 timeline (pre-fix tree)

Times are UTC. Sandbox IDs come from `task_run.sandbox`.

| time | event | director (task 1) | soak-chat |
|---|---|---|---|
| 05:52:16 | daemon start (fresh data dir) | run `1-1` in `1-1` | `soak-chat-1` in `soak-chat-1` |
| 05:53:22 | `between-3.txt` attached | in-process turn; file at `sandboxes/1-1/attachments/1-between-3.txt`, named in the next prompt | — |
| 05:53:56 | `midturn-4.txt` attached mid-turn | named as `attachments/2-midturn-4.txt` in the next turn's prompt | — |
| 05:54:28 | person removes `api:deployment:read` | capability restart; `1-2` in fresh `1-2`, with all three files in its `attachments/` | — |
| 06:00:50 | TTL 30 m → 1 m, director idle ~55 s | `run 1-2: the session idle TTL changed from 30m0s to 1m0s while it waited`; `1-2` parked, sandbox gone at 06:00:56 | unaffected |
| 06:04:29 | TTL back to 30 m | `1-3` (fresh `1-3`) logs the change back | — |
| 06:10:22 | **restart** | `1-3`, mid-setup, `interrupted`; `1-4` in fresh `1-4` | `soak-chat-99` (`soak-chat-1`) **`finish-failed`** (F1); `backing off after failed run` until 06:10:52; `soak-chat-100` in a **fresh** `soak-chat-100` |
| 06:24:03–06:24:23 | question over a person's comment | comment at :05, `ask_question` at :06 ("Recorded"); the next in-process turn got the comment as an addendum; no park | — |
| 06:24:46 | **restart**, chat in `wait_for_checks` | `1-4` adopted at cursor 166 ("0s of restart credited"), parked warm in `1-4`, reused by `1-5` | `soak-chat-174` adopted at cursor 166, parked warm in `soak-chat-100` |
| 06:32:41 | **stop, offline edit, start** | `1-5` parked; `1-6` in fresh `1-6` (grants changed, so the warm sandbox is rightly not reused) | `soak-chat-205` **`finish-failed`** (F1 again); `soak-chat-206` in fresh `soak-chat-206` |
| 06:36 | `grain metrics -window 1d` | 225 attempts started, outcomes `succeeded=218 parked=3 finish-failed=2 interrupted=1` | |

Sandbox reuse:
- **director:** `1-1`, `1-2`, `1-3`, `1-4` (two runs), `1-6`;
- **chat:** `soak-chat-1` (99 runs), `soak-chat-100` (106 runs), then `soak-chat-206`.

Each chat sandbox change follows a restart that hit F1. Each director change follows a capability edit, the TTL release, a restart mid-setup, or the offline grant change, all of which should start a fresh sandbox. **No sandbox was reaped while claimed.** The hour had no `reaped leaked agent sandbox` line, and the only reap lines were the startup sweep (see the cache note).

**Leak check.** Over 107 `SNAP` lines, every running `runhost-run-<run>` container belonged to a run that was the latest run of the director or of the chat. At most 2 run hosts existed at once, against a `MaxWorkers` of 2. `GET /api/sandboxes` listed exactly the sandbox directories on disk at every check (`1-6` idle session and `soak-chat-206` agent at 06:36).

## Soak 2 (fixed tree, full hour)

_In progress at the time of this commit; see the next revision._

## "What is NOT proved", item by item

| # | Item | Result |
|---|---|---|
| 1a | Attachments between turns (#394) | **Pass.** `between-N.txt` was written to `<sandbox>/attachments/<id>-between-N.txt` before the next turn, named in its prompt, and placed again in the next fresh sandbox after a capability restart. But an agent in a Docker run host cannot read that path (F3). |
| 1b | Attachments mid-turn (#394) | **Pass.** The mid-turn message and its file reached the next in-process turn's prompt as `attachments/<id>-midturn-N.txt`, with the file in the sandbox. The stub makes no tool call mid-turn, so delivery *during a tool call* was not exercised. |
| 1c | Ledger after ten turns, one charge per sandbox (#407) | **Not observable on this host.** `GET /api/sandboxes` `budget.known` stayed `false` ("no host reading, so nothing bounds how much machine this deployment's sandboxes commit"), with `committedCount 0`, so the agent-pool charge is never shown. `2026-11-01-warm-sandbox-adoption.md` item 5 hit the same limit. `grain metrics` reports attempts, not ledger charges. `reconcile_sandboxes_test.go` (CI) remains the evidence for #407. |
| 1d | TTL shortened while idle (#377) | **Pass.** The change was logged within 1 s, and the idle sandbox was released 6 s after the cut. Putting it back was logged too. |
| 1e | First-turn message (#370) | **Partly.** Director message 1 (05:52:17.809) reached the first exec (05:52:18), in its first prompt. The exact window, a post *during* a body-only first turn, was not hit with 5 s turns. |
| 1f | Restart mid-`grain ci wait` keeps credit (#351) | **Not walkable here (F3).** The chat's `wait_for_checks` could not start `grain mcpserver` inside its run host, so no wait was credited. `task_run_wait_ledger` was empty before and after the 06:24:46 restart. Adoption across that restart itself passed for both live runs. |
| 1g | Question while a person comments (#355) | **Inconclusive.** The comment reached the next turn and the director did not park. But the question was not relayed into the conversation either: the call went through a host-side mcpserver, the stub's own stream carried no `ask_question` tool_use, and so ProcessResult had nothing to relay or park on. `tests/e2e/interactive_message_delivery_walkthrough_test.go` (a real forked mcpserver) remains the evidence. |
| 2 | No orphan, no reaped-while-claimed (#369) | **Pass** (leak check above), with F2 and the cache note. |
| 3 | Snapshot after restart (#399/#359) | **Pass.** Before the stop: `writes=[acme/docs, acme/widgets]`, and the person's removal of `api:deployment:read` in place. `acme/docs` was then dropped from `console_sessions` in `tables/grain_config.json` and committed in the state repo while the daemon was down. After the start: `writes=[acme/widgets]`, and `api:deployment:read` still absent. The plan names `tables/console_session.json`, but there is no such file: console sessions are the `console_sessions` column of `tables/grain_config.json`, a settings table imported at a restart. |
| — | Stale deferral escalation (#357) | **Not run.** Neither session defers; covered by `liveness_internal_test.go` only. |

## Findings

### F1: a daemon stop while a finished run's result is processed is a strike (fixed here)

**Repro (live, 2 of 3 restarts in soak 1).**
- `soak-chat-99` answered at 06:10:21.83, and the stop came at 06:10:22. The run was recorded `finish-failed`: "this run's result could not be turned into a pull request or a comment: orchestrator: reading soak-chat's conversation: reading comments on task soak-chat: context canceled".
- The shutdown log said "none counts against its task", yet the restarted daemon logged `candidate soak-chat skipped: backing off after failed run` for 26 s and re-cloned a fresh sandbox.
- `soak-chat-205` did the same at 06:32:37.

**Cause.**
1. `afterAgentRun` runs `processResultFrom` on runOne's ctx, which is the daemon's.
2. If the daemon stops in that window, the first store read fails on the cancelled ctx.
3. `noteFinishFailure` then records `finish-failed`, which counts toward the failure streak and backoff.

A run stopped a moment earlier is `interrupted`, with no strike.

**Fix** (`pkg/orchestrator/cycle.go`, not the trust-boundary `finish.go`). A result-processing error with the daemon's ctx done is recorded as `interrupted` (`model.InterruptedDetail`), with the task note any interrupted run gets, and `errDaemonStopped` is returned. The next dispatch processes the conversation afresh.

**Deterministic regression test:** `TestADaemonStoppedWhileARunsResultIsProcessedIsInterrupted` (`pkg/orchestrator/interrupted_test.go`). The stub framework stops the daemon as it returns a clean answer.
- Before the fix it fails with `Outcome:finish-failed … reading whether t1 is awaiting a reply: context canceled`.
- After it, the run is interrupted, the task queued, and there is no strike.
- It is an orchestrator test rather than a `tests/e2e` one because the window lies between two internal calls, and only an in-package framework hook lands in it every time.
- `go test -race -count=2 ./pkg/orchestrator/` passes (760 s).

### F2: the orphaned run-host sweep fails whenever a run host vanishes mid-listing (fixed here)

**Repro.** Six times in soak 1's first 25 minutes:

```
orchestrator: sandbox reconciliation: sweeping orphaned run hosts: orchestrator: listing run hosts: docker inspect: exit status 1 (error: no such object: a1318bfd…)
```

**Cause.** `runhost.Docker.List` runs `docker ps`, then `docker inspect` on every ID. A per-turn run host that exits and is removed in between makes the inspect exit 1. `List` returned that error, and the cycle's orphan sweep was skipped. `docker inspect` still prints every container that is left (`docker inspect nonexistent` prints `[]` and exits 1).

**Fix** (`pkg/runhost/docker.go`, minimal). Use that output when the only failure is "no such object" and stdout is valid JSON. Any other failure still fails.

**Test:** `TestDockerListSkipsARunHostRemovedBeforeItsInspect` covers both halves and fails without the fix. `go test -race -count=2 ./pkg/runhost/` passes.

### F3: a Docker run host cannot reach its host-directory sandbox (filed)

Every attempt to start the run's own MCP server inside its run host failed:

```
grain mcpserver: -sandbox-root "/var/tmp/soak/sandboxes/1-4" is not a directory: stat /var/tmp/soak/sandboxes/1-4: no such file or directory
```

`docker inspect` of a live run host shows three mounts: the credential dir, `runhosts/spool` and `runhosts/staging`. The CLI's cwd is `/`.
- So with `-run-host-image` and host-directory sandboxes, a real agent has no sandbox tool.
- It cannot read the attachments #394 places, and it cannot call `ask_question` or `wait_for_checks`. That is why items 1f and 1g could not be walked through the agent.
- `daemon.CheckRunHostBackend` refuses only `-container-sandboxes` (finding 6 of `2026-10-14-run-hosts-daemon.md`). Production `-host-sandboxes` (`<sandbox-dir>/runs/<run>`) has the same gap.
- CI passes because every run-host stub prints canned stream-json and never starts the MCP server.

Which paths a run host may mount is a trust-boundary design decision: only the run's own sandbox root, never the whole `-sandbox-dir`. So it is **filed as a follow-up**.

### Note: the test backend's startup reap removes the caches

Every start, the first included, logged `reaped 1 sandbox(es) left behind by a previous process`.
- The entry reaped is `<sandbox-dir>/cache`: the package and Actions caches default there, they are opened before the reap, and `HostSandboxes.ReapOrphans` removes every non-live entry of its base directory.
- Only the `LocalSandboxes` test/e2e backend puts run directories straight in `-sandbox-dir`. Production `-host-sandboxes` uses `<sandbox-dir>/runs`, so its reap never sees `cache`. Not filed.

## Fixes

| Commit | Test (fails before, passes after) |
|---|---|
| `fix(orchestrator)`: interrupted, not finish-failed, when the daemon stops mid-finish | `TestADaemonStoppedWhileARunsResultIsProcessedIsInterrupted` |
| `fix(runhost)`: list the remaining run hosts when one vanishes before `docker inspect` | `TestDockerListSkipsARunHostRemovedBeforeItsInspect` |
| `test(cli)`: the soak harness, and its MCP-config parsing | `TestSessionLifecycleSoak` (manual, env-gated) |

## Follow-ups

- **Filed** (depends on this task): a Docker run host with host-directory sandboxes cannot reach its sandbox (F3).
- **Not filed:**
  - The ledger (#407) is not observable without a host reading; the same as 11-01.
  - Once F3 is fixed, the soak stub should also echo its `ask_question` as a stream-json tool_use, so 1g and 1f can be walked through the agent.

## Not run

- A Granule run host: no SSH user, workspace or guest image in this sandbox.
- A real agent backend: no credentials.
- The Health page in a browser: there was nothing to read on it, since the ledger is not filled on this host (1c).
- Stale deferrals (#357).
- Postgres.
- `grain daemon` as a separate binary: the soak drives `daemon.Run` in-process, as the 11-01 adoption walkthrough did.
