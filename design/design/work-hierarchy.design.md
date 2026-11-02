# Work hierarchy: features, user stories, tasks, and subtasks

Status: **design, not implemented**.

## Why

Grain today models every unit of work as a flat `model.Task` (`pkg/model/task.go`) organized on saved boards (`pkg/model/board.go`). While `Task` supports provenance links (`LinkProposedBy`, rendered by `Store.WorkTree` in `pkg/model/work_tree.go`), provenance only records *which agent run called `propose_task`*—not how work is intentionally structured, discussed, or tracked toward larger deliverables.

In practice, teams need a four-tier work breakdown hierarchy—**`Feature → User Story → Task → Subtask`**—for two reasons:

1. **Scoped planning and discussions.** A **Feature** captures a multi-story initiative (problem statement, architecture, cross-repository scope) and hosts high-level design discussions with humans and planning agents. A **User Story** captures user-facing value and acceptance criteria and hosts story-level decomposition discussions. A **Task** is an actionable engineering change (typically one pull request or investigation), and a **Subtask** is a granular step within a task.
2. **Progress monitoring toward larger pieces of work.** Operators need to see at a glance how far along a Feature or User Story is (`2/4 user stories · 7/12 tasks · 15/18 subtasks (78%)`), which stories or tasks are currently running, awaiting human reply, blocked, or failed, and have parent stories and features close automatically as their child work completes.

## What exists to build on

| Piece | Where | What it already does and where the gap is |
| --- | --- | --- |
| Unified `task` table and derived `task_state` view | `pkg/model/task.go`, `pkg/model/state.go`, `pkg/model/schema.go` | Every work item already gets comments (`task_comment`), interactive agent chat (`Task.Interactive`), attachments, custom fields, runs, goals (`task_goal`), and audit events (`task_event`). **Gap:** No `tier` or structural `parent_id` column exists on `task` or `template`. |
| Task kind badges | `pkg/model/task_kinds.go` (`TaskKindTags`) | Derives runtime/origin badges (`port`, `repair`, `session`, `proposal`, `durable`, `imported`). **Gap:** Orthogonal to work-breakdown tier (`feature`, `user_story`, `task`, `subtask`). |
| Provenance trees vs. `LinkChildOf` | `pkg/model/work_origin.go`, `pkg/model/work_tree.go`, `pkg/model/task.go` | `Task.Parent()` and `ui/src/workTree.js` read `LinkProposedBy` (`generatedFrom`), which only reflects run provenance. `LinkChildOf` exists in `pkg/model/task.go`, but `LinkChildOf.Blocks()` blocks whoever *holds* the link (`task_blocked` in `pkg/model/schema.go`), so placing `LinkChildOf` on a child pointing to its parent blocks the child on the parent (`tests/e2e/e2e_test.go`). **Gap:** Structural parent–child containment needs a dedicated `parent_id` column on `task` independent of `LinkProposedBy` and `task_blocked`. |
| Dispatch readiness (`task_ready`) | `pkg/model/schema.go`, `pkg/dispatch/dispatch.go` | Every approved non-deferred task in `queued` state is immediately dispatched to a coding sandbox targeting a single repository. **Gap:** Features and User Stories are primarily planning, discussion, and tracking containers (often spanning multiple repositories or having no single PR of their own) and must not be auto-dispatched as headless single-PR coding runs when approved. |
| Daemon-checked task goals | `pkg/model/task_goal.go`, `pkg/orchestrator/goals.go` | `GoalTasksClosed` (`tasks-closed`) closes a task when a static list of task IDs (`Spec.Tasks`) closes. **Gap:** Features and User Stories gain and reparent children dynamically over their lifetime and need a dynamic `children-closed` goal. |
| UI detail pane, boards, and filters | `ui/src/components/DetailOverlay.jsx`, `ui/src/components/detail/WorkTree.jsx`, `ui/src/taskAttributes.js`, `ui/src/workTree.js` | `DetailOverlay` supports discussions and interactive `TaskChat`, while `WorkTree.jsx` hides provenance inside a collapsed disclosure. **Gap:** No hierarchy breadcrumb bar, no inline child breakdown/composer above discussions, no rollup progress bars on cards/trees/Gantt views, and no `tier`, `parent`, `feature`, or `story` query filters. |

## Decisions at a glance

1. **One `task` table for all four tiers.** Features, user stories, tasks, and subtasks remain rows in `task` distinguished by `Task.Tier` (`feature`, `user_story`, `task`, `subtask`; default `task`). Every tier shares comments, interactive agent sessions, attachments, links, custom fields, goals, and `/tasks/{id}` deep links.
2. **Explicit `parent_id` on `task`, separate from `LinkProposedBy`.** Structural containment is stored in `task.parent_id` (at most one parent per item, cycle-free, tier-validated), while `LinkProposedBy` continues to record which agent run proposed a task.
3. **Flexible four-tier containment.** `feature` sits at the root (`parent_id = ""`); `user_story` may be a root item or a child of a `feature`; `task` may be a root item or a child of a `user_story` or `feature` (allowing lightweight features to omit stories); `subtask` must be a child of a `task`.
4. **Optional target repository on `feature` and `user_story`.** Higher-tier items may span multiple repositories or be planned before repositories are chosen. Child `task` and `subtask` items inherit their parent's target repository by default when omitted, or declare their own.
5. **Planning-first dispatch for containers.** Approving a `feature` or `user_story` marks it active for planning and progress tracking without queuing an automatic headless coding run. An agent sandbox runs on a `feature` or `user_story` only when it is in an active interactive chat turn (`Interactive = true`) or when a person explicitly triggers **Run now** (e.g., to ask an agent to decompose the feature or story into child proposals).
6. **Dynamic `children-closed` goal.** Features and User Stories default to `GoalChildrenClosed` (`children-closed`), which tracks direct and transitive children dynamically and closes the parent automatically once it has at least one child and all children are `closed` (unless the operator selects `person-closes` for manual sign-off).
7. **Recursive bottom-up `HierarchyRollup`.** Every item with children exposes tier counts (`stories`, `tasks`, `subtasks`), state breakdown (`proposed`, `queued`, `blocked`, `running`, `awaitingReply`, `awaitingSubmit`, `failed`, `done`), leaf-weighted completion percentage, distinct repositories, and bubbled-up attention flags (`needsAttention`).

---

## Data model and schema {#WH-1}

### `WorkTier` and `ParentID`

`pkg/model/task.go` defines `WorkTier`:

```go
type WorkTier string

const (
	TierFeature   WorkTier = "feature"
	TierUserStory WorkTier = "user_story"
	TierTask      WorkTier = "task"
	TierSubtask   WorkTier = "subtask"
)
```

- **Default normalization:** An empty `WorkTier("")` normalizes to `TierTask` (`"task"`) on store writes (`putTask`), reads (`scanTask`), and API projections (`taskFrom`), preserving backward compatibility for all existing rows and callers.
- **Tier rank and parenting (`WorkTier.CanParent`):**
  - `TierFeature.Rank() == 1`: `parent_id` must be `""`. Can parent `TierUserStory` and `TierTask`.
  - `TierUserStory.Rank() == 2`: `parent_id` may be `""` or reference a `TierFeature`. Can parent `TierTask`.
  - `TierTask.Rank() == 3`: `parent_id` may be `""` or reference a `TierUserStory` or `TierFeature`. Can parent `TierSubtask`.
  - `TierSubtask.Rank() == 4`: `parent_id` must reference a `TierTask`. Cannot parent any item (`CanParent` returns `false`).
- **`IsContainer()`:** `t == TierFeature || t == TierUserStory`.
- **Optional repository binding on containers:** `Task.Valid()` permits `Binding == BindingNone` (`Target == nil`) when `Tier.IsContainer()` is true. Leaf `TierTask` and `TierSubtask` items keep existing repository binding rules.

### Schema migration

An in-place migration in `pkg/model/migrations.go` (and matching DDL in `pkg/model/schema.go`) adds:

```sql
ALTER TABLE `task` ADD COLUMN `tier` TEXT NOT NULL DEFAULT 'task';
ALTER TABLE `task` ADD COLUMN `parent_id` TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS `task_parent` ON `task` (`parent_id`);
CREATE INDEX IF NOT EXISTS `task_tier` ON `task` (`tier`);
ALTER TABLE `template` ADD COLUMN `tier` TEXT NOT NULL DEFAULT 'task';
```

`pkg/staterepo` exports and imports `tier` and `parent_id` on `task` and `tier` on `template`.

### Hierarchy validation (`Store.ValidateHierarchy`) {#WH-2}

Every task creation and update path (`Store.CreateTask`, `Store.UpdateTask`, `Store.SetTaskHierarchy`) validates:

1. `tier.Valid()` is true.
2. If `tier == TierSubtask`, `parent_id` is non-empty.
3. If `parent_id != ""`:
   - `parent_id != task.ID` (no self-parenting).
   - The parent row exists in `task` and `parent.Tier.CanParent(tier)` is true.
   - Walking `parent_id` upward visits at most 3 ancestors without repeating `task.ID` (cycle-free and bounded to depth 4).
4. If the task already has children (`SELECT DISTINCT tier FROM task WHERE parent_id = ?`), changing `task.Tier` is allowed only if the new `tier.CanParent(childTier)` holds for every existing child.

---

## Lifecycle, dispatch, and dynamic rollup goals {#WH-3}

### Container dispatch rules

Features and User Stories represent scope and discussions first; implementation happens in their child `task` and `subtask` items.

- **Headless coding runs are not auto-dispatched for containers.** In `pkg/model/schema.go` (`task_ready`) and `pkg/model/store.go` (`Ready`, `ReadyTargets`, `ReadyPriorityDispatch`), a task with `t.tier IN ('feature', 'user_story')` is eligible for dispatch only when:
  1. `t.interactive = 1` and the task is waiting on an agent turn (newly approved interactive planning session or a human posted a follow-up message in `TaskChat`), or
  2. An operator explicitly requested an immediate run (`o.run_now_at IS NOT NULL`, e.g., clicking **Run now** to have a planning agent analyze the feature/story and propose child items).
- **Interactive planning without a single target repo:** When a `feature` or `user_story` has `Binding == BindingNone` (`Target == nil`) and runs an interactive planning turn, the agent runs in a workspace seeded with its declared `Reads` / `Writes` repositories (or a repo-less planning workspace with Grain task MCP tools) so it can inspect repositories, discuss architecture with the user, and call `propose_task` to create child stories and tasks targeting specific repositories.
- **Parent `task` with `subtask` children:** When a `task` has one or more open `subtask` children (`EXISTS (SELECT 1 FROM task AS c LEFT JOIN task_observation AS co ON co.task_id = c.id WHERE c.parent_id = t.id AND co.closed_at IS NULL)`):
  - If the parent `task` has `GoalChildrenClosed`, it acts as a container for its subtasks and closes automatically when all subtasks close.
  - Otherwise, open subtasks count in `task_blocked` as open blockers on the parent `task` so a parent verification/integration run waits until its subtasks close, without blocking the subtasks themselves.

### Dynamic `children-closed` goal (`GoalChildrenClosed`) {#WH-4}

`pkg/model/task_goal.go` adds `GoalChildrenClosed GoalKind = "children-closed"`:

- **Default for `feature` and `user_story`:** When a `feature` or `user_story` is filed without an explicit `Goal`, grain attaches `TaskGoal{Kind: GoalChildrenClosed}` by default. Operators can change the goal to `GoalPersonCloses` (`person-closes`) or clear it at any time.
- **Daemon evaluation (`pkg/orchestrator/goals.go`):**
  - Queries all direct children (`WHERE parent_id = ?`) and transitive descendants.
  - Computes `Done` (children in `closed` state) and `Total` (all direct children), plus a human-readable `Summary` (e.g., `"2/3 user stories closed (7/9 tasks)"`).
  - **Empty-children guard:** While `Total == 0` (a newly created Feature or User Story whose breakdown has not been added yet), the goal is **not** satisfied; `Summary` reports `"No child items yet"`.
  - **Cascade completion:** Once `Total > 0` and `Done == Total` (every direct child is `closed`, and no descendant remains open), `GoalChildrenClosed` is satisfied and the orchestrator closes the parent item, recording a `TaskEvent` (`"all N child items closed"`). Because goal evaluation runs each orchestrator cycle (and on child close), closing the last `subtask` of a `task` can close the `task`, which in turn closes its parent `user_story`, which in turn closes its parent `feature`.
- **Re-opening on new open child:** If a new open child is added under a `closed` `feature` or `user_story` that carries `GoalChildrenClosed`, grain reopens the parent (`Observation.ClosedAt = nil`, recording a `TaskEvent`) so the feature or story accurately reflects active work again.

---

## Hierarchy tree and bottom-up progress rollup {#WH-5}

### `HierarchyRollup` model (`pkg/model/hierarchy.go`)

For every work item that has children (or has `tier IN ('feature', 'user_story')`), grain computes a bottom-up `HierarchyRollup`:

```go
type TierCounts struct {
	Total int `json:"total"`
	Done  int `json:"done"`
}

type StateBreakdown struct {
	Proposed       int `json:"proposed"`
	Queued         int `json:"queued"`
	Blocked        int `json:"blocked"`
	Running        int `json:"running"`
	AwaitingReply  int `json:"awaitingReply"`
	AwaitingSubmit int `json:"awaitingSubmit"`
	Failed         int `json:"failed"`
	Done           int `json:"done"`
}

type HierarchyRollup struct {
	DirectChildren int                   `json:"directChildren"`
	DirectDone     int                   `json:"directDone"`
	TotalLeaves    int                   `json:"totalLeaves"`
	DoneLeaves     int                   `json:"doneLeaves"`
	PercentDone    int                   `json:"percentDone"`
	ByTier         map[WorkTier]TierCounts `json:"byTier,omitempty"`
	ByState        StateBreakdown        `json:"byState"`
	NeedsAttention bool                  `json:"needsAttention"`
	Repos          []string              `json:"repos,omitempty"`
}
```

- **Leaf-weighted `PercentDone`:** Every leaf descendant inside the subtree (e.g., each `subtask` of a decomposed `task`, or each `task` that has no subtasks) counts as 1 unit of work; a leaf is done when its state is `completed` or `closed` (`TreeStatusIsDone`). `PercentDone` is `round(DoneLeaves * 100 / TotalLeaves)` (or `100` if the item itself is `closed` with no children, `0` if open with no children).
- **Tier breakdown (`ByTier`):** Counts total and done descendants at each lower tier (`user_story`, `task`, `subtask`) so a Feature can display both `"2/4 stories"` and `"9/12 tasks · 6/6 subtasks"`.
- **State breakdown (`ByState`) and `NeedsAttention`:** Aggregates the current state of all descendants. `NeedsAttention` is `true` whenever `ByState.Failed > 0 || ByState.AwaitingReply > 0`, surfacing blocked/failing leaf work on the parent Story and Feature.
- **Unifying `Store.WorkTree` (`pkg/model/work_tree.go`):** `WorkTree` places nodes under their structural `ParentID` first, falling back to `LinkProposedBy` (`Task.Parent()`) only when `ParentID == ""` so existing provenance trees continue to render while structural hierarchies take precedence.

---

## REST API, MCP tools, CLI, and grainfiles {#WH-6}

### REST API (`pkg/ui`)

- **`ui.Task` wire shape (`pkg/ui/tasks.go`):**
  - `tier`: `"feature" | "user_story" | "task" | "subtask"`
  - `parentId`: `string` (`""` when root)
  - `ancestors`: `[]AncestorRef` (`[{id, title, tier, state}]` ordered from root ancestor down to direct parent, computed in memory during `ListTasks` / `GetTask`)
  - `rollup`: `*HierarchyRollup` (populated when `directChildren > 0` or `tier` is `feature` / `user_story`)
- **`POST /api/tasks` and `PATCH /api/tasks/{id}`:**
  - Accept `tier` (`*string`) and `parentId` (`*string`).
  - When `parentId` is set on creation and `repo` is omitted on a `task` or `subtask`, inherit `Target` and `Base` from the nearest ancestor that declares one.
  - Validate hierarchy constraints via `Store.ValidateHierarchy`, returning `400 Bad Request` with a clear message on invalid tier containment or cycle.
- **`POST /api/tasks/{id}/approve-children`:**
  - Approves all currently `proposed` direct children (or entire subtree when `{"recursive": true}`) of task `{id}` that the caller is permitted to approve, returning the approved task IDs.
- **`GET /api/tasks/{id}/tree` (`pkg/ui/work_tree.go`):**
  - Returns the full hierarchy rooted at `{id}`'s root ancestor (or `{id}` itself), with `tier`, `parentId`, and `rollup` on every `WorkTreeNode`.

### Agent MCP tools, CLI, and `.task.grain.md` (`pkg/mcp`, `cmd/grain`, `pkg/grainfile`) {#WH-7}

- **MCP `propose_task` (`pkg/mcp/task_tools.go`, `pkg/ui/agent_api_tools.go`):**
  - Adds optional `tier` (`"feature" | "user_story" | "task" | "subtask"`) and `parent_id` (`string`).
  - **Default child tier & parent inference:** When an agent running on task `P` calls `propose_task` without `parent_id`:
    - If `P.Tier == TierFeature`, `parent_id` defaults to `P.ID` and `tier` defaults to `TierUserStory` (or `TierTask` if specified).
    - If `P.Tier == TierUserStory`, `parent_id` defaults to `P.ID` and `tier` defaults to `TierTask`.
    - If `P.Tier == TierTask` and the caller explicitly passes `tier: "subtask"`, `parent_id` defaults to `P.ID`.
- **MCP `read_grain_task` and `list_grain_tasks`:**
  - Include `tier`, `parent_id`, `ancestors`, direct `children` summaries, and `rollup` in responses.
  - `list_grain_tasks` accepts optional `tier` and `parent_id` filters.
- **Host & sandbox CLIs (`cmd/grain`, `cmd/grain-sandbox`):**
  - `grain create` and `grain task propose` accept `-tier` / `--tier` and `-parent` / `--parent`.
  - `grain update <id>` accepts `-tier` and `-parent` (pass `-parent ""` to detach to root).
- **Task grainfiles (`.task.grain.md`, `pkg/grainfile`):**
  - Optional frontmatter fields `tier: feature | user_story | task | subtask` and `parent: <task-id or grainfile path>` allow declarative hierarchies to be committed and imported together.

---

## UI: planning, discussion, and progress monitoring {#WH-8}

### Hierarchy breadcrumbs and tier badges (`DetailOverlay.jsx`, `TierBadge.jsx`) {#WH-9}

- **`TierBadge.jsx`:** Renders a distinct, compact badge for each tier:
  - `Feature`: purple/indigo badge (`Feature`)
  - `User Story`: teal/cyan badge (`Story`)
  - `Task`: neutral slate badge (`Task`, omitted in compact task-only lists where `task` is the default, always shown in hierarchy views and detail headers)
  - `Subtask`: muted outline badge (`Subtask`)
- **Breadcrumb header in `DetailOverlay.jsx`:**
  - Above the title in `DetailOverlay`, render the `ancestors` chain (`Feature #10: Unified Auth › Story #14: OAuth2 PKCE Flow`) with each ancestor's `TierBadge`, title, and `StateDot`, clickable to navigate in place.
  - In the right-hand properties column (`Declared.jsx`), show **Tier** (editable selector) and **Parent** (searchable picker filtered to items whose tier `CanParent(t.tier)`).

### Work breakdown & co-planning workspace (`WorkBreakdown.jsx`, `DetailOverlay.jsx`) {#WH-10}

To make Features and User Stories effective spaces for **planning and discussions around a specific element of work**:

- **Prominent placement above discussions:** On any `feature`, `user_story`, or `task` that has children (or is a container tier), render a first-class **Work Breakdown** section in the main column directly between the description (`body`) and the discussion/chat stream (`TaskChat.jsx` / `Timeline.jsx`), replacing the collapsed "Provenance" disclosure (`WorkTree.jsx`).
- **Rollup progress bar and counters:**
  - Segmented horizontal progress bar colored by descendant state (`Done`, `Awaiting Submit`, `Running`, `Awaiting Reply`, `Queued`, `Blocked`, `Failed`, `Proposed`).
  - Tier and state summary strip: e.g., `75% complete · 3/4 Stories · 9/12 Tasks · 1 awaiting reply`.
- **Interactive child tree:**
  - Expandable tree of child `User Stories → Tasks → Subtasks` showing each item's `TierBadge`, ID, title, target repo chip, state badge, PR link, and nested rollup mini-bar.
- **Inline child composer (`+ Add User Story` / `+ Add Task` / `+ Add Subtask`):**
  - Inline quick-create row inside the Work Breakdown panel pre-populated with `parentId = t.id` and the natural child tier (`user_story` under a `feature`, `task` under a `user_story`, `subtask` under a `task`), plus an option to open the full `NewTaskOverlay` pre-seeded with the parent.
- **One-click batch approval after planning:**
  - When a planning agent or human proposes child stories/tasks during a discussion, a banner button **Approve all N proposed children** calls `POST /api/tasks/{id}/approve-children` so the team can discuss, refine, and greenlight the breakdown in one place.

### Boards, filters, tree view, and Gantt monitoring (`taskAttributes.js`, `TaskTree.jsx`, `GanttChart.jsx`) {#WH-11}

- **Query attributes (`ui/src/taskAttributes.js`):**
  - `tier` (aliases `level`, `workTier`): filters/sorts by `feature`, `user_story` (alias `story`), `task`, `subtask`.
  - `parent`: filters by direct `parentId` (`parent:14` or `parent:none`).
  - `feature`: filters to a Feature and all of its transitive descendants (`feature:10`).
  - `story`: filters to a User Story and all of its transitive descendants (`story:14`).
  - `progress`: numeric attribute (`0..100`) reading `task.rollup?.percentDone` for filtering (`progress:lt:100`) and sorting (`-sort progress:asc`).
- **Tree board (`ui/src/workTree.js`, `TaskTree.jsx`, `TaskTreeBoard.jsx`):**
  - `forestFromTasks` nests nodes by `parentId` first (falling back to `generatedFrom` only when `parentId` is empty).
  - Each parent node row displays its `TierBadge`, inline progress bar, `done/total` leaf counter, and any bubbled-up `needsAttention` chip (`Awaiting reply`, `Failed`).
  - `TaskTreeBoard` adds quick tier-scope toggles (`All tiers`, `Features`, `Stories & above`) and an `Expand to tier` control.
- **List, Kanban, and Gantt views (`TaskList.jsx`, `TaskBoard.jsx`, `GanttChart.jsx`):**
  - `TaskList` and `TaskBoard` cards render a compact ancestor breadcrumb pill (`#10 Unified Auth › #14 PKCE`) and, on `feature` / `user_story` items, a compact rollup progress bar (`75% · 9/12`).
  - `GanttChart` groups and indents bars by `Feature → User Story → Task → Subtask`, spanning each container bar from the earliest child `startedAt`/`createdAt` to the latest child `completedAt`/live run.

---

## Staged implementation plan {#WH-12}

| Stage | Scope | Depends on |
| --- | --- | --- |
| **WH-S1: Store model, migration, and hierarchy validation** | `WorkTier`, `Task.Tier`, `Task.ParentID`, `Template.Tier`, in-place schema migration (`task.tier`, `task.parent_id`, `template.tier`), `Store.ValidateHierarchy`, state-repo export/import, and `v2baseline` migration tests. | — |
| **WH-S2: Container dispatch rules and `children-closed` goal** | Update `task_ready`, `task_blocked`, and `Store.Ready*` for container tiers (`feature`, `user_story`) and parent tasks with subtasks; add `GoalChildrenClosed` (`children-closed`) in `pkg/model/task_goal.go` and `pkg/orchestrator/goals.go` with cascade completion and reopen-on-new-child. | WH-S1 |
| **WH-S3: `HierarchyRollup` engine and REST API** | `pkg/model/hierarchy.go`, structural `Store.WorkTree` unification, `ui.Task` fields (`tier`, `parentId`, `ancestors`, `rollup`), `POST /api/tasks` & `PATCH /api/tasks/{id}` hierarchy handling, and `POST /api/tasks/{id}/approve-children`. | WH-S1, WH-S2 |
| **WH-S4: Agent MCP tools, CLI, and grainfiles** | `propose_task` / `read_grain_task` / `list_grain_tasks` (`tier`, `parent_id`, default child tier inference), `grain` and `grain-sandbox` CLI flags (`-tier`, `-parent`), and `.task.grain.md` frontmatter support. | WH-S3 |
| **WH-S5: UI planning & discussion workspace** | `TierBadge.jsx`, ancestor breadcrumb bar and tier/parent editors in `DetailOverlay.jsx`, `WorkBreakdown.jsx` panel above `TaskChat` with segmented rollup bar, inline child composer, and batch child approval, plus tier-aware `NewTaskOverlay.jsx`. | WH-S3 |
| **WH-S6: UI progress monitoring, filters, and boards** | `tier`, `parent`, `feature`, `story`, and `progress` in `taskAttributes.js`; structural 4-tier `forestFromTasks` and rollup bars in `TaskTree.jsx` / `TaskTreeBoard.jsx`; breadcrumb pills and progress bars in `TaskList.jsx`, `TaskBoard.jsx`, and `GanttChart.jsx`. | WH-S5 |
