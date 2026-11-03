# Numeric Task Priorities Specification

**Status:** normative. Derived from the implementation on `main`.

This specification defines Grain's numeric task priority model: the integer domain and default, database storage, queue/dispatch/merge-queue ordering, origin defaults and template composition, child-proposal and dependency rules, permission and hold-for-approval gates, and REST, declaration, Grainfile, CLI, MCP, and UI surfaces.

- Architectural motivation and non-implemented ideas live in [`design/design/task-priorities.design.md`](../../design/task-priorities.design.md).
- Where the design document and this specification disagree, **this document wins**. If this document and the code disagree, the code wins and this document is the bug (§6 lists the audited source files).

The key words "MUST", "MUST NOT", "SHOULD", "SHOULD NOT", and "MAY" are to be interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and only when, they appear in all capitals.

## Contents

1. Value domain, constants, and validation
2. Storage and ordering
3. Origin defaults, templates, inheritance, and dependencies
4. Permissions, approval gates, and orthogonal ranks
5. External surfaces (REST, declarations, Grainfiles, CLI, MCP, UI)
6. Sources

## 1. Value domain, constants, and validation

### 1.1 Task priority value domain and default {#TP-1}

1.1.1. Every task (`model.Task` in `pkg/model/task.go`) carries an integer `Priority` field (`int`), where **a larger integer means higher priority** (dispatched, listed, and merged ahead of smaller integers within the same admission group).

1.1.2. The default task priority constant MUST be:

```go
const DefaultTaskPriority = 5
```

(`pkg/model/task.go`).

1.1.3. Valid explicit task priorities are positive integers (`Priority >= 1`). There is **no** upper bound or clamp, and the codebase defines **no** named priority bands. Tasks with `1 <= Priority < 5` sort below default-priority work; tasks with `Priority > 5` sort above it.

### 1.2 Task priority input validation {#TP-2}

1.2.1. Every task-facing write surface that accepts a `priority` parameter MUST reject `0` and negative integers (`priority < 1`). On the HTTP/UI client layer (`pkg/ui/client.go`), the refusal error is `errPriorityNotPositive`:

```text
priority must be a positive integer (1 or more; leave it out for the default, 5)
```

1.2.2. Omitting `priority` (passing `nil` / unset on task creation or proposal) selects `DefaultTaskPriority` (`5`), or the referenced template's priority when composed from a template with a positive `Priority` (§3.2). Passing `5` explicitly on task creation or update sets the task to the default priority.

1.2.3. Host CLI (`grain create -priority`, `grain update -priority`), sandbox CLI (`grain task propose --priority`), MCP (`propose_task`), and web UI forms (`New task`, `Edit`, `Bulk edit`) MUST likewise reject `0`, negative integers, and non-integer inputs before or at submission.

### 1.3 Internal zero normalization {#TP-3}

1.3.1. Because Go zero-initializes `model.Task.Priority` to `0` when internal callers construct a `model.Task` literal without setting `Priority`, internal store and projection boundaries MUST treat `0` on a `model.Task` as `model.DefaultTaskPriority` (`5`):

- `putTask` (`pkg/model/store.go`) writes `model.DefaultTaskPriority` (`5`) whenever `t.Priority == 0` (preserving explicit negative values only if ever constructed directly in low-level store tests, though all user/agent surfaces forbid `< 1`).
- `taskFrom` (`pkg/ui/tasks.go`) populates `TaskResponse.Priority` with `model.DefaultTaskPriority` (`5`) whenever `t.Priority == 0`.
- `queueOrder` (`pkg/orchestrator/sync.go`) treats `Priority == 0` as `model.DefaultTaskPriority` (`5`) when comparing merge-queue entries.

### 1.4 Template priority domain and validation {#TP-4}

1.4.1. `model.Template.Priority` (`pkg/model/template.go`) is an optional pointer `*int`. `nil` (or `*Priority == 0`) means the template specifies no priority override, leaving tasks composed from it on `model.DefaultTaskPriority` (`5`).

1.4.2. Template write surfaces (`POST /api/templates`, `PATCH /api/templates/{id}` in `pkg/ui/templates.go`, `.grain/templates/<stem>.yml` in `pkg/repodecl/parse.go`, and `settings/templates.yml` in `pkg/declschema/declschema.go` and `pkg/staterepo/decl.go`) MUST reject negative integers (`priority < 0`).

1.4.3. On template APIs and declarations, `0` is accepted as the unset sentinel: `POST /api/templates` and `PATCH /api/templates/{id}` normalize `0` to `nil` (clearing any previously saved priority on `PATCH`), while positive integers `>= 1` are stored and exported.

## 2. Storage and ordering

### 2.1 Database schema and migration {#TP-5}

2.1.1. Schema migration 73 (`pkg/model/migrations.go`, mirrored in `pkg/model/schema.go`) defines:

- `task.priority INTEGER NOT NULL DEFAULT 5`
- `template.priority INTEGER NOT NULL DEFAULT 0`

2.1.2. Reading a `template` row scans `template.priority` as an integer and leaves `Template.Priority == nil` when the stored column is `0` (`scanTemplate` in `pkg/model/store.go`). Writing a `template` row stores `0` when `Template.Priority == nil` (`putTemplate` in `pkg/model/store.go`).

2.1.3. There is no dedicated composite index on `(priority, order_key)` in `schema.go`.

### 2.2 Canonical SQL queue order (`taskQueueOrderSQL`) {#TP-6}

2.2.1. `Store.ListTasks`, `Store.TaskRepos`, `Store.Ready`, `Store.ReadyTargets`, `Store.ReadyMergers`, and `Store.ReadyPriorityDispatch` (`pkg/model/store.go`) MUST order candidate rows using `taskQueueOrderSQL`:

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

2.2.2. The precedence within every query governed by `taskQueueOrderSQL` is:

1. `t.priority DESC` — higher numeric priority always comes first.
2. `fixesBaseBranchOrderSQL` — among tasks of equal numeric `priority`, tasks carrying a `base-ci-repair:<branch>` tag (`model.BaseRepairBranchTag`, `Task.FixesBaseBranch`) sort ahead of tasks without one.
3. `t.order_key ASC` — among tasks of equal `priority` and equal base-repair status, tasks sort by ascending `order_key`.
4. `COALESCE(t.approved_at, t.created_at) ASC`, `t.created_at ASC`, `t.id ASC` — deterministic tie-breakers when `order_key` values are equal.

### 2.3 Dispatch admission groups {#TP-7}

2.3.1. `dispatch.Cycle` (`pkg/dispatch/dispatch.go`) evaluates ready tasks in three separate admission passes, each ordered internally by `taskQueueOrderSQL`:

1. **Priority-dispatch tasks** (`Store.ReadyPriorityDispatch`): console sessions and tasks holding `priority-dispatch` or `durable` (`Task.HasPriorityDispatch`). Admitted via `Budget.AdmitsPriority`, outside `max_workers`, merger counts, pipeline pauses (except upgrade holds), red base-branch holds, and model-connection concurrency limits (`durable` tasks remain subject to `MaxDurableRuns`).
2. **Mergers** (`Store.ReadyMergers`): `ReasonFix` tasks and in-flight merge-queue repairs, admitted under merger capacity.
3. **Ordinary tasks** (`Store.Ready`): all remaining ready tasks, admitted under `max_workers`, sandbox budget ceilings, pipeline pauses, red base-branch holds, and model-connection limits.

2.3.2. Numeric `Task.Priority` MUST NOT move a task between these three admission groups or bypass capacity limits. A high-priority ordinary task (`Priority: 100`) still waits behind a full `max_workers` pool and runs after ready priority-dispatch and merger tasks when worker slots open.

### 2.4 Merge-queue ordering {#TP-8}

2.4.1. `queueOrder` (`pkg/orchestrator/sync.go`) MUST sort merge-queue entries by:

1. Effective `Priority` descending (normalizing `0` to `model.DefaultTaskPriority`, `5`).
2. `FixesBaseBranch` (`true` before `false`).
3. Queue tenure: tasks already in the merge queue (`Observation.MergeQueueEnteredAt != nil`) before tasks entering the merge queue on the current reconcile cycle (`MergeQueueEnteredAt == nil`), preventing newly arriving peers of the same priority from disrupting an established batch. (A newly arriving task with a *strictly higher* `Priority` sorts ahead on rule 1.)
4. `OrderKey` ascending.
5. `ID` ascending.

2.4.2. `showQueueAtFrontOfBacklog` (`pkg/orchestrator/sync.go`) rewrites merge-queue tasks' `order_key` values in `queueOrder` sequence ahead of non-queue tasks so their relative backlog `order_key` order matches the merge queue.

### 2.5 `order_key` allocation, state transitions, and manual reorder {#TP-9}

2.5.1. `Store.OrderKeyForNewTask(ctx, atFront bool)` (`pkg/model/store.go`) computes a new task's `order_key` across the entire `task` table (not per priority):

- When `atFront` is `false` (default FIFO, unless `Config.NewestFirst` or caller `atFront` sets `true`), it returns `MAX(order_key) + orderKeySpacing` (`orderKeySpacing = 1 << 20`, or `orderKeySpacing` when the table is empty).
- When `atFront` is `true`, it returns `MIN(order_key) - orderKeySpacing`.

2.5.2. Task state transitions — including parking and waking on `resume_later`, `wait_for_task`, `wait_for_checks`, human question/reply cycles, pause/resume, and retries — MUST preserve the task's stored `Priority` and `OrderKey` unless a caller explicitly calls `MoveToFrontOfBacklog` or updates `Priority`.

2.5.3. `Store.Reorder(ctx, ids, afterID, beforeID)` (`POST /api/tasks/reorder`, `ui.ReorderRequest` in `pkg/ui/tasks.go`) updates only `order_key` (and `updated_at`) for `ids`, splitting the interval between `afterID`'s `order_key` and `beforeID`'s `order_key` and rebalancing all rows via `SELECT id FROM task ORDER BY order_key, id` when a bounded gap is smaller than `minOrderKeyGap` (`1e-6`). `Reorder` MUST NOT modify `task.priority` and accepts no priority-adoption flag.

## 3. Origin defaults, templates, inheritance, and dependencies

### 3.1 Filing defaults across origins {#TP-10}

3.1.1. Every task creation path starts at `model.DefaultTaskPriority` (`5`) unless an explicit positive `priority` is supplied on the filing call or inherited from a template (`Template.Priority`). Specifically:

- Human filings via the UI, `POST /api/tasks`, or `grain create` without `priority` get `5` (or the selected template's positive `Priority`).
- Automated filings in `pkg/orchestrator` — base-branch CI repairs (`basebranchci.go`), base-branch release repairs (`basebranchrelease.go`), base-repair bypass filings (`baserepairbypass.go`), merge-queue fixes, upgrade checks (`upgradecheck.go`), takeovers (`takeover.go`), console sessions, automated reviews (`review.go`), path triggers (`pathtrigger.go`), GitHub event tasks (`githubevents.go`), imported GitHub issues (`issueimport.go`), inline schedule firings, and branch ports (`port.go`) — get `5` unless composed from a template with a positive `Priority`.
- Branch ports (`pkg/orchestrator/port.go`) do **not** copy the source task's `Priority`; they pass `atFront: true` and default to `5`.

3.1.2. `model.Config` (`grain_config`) defines **no** `priority_defaults` map or per-origin priority configuration setting.

### 3.2 Template composition and non-template configuration entities {#TP-11}

3.2.1. `composeTemplate` (`pkg/model/compose.go`), shared by `ComposeTemplateTask`, template-backed `ComposeScheduleTask`, `ComposeSuitePass`, and `ComposeQualificationPass`, MUST set `Composition.Priority` to:

- `*tmpl.Priority` when `tmpl.Priority != nil && *tmpl.Priority != 0`;
- `model.DefaultTaskPriority` (`5`) otherwise.

3.2.2. When `POST /api/tasks` (`ui.CreateTask` in `pkg/ui/client.go`) references a `templateId` and also supplies an explicit `req.Priority`, the explicit `req.Priority` overrides the template's `Priority`. When `req.Priority` is `nil`, the composed template's `Priority` is used.

3.2.3. Inline `ComposeScheduleTask` (a `model.Schedule` with no `TemplateID`) sets `Composition.Priority = model.DefaultTaskPriority` (`5`). `model.Schedule` and `model.Suite` carry no `Priority` field of their own.

### 3.3 Follow-up proposals: no priority inheritance or parent-adjacent placement {#TP-12}

3.3.1. Follow-up tasks filed via MCP `propose_task` or sandbox CLI `grain task propose` (both live via `POST /api/tasks` with `ProposedBy` and at end-of-run via `orchestrator.relayProposedTasks` in `pkg/orchestrator/finish.go`) MUST NOT automatically inherit the parent task's `Priority`.

3.3.2. When a proposal omits `priority` (`Priority == 0` on `model.ProposedTask` or `Priority == nil` on `CreateTaskRequest`), its priority resolves to the referenced template's positive `Priority` if `template_id` is set, and `model.DefaultTaskPriority` (`5`) otherwise. An agent that wants a follow-up task to run above or below `5` MUST pass `priority` explicitly (subject to §4.2).

3.3.3. Proposals allocate `order_key` through `Store.OrderKeyForNewTask(ctx, p.AtFront)` (`at_front`), not adjacent to the parent task's `order_key` (there is no `OrderKeyAfter`).

### 3.4 Dependency independence (no blocker raising) {#TP-13}

3.4.1. Adding or removing task dependencies (`depends_on` / `Store.AddDependency`) MUST NOT alter the stored or effective `Priority` of blocking tasks. Ready queries (`Store.Ready*`) filter out tasks with open dependencies and rank the remaining ready tasks by their own `task.priority`.

## 4. Permissions, approval gates, and orthogonal ranks

### 4.1 Human writes {#TP-14}

4.1.1. A human principal (`PrincipalHuman`) creating a task (`POST /api/tasks`, `grain create`) or updating a task (`PATCH /api/tasks/{id}`, `grain update`) MAY set any integer `priority >= 1`. Raising or lowering `priority` as a human MUST NOT clear a task's approval (`ApprovedAt`) or move a `queued` task back to `proposed`.

### 4.2 Proposal approval ceiling (`PersonOnlyProposalSettings`) {#TP-15}

4.2.1. `model.PersonOnlyProposalSettings(parent, child)` (`pkg/model/hold_for_approval.go`) enforces the priority escalation gate whenever an agent task or console session `parent` proposes `child`:

```go
ceiling := max(parent.Priority, DefaultTaskPriority)
if child.Priority > ceiling {
    reasons = append(reasons, fmt.Sprintf(
        "priority %d is above this task's priority (%d)",
        child.Priority, ceiling,
    ))
}
```

4.2.2. Because `ceiling` is `max(parent.Priority, 5)`:

- A default-priority parent (`Priority == 5`) may queue proposals with `1 <= child.Priority <= 5` (when the parent's `AutoSubmits` or `ConsoleSession.TasksApproved` otherwise permits queuing) without human approval.
- A lower-priority parent (`1 <= parent.Priority < 5`) may still queue proposals up to `DefaultTaskPriority` (`5`) without human approval.
- A higher-priority parent (`parent.Priority > 5`, e.g. `10`) may queue proposals up to `parent.Priority` (`10`) without human approval.
- Any proposal with `child.Priority > max(parent.Priority, 5)` MUST be held unapproved (`ApprovedAt = nil`, state `proposed`) for a person to approve, with its requested `child.Priority` preserved on the filed task and an explanatory comment `_Filed for a person to approve: priority <child> is above this task's priority (<ceiling>)._` recorded on the task.

### 4.3 Agent API and external token permissions {#TP-16}

4.3.1. Under `agentAPIGate` (`pkg/ui/agent_api_gate.go`):

- `POST /api/tasks` requires `tasks:write` and permits numeric `priority` (`>= 1`). Agent callers without `tasks:admin` MUST NOT set `"approved": true`, and calls carrying `ProposedBy` remain subject to `PersonOnlyProposalSettings` (§4.2).
- `PATCH /api/tasks/{id}` permits numeric `priority` (`>= 1`) when the caller holds `tasks:write` (which presets `api:task-admin` and `api:dispatch-control` include).
- `PATCH /api/tasks/{id}` is **also** permitted when the caller holds the dangerous scope `tasks:admin` *without* `tasks:write`, if and only if the request body is a single JSON object containing no keys other than `"priority"` with a JSON number value (`tasksAdminAllowsTaskPatch` and `tasksAdminTaskPatchBodyAllowed`). Because `tasks:admin` is a dangerous scope, it is person-granted, MCP-only (not exposed on the sandbox CLI proxy), and requires a non-empty `X-Grain-Acting-Reason` header (`reason` argument on `api_update_task`).

### 4.4 `priorityDispatch` wire disambiguation and admission grants {#TP-17}

4.4.1. On `ui.UpdateTaskRequest` (`PATCH /api/tasks/{id}` in `pkg/ui/tasks.go` and `pkg/ui/client.go`):

- Numeric JSON `"priority": <int>` unmarshals into `UpdateTaskRequest.Priority *int` and updates `Task.Priority`.
- Boolean JSON `"priority": <bool>`, `"priorityDispatch": <bool>`, and `"priority_dispatch": <bool>` unmarshal into `UpdateTaskRequest.PriorityDispatch *bool` and toggle `model.PriorityDispatchCapability` (`priority-dispatch`). Mutating `PriorityDispatch` is a person-only capability grant change gated on `capabilities:admin` (never `tasks:write` or `tasks:admin`).
- Any other JSON type for `"priority"` (such as a string or array) MUST fail unmarshaling with `400 Bad Request`.

4.4.2. `Task.Priority` is orthogonal to:

- **`priority-dispatch` and `durable`** (`pkg/model/task.go`): admission grants controlling `Task.HasPriorityDispatch` (§2.3).
- **`ci.Priority`** (`pkg/ci/gate.go`): internal CI job gate rank (`PriorityNormal`..`PriorityRepair`).
- **`k8sPriorityClassInteractive`, `k8sPriorityClassAgent`, `k8sPriorityClassCi`** (`pkg/model/task.go`, `pkg/ui/settings.go`): Kubernetes pod `PriorityClass` names.

## 5. External surfaces (REST, declarations, Grainfiles, CLI, MCP, UI)

### 5.1 REST API (`tasks` and `templates`) {#TP-18}

5.1.1. **Tasks (`/api/tasks`):**

- `POST /api/tasks` (`ui.CreateTaskRequest`) accepts `"priority": <int>` (`>= 1`; omitted uses template priority or `5`).
- `PATCH /api/tasks/{id}` (`ui.UpdateTaskRequest`) accepts `"priority": <int>` (`>= 1`; omitted leaves `Task.Priority` unchanged).
- `GET /api/tasks`, `GET /api/tasks/{id}`, and mutation responses (`ui.TaskResponse`) always serialize `"priority": <int>` (`>= 1` on all valid tasks; internal `0` normalized to `5`), alongside `"priorityDispatch": <bool>` (`Task.HasPriorityDispatch()`).
- `POST /api/tasks/reorder` (`ui.ReorderRequest{IDs, AfterID, BeforeID}`) reorders `order_key` only and has no priority field.

5.1.2. **Templates (`/api/templates`):**

- `POST /api/templates` and `PATCH /api/templates/{id}` accept `"priority": <int>` (`>= 0`; `< 0` returns `400`; `0` clears `Template.Priority` to `nil`).
- `GET /api/templates` includes `"priority,omitempty"` (`*int`), omitting the key when unset (`nil` / `0`).
- `POST /api/tasks/composition`, `POST /api/schedules/composition`, and `POST /api/suites/composition` return `"priority": <int>` on the composed preview (`5` when unset, or the template's positive `Priority`, with `sources["priority"] = "template:<name>"` when set by the template).

### 5.2 Declarations and Grainfiles {#TP-19}

5.2.1. **Repository and config-repo templates:**

- `.grain/templates/<stem>.yml` (`pkg/repodecl/parse.go`) accepts optional `priority: <int>` (`< 0` fails declaration validation; `0` or omitted leaves `Template.Priority` unset; positive integers set `Template.Priority`).
- Config-repo `settings/templates.yml` (`pkg/declschema/declschema.go`, `pkg/staterepo/decl.go`) and `tables/template.json` serialize and parse optional `priority` (`>= 0`, `omitempty` in YAML when unset).

5.2.2. **Entities without `priority`:**

- `.grain/schedules/<stem>.yml`, `settings/schedules.yml`, and `POST`/`PATCH /api/schedules` (`model.Schedule`) have **no** `priority` field (a schedule inherits a non-default priority only by referencing a `template` that sets `priority`).
- `.grain/suites/<stem>.yml`, `settings/suites.yml`, and `POST`/`PATCH /api/suites` (`model.Suite`) have **no** `priority` field (individual suite items use their referenced template's `priority`, or `5`).
- Grainfiles (`<name>.task.grain.md` and `<name>.process.grain.md` in `design/spec/grainfiles/grainfiles.spec.md` and `design/spec/grainfiles/schemas/*.schema.json`) have **no** `priority` front-matter field; queue ranking is deployment-local.

### 5.3 MCP tools, host CLI, and sandbox CLI {#TP-20}

5.3.1. **MCP tools (`pkg/mcp` and `pkg/ui/agent_api_tools.go`):**

- `propose_task` exposes optional integer `priority` (`Task priority (default 5; higher numbers mean higher priority)`), validated `>= 1` on `model.ProposedTask` and `ui.CreateTask`.
- `list_tasks` (`pkg/mcp/task_views.go`, `pkg/mcp/task_list_cli.go`) supports filtering/sorting on `priority` and prints `, priority <N>` in text summaries when `N != 5` (always present in JSON).
- `get_task` (`pkg/mcp/task_views.go`) prints `Priority: <N>` in task headers.

5.3.2. **Host CLI (`pkg/cli`):**

- `grain create -priority <N>` and `grain update <id> -priority <N>` (`pkg/cli/taskcmd.go`) accept integers `>= 1` (`0` and negative values are rejected locally before calling the server; flag default `0` means omitted).
- `grain get <id>` (`pkg/cli/taskprint.go`) prints `priority:   <N>`.
- `grain list` (`pkg/cli/taskattributes.go`, `pkg/cli/taskprint.go`) exposes built-in numeric attribute `priority` (`Task priority`) for `-filter` (e.g. `priority:gt:5`, `priority:5`) and `-sort` (`priority:desc`, `priority:asc`), and includes `priority` in `-json` output; the default tabular `grain list` columns do not include a `PRIO` column.

5.3.3. **Sandbox CLI (`pkg/sandboxcli`):**

- `grain task propose --priority <N>` (`pkg/sandboxcli/task.go`, `pkg/ui/sandbox_cli_actions.go`) accepts integers `>= 1` (`--priority must be 1 or higher (default is 5)`).
- `grain task list` appends `(priority <N>)` when `N != 5` (and includes `priority` in `--json`); `grain task read` prints `Priority: <N>`.

### 5.4 Web UI {#TP-21}

5.4.1. **Task list (`ui/src/components/TaskList.jsx`):**

- Renders an outlined chip `P<n>` (`data-testid="task-priority-badge"`) with tooltip `Task priority <n> (default 5; higher numbers run first)` if and only if `t.priority != null && t.priority !== 5`. Tasks at default priority `5` render no priority badge.
- Drag-and-drop calls `POST /api/tasks/reorder` without changing `priority`.

5.4.2. **Task detail (`ui/src/components/DetailOverlay.jsx`, `ui/src/components/detail/Declared.jsx`):**

- Header metadata strip displays a `priority: <n>` chip (`t.priority ?? 5`).
- `Declared` tab displays a `Task priority` row reading `<n> (default 5)` when `(task.priority ?? 5) !== 5`.

5.4.3. **Forms and narrowing bar (`NewTaskOverlay.jsx`, `EditTaskForm.jsx`, `BulkEditOverlay.jsx`, `TemplateOverlay.jsx`, `CompositionPanel.jsx`, `taskAttributes.js`, `wholeNumber.js`):**

- `New task`, `Edit`, and `Bulk edit` render a `Task priority` integer input (`min: 1, step: 1`, helper `default 5; higher numbers run first`) validated as a whole number `>= 1` (`wholeNumber`).
- `Templates` renders an optional `Task priority` integer input (`min: 1, step: 1, placeholder: "5"`), sending `0` when cleared on edit so `PATCH /api/templates/{id}` clears `Template.Priority`; `CompositionPanel` previews `Task priority` with source attribution.
- `taskAttributes.js` registers built-in numeric attribute `priority` (`Task priority`) on the filter/sort narrowing bar (kept distinct from any deployment custom field named `priority`, which uses `custom.priority`). There are no band presets, visual band dividers, or high-priority confirmation dialogs.

## 6. Sources

This specification was verified directly against:

- `pkg/model/task.go` (`DefaultTaskPriority`, `Task.Priority`, `ProposedTask.Priority`, `HasPriorityDispatch`, `FixesBaseBranch`)
- `pkg/model/template.go`, `pkg/model/compose.go` (`Template.Priority`, `composeTemplate`, `ComposeScheduleTask`, `ComposeSuitePass`, `ComposeQualificationPass`)
- `pkg/model/migrations.go` (migration 73), `pkg/model/schema.go`, `pkg/model/store.go` (`putTask`, `putTemplate`, `scanTemplate`, `taskQueueOrderSQL`, `fixesBaseBranchOrderSQL`, `ListTasks`, `Ready*`, `OrderKeyForNewTask`, `Reorder`)
- `pkg/model/hold_for_approval.go` (`PersonOnlyProposalSettings`)
- `pkg/dispatch/dispatch.go` (`Cycle`)
- `pkg/orchestrator/sync.go` (`queueOrder`, `showQueueAtFrontOfBacklog`), `pkg/orchestrator/finish.go` (`relayProposedTasks`), and task-filing sites across `pkg/orchestrator/*.go`
- `pkg/ui/client.go`, `pkg/ui/tasks.go`, `pkg/ui/templates.go`, `pkg/ui/agent_api_gate.go`, `pkg/ui/agent_api_tools.go`, `pkg/ui/sandbox_cli_actions.go`
- `pkg/repodecl/parse.go`, `pkg/declschema/declschema.go`, `pkg/staterepo/decl.go`
- `pkg/cli/taskcmd.go`, `pkg/cli/taskattributes.go`, `pkg/cli/taskprint.go`, `pkg/sandboxcli/task.go`, `pkg/mcp/mock_tools.go`, `pkg/mcp/task_views.go`, `pkg/mcp/task_list_cli.go`
- `ui/src/wholeNumber.js`, `ui/src/taskAttributes.js`, and `ui/src/components/{TaskList.jsx,DetailOverlay.jsx,NewTaskOverlay.jsx,BulkEditOverlay.jsx,TemplateOverlay.jsx,CompositionPanel.jsx,detail/Declared.jsx,detail/EditTaskForm.jsx}`
