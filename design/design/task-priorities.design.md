# Numeric task priorities and flow continuity

Note: this design covers which ready task is dispatched first. It does not change who may run (grants), how many may run (`Limits`), or how the merge queue orders landings.

## Today

Grain keeps one order for queued work, and that order is a column: `task.order_key`, a float.

- `Store.OrderKeyForNewTask(atFront)` gives a new task a key one `orderKeySpacing` step past one end of the table. `Config.NewestFirst` picks which end by default. `atFront` (`propose_task`'s `at_front`/`position`, `grain create -position`, `CreateTaskRequest.AtFront`) overrides it. Base-branch repairs, review follow-ups, ports, upgrade checks and console sessions are filed at the front. Schedules, suites, path triggers and takeovers are filed at the end.
- `Store.Reorder` (`POST /api/tasks/reorder`, drag and drop) splits the gap between two neighbours. It rebalances when the gap gets too small.
- `Store.Ready`, `ReadyMergers`, `ReadyPriorityDispatch` and `ReadyTargets` all sort by `order_key, task_id`. `ListTasks` uses the same order, so the list a person sees is the dispatch order. `Store.Ready`'s doc comment calls this a deliberate invariant: an ordering that lives only in a dispatcher's `ORDER BY` is one nobody can see or drag.
- `dispatch.Cycle` splits the ready list into three admission groups and keeps Ready's order inside each group. Priority-dispatch tasks come first (`priority-dispatch`/`durable` grants and console sessions; they are admitted outside every count). Mergers come next (`mergerTaskSQL`: fix tasks and in-flight repairs, which use merger capacity). Ordinary work comes last.
- `showQueueAtFrontOfBacklog` rewrites the keys of merge-queue members so that they lead the backlog in landing order.

The only ways to say "this matters more" are a position and two booleans: `at_front`, and the `priority-dispatch` grant (person-only).

## Problem

Multi-step flows get lost.

- **FIFO buries continuations.** A run that splits its work with `propose_task` files its children at the end of the queue by default. A design → spec → tasks → implementation → follow-up chain does the same at every step. Each step queues behind every nightly sweep, schedule fire and hygiene task filed since the flow began. One five-step flow can wait out the whole backlog five times.
- **NewestFirst buries the older steps instead.** With `NewestFirst` on, children jump ahead. But a task that resumes (`resume_later`, `wait_for_task`, a review comment) keeps its old key, and every task filed since then is now ahead of it.
- **`at_front` does not compose.** "Front" means ahead of everything. If two flows both file at the front, they leapfrog each other on every step. A nightly sweep that files at the front beats an urgent fix filed a minute earlier.
- **`priority-dispatch` is the wrong tool.** It bypasses admission ceilings and pauses. Granting it to make a feature flow "go first" also lets that flow escape `max_workers` and connection limits. That is why it is person-only, and it cannot express "more important than A, less important than B".

What is missing is a relative importance that work inherits, that people can see, and that agents cannot inflate.

## Design

### Numeric priority

Every task gets an integer `priority`, where **a higher number means more important**. The default is `0`. Values are clamped to `-100..100`.

Four reference bands are documented and used by grain's own defaults, templates and the UI:

| Band | Value | Meant for |
|---|---|---|
| urgent | `20` | Base-branch repair, critical unblocks, release fixes |
| flow | `10` | Active, operator-initiated multi-step work and its continuations |
| normal | `0` | Ordinary bugs and features; the default |
| background | `-10` | Schedules, nightly walkthroughs, hygiene sweeps, auto-filed cleanups |

Any integer is valid; the bands are conventions, not an enum. Gaps of 10 leave room for "slightly above normal" (`5`) without a new band. A signed scale centred on `0` means that every existing task, and every task filed without a priority, is "normal" with no migration of meaning.

The scheduling rule is one ordering, used by every ready query and by `ListTasks`:

```
ORDER BY priority DESC, order_key ASC, id ASC
```

Higher priority always wins. `order_key` orders tasks that share a priority value, and it keeps every behaviour it has today within that value: FIFO or NewestFirst, `at_front`, drag and drop, and the merge queue's `MoveToFrontOfBacklog`. Because `ListTasks` uses the same order, the list stays the dispatch order and the invariant in `Store.Ready`'s doc comment holds.

`dispatch.Cycle` keeps its three admission groups, and each group is now sorted by the new order. Numeric priority decides order; it never decides admission. A priority-100 ordinary task still uses worker capacity and still waits behind a full `max_workers`.

### Flow continuity

Most of "keep longer flows from getting lost" comes from three rules.

**1. Children inherit.** A task filed by a run inherits the filing task's `priority` unless the call passes one explicitly. This covers `propose_task` (`LinkProposedBy`), decompositions, follow-ups and the next step of a design → spec → tasks chain. Suite steps and grainfile-imported `depends_on` chains take the priority of the suite run, or of the importing process, as a whole. So a flow started at `10` stays at `10` through every hand-off. It stays ahead of the background band no matter how much of it was filed since, and it does not need `at_front` to get there.

**2. Continuations are placed next to their flow, not at the end of the queue.** Inheritance puts a child in the right band. Within that band, a child is placed right after its parent: its `order_key` is split from the gap between the parent's key and the next key in the parent's band. Several children from one run are spaced in call order, the way `relayProposedTasks` already spaces a batch. The flow's next step therefore runs ahead of unrelated work of the same priority that was filed later, and behind work of that priority that was already waiting before the flow started. Neither FIFO nor NewestFirst can express that. It applies only when the child is not given an explicit `at_front` or `position`, which still mean the head or tail of the child's band.

A task that comes back to the queue keeps its own key, whatever `NewestFirst` says. This covers resuming after `resume_later`, `wait_for_task` or `wait_for_checks`, being woken by a review comment, and a merge-queue repair. It is already true, because no state transition rewrites `order_key`. The design makes it a stated rule rather than an accident, so that a later change does not "re-file" resumed work at the end of the queue.

**3. Blockers are raised to the priority of what waits on them.** A `priority: 20` task that `depends_on` a `priority: -10` task would otherwise wait for the background band to drain: a priority inversion. When a dependency edge is written, or a dependent's priority is raised, grain raises each open blocker's stored priority to at least the dependent's, and records a task event that says why. The raise is a write, not a hidden "effective priority" computed in the query, so the list shows the reason the blocker moved up. Blockers are never lowered again automatically.

**Rejected: a hidden continuation boost.** We could add `+N` at dispatch time for tasks that have already had a run. That ordering would not appear on any list a person can drag. It is exactly the "rule that lived only in this ORDER BY" that `Store.Ready`'s doc comment warns against, and it would quietly reward long-running flows over short ones. Placement within the band gets the same effect and stays visible.

**Starvation is intended, within limits.** The background band fills spare capacity and can wait indefinitely while higher bands have ready work. That is the purpose of the band. If it starves in practice, the fix is an explicit, visible aging rule (see Open questions), not a per-cycle boost.

### Default priority by origin

A task filed without an explicit priority gets a default based on how it was filed (`Origin.Reason` and the filing path). Grain's built-in defaults:

| Filed by | Default |
|---|---|
| Base-branch CI or release repair (`basebranchci`, `basebranchrelease`, base-repair bypass), merge-queue fix (`ReasonFix`), upgrade check | `20` |
| Console session, interactive task, review follow-up | `10` |
| A person through the UI or `grain create`; a GitHub issue or event | `0` |
| A run's `propose_task` | the parent's priority (inherited) |
| A suite run | the suite's `priority`, else `0` |
| A schedule fire, path trigger, nightly or hygiene template | the schedule's or template's `priority`, else `-10` |
| A port (`port.go`) | the source task's priority |

Deployments can override the table per origin in `grain_config` (`priority_defaults`). An explicit `priority` on the schedule, template, suite or request always wins over the table. Most places that pass `atFront=true` today do it to mean "this matters". Once they are given a band of their own, they can file at their band's default position.

### Who may set priority

- **People, and tasks holding `api:tasks:write` or `api:task-admin`,** may set any value on create or `PATCH`, and may drag across bands.
- **An ordinary task calling `propose_task`** may give a child any priority up to its own. Asking for more is not an error: the proposal is held for a person's approval, the way a proposal naming an escalated capability is (`EscalatedCapabilities`), with the requested value shown. This is what stops an unprivileged task from filing itself, or its children, at the top of the deployment.
- **Any task may lower its own priority, or a child's.**
- **Automatic writes** (origin defaults, inheritance, blocker raises) are made by grain itself and are never capped by the filing task's grants. A blocker raise cannot exceed the dependent's priority, which was already checked.
- **The clamp** (`-100..100`) applies to everyone. Values above `20` are reserved by convention for a person's own escalation, and the UI asks for confirmation before setting one.

### `priority-dispatch` stays an admission grant

`priority-dispatch` (and `durable`, which implies it) still means what `PriorityDispatchCapability`'s doc comment says: admitted outside worker and merger counts, passes non-upgrade pauses, is not held behind a red base branch or a connection's concurrency limit, and is person-only. It is not merged into the numeric scale.

The two answer different questions. `priority` answers "in what order?". `priority-dispatch` answers "does this wait for capacity at all?". `Cycle` already puts the priority-dispatch group first, and inside that group numeric priority now orders, for example, two console sessions against a steward. A task never needs `priority-dispatch` just to go first. Templates that grant it only for ordering should drop the grant and set a band instead.

The name is close to Kubernetes `PriorityClass` (`K8sPriorityClass*` in settings), which decides pod preemption on the cluster. That setting is unrelated and unchanged. The docs should say so where both appear.

## Surface

### Store and schema

- Add a `task.priority INTEGER NOT NULL DEFAULT 0` column, and an index on `(priority DESC, order_key)`. Existing rows read as `0`, so a deployment that never sets a priority dispatches in exactly today's order.
- Add `model.Task.Priority int`.
- `Ready`, `ReadyMergers`, `ReadyPriorityDispatch`, `ReadyTargets` and `ListTasks` use the one ordering. It is written once as a shared SQL fragment, the way `mergerTaskSQL` is, so the queries cannot drift apart.
- `OrderKeyForNewTask(ctx, priority, atFront)` computes the minimum or maximum key within the given priority, not across the whole table. A new `OrderKeyAfter(ctx, parentID)` places a continuation (rule 2).
- `Reorder` is unchanged when the drop stays inside one band; see UI.
- `queueOrder` and `showQueueAtFrontOfBacklog` keep ordering merge-queue members by `order_key` alone. Landing order is a property of the queue, not of importance. The members are completed tasks that are not dispatched from Ready, so this does not conflict with the list.

### Grainfiles, templates, schedules, suites

- `.task.grain.md` and `.process.grain.md` gain an optional `priority` (integer, `-100..100`; default: the origin default) in §7.1 and §7.2 of `design/spec/grainfiles/grainfiles.spec.md`, in `docs/grainfile-v1.md`, and in the JSON schemas.
- A process's `priority` applies to every task it fires.
- Task templates, schedules and suites gain the same field. A scheduled template or suite that sets no priority defaults to the background band.

### API, CLI and MCP

- `POST /api/tasks` (`CreateTaskRequest.Priority *int`) and `PATCH /api/tasks/{id}` (`UpdateTaskRequest.Priority *int`) take the field, and the permission rules above apply in `agent_api_gate.go`.
- `GET /api/tasks` returns it.
- `propose_task` takes `priority` (integer); when it is omitted, the child inherits.
- CLI: `grain create -priority N`, `grain update <id> -priority N`, and a `PRIO` column in `grain list`. `-position` keeps its meaning, now within the band.

### UI

- The task list shows a small band badge (`urgent`, `flow`, `background`; none for `0`) with the number on hover. The list is grouped visually by band, with a thin divider where the priority changes. Order inside a group is the same as today.
- The task page's edit form has a priority field with the four bands as presets plus a custom number.
- **Dragging within a band** rewrites `order_key` only, as today.
- **Dragging across bands** gives the task the priority of the neighbour it was dropped next to, and then places it between those neighbours. Otherwise a row could sit visually among `20`s while it dispatches as a `0`, and the list would stop being the dispatch order. The drop says what changed ("priority 0 → 20"). Raising a task across bands needs the same permission as setting that priority, and the drop is refused with that reason otherwise.
- `POST /api/tasks/reorder` gains an optional `adoptPriority` (default `true` for the UI). An API caller that wants to move a task only within its own band can set it to `false`.

## Rollout

1. Add the column, the field and the ordering, with every task at `0`. The order is unchanged.
2. Add inheritance and placing continuations after their parent (rules 1 and 2).
3. Add origin defaults. Repairs move from `atFront` to the urgent band, schedules to background.
4. Add the grainfile, template, schedule and suite field, the UI, and blocker raises (rule 3).

Each step is tested by asserting that `ListTasks` and `Ready` return the same order. Tests also cover inheritance through a multi-step chain, a held over-limit proposal, and a cross-band drag.

## Open questions

1. **Aging for the background band.** Should a task waiting more than N hours gain `+1` per period, as a visible written change capped at `0`? Or should the background band be allowed to starve, with an inbox alert when it has waited past a threshold? The recommendation is the alert first, and aging only if the alert fires in practice.
2. **Mergers versus urgent ordinary work.** Today `Cycle` always tries mergers before ordinary work. Should an ordinary priority-`20` task go before a priority-`0` merger when both have capacity? The recommendation is to keep the groups (mergers use reserved capacity anyway), and order inside each.
3. **Should `NewestFirst` stay?** Once continuations are placed after their parent, the main reason to turn it on (new work getting buried) mostly goes away. The recommendation is to keep it and revisit after rollout step 2.
4. **Default bands for console-session follow-ups.** Is `10` right for everything a director or chat files, or should that be the session's own configured priority?
