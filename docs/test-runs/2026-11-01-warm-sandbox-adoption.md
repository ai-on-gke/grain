# Warm session sandboxes across a daemon restart — test run (2026-11-01)

Plan: [`docs/test-plans/2026-11-01-late.md`](../test-plans/2026-11-01-late.md) §2 (PR #289: `5fa17498`, `4d275302`, `4eec19be`). Task 2244, filed by task 2227.

## Environment

- **Code under test:** `origin/main` at `ccc64703`, plus this branch's fix (below). For the upgrade step, binaries were also built at `df38c2b5` (`20d79d0b^1`, schema 78) and at `20d79d0b` itself.
- **Daemon:** a real `daemon.Run` (the code `grain daemon` runs) over one data dir, started and stopped four times. Each stop cancels the daemon's context, which is what its SIGTERM handler does. The upgrade step ran the built `grain daemon` binaries and stopped them with a real `kill -TERM`. `-sandbox-port-base=0` was used throughout, so the git proxy moves to a new port on every restart.
- **Run host:** the Docker run host (`GRAIN_RUNHOST_DOCKER=1`), using an image built from `debian:bookworm-slim` plus this checkout's `grain`. Sandboxes are host-directory sandboxes (`-sandbox-dir`), the backend the run-host CI job uses.
- **Agent:** a **stub `claude` CLI**, because the sandbox has no agent credentials. It writes a stream-json turn, holds the turn open until the test releases it, and then ends the turn with a plain-text answer. GitHub is the in-repo `githubsim` serving bare repos.
- **What was stubbed:** the agent CLI, GitHub and the second agent connection for step 7. The daemon, store, run host, git proxy, adoption, warm pool and UI API are all real.
- **Tools:** `sqlite3` (installed with apt) and Docker 1:1 on the sandbox host. Postgres was not available.

The walkthrough is pinned as `TestDaemonRestartKeepsAWarmSessionSandboxOnlyUnderItsRecordedKeys` (`pkg/cli/daemon_warm_session_restart_test.go`). It is added to the `run host` job in `.github/workflows/tests.yml`.

No `docs/test-runs/*interactive-sessions-warm*` report from `2026-11-01.md` §5 existed when this ran, so there was nothing to skip. `git log origin/main -- pkg/orchestrator pkg/ui/messages.go ui/src/components/TaskChat.jsx` showed no sibling fix to `adopt.go` or `session_sandboxes.go` since PR #289.

## Results

| # | "Not proved" item | Result |
|---|---|---|
| 1 | Real restart mid-turn reuses the same sandbox, with no re-clone | **Pass**, after fixing the bug below |
| 2 | Grants narrowed in the gap: sandbox released, old token refused | **Pass** |
| 3 | Missing `task_run_sandbox_key` row fails closed | **Pass** |
| 4 | Upgrade from `20d79d0b^1`; export/import | **Pass** (SQLite). Postgres not run |
| 5 | Ledger counts a first-attempt warm sandbox; TTL release | **Partly**: listed as idle and released on TTL. The budget count could not be observed on this host |
| 6 | `recreate_sandbox`, then a restart | **Pass** for the no-placement case. The placement-lost case is covered by unit tests only |
| 7 | Framework/model stickiness and fallback | **Stub backends only** (unit tests) |

### 1. Restart mid-turn, then the next turn reuses the sandbox

The task was interactive, targeting `acme/widgets` and reading `acme/docs`. Turn 1 (`warm-session-1`) went into a Docker run host, in sandbox `warm-session-1`. Its recorded row in `task_run_sandbox_key`:

```
sandbox_key="shape={CPUs:0 MemoryMB:0 DiskGB:0 NetworkAccess: OperatorEnv:true GrainSource:false Class:interactive Life:0s}|target=acme/widgets|base=|reads=acme/docs|grants="
grants_key="target=acme/widgets|base=|reads=acme/docs|grants="
```

The daemon was stopped mid-turn and a new one started:

```
orchestrator: run warm-session-1: left running in its run host for the next daemon to adopt
orchestrator: adopted run host …-runhost-run-warm-session-1-… for run warm-session-1 at cursor 0 after a 0s gap
grain daemon: run-host adoption: 1 adopted, 0 lost, 0 unclaimed run host(s) freed
orchestrator: resuming adopted run warm-session-1 (task warm-session) from cursor 0; 0s of restart credited
orchestrator: adopted run warm-session-1: parked its sandbox warm-session-1 warm for task warm-session's next turn (idle for up to 40s)
```

(The last line is new on this branch; see "Observability" below.)

- The run finished `succeeded`, and its `task_run_sandbox_key` row was gone afterwards.
- The sandbox directory survived, and a marker file the test wrote into it was still there when turn 2 started.
- Turn 2's run (`warm-session-2`) recorded `Sandbox = warm-session-1`, the same sandbox, not re-cloned.

**Bug found on the first attempt.** The test's message "turn 2, please" was posted about 1.6 s after turn 1's run row was finished. Grain's relayed answer to turn 1 landed after it with the same `CreatedAt` and a higher comment ID. The task then sat in `awaiting_reply`, and turn 2 was never dispatched. See "Bug: a message posted during a chat's first turn is dropped" below.

### 2. Narrowing in the restart gap

- Turn 3 ran in a fresh sandbox, `warm-session-3`. Its own `.git-credentials` token worked: `git ls-remote` through the git proxy succeeded.
- The daemon was stopped mid-turn. With the daemon down, the task's `reads` (`acme/docs`) were removed in the store.
- After adoption, the new daemon logged:
  ```
  orchestrator: adopted run warm-session-3: releasing its sandbox warm-session-3 rather than parking it warm: task warm-session's repos, grants or network access changed since it was dispatched
  ```
- The sandbox directory was removed.
- The old token was refused by the live git proxy. The proxy had moved port, so the token was sent to the address turn 4's sandbox reaches:
  ```
  git ls-remote http://sandbox:<turn-3 token>@127.0.0.1:<new port>/acme/widgets.git
  → exit status 128: remote: authentication required
  ```
  Turn 4's own token, sent to the same address, succeeded.
- Turn 4 got a fresh sandbox, `warm-session-4`.

Network access could not be narrowed here: host-directory sandboxes refuse `network_access: none`. `reads` is part of the same `sessionGrantsKey`.

### 3. Fail closed with no `task_run_sandbox_key` row

Turn 2 was in flight in warm sandbox `warm-session-1` when the daemon was stopped. With the daemon down:

```
sqlite3> DELETE FROM task_run_sandbox_key WHERE run_id = 'warm-session-2';
```

After adoption:

```
orchestrator: adopted run warm-session-2: releasing its sandbox warm-session-1 rather than parking it warm: no isolation keys were recorded when it was dispatched
```

- The sandbox was removed.
- Turn 3 dispatched into a fresh sandbox (`warm-session-3`) and finished `succeeded`. No error appeared in the conversation.

### 4. Upgrade

- **Old build.** `grain daemon` built at `df38c2b5` (`20d79d0b^1`) ran on a fresh data dir and was stopped with `kill -TERM`. `sqlite3 store/grain.db`: `grain_schema` = 78, 123 tables, no `task_run_sandbox_key`.
- **The `20d79d0b` binary on that data dir.** It started and stopped cleanly (exit 0). `task_run_sandbox_key` now exists and `grain_schema` is still **78**: the table appeared with no version bump, as the plan says.
- **The current build (`main`, schema 82) on a schema-78 data dir.**
  - Swapping the binary alone migrates the database to 82. The daemon then refuses to start: `state repository was written by an older build of grain: repository is at schema 78, this build knows 82`.
  - That refusal is the documented guard. The supported path is `grain state -data-dir D prepare-upgrade`, which reported `rehearsed: 41 rows across 15 tables migrate from schema 78 into 82` and `prepared 78 82 …/schema-upgrade`. The next start brought the state repo across: `the config repository's dump was rewritten at schema 82`. Schema 82 has the table.
- **Export.** A row was planted with `INSERT INTO task_run_sandbox_key VALUES ('planted-run','k','g')`. `grain state sync` then reported `nothing to commit`, and no file in the state repo mentions it. The table is local, as `pkg/staterepo/tables_internal_test.go` classifies it.
- **Import.** `grain state check <state-repo>` printed `ok: grain can load this`.
- **A finished run leaves no row behind:** checked in step 1.
- **Postgres** was not available in the sandbox and was not run. `pkg/model/postgres/adapter.go` lists the table's key, and CI's `postgres` job covers the adapter.

### 5. Budget ledger and idle TTL

Turn 4's first-attempt sandbox (`warm-session-4`, named after its run) parked warm. The session idle TTL was set to 40 s.

`GET /api/sandboxes` listed it:

```json
{"sandbox":"warm-session-4","kind":"idle session","backend":"host","ready":true,"terminationReason":"idle session"}
```

It was removed within the TTL window, and it then dropped off `/api/sandboxes`.

The **budget count could not be observed on this host**:
- The daemon logs `this host's cores and memory could not be read`.
- `budget.known` is `false`, and `committedCount` stays 0 for every sandbox, live ones included.

So whether the ledger counts the parked sandbox as an idle commitment is still only proved by `TestResumeAdoptedRunEnforcesWarmSandboxIsolationAndLedgerPark`. The new test asserts the agent-pool count whenever `budget.known` is true, so a runner that can read its host size checks it.

`grain metrics` shows only reconciler timings (`console-sessions`, `standing-sandboxes`, `persistent-sandboxes`, `sandbox-reconciliation`), with no warm or idle sandbox count. `orchestrator.WarmSessionSandboxCount` and `Ledger.IdleCount` are not used outside tests.

### 6. `recreate_sandbox` then a restart

1. Turn 5 ran in a fresh sandbox, `warm-session-5`.
2. `POST /api/tasks/warm-session/sandbox/recreate` (what the `recreate_sandbox` tool calls) was made mid-turn. It returned `restored: [its git credentials for grain's git proxy, a fresh clone of acme/widgets at ./work …]`. A file written before the rebuild was gone afterwards.
3. The daemon was restarted. The same POST to the adopting daemon also rebuilt the sandbox (200, same `restored`).
4. The turn was released, and the adopter logged `parked its sandbox warm-session-5 warm`.
5. Turn 6 reused `warm-session-5`, and its warm sandbox was released on TTL.

This task has no capability placements, and a host sandbox has no isolated VM shape, so "parked" is the expected branch. The "placements could not be restored → released" branch needs a capability that places files. It was not exercised live and is covered by `degraded_recreate_with_capability_grant_does_not_park_warm`.

Observation, not a security gap: the adopting daemon only knows about rebuilds *it* performed (`registerAdoptedRecreation`). A rebuild the first daemon did before the restart is not seen. `runOne` also parks warm after a rebuild without checking the rebuild's warnings. Every rebuild warning (`recreate.go`) describes something *missing* from the new sandbox (credentials, cache, capability files), never something extra. The worst case is a warm sandbox reused without a capability's files, which the next fresh dispatch restores.

### 7. Framework/model stickiness

Only one agent credential type could be stubbed end to end (the stub `claude` CLI). Stickiness and the paused-connection fallback were therefore checked with stub backends only, on this branch:

- `TestInteractiveFrameworkStickinessAndUsageLimitFallback`: PASS.
- `TestAgentPoolPrefersPreviousFrameworkAndModelAndFallsBackWhenPaused`: PASS.

In the Docker run every turn ran on `claude` / `claude-sonnet-5`, the only connection, so it says nothing about stickiness.

## Bug: a message posted during a chat's first turn is dropped

**Symptom.** A chat filed with only a body has no comment in its first turn's prompt. A person posts a message while that turn writes its answer, or in the seconds between the run finishing and grain relaying its answer. Grain posts the answer after the message, the task goes to `awaiting_reply`, and the message is never answered.

**Cause.**
- `runOne` marks the prompt's comments delivered with `AdvanceDelivered(lastCommentID(comments))`. With no comments that is `AdvanceDelivered(0)`, a no-op, so `Observation.DeliveredCommentID` stays nil.
- `undeliveredHumanComment` returns "nothing pending" whenever the mark is nil, so `interactiveMessageWaiting` cannot see the unseen message.
- `ResumeAdoptedRun` also advanced the mark only *after* `recordRunEnd` had already asked whether a message was waiting.

**Reproduction.**
- `TestInteractiveFirstTurnWithNoCommentsDispatchesForAMessageItNeverSaw` (`pkg/orchestrator/interactive_test.go`) fails on `main` with `state after turn 1 = "awaiting_reply", want "queued"`.
- The Docker walkthrough failed at turn 2 on `main`, with the task `awaiting_reply` and comments `[2: human "turn 2, please", 3: grain "turn 1 answered"]` both stamped `08:11:25.756650993`.

**Fix.**
- New `Store.AdvanceDeliveredFrom`, which records a zero mark ("shown nothing") for a turn whose prompt held no comment and otherwise behaves as `AdvanceDelivered`. `runOne` uses it.
- `ResumeAdoptedRun` now records the mark *before* `recordRunEnd`, as `runOne` does.

`pkg/orchestrator/finish.go` and `pkg/runhost/*` are not changed. The `.github/workflows/tests.yml` edit (the new test added to the `run host` job, with the job timeout raised from 15 to 25 minutes) is a trust-boundary path and needs a security review.

## Observability

Before this branch, an adopted run's sandbox was parked or released with no log line, so the journal could not say why a session lost its warm sandbox after a restart. `ResumeAdoptedRun` now logs one of:

- `parked its sandbox … warm`
- `releasing … : no isolation keys were recorded when it was dispatched`
- `… : task …'s repos, grants or network access changed since it was dispatched`
- `… : task …'s sandbox shape changed since it was dispatched`
- `… : a rebuild during the run could not restore its capabilities or isolation`

## Other test results on this branch

- `go test ./pkg/orchestrator ./pkg/model ./pkg/ui ./pkg/daemon ./pkg/staterepo`: `model` passes.
  - `TestSettingsLockedRefusesExactlyTheFieldsTheUIGreysOut` (`pkg/ui`) also fails on a clean `main` worktree with the same `gcpProject "proj" is not a GCP project ID` error, so it is not from this branch.
  - `TestKillStepRunnerKillsItsOwnRunner` and `TestOverlapPolicyDecidesASecondFiringWhileTheFirstIsStillRunning` failed once under the full-package parallel run and pass 3/3 in isolation on this branch.
- `GRAIN_RUNHOST_DOCKER=1 go test ./pkg/cli -run 'TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest'` (the existing acceptance test) passes in this sandbox in 37 s. The new walkthrough test passes in about 90 s.
