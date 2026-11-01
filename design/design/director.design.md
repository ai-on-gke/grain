# Director workflows and trust boundaries

Grain ships one standing interactive console session by default: the **director** (`model.DirectorSessionID = "director"`), pinned in the UI rail as the operator's persistent copilot for a single-tenant deployment. `docs/director.md` describes how console sessions are stored, reconciled, and kept warm today. This design defines **what workflows the director is expected to perform**, the **trust boundaries** around them, and the **lifecycle invariants** that keep it responsive without starving worker capacity.

It serves as the foundation for the companion specification `design/spec/director/director-capabilities.spec.md`, which derives the exact per-workflow capability grants, `api:*` scope presets, `CatalogueKindDirector` policy rules, and default envelope settings `director` ships with.

**The core rule:** the director **files work rather than doing it**. Its identity and operating rules live in reviewed code (`model.DirectorSessionBody`), not in a mutable settings form. It reads the deployment's configuration and runtime history, answers the operator concisely, runs bounded control-plane and hygiene actions through audited APIs, and turns repository changes, investigations, or long-running jobs into self-contained tasks (`propose_task`) that run in their own isolated, reviewed sandboxes.

## 1. Role of the director

The director (`ConsoleSessionTag("director")`) is an ordinary task with three standing-session exemptions enforced by `orchestrator.reconcileConsoleSessions`: it is filed approved so it is ready on first boot, parked back on its own conversation after each turn rather than completing, and never retired on a failure streak. Within the deployment's governance model, it plays three roles:

- **Operator copilot in the rail.** Advises the human sitting next to the queue in a few sentences, citing primary records and root-relative UI routes (`/tasks/<id>`, `/settings/...`, `/system/...`) that work behind any identity-aware or reverse proxy.
- **Root config & queue coordinator.** Acts as the deployment-wide counterpart to per-repository managers: inspects `grain/config` (`/home/debian/grain-state`), triages queues and merge gates across repositories, and coordinates multi-task flows.
- **Task author, not code author.** Never treats its standing sandbox as a workbench for product repositories. Anything that edits application code, runs a test suite, or spends sustained compute is proposed as a task with explicit `repo`, `depends_on`, and capability bounds so it goes through standard dispatch, CI, and review gates.

## 2. Core workflows

| Workflow | Primary surfaces | Key guardrail |
|---|---|---|
| **W1** Bootstrap & onboarding | `bootstrap-playbooks`, `deployment-health`, `request_secret` | Secrets write straight to `<data-dir>/secrets`; never read back |
| **W2** Live triage & observability | `self-debug` (`task://`), `ci://`, `api:ci-read`, `grain-log`, `grain metrics` | Read-only diagnostics; no product-repo edits in session |
| **W3** Decomposition & queue hygiene | `propose_task`, `api:task-admin`, `api:dispatch-control` | `TasksApproved` off by default; `hold_for_approval` & `expect` rails |
| **W4** Config & capability hygiene | `/home/debian/grain-state` (`grain/config`), `propose_config_change` | Human approval via `pkg/trustboundary` & `settings_locked` |
| **W5** CI, merge queue & releases | `ci:write`, `merge:write`, `api:release-manager`, `self-upgrade` | Never `autoMerge`, `submit`, or `merge:force`; honours upgrade holds |

### W1 — Bootstrap and onboarding

Guides first-time bring-up and incremental expansion of a deployment (`bootstrap-playbooks`):

- **Infrastructure readiness.** Verifies model backend connections, agent CLI placement, `grain hostd` connectivity, and sandbox backend health (`granule` / Kubernetes pods, resource ceilings) via `deployment-health` and read scopes.
- **Repository onboarding.** Configures target repositories, branch bases, merge strategies, and CI modes (`github`, `grain`, or `none`), and wires repository git credentials by reference.
- **Write-only credential intake.** Requests missing API keys, tokens, or cloud credentials with `request_secret` (always supplying a one-line `summary`), routing secret bytes straight from the operator's browser into `<data-dir>/secrets` without entering the conversation or prompt.

### W2 — Live triage, debugging, and observability

Answers "why did task X fail?", "why is the merge queue stuck?", and "how full or slow has this deployment been?" from recorded state rather than guesswork:

- **Task and run forensics (`self-debug`).** Reads task records, attempt outcomes, rendered prompts, and tool-by-tool transcripts (`grain task list|read|prompt|transcript`, `task://`) to separate prompt gaps, agent mistakes, capability refusals, and sandbox setup faults.
- **CI and merge-queue diagnostics.** Reads CI runs, step logs, shared cross-branch infrastructure faults, and test flake history (`ci://`, `api:ci-read`, `grain ci list|log|test-history`), alongside held merge-queue batches and security or review gates (`grain merge-queue`).
- **Controller and fleet telemetry.** Inspects daemon journals (`grain-log`), sandbox utilization, queue wait times, prompt token budgets, and tool-error censuses (`grain metrics`, `deployment-health`).

### W3 — Work decomposition and queue management

Turns operator goals, design documents, or triage findings into structured backlog work and keeps the queue moving:

- **Decomposition into task graphs.** Files self-contained tasks via `propose_task`, wiring `repo` (explicitly targeting the owning repository), `depends_on` for staged chains, `supersedes` to replace undispatched duplicates, inherited or band-appropriate `priority`, and `hold_for_approval: true` whenever a proposal needs human inspection before running—even on deployments where `ConsoleSession.TasksApproved` (`QueuesProposals`) lets the director queue routine work directly.
- **Day-to-day queue hygiene.** With `api:task-admin` / `api:dispatch-control`, reorders priorities, pauses or resumes tasks and dispatch, stops or retries wedged tasks using `expect` state preconditions, sets or repairs task goals (`GoalDecl`), sends guidance to running task agents (`api_message_task_agent`), and closes obsolete tasks with an audited `reason`.

### W4 — Configuration and capability hygiene

Maintains the deployment's declarative state in `grain/config` (`docs/design/config-repo-settings.md`):

- **Reading configuration vs. runtime state.** Inspects `grain/config` (`settings/*.yml`, `tables/*.json`, `memory/**`) in `/home/debian/grain-state` (`prepareDirectorState`) alongside live bootstrap and runtime settings (`/api/config`), never confusing exported configuration with task or run history in the controller database.
- **Proposing validated config changes.** Authors updates to templates, schedules, suites, repo settings, standing instructions, `tables/api_scope_set.json`, and `settings/capabilities.yml` (`CatalogueKindDirector`), submitting them either as `grain/config` pull requests or inline via `propose_config_change`—which runs four-stage `staterepo` validation, pins `headSha`, and parks on an in-chat approval card.
- **Grant cleanup.** Audits capability usage and detaches retired or over-broad capability grants across repositories, templates, schedules, and queued tasks so stale references neither block dispatch nor leak reach.

### W5 — CI, merge queue, and release operations

Keeps delivery pipelines healthy and, when explicitly authorized, coordinates releases:

- **Pipeline unblocking.** Cancels superseded or wedged CI runs, re-triggers failed CI jobs once transient infrastructure faults clear (`ci:write`), and puts stalled pull requests back into the merge queue (`grain retry-merge`, `merge:write`) or files targeted base-branch repair tasks when trunk is red.
- **Release and upgrade coordination.** Inspects trunk and release-train health (`releases:read`), and—when granted opt-in release or upgrade scopes (`api:release-manager`, `self-upgrade`)—runs qualification schedules, cuts or promotes release candidates, and initiates drained controller upgrades while honouring active upgrade holds.

## 3. Explicit non-goals and trust boundaries

Regardless of operator prompts or untrusted repository text read during triage, four boundaries are invariant:

1. **Never reads secret values.** No tool or namespace route exposes plaintext secrets from `<data-dir>/secrets` to the director. Credentials enter only via `request_secret` or bootstrap UI forms and are referenced solely as `$secret:<name>` handles.
2. **Never enables `autoMerge` or lands unreviewed changes.** Letting a change merge unreviewed is always a person's decision. `agentAPIGate` refuses agent attempts to set `autoMerge`, call `POST /api/tasks/{id}/submit`, or invoke `merge:force`. Enabling `TasksApproved` lets the director queue proposals (approved as `ConsoleSessionPrincipal`) without granting `autoMerge` or allowing capability/repository escalation beyond the director's own grants (`ConsoleSessionGrants`).
3. **Never bypasses `grain/config` human-approval gates.** When `settings_locked` is enabled, non-bootstrap UI/API mutations are refused in favour of `grain/config`. Every `grain/config` change—especially privileged tables and declarative settings (`StatePrivilege`, `NeverAutoMergePaths`)—is gated by `pkg/trustboundary.ClassifyState` (`RequiresHuman: true` by default, with `settings/config-policy.yml` read strictly from `main`). Neither PRs nor `propose_config_change` can land without a human approval on the exact validated commit.
4. **Never executes unguarded host commands or self-escalates.** The sandbox holds only a per-attempt token barred from `model_only` control tools and self-mutation (`selfMutableTaskRoutes`). The director cannot grant itself capabilities, modify another standing session, or run arbitrary commands on the controller host without explicit operator-gated capability boundaries: `self-repair` is off by default, `grain hostd` runs only static allowlisted binaries, and dangerous `api:*` scopes require a human grant plus a logged `reason` on every call.

## 4. Lifecycle, responsiveness, and worker-slot discipline

An operator opening the director rail expects an immediate reply even when `max_workers` is saturated or the pipeline is paused, without the session hoarding worker slots while idle or waiting:

- **Priority admission (`durable` / console-session dispatch).** `ConsoleSessionGrants` always attaches `durable` (`DurableCapability`), which implies `priority-dispatch` (`docs/design/durable-tasks.md`). Director turns are admitted outside `max_workers` (under `max_durable_runs`), bypass pipeline pauses and red-base repair holds, and survive controller restarts via session adoption or CLI `--resume`.
- **Releasing the active slot when waiting or idle.** Priority admission requires strict slot discipline: the director never polls inside a live run waiting on asynchronous tasks or CI runs. Once it replies, files work, or asks for input, it records standing context via `grain handover write` when needed (carried across `"Start a fresh chat"` resets by `CarryConsoleSessionHandover`) and releases its active run by calling `ask_question` (`request_secret`, `propose_config_change`) or ending its turn to rest on `person-closes` (`GoalPersonCloses`).
- **Two-layer warm idle pool.** Between turns, `continueSessionTurns` and `session_sandboxes.go` keep the CLI session and sandbox warm up to `session_sandbox_idle_ttl_ms` (default `30m`) for sub-second follow-ups. Held VMs appear as `"idle session"` on **System → Sandboxes** and are evicted oldest-first whenever queued worker runs need sandbox capacity.

## Companion specification

`design/spec/director/director-capabilities.spec.md` maps `W1`–`W5` and the boundaries above onto concrete `DefaultConsoleSessions` fields, shipped vs. opt-in capabilities, `api:*` scope presets, `grain/config` read/write rules, and `CatalogueKindDirector` restrictions in `settings/capabilities.yml`.
