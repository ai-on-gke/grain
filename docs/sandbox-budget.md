# Sandbox budget: what holds a run back, and what never holds a session

Every sandbox grain builds, whether for an agent run or a CI job, is charged
its shape (vCPU, memory, sandbox disk) to one ledger (`model.Ledger`). That
ledger is held against one budget (`model.Budget`): the host, or the
cluster's allocatable, times the overcommit ratios, less the host reserve.
[Architecture](../design/design/architecture.design.md) has the whole picture. This page covers who
waits for whom.

## Ordinary work, CI and repairs

| Who | Admitted when | Rule |
| --- | --- | --- |
| Ordinary agent run | the shape fits under every ceiling, **less the repair reserve** | `Budget.Admits` |
| CI job | the same, and the CI gate's own ordering lets it in: priority, age boost, and the *hole* it holds for a large job that has waited (`ci.SharedGate.ReserveAfter`) | `ci.SharedGate` |
| Base-branch repair, and the CI it runs | the shape fits under every ceiling, repair reserve included | `Budget.AdmitsRepair` |
| Deadlock-breaking CI job | at most **one** sandbox over the cap, while committed is still within the cap | `Budget.AdmitsException` |

## VM memory headroom

A granule VM's container is allowed more memory than its guest has. The
VMM process itself takes some. Its disk writes also go through host page
cache charged to the same cgroup, unless the disk is opened `direct=on`.
Granule's own default limit used to be guest + 512 MiB with no swap. At
that limit, a guest that had filled its RAM with page cache and then
wrote a multi-GB file (`pack-guest`'s `mke2fs`) got its VMM OOM-killed. The
guest only saw "the guest closed the connection". Since
gke-labs/granule#238, granule's default is guest + max(512 MiB, guest/16)
(`vmspec.VMSpec.ContainerMemoryLimitMB`), its writable disks are opened
`direct=on` where the filesystem allows it, and it attaches a
virtio-balloon with free page reporting so the guest hands freed pages
back. Grain's own headroom is still the larger on every shape, which
`TestGranuleMemoryLimitIsNeverBelowGranulesOwn` checks.

Grain therefore always sets the limit itself
(`orchestrator.GranuleMemoryLimitMB`):

    limit = guest memory + max(1024 MiB, 25% of guest memory)

The limit is capped at the host's memory (local granule) or the largest
node's allocatable memory (`kubernetes-granule`), and never set below the
guest. Some figures:

| Guest | Limit |
| --- | --- |
| 2 GiB | 3 GiB |
| 4 GiB | 5 GiB |
| 8 GiB | 10 GiB |
| 16 GiB | 20 GiB |
| 32 GiB | 40 GiB |

- **Local granule** passes it as `-memory-limit-mb` on `granule vm create`,
  which sets the container's `--memory` and `--memory-swap`.
- **`kubernetes-granule`** sets it as the pod's memory limit. The pod's
  memory request is the same figure divided by
  `sandbox_memory_overcommit`, as for any pod. The cluster fit check
  (`Budget.ClusterFit`) is asked about that pod shape, headroom included.

The ledger charges the headroom, so the larger limit does not
oversubscribe a host. On the two granule backends, `Budget.VMMHeadroom` and
`Ledger.VMMHeadroom` add each sandbox's headroom to its memory, both to the
sandbox being admitted and to every live sandbox in the sum it is held
against. A 16 GiB agent sandbox therefore takes 20 GiB of the memory
budget. The health page's committed total (`Ledger.Live`) includes the
headroom. Holders keep the guest's own shape, so a run's recorded size
and the per-holder rows do not change. Container sandboxes, plain pods and host directories charge
nothing extra, because their shape is all they are allowed.

The percentages are constants (`model.VMMMemoryHeadroomPct`,
`model.VMMMemoryHeadroomMinMB`), not settings. Every sandbox setting is a
`grain_config` column with a migration, a state-repo binding and a
Settings field, and this is a correctness margin that nobody should need to
tune.

When a VM is killed at that limit anyway, the run or CI job is reported as
**killed for host memory** (`SandboxOOM`) rather than as a generic exec
error. Grain recognises this from an `oom_kill` in the VM container's
cgroup `memory.events`, docker's `OOMKilled`, or, on
`kubernetes-granule`, the container's `OOMKilled` termination. It is
handled as an infrastructure failure: an agent run is requeued without
counting against the task, and a CI job is retried in a fresh sandbox.

## Sessions are never held by the budget

A console session (the director, or any configured chat), and any task a
person set to `admission: bypass` or `durable` (such as the pipeline
steward; the setting replaced the `priority-dispatch` grant), is
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
number of live bypass- and durable-admission runs, which is at most one run per such task.
Durable sessions also stay under `max_durable_runs`.

A session is also outside the other ordinary gates: `max_workers` and the
merger count (`Limits.Admits`), `Store.StartRun`'s in-transaction count, and
a model connection's concurrency limit. It is ordered ahead of mergers and
ordinary work, and it passes every pipeline pause except an upgrade's own hold.

### What can still hold a session, and how to see it

A session needs an agent to run on. When every connection that could serve
its model is paused after a usage limit, disabled, or draining for a host
upgrade, the model-availability check refuses it as it would any task. On
2026-10-31 that is what held session 2174 for 16 minutes, not the budget.
Each refusal is now diagnosable:

- `dispatch: candidate N skipped: admission refused by <gate> -- <reason>`
  names the admitter's gate: `model availability`, `local policy`,
  `capability <name>`, or `capability pass`. It also gives the reason that
  gate recorded on the task.
- A bypass- or durable-admission task's refusal, by an admitter or by the budget, is
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
