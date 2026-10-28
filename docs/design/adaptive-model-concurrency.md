# Adaptive Model Concurrency

Status: design, for the owner's review. Written for task 10 (origin task 1).

## Problem & Motivation

Grain currently controls how many agent runs can execute simultaneously with two static integer caps:

1. **Per-connection `max_concurrency`** (`model.Connection.MaxConcurrency` in `pkg/model/config.go`, defaulting to `DefaultConnectionMaxConcurrency = 4` since task 8).
2. **Global `max_agent_concurrency`** (`daemon.RuntimeSettings.MaxConcurrency` in `pkg/daemon/config.go`, defaulting to `DefaultMaxConcurrency = 10` since task 8), alongside host sandbox budgets (`model.Budget` / `model.Ledger` in `pkg/model/budget.go` and `Limits.Workers` / `Limits.Mergers` in `pkg/model/limits.go`).

This static, run-scoped model breaks down in three ways in production:

### 1. Model-connection slots are held across non-model waits

In `pkg/daemon/agents.go`, `AgentPool.Build` acquires a `connectionLease` (`pkg/daemon/agents.go:127`) when a run starts and increments `p.inFlight[connID]` and `p.totalInFlight`. That lease is released only in `BackendFramework.Run`'s `defer release()` (`pkg/daemon/agents.go:814`) when the entire agent subprocess exits.

During a typical coding run, the agent spends only a fraction of its wall-clock time inside active model inference turns. The rest is spent waiting on:

- **Grain CI checks (`wait_for_checks`)**: 15–45 minutes per push where `grain mcpserver` blocks waiting for workflow jobs to complete while sending wait-credit heartbeats (`POST /api/tasks/{id}/wait-credit` in `pkg/ui/wait_credit.go` and `pkg/orchestrator/waitcredit.go`).
- **Long sandbox commands (`run_command`)**: multi-minute `go test ./...`, package builds, container builds, or UI end-to-end suites executing inside the sandbox VM.

While four runs are parked inside `wait_for_checks`, `p.inFlight[connID]` sits at `4 of 4`. The upstream model provider is receiving **zero requests**, the host VM budget (`model.Ledger`) may have plenty of spare CPU/memory for additional sandboxes, and yet runnable tasks sit in the backlog unable to start.

### 2. Static concurrency caps either under-utilize or overload model sources

A fixed default of `4` concurrent runs per connection is a blind guess:

- **Too low when capacity is abundant**: An enterprise Vertex AI project, a high-tier Anthropic API key, or a healthy Google account can often sustain 8–12 concurrent active streaming turns without errors.
- **Too high when upstream capacity degrades**: When a model tier experiences regional congestion (`503 UNAVAILABLE`, `529 overloaded_error`, or repeated in-flight 429/503 retries in `agy --log-file`), continuing to dispatch 4 (or 10) concurrent turns amplifies retry storms, triggers `turnTierRetry` 90-second aborts (`pkg/agent/antigravity/turndeadline.go:33`), and burns task attempts on `CapacityError` (`pkg/agent/capacity.go`).
- **Thundering herd after quota pauses**: When a connection hits a `UsageLimitError` (429 quota window) and enters `orchestrator.Pause` (`pkg/orchestrator/pause.go`), the pause expiry immediately re-opens all `MaxConcurrency` slots at once, slamming the freshly reopened rate-limit window.

### 3. Setup-time race between `ModelAvailability` and `AgentPool.Build`

In `pkg/orchestrator/cycle.go`, `ModelAvailability` (`pkg/daemon/agents.go:353`) checks `candidatesLocked` before `Sandboxes.Acquire` (`pkg/orchestrator/cycle.go:2002`) without reserving a connection slot. `AgentPool.Build` (`pkg/daemon/agents.go:460`) only acquires the `connectionLease` *after* the sandbox VM has been provisioned (`pkg/orchestrator/cycle.go:2031`). When two tasks are admitted in the same dispatch cycle (or an interactive chat session starts while a task is provisioning its sandbox), the second run finishes `Sandboxes.Acquire` only to fail in `Build` with:

```text
setup-failed: orchestrator: building the agent framework for run 10-1:
no configured agent backend matches team "default", model "gemini-pro-coordinator":
of 1 configured, 1 connection at its concurrency limit (4 of 4 runs)
```

---

## Goals & Non-Goals

### Goals

1. **Decouple sandbox concurrency from model-connection concurrency**: Hold sandbox resources (`model.Ledger` / `MaxWorkers`) for the lifetime of a run, but hold model-connection turn slots (`AgentPool`) only while a run is in an active model turn (or within a short tool-execution grace window).
2. **Prevent deadlocks and starvation on re-admission**: Guarantee that a run returning from `wait_for_checks` or a long `run_command` re-acquires a model turn slot ahead of new dispatches, credits any slot-wait time to `RunWaitCredits`, and falls back to bounded soft overcommit rather than losing its sandbox.
3. **Eliminate the `ModelAvailability` → `Build` setup race**: Reserve a connection lease before provisioning the sandbox.
4. **Adapt per-connection concurrency automatically (AIMD)**: Grow a connection's effective concurrency when demand is present and model turns complete cleanly without latency spikes; step down proportionally on in-flight provider retries, terminal `CapacityError`s (503/529), and `UsageLimitError`s (429).
5. **Give operators clear controls and live visibility**: Support both `adaptive` (auto-scaling within `[min, max]`) and `fixed` modes per connection in **Settings → Models**, `ModelBackendPage.jsx`, `grain settings`, and `grain metrics`.

### Non-Goals

- **Suspending or snapshotting sandbox VMs during `wait_for_checks`**: A sandbox holds live processes, open network sockets, and uncommitted git worktrees. Sandboxes remain allocated in `model.Ledger` for the full run; only the model-connection turn slot is yielded.
- **Replacing provider quota windows (`orchestrator.Pause`)**: Hard account/project quota exhaustion with an explicit reset timestamp (`UsageLimitError`) continues to pause the connection via `orchestrator.Pause`; AIMD complements `Pause` by governing concurrency *before* and *after* pauses.

---

## 1. Separating Sandbox Concurrency from Model-Connection Concurrency

### 1.1 The Two-Resource Model

Every running agent consumes two orthogonal resources with different lifecycles:

| Resource | Governing Component | Lifetime | What It Protects |
| :--- | :--- | :--- | :--- |
| **Sandbox Slot** | `model.Budget` & `model.Ledger` (`PoolAgent` in `pkg/model/budget.go`), plus `Limits.Workers` (`MaxWorkers`) & `Limits.Mergers` (`MaxMergers`) in `pkg/model/limits.go` | **Entire run** (`Sandboxes.Acquire` → `defer Sandboxes.Release` in `pkg/orchestrator/cycle.go:2002-2038`) | Host VM/container CPU, RAM, disk floor (`DiskFloor`), and upper bound on live sandboxes |
| **Model-Turn Slot** | `AgentPool` (`pkg/daemon/agents.go`) per-connection `effectiveConcurrency` and pool-wide `maxConcurrent` | **Active model turn** (plus a short `ToolYieldGrace = 10s` buffer across fast tool calls) | Upstream model provider RPM/TPM/concurrent-stream limits and remote `modelexec` worker slots |

```
Run 10-2 lifecycle on connection "vertex-gemini-pro":

Sandbox Slot (model.Ledger / MaxWorkers)
[======================= HELD FOR ENTIRE RUN (42m) =======================]

Model-Turn Slot (AgentPool connectionLease)
[Turn 1..4 (3m)][--- Yielded: wait_for_checks (28m) ---][Gate (4s)][Turn 5..7 (11m)]
       ^                           ^                         ^            ^
  ActiveTurn                  YieldedWait               Reacquiring   ActiveTurn
 (counts as 1)               (counts as 0)             (FIFO priority) (counts as 1)
```

### 1.2 Phase-Aware `connectionLease` State Machine

Replace the single integer counter `inFlight map[string]int` in `AgentPool` (`pkg/daemon/agents.go:108`) with a per-connection state tracker (`connPoolState`) and a phase-aware `connectionLease` struct attached to each run context:

```go
type LeasePhase int

const (
    // LeaseReserved holds a turn slot between dispatch selection (before
    // Sandboxes.Acquire) and the start of BackendFramework.Run.
    LeaseReserved LeasePhase = iota

    // LeaseActiveTurn holds an active model-turn slot while the agent is
    // streaming a model response or executing a short tool call (< ToolYieldGrace).
    LeaseActiveTurn

    // LeaseYieldedWait remains attached to the connection for accounting and
    // re-admission affinity, but holds 0 active model-turn slots because the
    // run is blocked in wait_for_checks or a tool call >= ToolYieldGrace.
    LeaseYieldedWait

    // LeaseReacquiring is queued in the connection's priority FIFO turn gate
    // waiting to transition back from LeaseYieldedWait to LeaseActiveTurn.
    LeaseReacquiring
)
```

For each connection `id`, `AgentPool` tracks:

- `activeTurns int`: runs in `LeaseReserved` or `LeaseActiveTurn` (including any in soft overcommit).
- `yieldedWaits int`: runs in `LeaseYieldedWait`.
- `reacquiring []*connectionLease`: FIFO priority queue of already-running agents waiting to resume their next model turn after a yielded wait.
- `attachedRuns int`: `activeTurns + yieldedWaits + len(reacquiring)`.

#### Fixing the Setup Race (`ReserveConnection`)

Instead of calling `ModelAvailability` (read-only) and acquiring the lease minutes later inside `AgentPool.Build`, `runOne` (`pkg/orchestrator/cycle.go`) acquires a `LeaseReserved` lease **before** `Sandboxes.Acquire` and passes it via `context.Context` into `Agents.Build`. If sandbox acquisition fails, `defer lease.Release()` returns the slot immediately. This eliminates the `setup-failed: connection at its concurrency limit` race seen in task 10 attempt 1.

**Decision:** Should `AgentPool` reserve the connection lease before `Sandboxes.Acquire` rather than inside `AgentPool.Build`?
*Recommended: Yes. Acquiring `LeaseReserved` at admission time closes the setup race between concurrent task starts and interactive chats, and ensures sandbox setup never starts for a task whose model connection was just taken by a peer.*

### 1.3 Live Phase & Activity Detection Across Frameworks

A `connectionLease` transitions between `LeaseActiveTurn` and `LeaseYieldedWait` via two complementary signal paths:

#### Path A: Immediate Yield on Explicit Wait-Credit Events (`t = 0s`)

When an agent calls `wait_for_checks` inside `grain mcpserver`, the MCP server already sends `POST /api/tasks/{id}/wait-credit` (`pkg/ui/wait_credit.go` → `orchestrator.RunWaitCredits.Credit` in `pkg/orchestrator/waitcredit.go`) every few seconds while blocking on CI.

- On the first wait-credit request (or an explicit `"phase": "begin"` field on `waitCreditRequest`), the daemon notifies `AgentPool.NotifyWaitBegin(taskID)`.
- Because `wait_for_checks` is known up front to be a multi-minute non-model wait, the run's `connectionLease` transitions **immediately** (`t = 0s`) from `LeaseActiveTurn` to `LeaseYieldedWait`, waking the next waiter in `reacquiring` or opening a slot for a new task dispatch.

#### Path B: Stream-Transcript Tool Phase Detection (`ToolYieldGrace = 10s`)

During normal coding work, agents make many sub-second tool calls (`read_file`, `edit_file`, `git status`) interleaved with model turns. Yielding the connection slot on a 200ms `read_file` call would cause slot thrashing and unnecessary turn-gate waits.

Instead, each agent framework's live stream parser drives a `TurnPhaseObserver` interface on the `connectionLease`:

```go
type TurnPhaseObserver interface {
    OnModelTurnStart()
    OnToolExecStart(toolName string)
    OnToolExecEnd()
}
```

When `OnToolExecStart` fires (and no model stream is active), `connectionLease` arms a `ToolYieldGrace = 10s` timer:

- If `OnToolExecEnd` or `OnModelTurnStart` fires within `10s`, the timer is cancelled and the lease stays in `LeaseActiveTurn` with zero overhead.
- If the tool execution is still running after `10s` (e.g., `run_command` running `go test ./...`, a container build, or `retrigger_ci`), the timer fires and transitions the lease from `LeaseActiveTurn` to `LeaseYieldedWait`.

Each framework already parses the exact stream events needed to drive `TurnPhaseObserver`:

| Framework | Existing Stream Parser | Model Turn Active Signal | Tool Execution Active Signal |
| :--- | :--- | :--- | :--- |
| **`antigravity`** | `pkg/agent/antigravity/turndeadline.go` (`turnDeadline.Observe`, lines 187–217) | `step_update` with `step_type == "agent_response"`, or initial prompt dispatch (`armLocked`) | `step_update` with `step_type == "tool"` and `state == "ACTIVE"` (`disarmLocked` when `activeTools > 0`) |
| **`claude`** | `pkg/agent/claude/transcript.go` (`live_transcript.go` JSON line scanner) | Prompt dispatch; `"type": "user"` line with `"type": "tool_result"` clearing all pending `tool_use` IDs; `"type": "assistant"` streaming text/thinking | `"type": "assistant"` message emitting `"type": "tool_use"` blocks (`pending[id] = name` while awaiting matching `tool_result`) |
| **`codex`** | `pkg/agent/codex/transcript.go` (`codex exec --json` event scanner) | `turn.started` / `agent_message` / `reasoning` item in progress | `item.started` with `command_execution` or `mcp_tool_call` (`mcp_tool_call_begin` until `mcp_tool_call_end` / `item.completed`) |

**Decision:** What grace period should elapse inside a non-`wait_for_checks` tool call before yielding the model-connection slot?
*Recommended: `ToolYieldGrace = 10s` (with `0s` immediate yield for `wait_for_checks`). Fast file reads, edits, and git commands complete in under 2 seconds and keep their slot; multi-minute test suites and builds yield after 10 seconds.*

### 1.4 Re-Admission When Resuming a Model Turn

When a run in `LeaseYieldedWait` finishes `wait_for_checks` or a >10s `run_command`, the CLI subprocess (`agy`, `claude`, or `codex`) is parked waiting for `grain mcpserver` to write the JSON-RPC tool result back over stdio. The instant `grain mcpserver` returns that tool result, the CLI will send the next model turn request to the upstream provider.

If another task was dispatched into the yielded slot while the first run was in CI, `activeTurns` may now equal `effectiveConcurrency`. We need a re-admission mechanism that:

1. Gives already-running sandboxes strict priority over new dispatches.
2. Coordinates in-process before the CLI fires the upstream HTTP request.
3. Credits any re-admission wait time so `MaxRunRuntime` (`120m`) and `turnDeadline` (`8m`) are never penalized.
4. Never deadlocks or kills a live sandbox if existing turns take longer than expected.

#### Hybrid Turn Gate + Bounded Soft Overcommit

```
grain mcpserver (inside tool handler, e.g. wait_for_checks / run_command)
   │
   ├─ Tool work finishes (CI verdict ready / command exited)
   │
   ├─ POST /api/tasks/{id}/turn-acquire  ──────────────────────────────┐
   │    (blocks up to ReacquireSlotTimeout = 90s while daemon queues   │
   │     lease in conn.reacquiring FIFO ahead of any new dispatches;   │
   │     daemon credits wait duration to RunWaitCredits & resets       │
   │     turnDeadline)                                                 │
   │                                                                   ▼
   │  ◄── 200 OK {"granted": "slot" | "soft_overcommit", "waited_ms": 14200}
   │
   └─ Returns MCP tool result to CLI stdout ──► CLI starts next model turn
```

1. **In-Process MCP Turn Gate (`POST /api/tasks/{id}/turn-acquire`)**:
   - Before `grain mcpserver` returns the result of `wait_for_checks` or any `mcp_grain-sandbox_*` tool call that lasted `>= ToolYieldGrace` (or during which `wait-credit` was active), it calls `POST /api/tasks/{id}/turn-acquire` on the daemon.
   - If the run's lease is still in `LeaseActiveTurn`, the daemon returns `200 OK` immediately (0ms wait).
   - If the lease is in `LeaseYieldedWait` and `activeTurns < effectiveConcurrency` (with no earlier waiters), the daemon transitions the lease to `LeaseActiveTurn`, increments `activeTurns`, and returns `200 OK` immediately.
   - If `activeTurns >= effectiveConcurrency`, the daemon places the lease in `conn.reacquiring` (FIFO order) in state `LeaseReacquiring` and holds the HTTP request open until a turn slot is freed by another run yielding or finishing.

2. **Strict Priority Over New Dispatches**:
   - In `AgentPool.candidatesLocked` (`pkg/daemon/agents.go:258`), a connection is only considered to have spare capacity for a **new** task when:
     ```go
     conn.activeTurns + len(conn.reacquiring) < conn.effectiveConcurrency &&
         conn.attachedRuns < conn.maxAttachedRuns()
     ```
   - Whenever an active turn yields or finishes, `AgentPool` hands the freed slot to `conn.reacquiring[0]` before any new dispatch cycle can claim it.

3. **Wait-Credit and Turn-Deadline Protection**:
   - While a run is parked in `LeaseReacquiring`, the daemon automatically calls `RunWaitCredits.Credit(taskID, waited)` (`pkg/orchestrator/waitcredit.go`) and pauses the run's `turnDeadline` timer (`pkg/agent/antigravity/turndeadline.go`). Waiting for a connection slot never counts against `MaxRunRuntime` or triggers a false `turnTierFirstStep` / `turnTierTotal` timeout.

4. **Soft Overcommit Fallback (`ReacquireSlotTimeout = 90s`, `MaxSoftOvercommit = 2`)**:
   - What if all `effectiveConcurrency` active turns are long, multi-minute reasoning turns (or `effectiveConcurrency` was just reduced by a 503 congestion event) and no slot opens within `90s`? Or what if a run used a built-in non-MCP tool (e.g., a native CLI read/search) and resumed directly?
   - After `ReacquireSlotTimeout = 90s` (or immediately on a transcript-detected `OnModelTurnStart` when the lease was still `LeaseYieldedWait`), the daemon grants the transition to `LeaseActiveTurn` via **soft overcommit**, allowing `activeTurns` to temporarily exceed `effectiveConcurrency` up to `effectiveConcurrency + MaxSoftOvercommit` (`+2`).
   - While `activeTurns >= effectiveConcurrency`, new dispatches remain blocked and subsequent turn releases drain the overcommit back down to `effectiveConcurrency`.

5. **Bounding Attached Runs per Connection (`maxAttachedRuns`)**:
   - To prevent an unbounded pileup of yielded sandboxes on a single connection during a slow CI wave (which would cause a wave of simultaneous re-admissions when CI finishes), each connection enforces a cap on total attached runs (`activeTurns + yieldedWaits + len(reacquiring)`):
     ```go
     maxAttachedRuns = max(2 * effectiveConcurrency, effectiveConcurrency + 4)
     ```
   - For example, at `effectiveConcurrency = 4`, up to `8` total runs may be attached to that connection simultaneously (up to `4` in active model turns and up to `4` yielded in CI/tool waits), subject to the host's sandbox budget.

**Decision:** When a run finishes `wait_for_checks` and wants to resume its next model turn, should it wait briefly in-process on a connection slot or immediately soft-overcommit?
*Recommended: Hybrid—wait in-process at the MCP Turn Gate (`POST /api/tasks/{id}/turn-acquire`) with strict FIFO priority ahead of new dispatches for up to `ReacquireSlotTimeout = 90s` (crediting the wait to `RunWaitCredits`), then fall back to bounded soft overcommit (`+2`) so a live sandbox never deadlocks or fails.*

### 1.5 Interaction with Global `MaxWorkers` and `model.Budget` / `model.Ledger`

Yielding a model-connection slot **never** yields the run's sandbox reservation:

1. **`model.Budget` and `model.Ledger` (`pkg/model/budget.go`, `pkg/orchestrator/cycle.go:1022`)**:
   - `syncSandboxLedger` continues to account for every live sandbox (`activeTurns + yieldedWaits + reacquiring`) in `PoolAgent`.
   - `Sandboxes.CanFitTask` (`pkg/orchestrator/cycle.go:1182`) still gates whether the host has enough CPU, memory (with overcommit ratios and reserve), and disk floor (`DiskFloor`) to spin up an additional sandbox.
2. **Global `MaxWorkers` / `MaxMergers` (`pkg/model/limits.go`) and `max_agent_concurrency` (`pkg/daemon/config.go`)**:
   - `Limits.Workers` (`MaxWorkers`) and `Limits.Mergers` (`MaxMergers`), when configured (`> 0`), bound the total number of live agent/merger sandboxes on the daemon.
   - `RuntimeSettings.MaxConcurrency` (`max_agent_concurrency`, default `10` in `pkg/daemon/config.go`) bounds `AgentPool.totalActiveTurns`—the total number of **simultaneous active model turns** across all connections in `AgentPool`—while total live sandboxes are bounded by `model.Budget` / `MaxWorkers`.
   - **Concrete example**: Suppose a host has CPU/RAM budget for 10 sandboxes (`MaxWorkers = 10` or `model.Budget` capacity for 10 VMs) and 1 configured model connection with `effectiveConcurrency = 4`.
     - Under the **current system**, at most 4 runs can exist at once. When 3 of them enter a 30-minute `wait_for_checks`, 6 sandbox slots sit empty and only 1 model turn runs at a time (25% model utilization, 40% sandbox utilization).
     - Under **this design**, as soon as those 3 runs enter `wait_for_checks`, their leases transition to `LeaseYieldedWait` (`activeTurns` drops from `4 → 1`, `attachedRuns` is `4`). The orchestrator admits 3 more tasks from the backlog into fresh sandboxes (`attachedRuns` becomes `7 <= maxAttachedRuns(8)`, `activeTurns` returns to `4`, total sandboxes `7 <= 10`). Model turn utilization stays at 100% (`4/4` active turns) without exceeding either the connection's active turn cap (`4`) or the host's sandbox budget (`10`).

---

## 2. Adaptive Per-Connection Concurrency (AIMD / Congestion Control)

Rather than requiring the operator to guess a static `max_concurrency` for each model connection, each connection runs an independent **Additive-Increase / Multiplicative-Decrease (AIMD)** congestion controller (`adaptiveController`) inside `AgentPool`.

### 2.1 Controller State & Bounds

For each `model.Connection`, the controller maintains:

```go
type adaptiveController struct {
    mode               ConcurrencyMode // "adaptive" (default) or "fixed"
    minConcurrency     int             // floor (default 1)
    initialConcurrency int             // starting window (default 4)
    maxConcurrency     int             // ceiling (default 12 in adaptive, 4 in fixed)

    effective          int             // current active-turn window W
    cleanTurns         int             // clean model turns since last window change
    cleanTurnDuration  time.Duration   // cumulative active model-turn time since last change
    demandObserved     bool            // true if connection was at W with waiting work in window

    baselineLatencyMs  float64         // slow EWMA (alpha=0.05) of model turn latency / TTFB
    recentLatencyMs    float64         // fast EWMA (alpha=0.25) of model turn latency / TTFB

    lastScaleAt        time.Time
    lastScaleReason    string          // human-readable audit string for UI/CLI
    decreaseCooldownTo time.Time       // debounce window for concurrent in-flight retry blips
    increaseCooldownTo time.Time       // hold window after congestion before probing upward
    prePauseEffective  int             // W saved when entering a 429 UsageLimitError pause
}
```

Default bounds in `adaptive` mode:
- `MinConcurrency = 1`
- `InitialConcurrency = 4` (`DefaultConnectionMaxConcurrency`)
- `MaxConcurrency = 12` (`DefaultAdaptiveMaxConcurrency`, capped at `executor.AdvertisedConcurrency` for remote `modelexec` workers)

### 2.2 Additive Increase (Probing for Saturation)

A connection should only increase its concurrency window $W$ (`effective`) when **both** of the following hold: it actually needs more slots to serve demand, and its current concurrency level $W$ is running cleanly without upstream queueing delay.

#### 1. Demand Gate (Saturation Condition)

Growing $W$ when only 1 task is running would inflate $W$ to `MaxConcurrency` in the dark, removing congestion protection when a burst of 12 tasks suddenly arrives. Therefore, `cleanTurns` and `cleanTurnDuration` only accumulate toward an additive increase while **at least one** saturation condition holds during the turn:
- `activeTurns >= effective` (the connection is at its active turn cap), **or**
- `len(reacquiring) > 0` or `ModelAvailability` recorded at least one runnable task blocked (`busy`) on this connection within the last 60 seconds.

#### 2. Clean Work Threshold

Once `time.Now().After(increaseCooldownTo)`, the controller increments $W \leftarrow \min(W + 1, \text{MaxConcurrency})$ when the connection has accumulated **both**:
- **At least $N_{\text{clean}} = 8$ completed model turns** without any provider retry or error signal, **and**
- **At least $T_{\text{clean}} = 3\text{ minutes}$ of cumulative active model-turn duration** (`cleanTurnDuration >= 3m`).

Requiring *both* a turn count ($8$ turns) and cumulative turn time ($3\text{m}$) prevents 8 sub-second cached/trivial turns from ramping concurrency in 10 seconds, while also preventing a single 15-minute turn from ramping concurrency on a sample size of 1.

#### 3. Latency Degradation Guard (Queueing Delay Detection)

Before upstream providers return HTTP `429` or `503`, they often queue requests at the inference router, causing time-to-first-token (TTFT) or per-turn response time to climb sharply.

- Whenever a model turn completes cleanly, the controller updates `baselineLatencyMs` (slow EWMA, $\alpha = 0.05$, initialized over the first 4 turns) and `recentLatencyMs` (fast EWMA, $\alpha = 0.25$) using the turn's time-to-first-response-step (from `live_transcript` or `modelproxy` TTFB).
- If `recentLatencyMs > 2.5 * baselineLatencyMs` (and `recentLatencyMs - baselineLatencyMs > 3000ms`), the connection is experiencing **upstream queueing delay**: the controller suppresses the $+1$ additive increase and records `lastScaleReason = "held at Nx: turn latency 2.8x above baseline"`.

**Decision:** What exact signals should trigger additive increase?
*Recommended: Require (1) active saturation/demand on the connection, (2) at least 8 consecutive clean model turns AND at least 3 cumulative turn-minutes with zero retry/capacity signals, and (3) fast-EWMA turn first-response latency within $2.5\times$ of the connection's baseline EWMA.*

### 2.3 Multiplicative Decrease & Cooldowns (Backing Off on Congestion)

Congestion signals fall into three severity tiers, each with its own step-down formula and cooldown:

| Tier | Signal Source | Severity | Window Adjustment ($W \leftarrow \dots$) | Cooldown / Debounce Behavior |
| :--- | :--- | :--- | :--- | :--- |
| **Tier 1: In-Flight Provider Retry** | `agy` `--log-file` `providerRetryRecord` (`turndeadline.go:407`), `codex` `"Reconnecting... n/5"` (`codex/retry.go:150`), `claude` single-turn wrapper retry (`claude/retry.go`) or silent turn stall `> 45s`, `modelproxy` upstream `429`/`503`/`529` retry | **Mild / Early warning** (turn has not failed yet, provider is pushing back) | $W \leftarrow \max(\text{Min}, \min(W - 1, \lfloor 0.75 \times W \rfloor))$ | **`DecreaseCooldown = 45s`** debounce against concurrent in-flight turns; **`IncreaseCooldown = 90s`** before probing up |
| **Tier 2: Terminal `CapacityError` or Retry Cutoff** | `agent.AsCapacityError(err)` (`503 UNAVAILABLE`, `529 overloaded_error` in `pkg/agent/*/capacity.go`) or `antigravity` `turnTierRetry` (`90s` backoff cutoff in `turndeadline.go:33`) | **Hard congestion** (a run attempt was aborted by provider overload) | $W \leftarrow \max(\text{Min}, \lfloor 0.50 \times W \rfloor)$ | Supersedes any Tier 1 debounce; **`IncreaseCooldown = 3m`** before probing up; resets `cleanTurns = 0` |
| **Tier 3: Terminal `UsageLimitError` (429 Quota Pause)** | `agent.AsUsageLimitError(err)` (`pkg/agent/usagelimit.go`) triggering `orchestrator.Pause.Begin` (`pkg/orchestrator/pause.go`) | **Quota window exhausted** (connection paused until `resetsAt`) | Save `prePauseEffective = W`; set $W = \text{Min}$ during pause; on unpause set $W \leftarrow \max(\text{Min}, \lceil 0.50 \times \text{prePauseEffective} \rceil)$ | **Slow-start recovery** after pause lifts, with **`IncreaseCooldown = 60s`** after `resetsAt` to avoid slamming the reopened quota window |

#### Why Coalescing / Debouncing (`DecreaseCooldown = 45s`) Is Essential

When a connection is running at $W = 8$ and the upstream model tier has a 5-second regional blip, all 8 in-flight turns may log a `503 UNAVAILABLE` retry within the same 2-second window. If each retry independently applied $W \leftarrow \lfloor 0.75 \times W \rfloor$, a single transient blip would collapse $W$ from $8 \to 6 \to 4 \to 3 \to 2 \to 1$.

To prevent cascade collapse:
- When a Tier 1 decrease fires at $t_0$, `decreaseCooldownTo` is set to $t_0 + 45\text{s}$.
- Any further Tier 1 signals arriving before `decreaseCooldownTo` are counted in metrics (`coalesced_retries++`) and refresh `increaseCooldownTo = now + 90s`, but do **not** step $W$ down a second time.
- If a **Tier 2** terminal `CapacityError` occurs during a Tier 1 cooldown, it *does* apply the remainder of the $0.50 \times W_{\text{pre-blip}}$ cut so that a genuine outage still halves the pre-incident window.

#### What Happens to In-Flight Runs When $W$ Decreases?

Stepping $W$ down (e.g., from `8 → 4`) **never kills or preempts** the runs currently in `LeaseActiveTurn`. Instead, `activeTurns` (`8`) simply sits above the new `effective` (`4`); no new tasks or `reacquiring` runs are admitted (except via the `90s` soft-overcommit safety valve) until natural turn completions and tool yields drain `activeTurns` below `4`.

**Decision:** How should multiplicative decrease handle concurrent retry signals and post-429 pause recovery?
*Recommended: Use a three-tier step-down ($0.75\times$ on in-flight retry with a 45s coalesce window, $0.50\times$ on terminal 503/529 `CapacityError` with a 3m increase cooldown, and $0.50\times$ slow-start resume after a 429 `orchestrator.Pause` lifts) without preempting already-active turns.*

### 2.4 Framework & Transport Signal Matrix

Different agent frameworks expose different levels of wire visibility. `AgentPool` unifies them behind a common `CongestionObserver` callback attached to `connectionLease`:

```go
type CongestionObserver interface {
    OnInFlightRetry(status int, detail string)
    OnTurnCompleted(duration, firstTokenLatency time.Duration)
    OnTerminalCapacityError(err *agent.CapacityError)
    OnTerminalUsageLimit(err *agent.UsageLimitError)
}
```

| Framework / Transport | Turn Boundaries & Latency | In-Flight Retry Signal (Tier 1) | Terminal Capacity Signal (Tier 2) | Quota Pause Signal (Tier 3) |
| :--- | :--- | :--- | :--- | :--- |
| **`antigravity`** (`pkg/agent/antigravity`) | `turnDeadline.Observe` (`turndeadline.go:187`): `step_update` `agent_response` vs `tool` `ACTIVE`/`DONE`; first step latency | `pollRetryLog` (`turndeadline.go:290-335`) tailing `agy --log-file` (`providerRetryRecord` on 429/503/`UNAVAILABLE`/`RESOURCE_EXHAUSTED`) | `antigravity.ClassifyCapacityError` (`antigravity/capacity.go`) + `turnTierRetry` 90s retry-backoff kill (`turndeadline.go:33`) | `antigravity.ClassifyUsageLimit` (`antigravity/usagelimit.go`) |
| **`claude`** via **`pkg/modelproxy`** (Vertex AI / proxied API key) | `claude/transcript.go` (`tool_use` / `tool_result`) + `modelproxy` exact HTTP request duration & TTFB | **`modelproxy` wire inspection**: direct HTTP `429`, `503`, `529` status codes seen on upstream round-trips before Claude CLI retries internally | `claude.ClassifyCapacityError` (`529 overloaded_error`, `503`) + `modelproxy` consecutive 529/503 responses | `claude.ClassifyUsageLimit` (`claude/usagelimit.go`) |
| **`claude`** direct (OAuth / direct `ANTHROPIC_API_KEY`) | `claude/transcript.go` (`tool_use` / `tool_result`) + time from prompt/tool_result to first `assistant` stream line | `claude/retry.go` single-turn retry callback + **new `turnStallObserver`**: if a turn emits no stream event for `> 45s` (indicating silent internal `CLAUDE_CODE_MAX_RETRIES` backoff) or emits an `api_error` stream event, report Tier 1 congestion | `claude.ClassifyCapacityError` (`claude/capacity.go`: `529 overloaded_error` after exhausting internal retries) | `claude.ClassifyUsageLimit` (`claude/usagelimit.go`) |
| **`codex`** (`pkg/agent/codex`) | `codex/transcript.go` (`turn.started` / `turn.completed`, `item.started` / `item.completed`) | `codex/retry.go` (`isTransientEvent`: `"Reconnecting... n/5"` stream events on 503/disconnect) | `codex.ClassifyCapacityError` (`codex/capacity.go`: 503/529, plus fast-fail `< 30s` 429 rate-limit spikes) | `codex.ClassifyUsageLimit` (`codex/usagelimit.go`: sustained `> 30s` quota exhaustion) |
| **`pkg/modelexec`** remote workers (`pkg/modelexec/server.go`) | Forwarded over the modelexec websocket control stream from the remote worker's framework runner | Forwarded `OnInFlightRetry` control frames from remote worker | Forwarded `CapacityError` in `done` message; ceiling bounded by remote worker's `advertisedConcurrency` | Forwarded `UsageLimitError` in `done` message |

#### Integrating `pkg/modelproxy` (`DefaultMaxConcurrent` & Wire Telemetry)

Currently, `pkg/modelproxy/proxy.go` creates a static `make(chan struct{}, DefaultMaxConcurrent)` (`4`) with `DefaultSlotWait = 10s`. Under adaptive concurrency:

1. **Dynamic Proxy Semaphore**: `modelproxy.Server` replaces the fixed channel with a dynamic gate coupled to its parent connection's `effectiveConcurrency`, plus `+2` burst slots for lightweight auxiliary requests (such as Claude Code's concurrent background title/haiku summarization calls made alongside an active main turn).
2. **Direct Upstream Status Hooks**: Because Claude Code hides internal HTTP `429`/`529` retries inside the `claude` binary (`CLAUDE_CODE_MAX_RETRIES`), routing Vertex AI (and optionally API-key connections) through `pkg/modelproxy` gives the daemon immediate, zero-delay visibility into every upstream `429`, `503`, `529`, and `Retry-After` header on the wire—feeding `CongestionObserver.OnInFlightRetry` on the first failed HTTP attempt rather than waiting 2 minutes for all 10 internal CLI retries to fail.

#### Integrating `pkg/modelexec` Remote Executors

In `pkg/modelexec/server.go`, a remote `grain modelexec` worker advertises `advertisedConcurrency` (its host hardware limit, e.g. `--max-concurrency 6`) in its `hello` message (`pkg/modelexec/server.go:53`), and the daemon pushes `configuredConcurrency` via `SetLimits` (`controlMsg{Type: "limits"}`).

- When a connection backed by `modelexec` is in `adaptive` mode, the controller clamps its ceiling to `min(conn.MaxConcurrency, executor.AdvertisedConcurrency)`.
- Whenever the AIMD controller adjusts `effectiveConcurrency`, `AgentPool` calls `executor.SetLimits(effectiveConcurrency, rateLimit)` so the remote worker's local gate stays synchronized with the daemon.

---

## 3. Configuration, Overrides & UI

### 3.1 Configuration Schema (`pkg/model/config.go` & `pkg/daemon/config.go`)

Extend `model.Connection` in `pkg/model/config.go` (persisted in `model_connections.config` JSONB):

```go
type ConcurrencyMode string

const (
    ConcurrencyModeAdaptive ConcurrencyMode = "adaptive" // default when empty
    ConcurrencyModeFixed    ConcurrencyMode = "fixed"
)

const (
    DefaultConnectionMinConcurrency     = 1
    DefaultConnectionInitialConcurrency = 4 // matches legacy DefaultConnectionMaxConcurrency
    DefaultConnectionAdaptiveCeiling    = 12
)
```

Fields on `model.Connection`:

| JSON Field | Go Field | Default (`adaptive`) | Default (`fixed`) | Semantics |
| :--- | :--- | :--- | :--- | :--- |
| `concurrency_mode` | `ConcurrencyMode` | `"adaptive"` | `"fixed"` | Whether AIMD auto-scaling is active or pinned to a fixed cap |
| `min_concurrency` | `MinConcurrency` | `1` | `MaxConcurrency` | Floor for AIMD step-downs (`>= 1`) |
| `initial_concurrency` | `InitialConcurrency` | `4` | `MaxConcurrency` | Starting window $W_0$ on daemon startup or connection creation |
| `max_concurrency` | `MaxConcurrency` | `12` (when `0`) | `4` (when `0`) | Ceiling in `"adaptive"` mode; exact fixed cap in `"fixed"` mode |

#### Backwards Compatibility & Migration

- **Unconfigured connections (`concurrency_mode == ""` and `max_concurrency == 0`)**: Default to `concurrency_mode = "adaptive"`, `min = 1`, `initial = 4`, `max = 12`.
- **Connections where an operator previously set `max_concurrency > 0` (and `concurrency_mode == ""`)**: Treat `max_concurrency` as the operator's chosen **ceiling** in `"adaptive"` mode (`min = 1`, `initial = min(4, max_concurrency)`, `max = max_concurrency`), unless `max_concurrency == 1` (which implies `min = 1, initial = 1, max = 1`). Operators who want a strict non-scaling cap can toggle **Fixed cap** (`concurrency_mode = "fixed"`).
- **Phase-aware yielding (`LeaseYieldedWait` during `wait_for_checks` / long tools)**: Enabled in **both** `adaptive` and `fixed` modes so that even a connection pinned to a fixed cap of `4` means "4 concurrent active model turns," not "4 sandboxes parked in CI."

**Decision:** How should existing connections with a configured `max_concurrency` behave when upgrading?
*Recommended: Default `concurrency_mode` to `"adaptive"`. If `max_concurrency` was unset (`0`), use `min=1, initial=4, max=12`. If an operator had set `max_concurrency = N`, preserve `N` as the adaptive ceiling (`min=1, initial=min(4, N), max=N`), and provide an explicit `"fixed"` mode toggle in the UI and CLI for operators who want no AIMD scaling below `N`.*

### 3.2 Web UI (`ModelBackendPage.jsx` & `ModelsSection.jsx`)

#### Per-Connection Detail Page (`ui/src/components/ModelBackendPage.jsx`)

Replace the single static `Max concurrent runs` number input (`ModelBackendPage.jsx:754-775`) with a **Concurrency** card containing:

1. **Mode Segmented Control**:
   - **Auto-scale (AIMD)** (`concurrency_mode: "adaptive"`, default)
   - **Fixed cap** (`concurrency_mode: "fixed"`)
2. **Bounds Inputs**:
   - In **Auto-scale** mode: three compact inline numeric inputs:
     - **Min floor** (placeholder `1`)
     - **Initial target** (placeholder `4`)
     - **Max ceiling** (placeholder `12`)
   - In **Fixed cap** mode: single **Max concurrent active turns** input (placeholder `4`).
3. **Live Concurrency & Congestion Telemetry Strip**:
   - Rendered directly inside the Concurrency section when viewing an existing connection:
     ```text
     Effective window: 7 / 12 (auto)   ·   Active turns: 6   ·   Waiting on CI/tools: 3   ·   Queued to resume: 0
     Last adjustment: +1 after 8 clean turns / 3m14s active (4m ago)   ·   Turn latency: 1.8s EWMA (1.1x baseline)
     ```
   - When the connection is in a congestion cooldown, display an amber badge:
     ```text
     Cooldown (1m42s left): stepped down 8x → 6x on in-flight 503 retry in run 14-1
     ```

#### Settings → Models Overview (`ui/src/components/settings/ModelsSection.jsx`)

1. **Connection Row Badge (`ModelsSection.jsx:391-421`)**:
   - Replace the static `${conn.max_concurrency || 4}x` badge with a live status pill:
     - Idle auto-scaling connection: `auto 4x (1–12)`
     - Active connection with yielded CI runs: `4/6x active · +2 in CI`
     - Congestion step-down cooldown: amber `3/3x (cooled down from 6x)`
     - Fixed mode connection: `2/4x fixed · +1 in CI`
2. **Global Concurrency Control (`ModelsSection.jsx:1049-1071`)**:
   - Update the label and helper text for `max_agent_concurrency` (`Max concurrent active model turns`, default `10`, with an option to leave blank/auto so the global active-turn ceiling scales with the sum of connection ceilings up to `24`), clearly distinguishing it from host sandbox VM capacity (`model.Budget`).

### 3.3 Observability in `grain settings` and `grain metrics`

#### `/api/settings` (`pkg/ui/settings.go`) & `grain settings` (`pkg/cli/settingsprint.go`)

Extend `modelConnectionView` in `pkg/ui/settings.go:93` with live snapshot fields from `AgentPool`:

```go
type modelConnectionView struct {
    // ... existing fields ...
    ConcurrencyMode     string     `json:"concurrency_mode"`
    MinConcurrency      int        `json:"min_concurrency,omitempty"`
    InitialConcurrency  int        `json:"initial_concurrency,omitempty"`
    MaxConcurrency      int        `json:"max_concurrency,omitempty"`
    EffectiveConcurrency int       `json:"effective_concurrency"`
    ActiveTurns         int        `json:"active_turns"`
    YieldedWaits        int        `json:"yielded_waits"`
    ReacquiringTurns    int        `json:"reacquiring_turns"`
    LastScaleReason     string     `json:"last_scale_reason,omitempty"`
    LastScaleAt         *time.Time `json:"last_scale_at,omitempty"`
    CooldownUntil       *time.Time `json:"cooldown_until,omitempty"`
}
```

In `grain settings` (`pkg/cli/settingsprint.go:255`), update the connection table's concurrency column from `4x` to:
- `auto 6x (1..12) [5 active, 2 wait]`
- `fixed 4x [2 active, 1 wait]`

And support CLI flags on `grain settings model add/edit`:
- `--concurrency-mode=adaptive|fixed`
- `--min-concurrency=1`
- `--initial-concurrency=4`
- `--max-concurrency=12`

#### `/api/metrics` (`pkg/ui/metrics.go`) & `grain metrics` (`pkg/cli/metrics.go`)

Add a `ModelConcurrency []ModelConcurrencyMetrics` slice to `MetricsSummary` (`pkg/ui/metrics.go:25`) and render a **Model Concurrency & Saturation** table in `grain metrics` (`pkg/cli/metrics.go`):

```text
MODEL CONCURRENCY & SATURATION (last 24h)
CONNECTION            MODE       WINDOW (CUR/AVG/MAX)   ACTIVE TURN TIME   YIELDED WAIT TIME   GATE WAITS (P95)   SCALE + / - (RETRY / 503 / 429)
vertex-gemini-pro     adaptive   7x / 6.2x / 12x        4h 18m (68%)       9h 42m (CI: 8h50m)  14 (4.2s)          +9 / -3 (2 / 1 / 0)
claude-oauth-team     adaptive   4x / 3.8x / 8x         2h 05m (51%)       3h 11m (CI: 2h40m)   6 (1.1s)          +4 / -2 (1 / 1 / 0)
```

Tracked per-connection counters and histograms:
- `effective_concurrency`, `min_effective_seen`, `max_effective_seen`, time-weighted `avg_effective_concurrency`.
- `active_turn_seconds`, `yielded_ci_wait_seconds`, `yielded_tool_wait_seconds`, `reacquire_gate_wait_seconds`.
- `turn_gate_acquisitions`, `turn_gate_waited_count`, `turn_gate_p95_wait_ms`, `soft_overcommit_count`.
- `scale_up_count`, `scale_down_retry_count`, `scale_down_capacity_count`, `scale_down_usagelimit_count`, `coalesced_retry_signals`.

---

## 4. Rollout & Implementation Plan

When this design is approved, implementation splits cleanly into five sequential stages.

### Stage 1: Phase-aware connection leases and reservation before sandbox setup {#AMC-1}

- Refactor `AgentPool` (`pkg/daemon/agents.go`) so `connectionLease` tracks `LeasePhase` (`LeaseReserved`, `LeaseActiveTurn`, `LeaseYieldedWait`, `LeaseReacquiring`) and per-connection counters (`activeTurns`, `yieldedWaits`, `attachedRuns`, `maxAttachedRuns`).
- Add `AgentPool.ReserveConnection` and call it in `orchestrator.runOne` (`pkg/orchestrator/cycle.go`) **before** `Sandboxes.Acquire`, passing the reserved lease into `Agents.Build` via context to eliminate the setup-time concurrency race.
- Add unit tests in `pkg/daemon/agents_test.go` and `pkg/orchestrator` verifying that reserving a lease before sandbox setup prevents `setup-failed` races and that `LeaseYieldedWait` frees an active turn slot while respecting `maxAttachedRuns`.

### Stage 2: MCP turn gate and live transcript tool-yield hooks {#AMC-2}

- Add `POST /api/tasks/{id}/turn-acquire` in `pkg/ui/wait_credit.go` and wire `POST /api/tasks/{id}/wait-credit` so entering `wait_for_checks` immediately transitions the run's `connectionLease` to `LeaseYieldedWait`.
- Implement the priority FIFO `reacquiring` queue on `AgentPool` with automatic `RunWaitCredits` accounting, `turnDeadline` pause during gate wait, and `ReacquireSlotTimeout = 90s` fallback to bounded soft overcommit (`MaxSoftOvercommit = 2`).
- Wire `TurnPhaseObserver` into `pkg/agent/antigravity/turndeadline.go`, `pkg/agent/claude/transcript.go`, and `pkg/agent/codex/transcript.go` with `ToolYieldGrace = 10s` for long non-CI tool calls (`run_command`), and call `/api/tasks/{id}/turn-acquire` in `grain mcpserver` before returning from yielded tool calls.

### Stage 3: Per-connection AIMD concurrency controller and congestion signals {#AMC-3}

- Implement `adaptiveController` in `pkg/daemon/adaptive_concurrency.go` with demand-gated additive increase ($8$ clean turns and $3\text{m}$ active turn duration + $2.5\times$ EWMA latency guard) and three-tier multiplicative decrease (Tier 1: $0.75\times$ with `45s` coalesce cooldown; Tier 2: $0.50\times$ on `CapacityError`/`turnTierRetry` with `3m` cooldown; Tier 3: $0.50\times$ slow-start recovery on `UsageLimitError` pause expiry).
- Connect `CongestionObserver` to `antigravity` (`pollRetryLog` and `turnTierRetry` in `pkg/agent/antigravity/turndeadline.go`), `claude` (`pkg/agent/claude/retry.go`, terminal `CapacityError`, and a `45s` silent-turn stall detector), and `codex` (`pkg/agent/codex/retry.go` and `capacity.go`).
- Add deterministic simulation tests in `pkg/daemon/adaptive_concurrency_test.go` covering ramp-up under saturation, no ramp-up when idle, latency-spike hold, concurrent retry burst coalescing, 503 halving, and post-429 slow-start recovery.

### Stage 4: Model proxy and remote modelexec adaptive integration {#AMC-4}

- Update `pkg/modelproxy/proxy.go` so per-connection proxy concurrency tracks `effectiveConcurrency` (plus `+2` auxiliary burst headroom) rather than a static `DefaultMaxConcurrent = 4`, and feeds upstream HTTP `429`/`503`/`529` status codes and TTFB latency directly into `CongestionObserver`.
- Update `pkg/modelexec/server.go` and the daemon `modelexec` bridge so remote workers cap `MaxConcurrency` at `advertisedConcurrency`, receive updated `effectiveConcurrency` via `SetLimits`, and forward turn-phase and retry events over the websocket control stream.

### Stage 5: Settings schema, UI controls, and CLI/metrics observability {#AMC-5}

- Add `ConcurrencyMode`, `MinConcurrency`, and `InitialConcurrency` to `model.Connection` (`pkg/model/config.go`), `/api/settings` (`pkg/ui/settings.go`), and `grain settings` (`pkg/cli/settingscmd.go`, `pkg/cli/settingsprint.go`).
- Update `ui/src/components/ModelBackendPage.jsx` and `ui/src/components/settings/ModelsSection.jsx` with the **Auto-scale (AIMD)** / **Fixed cap** toggle, min/initial/max inputs, and live active-turn / yielded-wait / cooldown telemetry badges.
- Add per-connection concurrency and congestion metrics to `/api/metrics` (`pkg/ui/metrics.go`) and `grain metrics` (`pkg/cli/metrics.go`).

---

## Summary of Decisions

| # | Question | Recommended Decision |
| :- | :--- | :--- |
| 1 | When is a model-connection lease acquired? | Reserve `LeaseReserved` at admission time **before** `Sandboxes.Acquire` in `runOne`, closing the `ModelAvailability` → `Build` setup race. |
| 2 | When does a running agent yield its model-connection turn slot? | Immediately (`0s`) when entering `wait_for_checks` (`wait-credit`), and after `ToolYieldGrace = 10s` inside any other tool execution (`run_command`, etc.) detected via live stream transcripts. |
| 3 | How does a yielded run re-acquire a model-connection slot when CI or a long tool finishes? | Via an in-process MCP Turn Gate (`POST /api/tasks/{id}/turn-acquire`) queued in a per-connection priority FIFO ahead of new dispatches for up to `ReacquireSlotTimeout = 90s` (credited to `RunWaitCredits`), falling back to bounded soft overcommit (`+2`) so live sandboxes never deadlock. |
| 4 | How is total sandbox count bounded when runs yield model slots? | By `model.Budget` / `model.Ledger` (`PoolAgent`) and `MaxWorkers` / `MaxMergers` globally, plus `maxAttachedRuns = max(2*W, W+4)` per connection. |
| 5 | What triggers additive increase ($W \leftarrow W + 1$)? | Active demand/saturation on the connection + at least **8 clean model turns** AND **3 cumulative active turn-minutes** with zero retries, provided fast-EWMA turn latency is within **$2.5\times$** of baseline EWMA. |
| 6 | What triggers multiplicative decrease and how are retry storms debounced? | **Tier 1** (in-flight retry): $\lfloor 0.75 W \rfloor$ with a `45s` coalesce window; **Tier 2** (terminal 503/529 `CapacityError` or `turnTierRetry`): $\lfloor 0.50 W \rfloor$ with a `3m` increase cooldown; **Tier 3** (429 `UsageLimitError` pause): resume at $\lceil 0.50 W_{\text{pre-pause}} \rceil$ slow-start when the pause lifts. |
| 7 | How do existing connection configs migrate? | Default `concurrency_mode` to `"adaptive"` (`min=1, initial=4, max=12` when `max_concurrency` was unset, or `max=N` when `max_concurrency=N` was set), with an explicit `"fixed"` mode in the UI and CLI. |
