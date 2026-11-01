# Sandbox budget: what holds a run back, and what never holds a session

Every sandbox grain builds, whether for an agent run or a CI job, is charged
its shape (vCPU, memory, sandbox disk) to one ledger (`model.Ledger`). That
ledger is held against one budget (`model.Budget`): the host, or the
cluster's allocatable, times the overcommit ratios, less the host reserve.
[Architecture](architecture.md) has the whole picture. This page covers who
waits for whom.

## Ordinary work, CI and repairs

| Who | Admitted when | Rule |
| --- | --- | --- |
| Ordinary agent run | the shape fits under every ceiling, **less the repair reserve** | `Budget.Admits` |
| CI job | the same, and the CI gate's own ordering lets it in: priority, age boost, and the *hole* it holds for a large job that has waited (`ci.SharedGate.ReserveAfter`) | `ci.SharedGate` |
| Base-branch repair, and the CI it runs | the shape fits under every ceiling, repair reserve included | `Budget.AdmitsRepair` |
| Deadlock-breaking CI job | at most **one** sandbox over the cap, while committed is still within the cap | `Budget.AdmitsException` |

## Sessions are never held by the budget

A console session (the director, or any configured chat), and any task a
person granted `priority-dispatch` (such as the pipeline steward), is
admitted under its own rule, `Budget.AdmitsPriority`. Only what is
**physically short** refuses one:

- the host disk floor (`Budget.DiskFloor`): the volume really is nearly
  full, and a new sandbox would fail rather than run;
- on a Kubernetes backend, `Budget.ClusterFit`: no node can schedule the
  pod right now.

None of these hold a session:

- the vCPU, memory or disk-ratio ceilings, however much of them CI or
  ordinary work holds;
- the repair reserve;
- a commitment that is already over the cap, whether a CI exception or
  earlier sessions pushed it there;
- a hole the CI gate is holding for a large job, which only that gate
  consults.

The session's sandbox is still **charged**. Until it is released, CI jobs and
ordinary agent runs see the room it took and wait for it. That is the
mechanism: a session *defers* CI rather than queueing behind it, and no
running CI job is killed to make room. Over-commitment is bounded by the
number of live priority-dispatch runs, which is at most one run per such task.
Durable sessions also stay under `max_durable_runs`.

A session is also outside the other ordinary gates: `max_workers` and the
merger count (`Limits.Admits`), `Store.StartRun`'s in-transaction count, and
a model connection's concurrency limit. It is ordered ahead of mergers and
ordinary work, and it passes every pipeline pause except an upgrade's own hold.

### What can still hold a session, and how to see it

A session needs an agent to run on. When every connection that could serve
its model is paused after a usage limit, disabled, or draining for a hostd
upgrade, the model-availability check refuses it as it would any task. On
2026-10-31 that is what held session 2174 for 16 minutes, not the budget.
Each refusal is now diagnosable:

- `dispatch: candidate N skipped: admission refused by <gate> -- <reason>`
  names the admitter's gate: `model availability`, `local policy`,
  `capability <name>`, or `capability pass`. It also gives the reason that
  gate recorded on the task.
- A priority-dispatch task's refusal, by an admitter or by the budget, is
  logged as `dispatch: WARNING: priority-dispatch task N ...`.
- The task's row shows the wait: `waiting: all connections serving X are
  busy (...)` for a model wait, and `waiting for sandbox budget: <the
  budget's words>` for the disk floor or the cluster.

The regression tests that pin this guarantee are named for it:
`TestConsoleSessionAdmittedWhenSandboxBudgetFullOfCI` (in `pkg/model` and
`pkg/dispatch`), `TestPriorityDispatchTaskAdmittedWhenSandboxBudgetFullOfCI`,
`TestConsoleSessionAdmittedWhileCIHoldsAHoleForABigJob`,
`TestEveryConsoleSessionAdmittedWhileTheBudgetIsOverItsCap`,
`TestStartRunNeverRefusesAConsoleSessionAtFullCapacity`, and
`TestConsoleSessionStartsWithinStartupBoundWhenSandboxBudgetFullOfCI`
(`pkg/orchestrator`, the 45-second initial-startup bound).
