# Auto overcommit

The sandbox budget (`pkg/model/budget.go`) is

    cpu budget    = (host cpus   - reserve) x cpu overcommit
    memory budget = (host memory - reserve) x memory overcommit

and until now both ratios were fixed: `sandbox_cpu_overcommit` (default
2.0) and `sandbox_memory_overcommit` (default 1.0). A fixed ratio is a
guess made without knowing what the sandboxes really use. On 2026-10-23
a 188 vCPU host sat at 184 vCPU committed for hours, skipping 50+
candidates a cycle for capacity, while most of those agent sandboxes
were idle, waiting on model turns. Memory fails the opposite way. It
starts at 1.0 because of grain/task-173: a host that promised 136 GiB
of a 128 GiB machine went 12 GiB into swap, its load reached the core
count, and the UI slowed down. A fixed 1.0 never finds out how much
headroom a host really has.

**Auto overcommit** puts the two ratios under a feedback controller
(`model.OvercommitController`). It trades a few OOM-killed sandboxes,
which are retried, for more concurrent work.

## The setting

`grain_config.sandbox_overcommit` (schema 35), JSON, exported to
grain/config with the rest of the row and edited through
`PUT /api/settings` as `sandboxOvercommit`:

| field | default | meaning |
|---|---|---|
| `mode` | `fixed` | `fixed` admits at the stored ratios, exactly as before. `auto` admits at the controller's. |
| `cpuMin` / `cpuMax` | 1.0 / 6.0 | bounds on the CPU ratio |
| `memoryMin` / `memoryMax` | 1.0 / 2.0 | bounds on the memory ratio |
| `cpuPressureTarget` | 25 | CPU PSI `some` avg10, in %, to stay under |
| `memoryPressureTarget` | 5 | memory PSI `some` avg10, in %, to stay under |

Zero bounds mean grain's defaults. A setting that does not validate is
treated as `fixed`, and `statecheck` reports it. In auto mode the stored
fixed ratios are only the starting point, clamped into the bounds. Memory
therefore starts at 1.0, for grain/task-173's reason.

## The controller

Every 30 s (`model.OvercommitInterval`) the daemon reads the host:

- PSI `some`/`full` avg10 for cpu, memory and io, from `/proc/pressure`
  (`sysstat.ReadPressure`);
- `pswpin` and `oom_kill` from `/proc/vmstat`, turned into a swap-in
  rate and a count of OOM kills since the last reading;
- available memory, swap in use, and load relative to cores;
- what the live sandboxes commit (`Ledger.Live`).

It then steps the controller:

- **Multiplicative decrease** on the first reading where memory PSI is
  over its target, swap-in is over 4 MB/s, or anything was OOM-killed.
  Memory is multiplied by 0.8, or by 0.7 for an OOM kill. If CPU PSI is
  over its target or load is over 1.5 per core, CPU is multiplied by 0.8.
- **Additive increase** (+0.25 CPU, +0.05 memory) for a dimension only
  when every reading is under *half* its target, there is no swap-in and
  no OOM kill, at least 85% of that dimension's budget is committed
  (work is waiting for it), there have been 4 calm readings in a row
  since the last change, and at least 10 minutes have passed since the
  last decrease.
- Between half the target and the target, nothing moves (hysteresis).

A lower ratio never kills anything. `Budget.Admits` is its only reader,
so a smaller budget only refuses the next admission. A decrease also
never goes below the ratio the live sandboxes already hold: at that
point nothing more is admitted, and dropping further would only delay
recovery once they drain.

Every change is logged with its inputs, for example:

    grain daemon: auto overcommit: memory 1.40x -> 1.12x (memory pressure 9.3% over its 5.0% target; psi cpu 4.1% memory 9.3/2.0% io 0.4/0.1%, swap-in 0.0 MB/s, ...)

The controller runs only on backends whose sandboxes are built on the
daemon's own machine (granule, container, host). Under Kubernetes the
cluster scheduler does admission, and this host's pressure says nothing
about it.

## Where to see it

- `GET /api/settings`: `sandboxOvercommit` (resolved), and
  `sandboxCommitment.budget.mode` / `.overcommit`, which hold the
  effective ratios, the last reading, recent adjustments and OOM kills.
  In auto mode, `budget.cpuOvercommit`/`memoryOvercommit` are the
  controller's ratios: the budget that is really admitted against.
- `GET /api/metrics` / `read_grain_metrics` section `host`:
  `host.overcommit`, with the adjustments made inside the window, to
  read against throughput and `capacity`. `grain metrics` prints it
  under the host section.

## Sacrificing sandboxes, not the controller

When memory runs out, the kernel has to kill a sandbox, never the
daemon:

- `grain-daemon.service` (host mode) and `grain-hostd.service` carry
  `OOMScoreAdjust=-900`. The containerised daemon `setup.sh` starts runs
  with `--oom-score-adj -900`.
- hostd sets every process it starts back to `+500`
  (`hostd.ChildOOMScoreAdj`), since children inherit the unit's score.
- Container sandboxes run with `--oom-score-adj 500`, with
  `--memory`/`--memory-swap` at the shape's size, so they cannot swap
  past their own memory.
- Granule VM sandboxes pass `-memory-limit-mb` (`shape.MemoryMB` plus
  512 MiB VMM overhead) and `-oom-score-adj 500` on `granule vm create`,
  which `dockervm.Create` applies as `--oom-score-adj 500`,
  `--memory <limit>m` and `--memory-swap <limit>m` on the VM container
  (setting `memory.max` and bounding `memory.swap.max` to 0).
- Kubernetes sandboxes (`K8sSandboxes` and `K8sGranuleSandboxes`) set
  matching `resources.requests.memory` and `resources.limits.memory` on
  the sandbox container (`pkg/k8s/k8s.go`), which kubelet enforces as a
  per-pod cgroup `memory.max` with swap disabled.
- Host-mode `HostSandboxes` (`RunShell`, `mcp.NewHostSandboxTools`,
  `agent.LocalHost`, `console.Manager`) reset every spawned child
  process to `+500` (`procgroup.SacrificeOnOOM`,
  `procgroup.ChildOOMScoreAdj`), matching `hostd`.
- `MinReserveCPUs` and the memory reserve are unchanged. They come off
  the top before either ratio applies.

## When a sandbox is OOM-killed

grain recognises its own sandboxes dying of OOM
(`orchestrator.SandboxOOMReporter`), asked once a run or CI job has
failed and before its sandbox is released:

| backend | what counts |
|---|---|
| granule | the VM container's `docker inspect` says `OOMKilled`, or it exited 137 (the VM process was SIGKILLed), or the guest's serial console holds the guest kernel's OOM message (`invoked oom-killer`, `Out of memory: Killed process`). The console line is only logged: the guest can write to its console, so none of it is quoted into the conversation note or a CI job's warning |
| container | `docker inspect` `State.OOMKilled` is true (exit 137 alone is not enough: `docker kill` exits 137 too) |
| k8s | a container status, current or last, has reason `OOMKilled` |

Every one found calls `OvercommitController.NoteOOM` (through
`orchestrator.Config.NoteOOM` and `ci.Engine.NoteOOM`, both wired to the
daemon's `Config.Overcommit`), so the controller backs off whether or not
the host kernel's own `oom_kill` counter saw it.

**Agent runs** that failed this way are recorded as `model.OOMOutcome`
(`oom`) instead of `failed` (`orchestrator.applySandboxOOM`):

- no failure streak counts it, so it does not move the task towards
  `MaxConsecutiveFailures`, and it adds no retry backoff;
- the task goes back to the queue and resumes its conversation, as after
  a daemon restart;
- the task's conversation gets a note that opens "sandbox was OOM-killed
  under memory overcommit; retrying";
- the task row reads "retrying after OOM kill" until its next run starts
  (`Store.OOMRetrying`);
- the second OOM kill in a row doubles the task's `SandboxMemoryMB`,
  from the task's own size or else the backend's default, and so does
  the third, but never past four times the backend's default
  (`oomMemoryCeilingFactor`): a later streak, after an ordinary failure,
  does not raise it again;
- past `model.MaxOOMRetries` (3) in a row the run is recorded as an
  ordinary failure again. A task that needs more memory than the host
  has still reaches `MaxConsecutiveFailures`.

**CI jobs** whose sandbox was OOM-killed are retried once in a fresh
sandbox through the engine's existing retry path, whatever the job
declares. A pass on the retry is `passed (retry after OOM kill)` with an
`infrastructure:` warning. It is not marked `Flake`, so test history does
not count it as a flake or a failure. A second OOM kill on the retry is
`errored`, the machine's answer rather than the tree's, and there is no
third attempt.

## Not done yet

These are follow-ups and are not in this change:

- The Settings → Sandboxes and System → Health panes drawing the mode,
  the effective ratios, the adjustments and the OOM kills, and a label
  for the `oom` outcome in the attempt timeline (`ui/src/state.js`
  `OUTCOME_LABELS`). The data is in the API (above); the panes do not
  draw it yet.
- Pinning a task to non-overcommitted admission after repeated OOM
  kills, as an alternative to asking for more memory.
