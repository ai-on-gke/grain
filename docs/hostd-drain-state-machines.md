# Host agent drain, cutover, handover and drain-cap: state machines

How grain hostd (`pkg/hostd`) and the daemon side (`pkg/runhost`,
`pkg/orchestrator`) move a run, a start request and hostd itself through
an upgrade. This is a map for reviewing changes here, not a contract.
The wire contract is `design/design/agentd-stable-contract.design.md`.
Written for the 2026-11 bug hunt (task 4821). Line references are to
`pkg/hostd/server_linux.go` unless named.

## hostd's own state

Fields under `Server.mu`: `draining`, `cutover`, `handover`, `closing`,
`handedOver`. The channels are `drainCh` (closed once, wakes
`waitDrained`), `handoverCh` (closed when a handover completes) and
`changed` (a 1-slot wake-up whenever a process ends, a start is admitted
or a detached stdin closes).

| State | Entered by | Starts | Hello says | Leaves to |
|---|---|---|---|---|
| Serving | `Serve` | taken | — | any below |
| Cutover | `OpDrain{Idle}` → `markCutover`, answer, `beginCutover`; or `Cutover()` | taken | `Cutover` | Closing at the first moment `n == 0`; Draining once `drain_cap` has passed |
| Handover | `OpDrain{Idle,Handover}` with `Reexec`/`AllowHandover` → `markHandover`, answer, `beginHandover` | taken | `Cutover` | Closing+handedOver once no protocol-1 CLI, carried listener, admitted start or open detached stdin remains; Draining at `drain_cap` (handover semantics then end, and every detached run counts) |
| Draining | `OpDrain{}` → `refuseStarts`, answer, `startDrainLocked`; `Drain()`; a cutover's first cap | CLI refused (`Refusal.Draining`); capability servers taken | `Draining` | Closing when `n == 0`; at the second cap stuck runs are killed with `killDraining` |
| Closing | `waitDrained` sees `n == 0`, under the same lock as the count | all refused | `Draining` | `waitDrained` returns → `cancel()` → Serve returns (exit; systemd starts the new build), or `Reexec` in the same pid |
| Stopped from outside | SIGTERM / ctx done, not handed over | — | — | every live process is killed with `killStopping`: its exit says Draining, with the Error "stopped because the host agent stopped or restarted" (since task 4821; `killStopping` since task 4910) |

`n` in `waitDrained` is `cliStarts` (starts admitted but not yet in
`procs`) + live non-capability processes (minus kept detached ones in a
handover) + `listeners` in a handover.

Orderings that matter:

* **The answer comes after the mark, the drain loop after the answer.**
  For a plain drain, starts are refused before the OK is written. For a
  cutover/handover the flags are set before the OK (#4715 / 727b8c86d),
  so a Hello sent once the daemon has its answer always reports it. The
  loop starts after the OK, because with nothing running it exits hostd
  at once.
* **Admission vs closing.** `serveConn`'s `OpStart` reads `closing` /
  `draining` and bumps `cliStarts` under one lock. `waitDrained` counts
  `cliStarts` and sets `closing` under that lock too, so no start slips
  between the count and the exit.
* **`drain_cap` is re-read on every pass** (`drainCap(limit)`), and it
  counts from when starts were first refused.

## A run's process

States are carried on `proc`: `info.Exited`, `info.Exit`, `killKind`
(first reason hostd killed it), `stdinOpen`, `attachGen`. Protocol-1
starts (`run`) are not attachable.

```
start admitted ──► running ──(exit)──► exited (exit.json written, p.done closed)
   │                 │                    │
   │                 │                    ├─ exit frame written to an attached daemon ─► released (spool removed)
   │                 │                    └─ no daemon attached ─► kept ≤ ExitedRetention, across restarts (loadSpools)
   │                 ├─ OpCancel ─► killCanceled ─► exited
   │                 ├─ deadline timer ─► killDeadline ─► exited (Exit.Deadline)
   │                 ├─ drain_cap ─► killDraining ─► exited (Exit.Draining)
   │                 ├─ hostd stopped ─► killStopping ─► exited (Exit{Draining, "stopped because the host agent stopped or restarted"})
   │                 ├─ handover ─► watcher released, process left running ─► adopted by the re-exec'd hostd (canAdoptLiveSpool)
   │                 └─ hostd crashed ─► no exit.json ─► next hostd writes Exit{Draining, "the host agent stopped while it ran"}
   └─ refused (draining / closing) ─► Refusal{Draining}
```

How the daemon reads an exit (`pkg/agent/hostd.go`, `runhost.Hostd.Attach`,
`orchestrator.classifyRunEnd`):

* `Refusal.Draining` or `Exit.Draining` with `Exit.Error == ""` (`drain_cap`)
  → `agent.ErrHostdDraining` → **interrupted**, no strike.
* `Exit.Draining` with `Exit.Error != ""` (`Exit.Stopped`: `killStopping` or
  `loadSpools` after a crash) → `agent.ErrHostdStopped` → **interrupted**,
  no strike, saying the host agent stopped or restarted rather than drained.
* The stream drops (`hostd.ErrNoExit`), or an adopted run's attach never
  reaches hostd (`hostd.ErrUnreachable`, since task 4821) → `reattach`
  for `-hostd-reattach-timeout` → `agent.ErrHostdLost` past it →
  **interrupted**. A refusal on reattach ("no detached process") is lost
  too.
* Anything else non-zero → the CLI's own failure.

## Interleavings checked

| Interleaving | Where | Outcome |
|---|---|---|
| Drain answered vs a start the daemon sends next | `serveConn` OpDrain | refused (fixed in 4c4daf6a3) |
| Cutover answered vs Hello | `markCutover` | reports Cutover (fixed in 727b8c86d, #4715) |
| Start admitted vs `waitDrained` count | `cliStarts` under `mu` | counted, holds the drain open |
| Process exit vs drain-cap kill | `p.cancel` reads `Exited` under `mu` | first `killKind` wins; an exited process is not re-killed |
| Serve stop vs protocol-1 `CommandContext` kill | `run` | the kill through Serve's ctx is counted as `killStopping` (task 4821; `killDraining` until task 4910) |
| Exit written vs hostd exiting | `attach` is in `s.wg`; Serve waits for it | the exit is delivered, or the spool is kept |
| Handover close vs attach loop | `handoverCh` in the attach select; `handedOverNow` | stream detaches, the daemon reattaches |
| Hostd away when an adopted run attaches | `runhost.Hostd.stream` | waits out ReattachTimeout (task 4821) |
| Reattach before an adopted attach has re-read its cursor | `cursorWriter.written` | resumes from what reached the parsed stdout, not the cursor (task 4821) |
| Reexec fails after watchers were released | `Serve`, `rewatchHandedOver` | the watchers start again, so each run is reaped with a Draining exit and Serve returns at once (task 4821; before, nothing reaped them and Serve waited `WaitDelay+1s` per run in turn) |
| Old Serve's socket cleanup vs the replacement binding the same path | `Serve` | Serve waits for the remove before it returns (task 4821; before, a late remove could unlink the new hostd's socket) |
