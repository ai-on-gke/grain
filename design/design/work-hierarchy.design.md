# Work hierarchy: features, user stories, tasks, and subtasks

**Owner:** @AntonTyb

**The core rule:** every work item in Grain lives in the unified `task` table with a configurable `tier` (defaulting out of the box to `feature → user_story → task → subtask`) and an explicit structural `parent_id` distinct from `LinkProposedBy` run provenance; each tier's behavior is driven by a configurable tier definition, a natural-language prompt fragment, and optional tier lifecycle hooks (Python DSL) that teams or background agents can progressively codify from the prompt.

Status: **design, not implemented**. The normative specification—covering the `settings/tiers.yml` schema, SQL DDL, `PromptExtensionFor` composition, Python DSL hook contract, `HierarchyRollup` wire shapes, REST/MCP/CLI/Grainfile surfaces, and UI components—is [`work-hierarchy.spec.md`](../spec/work-hierarchy/work-hierarchy.spec.md). Where this design document and the specification disagree, the specification wins.

## Why

Grain today models every unit of work as a flat `model.Task` (`pkg/model/task.go`) organized on saved boards (`pkg/model/board.go`). While `Task` supports provenance links (`LinkProposedBy`, rendered by `Store.WorkTree` in `pkg/model/work_tree.go`), provenance only records *which agent run called `propose_task`*—not how work is intentionally structured, discussed, or tracked toward larger deliverables.

In practice, teams need a structured work breakdown hierarchy—by default **`Feature → User Story → Task → Subtask`**—for three reasons:

1. **Scoped planning and discussions.** A **Feature** captures a multi-story initiative (problem statement, architecture, cross-repository scope), drives design docs (`design/design/*.design.md`), and hosts high-level planning discussions with humans and agents. A **User Story** captures user-facing value, acceptance criteria, and normative specifications (`design/spec/**`) and hosts story-level decomposition discussions. A **Task** is an actionable engineering change (typically one pull request or investigation), and a **Subtask** is a granular step within a task.
2. **Progress monitoring toward larger pieces of work.** Operators need to see at a glance how far along a Feature or User Story is (`2/4 user stories · 7/12 tasks · 15/18 subtasks (78%)`), which stories or tasks are currently running, awaiting human reply, blocked, or failed, and have parent stories and features close automatically as their child work completes.
3. **Out-of-the-box ergonomics with configurable team processes.** Issue trackers and work-management systems (such as Buganizer and Linear) work best when they provide an opinionated, zero-config hierarchy out of the box while allowing teams to customize tier names, containment rules, and per-tier agent workflows without inventing a heavy, rigid workflow language upfront.

## What exists to build on

| Piece | Where | What it already does and where the gap is |
| --- | --- | --- |
| Unified `task` table and derived `task_state` view | `pkg/model/task.go`, `pkg/model/state.go`, `pkg/model/schema.go` | Every work item already gets comments (`task_comment`), interactive agent chat (`Task.Interactive`), attachments, custom fields, runs, goals (`task_goal`), and audit events (`task_event`). **Gap:** No `tier` or structural `parent_id` column exists on `task` or `template`. |
| Task kind badges | `pkg/model/task_kinds.go` (`TaskKindTags`) | Derives runtime/origin badges (`port`, `repair`, `session`, `proposal`, `durable`, `imported`). **Gap:** Orthogonal to work-breakdown tier (`feature`, `user_story`, `task`, `subtask`). |
| Provenance trees vs. `LinkChildOf` | `pkg/model/work_origin.go`, `pkg/model/work_tree.go`, `pkg/model/task.go` | `Task.Parent()` and `ui/src/workTree.js` read `LinkProposedBy` (`generatedFrom`), which only reflects run provenance. `LinkChildOf` exists in `pkg/model/task.go`, but `LinkChildOf.Blocks()` blocks whoever *holds* the link (`task_blocked` in `pkg/model/schema.go`), so placing `LinkChildOf` on a child pointing to its parent blocks the child on the parent (`tests/e2e/e2e_test.go`). **Gap:** Structural parent–child containment needs a dedicated `parent_id` column on `task` independent of `LinkProposedBy` and `task_blocked`. |
| Standing prompt extensions | `pkg/model/prompt_extension.go`, `pkg/orchestrator/run.go` (`resolvePromptExtension`) | Composes deployment-wide (`Config.PromptExtension`), repo-wide (`RepoConfig.PromptExtension` + `.grain/settings.yml`), and per-task (`Task.PromptExtension`) instructions. **Gap:** No per-tier prompt fragment layer exists to tell an agent how to handle a `feature` (write a design doc & propose stories) versus a `user_story` (write a spec & propose tasks) versus a `task` (implement & open a code PR). |
| Dispatch readiness (`task_ready`) & daemon-checked goals | `pkg/model/schema.go`, `pkg/dispatch/dispatch.go`, `pkg/model/task_goal.go`, `pkg/orchestrator/goals.go` | Every approved non-deferred task in `queued` state is immediately dispatched to a coding sandbox targeting a single repository, and `GoalTasksClosed` (`tasks-closed`) only tracks a static list of task IDs. **Gap:** Container tiers need configurable dispatch modes, optional repo-less planning workspaces, and a dynamic `children-closed` goal (`GoalChildrenClosed`). |
| UI detail pane, boards, and filters | `ui/src/components/DetailOverlay.jsx`, `ui/src/components/detail/WorkTree.jsx`, `ui/src/taskAttributes.js`, `ui/src/workTree.js` | `DetailOverlay` supports discussions and interactive `TaskChat`, while `WorkTree.jsx` hides provenance inside a collapsed disclosure. **Gap:** No hierarchy breadcrumb bar, no inline child breakdown/composer above discussions, no rollup progress bars on cards/trees/Gantt views, and no `tier`, `parent`, `feature`, or `story` query filters. |

## Decisions at a glance

1. **One `task` table for all tiers.** Features, user stories, tasks, subtasks (and any custom tiers a deployment configures) remain rows in `task` distinguished by `Task.Tier` (default `"task"`). Every tier shares comments, interactive agent sessions, attachments, links, custom fields, goals, and `/tasks/{id}` deep links.
2. **Explicit `parent_id` on `task`, separate from `LinkProposedBy`.** Structural containment is stored in `task.parent_id` (at most one parent per item, cycle-free, validated against the active tier catalogue), while `LinkProposedBy` continues to record which agent run proposed a task.
3. **Configurable tier catalogue (`settings/tiers.yml`) with an opinionated 4-tier default (`DefaultWorkTiers`).** Out of the box, Grain provides `feature → user_story → task → subtask` (aligned with Buganizer/Linear best practices: fixed primitive mechanics for parent trees, rollups, and states, paired with configurable tier names, allowed parent tiers, default child tiers, default goals, dispatch modes, and prompt fragments).
4. **Per-tier prompt fragments (`promptFragment`).** Each tier carries a standing prompt fragment injected into `resolvePromptExtension` between the deployment layer and the repository layer. Instead of hardcoding what an agent does at each tier, the tier's prompt fragment instructs the agent on its role—for example, directing a `feature` run to draft or update a design doc under `design/design/` and decompose into `user_story` proposals, or directing a `user_story` run to author a spec under `design/spec/` and file child `task` items.
5. **Progressive process codification: from prompt fragments to tier Python DSL hooks (`scripts/tiers/<tier>.py`).** Teams define their workflow initially through tier states and natural-language prompt fragments rather than a complex upfront workflow language. As parts of a tier's process become clear and repeatable (when a tier item may dispatch, how parent/child states interlock, or what conditions must hold before closing), an operator or a background process-codifier agent can extract those unambiguous rules into small, deterministic Python DSL hooks (`can_dispatch`, `on_child_state_change`, `can_close`) while leaving nuanced judgment calls in the tier's prompt fragment.
6. **Configurable container dispatch & dynamic `children-closed` goal.** Each tier configures its `dispatchMode` (`auto`, `interactive_or_manual`, or `manual_only`) and `defaultGoal` (`children-closed`, `person-closes`, or `none`). By default, `feature` and `user_story` use `interactive_or_manual` dispatch (running an agent session when in an interactive planning chat turn or when an operator clicks **Run now**, or `auto` if a team configures automatic design-doc generation) and `GoalChildrenClosed` (`children-closed`), closing automatically once at least one child exists and all children close.
7. **Recursive bottom-up `HierarchyRollup`.** Every item with children exposes tier counts (`byTier`), state breakdown (`byState`), leaf-weighted completion percentage (`percentDone`), distinct target repositories (`repos`), and bubbled-up attention flags (`needsAttention`).

---

## Architecture

### 1. Configurable tiers with Buganizer-style defaults (`settings/tiers.yml`)

Task management tools face a classic tension between zero-config usability and organizational flexibility. Following the Buganizer / Linear model:

- **Fixed core mechanics:** Structural single-parent trees (`parent_id`), cycle prevention, bottom-up leaf-weighted rollups (`HierarchyRollup`), dynamic `children-closed` goal evaluation, and prompt-fragment/hook injection are built-in engine primitives.
- **Configurable tier catalogue:** The set of tiers, their display names/badges, parent-child containment graph (`allowedParents`, `defaultChildTier`), repository-binding requirement (`allowRepoLess`), default dispatch mode (`dispatchMode`), default completion goal (`defaultGoal`), natural-language `promptFragment`, and optional lifecycle script (`scriptPath`) are declarative settings stored in `tier_config` and projected to `settings/tiers.yml` in the config repository (`design/design/settings.design.md`).

When a deployment has not customized `settings/tiers.yml`, Grain seeds `DefaultWorkTiers`:

| Tier ID | Badge | Allowed parents | Default child tier | Repo binding | Default `dispatchMode` | Default goal | Default agent role (`promptFragment`) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `feature` | `Feature` (indigo) | `""` (root only) | `user_story` | Optional (`allowRepoLess: true`) | `interactive_or_manual` | `children-closed` | Scope the initiative across repositories, draft or update a high-level design doc in `design/design/<slug>.design.md`, and propose child `user_story` (or `task`) items via `propose_task`. Do not write production code directly. |
| `user_story` | `Story` (teal) | `""`, `feature` | `task` | Optional (`allowRepoLess: true`) | `interactive_or_manual` | `children-closed` | Define user-facing acceptance criteria, draft or update the normative spec in `design/spec/<slug>/<slug>.spec.md`, and decompose the story into PR-sized child `task` items with `depends_on` ordering. |
| `task` | `Task` (slate) | `""`, `user_story`, `feature` | `subtask` | Required | `auto` | *(PR merge / standard)* | Implement and verify the scoped change on a branch, open a pull request, and optionally break complex steps into `subtask` items. |
| `subtask` | `Subtask` (muted) | `task` (required) | — | Required (inherits parent) | `auto` | *(PR merge / standard)* | Execute the focused step or verification and close or merge so the parent `task` can complete. |

### 2. Per-tier prompt fragments and progressive codification into Python DSL

Rather than inventing a heavyweight workflow language or baking rigid tier rules into SQL, a team's development process is expressed through **tiers + states + prompt fragments**, with a smooth path to codify deterministic rules into Python DSL hooks over time:

```text
Stage 1: Natural-language prompt fragment (settings/tiers.yml)
  "For a feature, collaborate on design/design/<slug>.design.md and propose user stories..."
        │
        │  Background process-codifier agent (or operator) observes repeated patterns
        ▼
Stage 2: Hybrid (Tier Python DSL hook in scripts/tiers/<tier>.py + thinner prompt fragment)
  - Deterministic rules (can_dispatch, on_child_state_change, can_close) run as sandboxed Python DSL
  - Ambiguous / creative instructions stay in promptFragment
```

1. **Prompt fragment composition (`PromptExtensionFor`):**
   - `model.PromptExtensionFor` (`pkg/model/prompt_extension.go`) and `orchestrator.resolvePromptExtension` (`pkg/orchestrator/run.go`) gain the tier layer:
     1. Deployment-wide instructions (`Config.PromptExtension`)
     2. **Tier prompt fragment** (`TierConfig.PromptFragment` for `task.Tier`)
     3. Repository instructions (`RepoConfig.PromptExtension`, `.grain/settings.yml` `prompt-extension`, and optional repo-local `.grain/settings.yml` `tier-prompts[<tier>]`)
     4. Per-task override (`Task.PromptExtension`, which replaces the standing layers when set)
   - Because `.grain/settings.yml` may already declare `prompt-extension` under the authority rule in `pkg/declschema/declschema.go` (*"changes what a repository's own work does, and never what that work is permitted to do"*), a repository can also append repo-specific instructions per tier (e.g., telling `feature` runs in that repository which folder holds its design docs) without altering deployment authority or capabilities.
2. **Tier lifecycle hooks / Python DSL (`scripts/tiers/<tier>.py`):**
   - Each tier in `settings/tiers.yml` may optionally reference `script: scripts/tiers/<tier>.py` (executed in an isolated, deterministic Starlark / restricted-Python runtime with no network or secret access, receiving a read-only snapshot of the item, its ancestors, its children, its `HierarchyRollup`, and linked PR/file metadata).
   - Supported lifecycle entrypoints:
     - `can_dispatch(ctx) -> Decision`: decides whether a queued item of this tier should dispatch a sandbox run now or wait (e.g., waiting until a parent Feature's design PR has merged, or allowing `interactive_or_manual` items to run when triggered).
     - `on_child_state_change(ctx) -> list[Effect]`: reacts when a child item changes state (e.g., automatically unblocking or queuing an integration verification run when all subtasks complete).
     - `can_close(ctx) -> Decision`: enforces tier-specific closing invariants on top of `GoalChildrenClosed` (e.g., requiring a linked design doc PR or qualification suite to be green before a `feature` or `user_story` closes).
3. **Background process codification:**
   - Operators can start with 100% prompt-driven tiers (`promptFragment` only, no `script`).
   - A built-in template / background agent (`tier-process-codifier`) can inspect a tier's `promptFragment` alongside completed task histories and propose a config-repo PR that moves unambiguous procedural rules (such as gating conditions or child-creation checks) into `scripts/tiers/<tier>.py` while trimming the corresponding boilerplate from `promptFragment`.

### 3. Container dispatch, `GoalChildrenClosed`, and `HierarchyRollup`

- **Container dispatch (`WH-3`):** Whether a tier auto-dispatches headless sessions on approval is governed by its configured `dispatchMode` (and optional `can_dispatch` Python hook) rather than hardcoded tier names:
  - `auto` (default for `task` and `subtask`): dispatches as soon as approved and unblocked.
  - `interactive_or_manual` (default for `feature` and `user_story`): approving the item activates it as a planning/tracking container without queuing an automatic headless coding run; a sandbox session runs whenever `t.interactive = 1` with a pending user message or when an operator clicks **Run now** (`o.run_now_at IS NOT NULL`). Teams that want every newly approved `feature` to automatically spin up a design-doc drafting run simply set `dispatchMode: auto` on `feature` in `settings/tiers.yml`.
  - `manual_only`: dispatches only on explicit **Run now** or interactive chat.
- **Dynamic `children-closed` goal (`GoalChildrenClosed`, `WH-5`):**
  - Attached by default to tiers whose `defaultGoal` is `children-closed` (`feature` and `user_story`).
  - Remains unsatisfied while `DirectChildren == 0` (`"No child items yet"`), and closes the parent automatically once `DirectChildren > 0` and all direct and transitive descendants are `closed` (cascading bottom-up from `subtask → task → user_story → feature`). Adding a new open child under a closed container with `GoalChildrenClosed` reopens the container automatically.
- **Recursive `HierarchyRollup` (`WH-6`):**
  - Computed bottom-up for every item with children (or container tier): `directChildren`, `directDone`, leaf-weighted `totalLeaves` / `doneLeaves` / `percentDone`, `byTier` counts keyed by configured tier ID, `byState` counts, `needsAttention` (`failed > 0 || awaitingReply > 0`), and distinct `repos`.
  - `Store.WorkTree` places nodes under `ParentID` first and falls back to `LinkProposedBy` only when `ParentID == ""`.

### 4. Surfaces and UI workspace

The normative details for all external surfaces live in [`work-hierarchy.spec.md`](../spec/work-hierarchy/work-hierarchy.spec.md):

- **REST, MCP, CLI, and Grainfiles (`WH-7`, `WH-8`):**
  - `/api/tasks` exposes `tier`, `parentId`, `ancestors`, and `rollup`; `/api/tasks/{id}/approve-children` batch-approves proposed children after planning; `/api/tiers` returns the active tier catalogue.
  - `propose_task` infers `parent_id = P.ID` and `tier = parentTier.defaultChildTier` when called from a parent tier without explicit overrides.
  - `grain create`, `grain update`, `grain task propose`, and `.task.grain.md` frontmatter accept `tier` and `parent`.
- **UI planning & monitoring (`WH-9` – `WH-11`):**
  - `TierBadge.jsx` renders badges from the configured tier catalogue (`badgeLabel`, `color`).
  - `DetailOverlay.jsx` displays the clickable ancestor breadcrumb bar above the title and places the **Work Breakdown** panel (`WorkBreakdown.jsx`) directly above the discussion/chat stream (`TaskChat.jsx`), featuring a segmented state-rollup progress bar, expandable child tree, inline child composer (`+ Add <defaultChildTier>`), and one-click **Approve all N proposed children** banner.
  - Boards, `TaskTree.jsx`, and `GanttChart.jsx` support `tier`, `parent`, `feature`, `story`, and `progress` narrowing attributes and render compact rollup progress bars.

---

## Staged implementation plan

| Stage | Scope | Spec sections | Depends on |
| --- | --- | --- | --- |
| **WH-S1: Configurable tier catalogue, store schema, and hierarchy validation** | `WorkTier`, `WorkTierDefinition`, `DefaultWorkTiers`, `settings/tiers.yml` projection, `task.tier`, `task.parent_id`, `template.tier` migration, and `Store.ValidateHierarchy`. | `WH-1`, `WH-2` | — |
| **WH-S2: Per-tier prompt fragments, dispatch modes, and `GoalChildrenClosed`** | `PromptExtensionFor` tier layer + `.grain/settings.yml` `tier-prompts`, configurable `dispatchMode` in `task_ready`/`Store.Ready*`, and `GoalChildrenClosed` cascade/reopen in `pkg/orchestrator/goals.go`. | `WH-3`, `WH-5` | WH-S1 |
| **WH-S3: Tier Python DSL hooks and background process codifier** | Deterministic `scripts/tiers/<tier>.py` runner (`can_dispatch`, `on_child_state_change`, `can_close`) and `tier-process-codifier` template/workflow for extracting prompt rules into code. | `WH-4` | WH-S2 |
| **WH-S4: `HierarchyRollup` engine and REST API** | `pkg/model/hierarchy.go`, structural `Store.WorkTree` precedence, `ui.Task` wire fields (`tier`, `parentId`, `ancestors`, `rollup`), `/api/tiers`, and `POST /api/tasks/{id}/approve-children`. | `WH-6`, `WH-7` | WH-S1, WH-S2 |
| **WH-S5: Agent MCP tools, CLI, and Grainfiles** | `propose_task` / `read_grain_task` / `list_grain_tasks` (`tier`, `parent_id`, `defaultChildTier` inference), `grain` and `grain-sandbox` CLI flags (`-tier`, `-parent`), and `.task.grain.md` frontmatter. | `WH-8` | WH-S4 |
| **WH-S6: UI planning & discussion workspace** | Dynamic `TierBadge.jsx`, ancestor breadcrumbs in `DetailOverlay.jsx`, `WorkBreakdown.jsx` above `TaskChat` with segmented rollup bar, inline child composer, and batch child approval. | `WH-9`, `WH-10` | WH-S4 |
| **WH-S7: UI progress monitoring, filters, and boards** | `tier`, `parent`, `feature`, `story`, and `progress` in `taskAttributes.js`; structural hierarchy and rollup bars in `TaskTree.jsx`, `TaskList.jsx`, `TaskBoard.jsx`, and `GanttChart.jsx`. | `WH-11`, `WH-12` | WH-S6 |
