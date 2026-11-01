# Interactive and console sessions: warm sandboxes, in-process turns, `keep_running`, live grant edits and admission — 2026-11-01

Plan: [`docs/test-plans/2026-11-01.md`](../test-plans/2026-11-01.md) §5 (PRs #198, #246, #280, #287, #273, #299, #243, #307, #245, #251). Task 2220, filed by the nightly test-plan job (task 2084).

## Environment

- Tree: `origin/main` at `30f47366`, branch `grain/049f1e09/task-2220`.
- **Daemon:** a real `grain daemon` built from this tree. All times below are UTC on 2026-11-01. Flags:
  - `-sandbox-port-base=0 -ui-addr 127.0.0.1:4242 -ci=false -poll-interval 2s`
  - `-repo <local checkout with origin github.com/acme/widgets>`
  - `-max-workers 2 -sandbox-cpu-overcommit 0.8 -repair-reserve-sandboxes 0`
- **Sandbox pool: local Docker containers.**
  - Flags: `-container-sandboxes -container-sandbox-network bridge -container-git-proxy-host 10.240.0.1`, image `ghcr.io/bwsalmon/grain/k8s-sandbox:latest`.
  - One container per sandbox, named `grain-sandbox-<task>-<attempt>`.
  - `/dev/kvm` exists, but Granule needs an SSH user, a workspace and a guest image that this sandbox does not have. The Docker pool is the one `tests/e2e` and `GRAIN_RUNHOST_DOCKER=1` use.
- **Sandbox budget of 2.** Each sandbox is 2 vCPU / 3 GiB (`sandboxMemoryMb: 3072`). The daemon reported "7.8 GiB of sandbox memory … and 4 vCPU (7 host vCPU less 2 reserved, at 0.80x)", so two sandboxes fit and a third is refused.
- **Agent: stub framework, not a real model.** This deployment's credentials do not reach the sandbox, and there is no GitHub credential either.
  - The connection is a `claude` agent backend whose `command` is a shell stub (`stub/claude`).
  - The stub logs argv and stdin, honours `--resume <session>`, can sleep N seconds, and prints `init` → `assistant` text → `result` with a resumable `session_id`.
  - It makes **no MCP tool calls**. Everything the agent does through tools (`ask_question`, `comment_on_issue`, git pushes) is therefore out of reach of this walkthrough.
- **Browser:** Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) against the daemon's UI, after `vite build` into `pkg/ui/static`. Shots were taken at 1440×900 and 390×844.

## Latency (item 1, walkthrough step 2)

Director session (task 1), three messages, `GET /api/tasks/1` `attempts[].startup`:

| Message | Sent → answer relayed | How served | Sandbox |
|---|---|---|---|
| 1 (09:12:56) | 2.6 s. `startedToFirstTurnSeconds` 2.575: queued→dispatched 0.002, sandbox acquire 0.757, prepare 1.796, agent launch→first turn 0.023 | fresh dispatch, attempt 1 | `grain-sandbox-1-1` |
| 2 (09:13:21.145) | **0.19 s** | in-process continuation of attempt 1 (`--resume stub-1793524379490931795`) | same `grain-sandbox-1-1`, no re-clone |
| 3 (09:13:21.415) | **0.06 s** | in-process, same session | same |

- **First turn 2.6 s against the < 45 s target, later turns 0.06–0.19 s against < 1 s. Pass** for grain's own share of the latency. A real model adds its own time to the first turn and to every turn.
- Later fresh dispatches (attempts 2–9, after grant edits, restarts and TTL expiry) all had `startedToFirstTurnSeconds` between 2.38 and 2.74 s.
- `grain -server http://127.0.0.1:4242 metrics -window 1d` after the three messages:

```
interactive startup (per-phase breakdown)
  stage                                        n        p50        p90        max
  queued -> dispatched                         1         0s         0s         0s
  sandbox acquire                              1         1s         1s         1s
  prepare sandbox                              1         2s         2s         2s
  agent launch -> first turn                   1         0s         0s         0s
  attempt started -> agent's first turn        1         3s         3s         3s
```

`GET /api/metrics` `.interactiveStartup` reported the same stages with sub-second precision.

## Pages and widths shot

- `/tasks/1` (the director session) at 1440×900 and 390×844, with the conversation showing and with the capability sidebar opened ("edit director capabilities").
- Asserted from the DOM, not from looking at the PNGs (the sandbox's file reader cannot render them):
  - the stub's answers render;
  - `document.documentElement.scrollWidth - innerWidth` is 0 at both widths, closed and open;
  - the sidebar lists the capability toggles, the Network select (`open`), the Model select (`claude-stub-2`) and the "keep running" toggle at both widths.

## "What is NOT proved", item by item

| # | Item | Result |
|---|---|---|
| 1 | Real sandbox, latency, the second message reuses the sandbox ID without a re-clone | **Pass** (stub framework). See the latency table above. |
| 2a | TTL expiry at 1 m | **Pass** on the fixed build. Idle from 09:28:40; `grain-sandbox-1-6` released at 09:29:41. The next message was a fresh dispatch: attempt 7, new `grain-sandbox-1-7`, 2.40 s. |
| 2b | TTL changed while a session is idle | **Finding F1, fixed here.** |
| 3a | Grant edit mid-session (API and sidebar): warm sandbox released, run restarted | **Pass.** `orchestrator: run 1-11 of task 1 restarted to pick up a change to its capabilities` (#251's `capability_restart.go`) arrived about 1.5 s after the sidebar edit, and the warm container was gone. |
| 3b | Stale token after narrowing | **Pass, and not a security bug.** See F2. |
| 3c | The next turn holds the new grants | **Pass.** After `self-debug` was removed and the model changed to `claude-stub-2`, the next turn ran in a new `grain-sandbox-1-2` with `--model claude-stub-2`. Its token's `grain task list` shows only its own task (it had shown all 9 with `self-debug`). |
| 3d | Narrowing `writes` | **Not walked.** There is no GitHub credential, so no push can be exercised. |
| 3e | A removed default is not re-added after a restart (#299 `7622acf3`) | **Finding F3, filed.** A removal made through the task capability endpoint is reverted on restart. |
| 4a | `keep_running` restarts after a normal finish, with no failure streak | **Pass.** Console session `kr3` (task 4, stub sleeping 3 s) ran attempts 1–4 back to back in one sandbox, all `succeeded`, with no streak. A `noRepo` batch task with `keepRunning` (09:44) did the same in `grain-sandbox-2-1`. |
| 4b | No restart after an operator stop | **Pass.** `POST /api/console-sessions/kr3/cancel` at 09:32:08: attempt 5 `cancelled`, state `awaiting_reply`, no new attempt in the following 14 s, sandbox released. |
| 4c | No restart after a question | **Not walked.** The stub cannot call `ask_question`. CI covers it in `TestKeepRunningTaskStopsWhenClosedDeferredOrParked`. |
| 4d | Clearing `keep_running` reclaims the sandbox | **Pass.** On the batch task: cleared at 09:44:33; the running attempt finished and `grain-sandbox-2-1` was gone by 09:44:39 with no restart. On an interactive session, clearing it ends the loop and the run falls back to the ordinary warm-session wait under the TTL, as `6b80075c` intends. |
| 4e | A system schedule refuses the edit | **Pass.** `PATCH /api/schedules/system:memory-agent {"keepRunning":true}` → 400 "system:memory-agent is one of grain's own scheduled jobs, so whether its tasks keep running is not this deployment's to change …". |
| 4f | (found while walking 4a) | **Finding F4, filed:** the reaper removed a `keep_running` session's warm sandbox between two runs. |
| 5 | Admission with batch work filling the ceiling | **Pass.** See below. |
| 6a | A reply posted during the turn is shown to the next turn | **Pass, partly.** The stub has no tool calls, so the reply was posted during the turn rather than during its last tool call. "message 13" was posted 1.5 s into a 4 s turn, and the next in-process turn (invocation 72) received it as an addendum. |
| 6b | Attachments are present the moment the message is | **Pass at the API**, but see F5. The comment POST returned with `note.txt`, and the first `GET /api/tasks/1/messages` showed it with the attachment. The agent never got the file: **finding F5, filed**. |
| — | Capability change mid-session restarts the run (#251) | **Pass.** Restarted on each of the four capability edits, at 09:17:31, 09:19:34, 09:19:57 and 09:40:09. |
| — | Admission refusal names its gate | **Pass.** A model nobody serves gives `dispatch: WARNING: priority-dispatch task 1 (a console session or a priority-dispatch holder) refused admission by model availability -- no enabled connection serves model claude-stub-2 -- the task is pinned to claude-stub-2; it does not fall back` (09:17:57–09:19:20, until the model was added to the connection). |

### Admission detail (item 5)

1. Batch task 7 (`noRepo`, stub sleeping 60 s) ran in `grain-sandbox-7-1`, with tasks 8 and 9 queued behind it, leaving 6.0 GiB of the 7.8 GiB committed.
2. A director message at 09:33:57.682 was admitted beyond the ceiling and answered at 09:34:00.250 (2.57 s, attempt 9, `grain-sandbox-1-9`).
3. The refused batch candidates named their gate on every cycle:

```
dispatch: candidate 8 skipped: no capacity for worker/merger -- memory: 6.0 GiB is committed by this deployment's live sandboxes of its 7.8 GiB sandbox budget (15.6 GiB host, 7.8 GiB reserved for the host itself), and this one wants 3.0 GiB
dispatch: candidate 9 skipped: no capacity for worker/merger -- memory: 6.0 GiB is committed by ...
```

Earlier, two keep-running sessions filled both slots and a third session (the director) was still admitted, as a third container.

## Findings

### F1: a shortened session TTL did not apply to an idle session (fixed here)

**Repro.**
1. Director session idle in-process from 09:13:21 under the default 30 m.
2. `POST /api/settings {"sessionSandboxIdleTtl":"1m"}` at 09:13:47.
3. `grain-sandbox-1-1` was still up, with the task in `awaiting_reply`, at 09:16:49, three minutes later. Nothing in the log.

**Causes.**
- `continueSessionTurns` read the TTL once (`ttl := cfg.sessionSandboxIdleTTL()`, filled from the store at dispatch) and waited on `context.WithTimeout(hostCtx, ttl)`.
- A sandbox parked warm afterwards had `ExpiresAt` computed from the same dispatch-time value, and `reconcileSessionSandboxes` only compared against `ExpiresAt`.

**Fix** (`pkg/orchestrator/session_sandboxes.go`, `cycle.go`, `orchestrator.go`).
- A run whose TTL came from the stored setting (`Config.sessionIdleTTLStored`, set where `RunCycle` fills the TTL in) re-reads it on each idle poll. It ends the wait on the new value and logs `the session idle TTL changed from 30m0s to 20s while it waited`.
- The parked-sandbox reconcile also releases an entry that has been idle for the TTL as it stands.
- A caller-pinned `Config.SessionSandboxIdleTTL` is still held as given.

**Verified live** on the rebuilt daemon. Idle from 09:28:08, TTL set to 20 s at 09:28:09, the change logged at 09:28:09, the run parked and `grain-sandbox-1-5` was gone by 09:28:30. Then 2a above.

**Regression tests**, both failing without the fix:
- `TestInProcessSessionIdleWaitFollowsAChangedStoredTTL`, which also checks the pinned case;
- `TestParkedSessionSandboxIsReclaimedOnAShortenedTTL`.

### F2: the stale token after a grant narrowing (no bug)

A tight loop of `grain task list` used the warm sandbox's own token while `self-debug` was removed with `POST /api/tasks/1/capabilities`. Without `self-debug` a token lists only its own task; with it, every task.

```
     15 09:40:50.875 9 task(s) shown of 9 matching        <- self-debug held
EDIT SENT 09:40:51.343 / EDIT RETURNED 09:40:51.359
     40 09:40:51.368 1 task(s) shown of 1 matching        <- narrowed 9 ms after the edit returned
     19 09:40:52.713 grain: forbidden: no live task run on this sandbox
     16 09:40:53.250 grain: authentication required       <- token revoked
```

- The proxy evaluates the task's live grants, so **the removed grant is never honoured after the edit returns**.
- The token stays valid with the narrowed grants for about 1.35 s, until the capability restart ends the run, and is revoked about 1.9 s after the edit.
- The sidebar edit (09:40:07.8 → refused 09:40:09.556 → revoked 09:40:09.951) and the earlier API edit (09:19:56.338 → revoked 09:19:57.683) behaved the same way.
- Not walked: narrowing `writes` (3d).

### F3: console-session grant edits made through the task are reverted at the next restart (filed)

**Repro** (fresh `-data-dir`, so `consoleSessions` has never been stored):
1. The director (task 1) is filed with `self-debug`.
2. `POST /api/tasks/1/capabilities {"id":"self-debug","attach":false}`. Both the task and `GET /api/console-sessions` lose it.
3. Restart the daemon. Both have `self-debug` again.

The same happened at 09:19:56 → 09:20:07 in the main run.

**Cause.**
- The edit changes only the task.
- The pre-edit snapshot that tells `mergeConsoleSessionGrants` the capability was removed lives in the in-memory `consoleSessionSnapshots` map.
- `GET /api/console-sessions` derives capabilities from the task, which makes the edit look as if it landed on the session.
- After a restart `hasPrev` is false and the session config's capability is re-added. An attached capability the config does not list is dropped the same way.

It appears to work once anything has persisted `cfg.ConsoleSessions` from the live view. Creating any console session does that, which is why later attempts in the main run held. Filed with the repro and a fix direction (persist the snapshot).

### F4: the reaper removed a keep_running session's warm sandbox between two runs (filed)

**Repro.** The stub answered in about 0.2 s, so keep-running console session task 2 ran 49 attempts in 12 s, all in `grain-sandbox-2-1`.

```
task_run 2-49 finished 09:30:28.6098, 2-50 started 09:30:28.6430, both sandbox "2-1"
orchestrator: sandbox reconciliation: reaped leaked agent sandbox 2-1 (grain-sandbox-2-1): run 2-1 already finished
orchestrator: run 2-50 (task 2): orchestrator: configuring git credentials for run 2-50: ... container e89aad5d... is not running
```

Attempt 50 ended `suspended`, and the task then sat in `candidate 2 skipped: backing off after failed run`.

**Cause.** The reaper protects live runs' `Sandbox` and the warm pool. It does not cover the moment between one run finishing and the sandbox being parked, nor the moment between the next run claiming it and that run being recorded. Filed.

The hot loop itself is `keep_running` as designed, at the stub's speed. A real agent restarts as often as its turns end.

### F5: a message's attachment never reaches an in-process or mid-run turn (filed)

`note.txt`, attached to "message 13", reached the next in-process turn only as the body text: `a human (debian) just added this to the task while you're already working on it -- read it and factor it in:\n\nmessage 13: posted mid-turn`.

`placeAttachments` runs only at dispatch, so the file is not written into the sandbox or mentioned. Filed.

## Bugs fixed and filed

- **Fixed here:** F1, with two orchestrator regression tests and a live re-check on the daemon.
- **Filed** (each depends on this task):
  - F3: console-session grant edits reverted on restart;
  - F4: keep_running warm sandbox reaped mid-reuse;
  - F5: attachments on in-process and mid-run messages.
- **Security:** no token outlives a grant narrowing with its old grants (F2).
