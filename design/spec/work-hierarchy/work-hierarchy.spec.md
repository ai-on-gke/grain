# Work Hierarchy Specification

**Specifies:** [`work-hierarchy.design.md`](../../design/work-hierarchy.design.md)

**Status:** normative specification for [`work-hierarchy.design.md`](../../design/work-hierarchy.design.md).

This specification defines Grain's configurable work-breakdown hierarchy: the `WorkTierDefinition` catalogue and out-of-the-box four-tier defaults (`feature`, `user_story`, `task`, `subtask`), database schema (`task.tier`, `task.parent_id`, `template.tier`, `tier_config`), cycle-free hierarchy validation, per-tier prompt fragments (`PromptExtensionFor` composition), tier Python DSL lifecycle hooks (`scripts/tiers/<tier>.py`), dynamic `children-closed` goals (`GoalChildrenClosed`), recursive `HierarchyRollup` computation, and REST, MCP, CLI, Grainfile, and Web UI surfaces.

- Architectural rationale and staged rollout live in [`design/design/work-hierarchy.design.md`](../../design/work-hierarchy.design.md).
- Where the design document and this specification disagree, **this specification wins**.

The key words "MUST", "MUST NOT", "SHOULD", "SHOULD NOT", and "MAY" are to be interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and only when, they appear in all capitals.

## Contents

1. Configurable tier catalogue and out-of-the-box defaults
2. Database schema and hierarchy validation
3. Per-tier dispatch modes, prompt fragments, and progressive Python DSL hooks
4. Dynamic `children-closed` goal and bottom-up `HierarchyRollup`
5. External surfaces (REST API, MCP tools, CLI, Grainfiles)
6. Web UI workspace, boards, and filters

---

## 1. Configurable tier catalogue and out-of-the-box defaults

### 1.1 `WorkTier` and `WorkTierDefinition` schema {#WH-1}

1.1.1. `pkg/model/task.go` defines `WorkTier` as a normalized lowercase ASCII slug (`^[a-z][a-z0-9_]{0,31}$`), with built-in constants for the four default tiers:

```go
type WorkTier string

const (
	TierFeature   WorkTier = "feature"
	TierUserStory WorkTier = "user_story"
	TierTask      WorkTier = "task"
	TierSubtask   WorkTier = "subtask"
)
```

1.1.2. Each deployment maintains an ordered catalogue of `WorkTierDefinition` entries (`pkg/model/tier.go`), projected to and from `settings/tiers.yml` in the config repository (`pkg/staterepo`):

```go
type TierDispatchMode string

const (
	TierDispatchAuto                TierDispatchMode = "auto"
	TierDispatchInteractiveOrManual TierDispatchMode = "interactive_or_manual"
	TierDispatchManualOnly          TierDispatchMode = "manual_only"
)

type WorkTierDefinition struct {
	ID               WorkTier         `json:"id" yaml:"id"`
	Name             string           `json:"name" yaml:"name"`
	BadgeLabel       string           `json:"badgeLabel" yaml:"badge-label"`
	Color            string           `json:"color" yaml:"color"`
	Rank             int              `json:"rank" yaml:"rank"`
	AllowedParents   []WorkTier       `json:"allowedParents" yaml:"allowed-parents"` // "" allows root (parent_id == "")
	DefaultChildTier WorkTier         `json:"defaultChildTier,omitempty" yaml:"default-child-tier,omitempty"`
	AllowRepoLess    bool             `json:"allowRepoLess,omitempty" yaml:"allow-repo-less,omitempty"`
	DispatchMode     TierDispatchMode `json:"dispatchMode" yaml:"dispatch-mode"`
	DefaultGoal      GoalKind         `json:"defaultGoal,omitempty" yaml:"default-goal,omitempty"`
	PromptFragment   string           `json:"promptFragment,omitempty" yaml:"prompt-fragment,omitempty"`
	ScriptPath       string           `json:"scriptPath,omitempty" yaml:"script,omitempty"`
}
```

1.1.3. **Default normalization:** An empty `WorkTier("")` MUST normalize to `TierTask` (`"task"`) on store writes (`putTask`), store reads (`scanTask`), and API projections (`taskFrom`), preserving backward compatibility for all existing rows and callers.

1.1.4. **Catalogue invariants:**
- Every tier catalogue MUST include `"task"` (`TierTask`).
- `AllowedParents` MUST only reference tier IDs with a strictly smaller `Rank` (or `""` for root placement), guaranteeing a DAG of tier ranks with no cycles.
- Maximum hierarchy depth across the catalogue MUST NOT exceed 6 levels (4 levels in `DefaultWorkTiers`).

### 1.2 Out-of-the-box `DefaultWorkTiers` (`settings/tiers.yml`) {#WH-2}

1.2.1. When `settings/tiers.yml` is absent or unmodified, Grain MUST seed `DefaultWorkTiers` with four tiers (`feature`, `user_story`, `task`, `subtask`):

```yaml
tiers:
  - id: feature
    name: Feature
    badge-label: Feature
    color: indigo
    rank: 1
    allowed-parents: [""]
    default-child-tier: user_story
    allow-repo-less: true
    dispatch-mode: interactive_or_manual
    default-goal: children-closed
    prompt-fragment: |
      This item is a Feature (rank-1 initiative container).
      1. Collaborate with the operator to clarify problem statement, cross-repository architecture, and non-goals.
      2. Draft or update the high-level design document in `design/design/<slug>.design.md` (following `contributions.spec.md` headers).
      3. Decompose approved scope into child `user_story` items (or direct `task` items for lightweight features) using `propose_task` with `parent_id` set to this Feature.
      4. Do not implement production code directly in a Feature run unless explicitly instructed.

  - id: user_story
    name: User Story
    badge-label: Story
    color: teal
    rank: 2
    allowed-parents: ["", "feature"]
    default-child-tier: task
    allow-repo-less: true
    dispatch-mode: interactive_or_manual
    default-goal: children-closed
    prompt-fragment: |
      This item is a User Story (rank-2 deliverable container).
      1. Define clear user-facing acceptance criteria and verification requirements.
      2. Where normative interfaces or schemas change, draft or update the specification in `design/spec/<slug>/<slug>.spec.md`.
      3. Decompose the story into PR-sized child `task` items via `propose_task` (setting `depends_on` where tasks must sequence).

  - id: task
    name: Task
    badge-label: Task
    color: slate
    rank: 3
    allowed-parents: ["", "user_story", "feature"]
    default-child-tier: subtask
    allow-repo-less: false
    dispatch-mode: auto
    prompt-fragment: |
      This item is an actionable engineering Task.
      Implement and test the requested change on the task branch, open or update its pull request, and split out `subtask` items via `propose_task` only when parallel or staged sub-steps are needed.

  - id: subtask
    name: Subtask
    badge-label: Subtask
    color: muted
    rank: 4
    allowed-parents: ["task"]
    allow-repo-less: false
    dispatch-mode: auto
    prompt-fragment: |
      This item is a Subtask under parent Task {{parent_id}}.
      Complete and verify this focused step so the parent Task can advance.
```

1.2.2. **Optional repository binding (`allowRepoLess`):** `Task.Valid()` MUST permit `Binding == BindingNone` (`Target == nil`) when the task's tier definition has `AllowRepoLess == true` (`feature` and `user_story` by default). Tiers with `AllowRepoLess == false` (`task` and `subtask` by default) MUST retain existing target-repository binding requirements.

---

## 2. Database schema and hierarchy validation

### 2.1 Schema migration and state-repo projection {#WH-3}

2.1.1. An in-place migration in `pkg/model/migrations.go` (mirrored in `pkg/model/schema.go`) MUST add:

```sql
ALTER TABLE `task` ADD COLUMN `tier` TEXT NOT NULL DEFAULT 'task';
ALTER TABLE `task` ADD COLUMN `parent_id` TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS `task_parent` ON `task` (`parent_id`);
CREATE INDEX IF NOT EXISTS `task_tier` ON `task` (`tier`);
ALTER TABLE `template` ADD COLUMN `tier` TEXT NOT NULL DEFAULT 'task';

CREATE TABLE IF NOT EXISTS `tier_config` (
  `id` TEXT PRIMARY KEY,
  `name` TEXT NOT NULL,
  `badge_label` TEXT NOT NULL,
  `color` TEXT NOT NULL DEFAULT 'slate',
  `rank` INTEGER NOT NULL,
  `allowed_parents_json` TEXT NOT NULL DEFAULT '[""]',
  `default_child_tier` TEXT NOT NULL DEFAULT '',
  `allow_repo_less` INTEGER NOT NULL DEFAULT 0,
  `dispatch_mode` TEXT NOT NULL DEFAULT 'auto',
  `default_goal` TEXT NOT NULL DEFAULT '',
  `prompt_fragment` TEXT NOT NULL DEFAULT '',
  `script_path` TEXT NOT NULL DEFAULT ''
);
```

2.1.2. `pkg/staterepo` MUST classify `tier_config` in `SettingsTables` (projected as `settings/tiers.yml` and optional `scripts/tiers/<tier>.py` files) and MUST export/import `tier` and `parent_id` on `task` and `tier` on `template`.

2.1.3. Every task creation and update path (`Store.CreateTask`, `Store.UpdateTask`, `Store.SetTaskHierarchy`) MUST validate hierarchy constraints via `Store.ValidateHierarchy`:
1. `tier` exists in the active `WorkTierDefinition` catalogue (`tierDef`).
2. If `parent_id == ""`:
   - `""` MUST be present in `tierDef.AllowedParents` (for example, `subtask` requires a `task` parent and rejects `parent_id == ""`).
3. If `parent_id != ""`:
   - `parent_id != task.ID` (no self-parenting).
   - The parent row MUST exist in `task`, and `parent.Tier` MUST be present in `tierDef.AllowedParents` (`parentTier.CanParent(tier)`).
   - Walking `parent_id` upward MUST visit at most 5 ancestors without repeating `task.ID` (cycle-free and bounded depth).
4. If the task already has children (`SELECT DISTINCT tier FROM task WHERE parent_id = ?`), changing `task.Tier` is permitted only if the new tier is in `childTierDef.AllowedParents` for every existing child tier.

---

## 3. Per-tier dispatch modes, prompt fragments, and progressive Python DSL hooks

### 3.1 Per-tier dispatch readiness and prompt composition {#WH-4}

3.1.1. **Configurable dispatch modes (`TierDispatchMode`):** In `pkg/model/schema.go` (`task_ready`) and `pkg/model/store.go` (`Ready`, `ReadyTargets`, `ReadyPriorityDispatch`), a queued task's eligibility for sandbox dispatch is determined by its tier's `DispatchMode` (and optional `can_dispatch` script hook, §3.2):
- `TierDispatchAuto` (`"auto"`, default for `task` and `subtask`): eligible for automatic dispatch whenever approved, queued, and unblocked.
- `TierDispatchInteractiveOrManual` (`"interactive_or_manual"`, default for `feature` and `user_story`): approving the item marks it active for planning and rollup tracking without queuing an automatic headless coding run. It dispatches a sandbox session only when:
  1. `t.interactive = 1` and the item is waiting on an agent turn (newly approved interactive planning session or a human posted a follow-up message in `TaskChat`), or
  2. An operator or trigger explicitly requested an immediate run (`o.run_now_at IS NOT NULL`, e.g. clicking **Run now**).
- `TierDispatchManualOnly` (`"manual_only"`): dispatches only when `o.run_now_at IS NOT NULL` or during an active interactive turn.

3.1.2. **Repo-less planning workspaces:** When an item whose tier sets `AllowRepoLess == true` has `Binding == BindingNone` (`Target == nil`) and dispatches a session, the orchestrator MUST provision a workspace seeded with its declared `Reads` / `Writes` repositories (or a repo-less planning workspace with Grain task MCP tools enabled) so the agent can inspect repositories, discuss architecture with the user, author design/spec PRs, and call `propose_task`.

3.1.3. **Parent `task` with open `subtask` children:** When a `task` has one or more open child items (`EXISTS (SELECT 1 FROM task AS c LEFT JOIN task_observation AS co ON co.task_id = c.id WHERE c.parent_id = t.id AND co.closed_at IS NULL)`):
- If the parent `task` carries `GoalChildrenClosed`, it acts as a container for its subtasks and closes automatically when all subtasks close (§4.1).
- Otherwise, open subtasks count in `task_blocked` as open blockers on the parent `task` so a parent integration/verification run waits until its subtasks close, without blocking the subtasks themselves.

3.1.4. **Per-tier prompt fragment composition (`PromptExtensionFor`):**
- `model.PromptExtensionFor` (`pkg/model/prompt_extension.go`) and `orchestrator.resolvePromptExtension` (`pkg/orchestrator/run.go`) MUST compose standing instructions in four layers:
  1. **Deployment layer:** `Config.PromptExtension`
  2. **Tier layer:** `TierDef.PromptFragment` (with `{{tier}}`, `{{parent_id}}`, and `{{default_child_tier}}` interpolated)
  3. **Repository layer:** `RepoConfig.PromptExtension`, `.grain/settings.yml` `prompt-extension`, and optional repo-declared `.grain/settings.yml` `tier-prompts[<tier>]` (`RepoSettingsSchema` in `pkg/declschema/declschema.go`)
  4. **Task override layer:** `Task.PromptExtension` (which, when non-empty, replaces layers 1–3 for that task, matching `pkg/model/prompt_extension.go`)

### 3.2 Tier Python DSL lifecycle hooks and progressive codification {#WH-5}

3.2.1. **Purpose:** Teams can define their process initially using tier states and natural-language `promptFragment`s, and progressively codify unambiguous rules into deterministic tier scripts (`scripts/tiers/<tier>.py` in the config repository) either by hand or via a background process-codifier agent.

3.2.2. **Execution environment:** When `WorkTierDefinition.ScriptPath` is set (e.g., `scripts/tiers/feature.py`), the controller evaluates the script in a sandboxed, deterministic Starlark / restricted-Python interpreter with:
- No network access, no filesystem writes, and no secret access.
- A bounded execution step limit (10,000 steps / 50 ms timeout per hook invocation).
- A read-only context object `ctx` exposing:
  - `ctx.task`: `{id, title, tier, parent_id, state, interactive, run_now, repo, labels, custom, goal, pr_status}`
  - `ctx.parent`: parent task summary or `None`
  - `ctx.children`: list of direct child task summaries (`[{id, tier, state, repo, pr_status}]`)
  - `ctx.rollup`: current `HierarchyRollup` dictionary (`direct_children`, `direct_done`, `total_leaves`, `done_leaves`, `percent_done`, `by_tier`, `by_state`)

3.2.3. **Supported hook functions:**
1. `def can_dispatch(ctx) -> dict | bool`:
   - Called during dispatch readiness evaluation. Returns `True` / `{"allow": True}` or `False` / `{"allow": False, "reason": "waiting for parent feature design PR to merge"}`.
2. `def on_child_state_change(ctx) -> list[dict]`:
   - Called when a direct or transitive child transitions state. May return declarative effects from a fixed safe vocabulary:
     - `{"effect": "comment", "body": "..."}`
     - `{"effect": "run_now", "reason": "all subtasks completed; run integration check"}`
     - `{"effect": "add_label", "label": "..."}` / `{"effect": "remove_label", "label": "..."}`
3. `def can_close(ctx) -> dict | bool`:
   - Called before `GoalChildrenClosed` (or auto-close) marks the item closed. Returns `True` or `{"allow": False, "reason": "..."}` to hold the item open until tier-specific exit criteria are met.

3.2.4. **Progressive codification (`tier-process-codifier`):**
- Grain ships a built-in template `.grain/templates/tier-process-codifier.yml` that a deployment owner or schedule can run against the config repository.
- The codifier agent inspects `settings/tiers.yml` (`prompt-fragment`), existing `scripts/tiers/<tier>.py` hooks, and recent task event histories, identifying deterministic rules written in prose (for example, *"do not start child tasks until the feature has at least one approved user story"* or *"require a `design/design/` PR before closing"*) and proposing a config-repo pull request that moves those checks into `scripts/tiers/<tier>.py` while simplifying `prompt-fragment`.

---

## 4. Dynamic `children-closed` goal and bottom-up `HierarchyRollup`

### 4.1 Dynamic `children-closed` goal (`GoalChildrenClosed`) {#WH-6}

4.1.1. `pkg/model/task_goal.go` MUST define `GoalChildrenClosed GoalKind = "children-closed"`.

4.1.2. **Default attachment:** When a task is created in a tier whose `WorkTierDefinition.DefaultGoal == GoalChildrenClosed` (`feature` and `user_story` by default) and the caller does not specify an explicit `Goal`, Grain MUST attach `TaskGoal{Kind: GoalChildrenClosed}`. Operators MAY change the goal to `GoalPersonCloses` (`person-closes`) or clear it at any time.

4.1.3. **Evaluation (`pkg/orchestrator/goals.go`):**
- Queries all direct children (`WHERE parent_id = ?`) and transitive descendants.
- Computes `Done` (direct children in `closed` state) and `Total` (all direct children), plus a human-readable `Summary` (e.g., `"2/3 user stories closed (7/9 tasks)"`).
- **Empty-children guard:** While `Total == 0`, `GoalChildrenClosed` MUST NOT be satisfied; `Summary` reports `"No child items yet"`.
- **Cascade completion:** Once `Total > 0`, `Done == Total`, no transitive descendant remains open, and the tier's `can_close(ctx)` hook (if any) returns `allow: True`, `GoalChildrenClosed` is satisfied and the orchestrator closes the parent item, recording a `TaskEvent` (`"all N child items closed"`).
- **Re-opening on new open child:** If a new open child is created or reparented under a `closed` item that carries `GoalChildrenClosed`, Grain MUST reopen the parent (`Observation.ClosedAt = nil`, recording a `TaskEvent`).

### 4.2 `HierarchyRollup` model and `Store.WorkTree` unification {#WH-7}

4.2.1. For every work item that has children (or belongs to a tier with `DefaultChildTier != ""`), Grain (`pkg/model/hierarchy.go`) MUST compute a bottom-up `HierarchyRollup`:

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
	DirectChildren int                     `json:"directChildren"`
	DirectDone     int                     `json:"directDone"`
	TotalLeaves    int                     `json:"totalLeaves"`
	DoneLeaves     int                     `json:"doneLeaves"`
	PercentDone    int                     `json:"percentDone"`
	ByTier         map[WorkTier]TierCounts `json:"byTier,omitempty"`
	ByState        StateBreakdown          `json:"byState"`
	NeedsAttention bool                    `json:"needsAttention"`
	Repos          []string                `json:"repos,omitempty"`
}
```

4.2.2. **Leaf-weighted `PercentDone`:** Every leaf descendant inside the subtree counts as 1 unit of work; a leaf is done when its state is `completed` or `closed` (`TreeStatusIsDone`). `PercentDone` MUST be `round(DoneLeaves * 100 / TotalLeaves)` (or `100` if the item itself is `closed` with no children, `0` if open with no children).

4.2.3. **`NeedsAttention`:** MUST be `true` whenever `ByState.Failed > 0 || ByState.AwaitingReply > 0`.

4.2.4. **Unifying `Store.WorkTree` (`pkg/model/work_tree.go`):** `WorkTree` MUST place nodes under their structural `ParentID` first, falling back to `LinkProposedBy` (`Task.Parent()`) only when `ParentID == ""`.

---

## 5. External surfaces (REST API, MCP tools, CLI, Grainfiles)

### 5.1 REST API (`pkg/ui`) {#WH-8}

5.1.1. **`ui.Task` wire shape (`pkg/ui/tasks.go`):**
- `tier`: `string` (normalized `WorkTier`, default `"task"`)
- `parentId`: `string` (`""` when root)
- `ancestors`: `[]AncestorRef` (`[{id, title, tier, state}]` ordered from root ancestor down to direct parent)
- `rollup`: `*HierarchyRollup` (populated when `directChildren > 0` or the tier defines a `defaultChildTier`)

5.1.2. **`POST /api/tasks` and `PATCH /api/tasks/{id}`:**
- Accept `tier` (`*string`) and `parentId` (`*string`).
- When `parentId` is set on creation and `repo` is omitted on a tier that requires a repository, inherit `Target` and `Base` from the nearest ancestor that declares one.
- Validate hierarchy constraints via `Store.ValidateHierarchy`, returning `400 Bad Request` on invalid tier containment or cycle.

5.1.3. **`POST /api/tasks/{id}/approve-children`:**
- Approves all currently `proposed` direct children (or entire subtree when `{"recursive": true}`) of task `{id}` that the caller is permitted to approve, returning `{approvedIds: [...]}`.

5.1.4. **`GET /api/tasks/{id}/tree` and `GET /api/tiers`:**
- `GET /api/tasks/{id}/tree` (`pkg/ui/work_tree.go`) returns the hierarchy rooted at `{id}`'s root ancestor with `tier`, `parentId`, and `rollup` on every `WorkTreeNode`.
- `GET /api/tiers` returns the active ordered `[]WorkTierDefinition` catalogue for UI badges, pickers, and filters.

### 5.2 Agent MCP tools, CLI, and `.task.grain.md` Grainfiles {#WH-9}

5.2.1. **MCP `propose_task` (`pkg/mcp/task_tools.go`, `pkg/ui/agent_api_tools.go`):**
- Accepts optional `tier` (`string`) and `parent_id` (`string`).
- **Default child tier & parent inference:** When an agent running on parent item `P` calls `propose_task` without `parent_id`:
  - If `P`'s tier definition has a non-empty `DefaultChildTier` (e.g., `feature → user_story`, `user_story → task`) and the caller omits `tier` (or passes a tier allowed under `P.Tier`), `parent_id` defaults to `P.ID` and `tier` defaults to `P.TierDef.DefaultChildTier`.
  - If `P.Tier == TierTask` and the caller explicitly passes `tier: "subtask"`, `parent_id` defaults to `P.ID`.

5.2.2. **MCP `read_grain_task` and `list_grain_tasks`:**
- Include `tier`, `parent_id`, `ancestors`, direct `children` summaries, and `rollup`.
- `list_grain_tasks` accepts optional `tier` and `parent_id` filters.

5.2.3. **Host & sandbox CLIs (`cmd/grain`, `cmd/grain-sandbox`):**
- `grain create` and `grain task propose` accept `-tier` / `--tier` and `-parent` / `--parent`.
- `grain update <id>` accepts `-tier` and `-parent` (pass `-parent ""` to detach to root).

5.2.4. **Task Grainfiles (`.task.grain.md`, `pkg/grainfile`):**
- Support optional frontmatter fields `tier: <tier-id>` and `parent: <task-id or relative .task.grain.md path>` so multi-tier hierarchies can be committed and imported together.

---

## 6. Web UI workspace, boards, and filters

### 6.1 Hierarchy breadcrumbs and dynamic tier badges (`DetailOverlay.jsx`, `TierBadge.jsx`) {#WH-10}

6.1.1. **`TierBadge.jsx`:** Renders a compact badge driven by the active `WorkTierDefinition` (`badgeLabel` and `color` palette: `indigo` for `Feature`, `teal` for `Story`, `slate` for `Task`, `muted` for `Subtask`, plus custom tier colors).

6.1.2. **Breadcrumb header and properties in `DetailOverlay.jsx`:**
- Above the task title in `DetailOverlay`, render the `ancestors` chain (`Feature #10: Unified Auth › Story #14: OAuth2 PKCE Flow`) with each ancestor's `TierBadge`, title, and `StateDot`, clickable to navigate in place.
- In `Declared.jsx`, display **Tier** (editable selector populated from `/api/tiers`) and **Parent** (searchable picker filtered to items whose tier is in `AllowedParents`).

### 6.2 Work Breakdown & co-planning workspace (`WorkBreakdown.jsx`, `DetailOverlay.jsx`) {#WH-11}

6.2.1. **Placement above discussions:** On any item that has children or belongs to a container tier (`DefaultChildTier != ""`), `DetailOverlay.jsx` MUST render a **Work Breakdown** section (`WorkBreakdown.jsx`) in the main column directly between the description (`body`) and the discussion/chat stream (`TaskChat.jsx` / `Timeline.jsx`).

6.2.2. **Workspace controls:**
- **Rollup progress bar and counters:** Segmented horizontal bar colored by descendant state (`Done`, `Awaiting Submit`, `Running`, `Awaiting Reply`, `Queued`, `Blocked`, `Failed`, `Proposed`) with a tier/state summary strip (`75% complete · 3/4 Stories · 9/12 Tasks · 1 awaiting reply`).
- **Interactive child tree:** Expandable tree of descendants showing `TierBadge`, ID, title, target repo chip, state badge, PR link, and nested rollup mini-bar.
- **Inline child composer (`+ Add <DefaultChildTier>`):** Quick-create row pre-populated with `parentId = t.id` and `tier = tierDef.defaultChildTier`, plus a button to open `NewTaskOverlay` pre-seeded with the parent.
- **One-click batch approval:** When proposed children exist under the item, a banner button **Approve all N proposed children** invokes `POST /api/tasks/{id}/approve-children`.

### 6.3 Boards, filters, tree view, and Gantt monitoring (`taskAttributes.js`, `TaskTree.jsx`, `GanttChart.jsx`) {#WH-12}

6.3.1. **Query attributes (`ui/src/taskAttributes.js`):**
- `tier` (aliases `level`, `workTier`): filters/sorts by any configured tier ID (`feature`, `user_story` / alias `story`, `task`, `subtask`, or custom tier IDs).
- `parent`: filters by direct `parentId` (`parent:14` or `parent:none`).
- `feature`: filters to a Feature and all of its transitive descendants (`feature:10`).
- `story`: filters to a User Story and all of its transitive descendants (`story:14`).
- `progress`: numeric attribute (`0..100`) reading `task.rollup?.percentDone` for filtering (`progress:lt:100`) and sorting (`-sort progress:asc`).

6.3.2. **Tree, List, Kanban, and Gantt views:**
- `forestFromTasks` (`ui/src/workTree.js`) nests nodes by `parentId` first, falling back to `generatedFrom` (`LinkProposedBy`) only when `parentId` is empty.
- `TaskTree.jsx` and `TaskTreeBoard.jsx` display each parent row's `TierBadge`, inline progress bar, `done/total` leaf counter, `needsAttention` chip, and tier-scope toggles (`All tiers`, `Features`, `Stories & above`).
- `TaskList.jsx` and `TaskBoard.jsx` render a compact ancestor breadcrumb pill (`#10 Unified Auth › #14 PKCE`) and, on container items, a compact rollup progress bar (`75% · 9/12`).
- `GanttChart.jsx` groups and indents bars by hierarchy depth, spanning each container bar from the earliest child `startedAt`/`createdAt` to the latest child `completedAt`/live run.
