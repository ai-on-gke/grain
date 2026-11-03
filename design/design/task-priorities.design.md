# Numeric task priorities

Note: this design covers how tasks are ordered within each dispatch admission group, in the task list, and in the merge queue. It does not change capability grants, concurrency limits (`Limits`), or Grain CI gate ranks (`ci.Priority`). The normative specification is [`task-priorities.spec.md`](../spec/task-priorities/task-priorities.spec.md).

## Motivation

Ordering a backlog with `task.order_key` and `at_front` alone gives callers only one queue and two extremes:

- **`at_front` does not compose.** "Front" means ahead of everything already queued. Two urgent filings leapfrog each other, and there is no way to express "more important than ordinary backlog work, less important than an emergency fix".
- **`priority-dispatch` is an admission grant, not an ordering rank.** Granting `priority-dispatch` (or `durable`) to make a task run sooner also lets it bypass `max_workers`, merger counts, pipeline pauses, red base-branch holds, and model-connection concurrency limits. That is why it is person-only.

Numeric task priority gives every task a visible integer rank that orders dispatch and the merge queue without bypassing capacity limits, while holding agent-escalated proposals for human approval.

## Design

### Scale and default

Every task carries an integer `Priority` (`model.Task.Priority`, SQL `task.priority INTEGER NOT NULL DEFAULT 5`), where **a higher number means higher priority**.

- **Default:** `model.DefaultTaskPriority = 5` (`pkg/model/task.go`).
- **Allowed range:** Positive integers (`1` or more). There is no upper clamp and there are no named priority bands in code.
- **Validation on task surfaces:** Every task-facing write surface (`POST /api/tasks`, `PATCH /api/tasks/{id}`, `grain create -priority`, `grain update -priority`, `grain task propose --priority`, `propose_task`, and the UI forms) refuses `0` and negative integers (`priority must be a positive integer (1 or more; leave it out for the default, 5)`). Leaving `priority` out selects the default (`5`, or the template's priority). Setting `5` explicitly restores an edited task to the default priority.
- **Internal zero normalization:** An unset `model.Task.Priority == 0` in Go normalizes to `DefaultTaskPriority` (`5`) when written to the store (`putTask` in `pkg/model/store.go`), projected to API responses (`taskFrom` in `pkg/ui/tasks.go`), or compared in the merge queue (`queueOrder` in `pkg/orchestrator/sync.go`).
- **Templates:** `model.Template.Priority` is `*int` (SQL `template.priority INTEGER NOT NULL DEFAULT 0`). `nil` or `0` means the template sets no priority, leaving tasks composed from it on `DefaultTaskPriority` (`5`). Template APIs and `.grain/templates/*.yml` refuse negative integers (`< 0`) and treat `0` as clearing any saved priority.

### Queue, dispatch and merge-queue ordering

One shared SQL `ORDER BY` fragment, `taskQueueOrderSQL` (`pkg/model/store.go`), orders `Store.ListTasks`, `Store.TaskRepos`, `Store.Ready`, `Store.ReadyTargets`, `Store.ReadyMergers`, and `Store.ReadyPriorityDispatch`:

```sql
`t`.`priority` DESC,
CASE WHEN EXISTS (
  SELECT 1 FROM `task_tag` AS `rt`
  WHERE `rt`.`task_id` = `t`.`id` AND `rt`.`tag` LIKE 'base-ci-repair:%'
) THEN 0 ELSE 1 END,
`t`.`order_key`,
COALESCE(`t`.`approved_at`, `t`.`created_at`),
`t`.`created_at`,
`t`.`id`
```

1. **Higher `priority` always wins.**
2. **Base-branch repairs lead their priority level.** Within the same numeric `priority`, tasks carrying `BaseRepairBranchTag` (`model.FixesBaseBranch`, `fixesBaseBranchOrderSQL`) sort ahead of ordinary tasks.
3. **`order_key` orders peers.** Tasks sharing both `priority` and base-repair status sort by ascending `order_key`, preserving FIFO (`atFront = false`), `Config.NewestFirst`, explicit `at_front` / `-position`, manual `Reorder`, and `MoveToFrontOfBacklog` within that slice.
4. **Deterministic tie-breakers.** `COALESCE(approved_at, created_at)`, `created_at`, and `id` ascending break any equal `order_key`.

Because `ListTasks` and all `Ready*` queries use `taskQueueOrderSQL`, the task list displays the dispatch order within each admission group.

**Dispatch groups.** `dispatch.Cycle` (`pkg/dispatch/dispatch.go`) splits ready work into three admission groups and preserves `taskQueueOrderSQL` inside each:
1. Priority-dispatch tasks (`ReadyPriorityDispatch`: console sessions and holders of `priority-dispatch` or `durable`), admitted outside worker and merger counts.
2. Mergers (`ReadyMergers`: `ReasonFix` tasks and in-flight merge-queue repairs), admitted under merger capacity.
3. Ordinary tasks (`Ready`), admitted under `max_workers`.

Numeric `priority` orders candidates inside each group; it never grants admission capacity. A priority-`100` ordinary task still waits behind a full `max_workers`.

**Merge queue.** `queueOrder` (`pkg/orchestrator/sync.go`) orders merge-queue members by `Priority` descending (treating `0` as `5`), then `FixesBaseBranch` (`true` first), then established queue members ahead of first-cycle newcomers (`Observation.MergeQueueEnteredAt == nil`), then `OrderKey` ascending and `ID` ascending. `showQueueAtFrontOfBacklog` writes `order_key` values in that order ahead of non-queue tasks.

**`order_key` allocation, resumption, and reorder.**
- `Store.OrderKeyForNewTask(ctx, atFront)` steps `orderKeySpacing` (`1 << 20`) past the global minimum (`atFront = true`) or maximum (`atFront = false`) `order_key` across `task`.
- State transitions (`resume_later`, `wait_for_task`, `wait_for_checks`, comment wakeups, pause/resume) leave both `priority` and `order_key` untouched.
- `Store.Reorder` (`POST /api/tasks/reorder`, `ui.ReorderRequest{IDs, AfterID, BeforeID}`) splits `order_key` between `afterId` and `beforeId`, rebalancing all tasks by `ORDER BY order_key, id` when a bounded gap falls below `minOrderKeyGap` (`1e-6`). `Reorder` never alters `priority`, so drag-and-drop reorders tasks only relative to peers of the same `priority` (and base-repair status).

### Filing defaults and templates

Every task starts at `DefaultTaskPriority` (`5`) unless the filing request or its template specifies another positive integer. There is no per-origin priority table and no `priority_defaults` column in `grain_config`.

- **Direct and automated filings:** Tasks filed by a person (`/new`, `POST /api/tasks`, `grain create`), base-branch CI and release repairs, merge-queue fixes, upgrade checks, takeovers, console sessions, reviews, GitHub events, imported issues, inline schedules, and branch ports all start at `5` unless composed from a template with a non-zero `Priority`. (Repairs, upgrade checks, console sessions, and ports still pass `atFront = true` when computing `order_key`.)
- **Follow-up proposals (`propose_task` and `grain task propose`):** Proposals **do not inherit** their parent task's priority. When `priority` is omitted, both live filing (`ui.CreateTask`) and end-of-run relay (`orchestrator.relayProposedTasks` in `pkg/orchestrator/finish.go`) use the referenced template's `Priority` if set, and `DefaultTaskPriority` (`5`) otherwise. A high-priority task that wants an equally urgent follow-up passes `priority` explicitly.
- **Template composition (`pkg/model/compose.go`):** `composeTemplate` (used by `ComposeTemplateTask`, template-backed `ComposeScheduleTask`, `ComposeSuitePass`, and `ComposeQualificationPass`) resolves `Composition.Priority` to `*tmpl.Priority` when non-nil and non-zero, and `DefaultTaskPriority` (`5`) otherwise. Schedules (`model.Schedule`) and suites (`model.Suite`) have no `Priority` field of their own; inline schedule firings get `5`.

### Who may set priority

- **People** (UI, REST, host CLI) may set any integer `>= 1` when creating or editing a task, without unapproving a queued task.
- **Proposals (`model.PersonOnlyProposalSettings` in `pkg/model/hold_for_approval.go`):** When a task proposes a child (`propose_task` or `grain task propose`), its auto-approval permission (`AutoSubmits` or `ConsoleSession.TasksApproved`) covers priorities up to `max(parent.Priority, model.DefaultTaskPriority)` (`max(parent.Priority, 5)`). Including `5` in the ceiling lets a lower-priority parent (for example `priority: 2`) file ordinary default-priority (`5`) proposals without escalation. If `child.Priority > max(parent.Priority, 5)` (or `child.KeepRunning` is `true`), the proposal is not refused: it is filed with the requested priority, held unapproved for a person to approve, and annotated with `_Filed for a person to approve: …_`.
- **Agent API and external agent tokens (`pkg/ui/agent_api_gate.go`):**
  - `POST /api/tasks` requires `tasks:write` and accepts numeric `priority` (`>= 1`). Agents cannot pass `"approved": true` without `tasks:admin`, and `ProposedBy` calls enforce `PersonOnlyProposalSettings`.
  - `PATCH /api/tasks/{id}` allows numeric `priority` (`>= 1`) under `tasks:write` (included in presets `api:task-admin` and `api:dispatch-control`). Callers holding the dangerous scope `tasks:admin` *without* `tasks:write` may also call `PATCH /api/tasks/{id}` when the body is a single JSON object containing only numeric `"priority"` (`tasksAdminAllowsTaskPatch` and `tasksAdminTaskPatchBodyAllowed`), with `X-Grain-Acting-Reason` required (MCP-only).
  - Boolean `"priority"`, `"priorityDispatch"`, and `"priority_dispatch"` on `UpdateTaskRequest` toggle `PriorityDispatchCapability` rather than numeric `Priority`, and remain person-only grant mutations requiring `capabilities:admin`.

### `priority-dispatch`, `durable`, CI priority, and Kubernetes `PriorityClass`

- **`priority-dispatch` and `durable`** (`model.PriorityDispatchCapability`, `model.DurableCapability`, `Task.HasPriorityDispatch`) remain person-only admission grants. `priority` decides *in what order* tasks within a group run or merge; `priority-dispatch` decides *whether* a task waits for worker capacity, pauses, red base branches, or model-connection slots at all (`Budget.AdmitsPriority`). Inside the priority-dispatch group, `ReadyPriorityDispatch` orders tasks by `taskQueueOrderSQL` (`priority DESC`, …).
- **`ci.Priority`** (`pkg/ci/gate.go`: `PriorityNormal`, `PriorityUnderway`, `PriorityMergeHead`, `PriorityReleaseGate`, `PriorityRepair`) ranks Grain CI jobs at `SharedGate` and is separate from `Task.Priority`.
- **`k8sPriorityClass*`** (`k8sPriorityClassInteractive`, `k8sPriorityClassAgent`, `k8sPriorityClassCi`) configures Kubernetes pod `PriorityClass` names for cluster scheduling and preemption, and is unrelated to `Task.Priority`.

## Surface

### Store, declarations, API, CLI, and MCP

- **Schema:** Migration 73 (`pkg/model/migrations.go`) adds `task.priority INTEGER NOT NULL DEFAULT 5` and `template.priority INTEGER NOT NULL DEFAULT 0`.
- **Declarations:** `.grain/templates/<stem>.yml` (`pkg/repodecl/parse.go`) and config-repo `settings/templates.yml` (`pkg/declschema/declschema.go`, `pkg/staterepo/decl.go`) carry optional `priority` on templates.
- **REST API:**
  - `POST /api/tasks` (`CreateTaskRequest.Priority *int`) and `PATCH /api/tasks/{id}` (`UpdateTaskRequest.Priority *int`) accept integers `>= 1`; `GET /api/tasks` and `GET /api/tasks/{id}` return `priority` (`int`, defaulting unset `0` to `5`).
  - `POST /api/templates` and `PATCH /api/templates/{id}` accept `priority *int` (`>= 0`, where `0` clears a saved template priority).
- **MCP and CLI:**
  - `propose_task` and `grain task propose --priority <N>` take an integer `>= 1` (omitted defaults to `5` or the template's priority).
  - Host CLI: `grain create -priority <N>` and `grain update <id> -priority <N>` (`>= 1`); `grain get <id>` prints `priority:   <N>`; `grain list` filters and sorts via the built-in numeric `priority` attribute (`-filter priority:gt:5`, `-sort priority:desc`).
  - Sandbox CLI and MCP task views: `grain task list` and `list_tasks` append `(priority <N>)` / `priority <N>` when `N != 5` (and include `priority` in `--json`); `grain task read` and `get_task` print `Priority: <N>`.

### UI

- **Task list (`TaskList.jsx`):** Shows an outlined `P<n>` chip (`data-testid="task-priority-badge"`) when `t.priority != null && t.priority !== 5`, with hover tooltip `Task priority <n> (default 5; higher numbers run first)`. Tasks at the default `5` show no priority chip.
- **Task detail (`DetailOverlay.jsx`, `Declared.jsx`):** Shows a `priority: <n>` chip in the header strip, and a `Task priority` row reading `<n> (default 5)` under `Declared` when `n != 5`.
- **Forms (`NewTaskOverlay.jsx`, `EditTaskForm.jsx`, `BulkEditOverlay.jsx`, `TemplateOverlay.jsx`, `CompositionPanel.jsx`):**
  - `New task`, `Edit`, and `Bulk edit` provide a `Task priority` whole-number input (`min: 1, step: 1`, helper `default 5; higher numbers run first`), rejecting `0`, negative, and fractional values.
  - `Templates` provides an optional `Task priority` input (`placeholder="5"`, blank clears to `0` on save), and `CompositionPanel` previews `Task priority` attributed to the template.
- **Filter and sort (`taskAttributes.js`):** Built-in numeric `Task priority` (`priority`) attribute on the narrowing bar (distinct from any deployment-declared `custom.priority` field).

## Not yet implemented

The following ideas from earlier drafts are **not** in the codebase:

- Named priority bands (`urgent`, `flow`, `normal`, `background`), band presets or visual band dividers in the UI, an upper clamp on `priority`, and confirmation prompts for high values.
- Per-origin automatic priority defaults (for repairs, console sessions, schedules, suites, or ports) and a `priority_defaults` map in `grain_config`.
- Automatic priority inheritance by `propose_task` / `grain task propose` children when `priority` is omitted.
- Continuation placement immediately after the parent task (`OrderKeyAfter`) and per-priority `OrderKeyForNewTask`.
- Automatic blocker priority raising when a higher-priority task `depends_on` a lower-priority open task.
- Cross-priority drag-and-drop priority adoption (`adoptPriority` on `POST /api/tasks/reorder`).
- `priority` fields on schedules (`model.Schedule`), suites (`model.Suite`), and Grainfiles (`.task.grain.md` and `.process.grain.md`).
- A `PRIO` column in `grain list`'s default table output, a dedicated SQL index on `(priority DESC, order_key)`, and automatic aging or starvation alerts for low-priority tasks.
