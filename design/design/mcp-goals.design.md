# Extensible MCP goals for completion and scheduling

**Owner:** @ai-on-gke/ai-factory-root-owners

**Status:** proposed architecture; documentation only, not implemented. Implementation phases below are dependencies for later work, not authorization to implement, deploy, or file follow-up tasks.

**The core rule:** Grain alone owns task lifecycle and dispatch. Explicitly attached, pinned, read-only MCP goal evaluators explain whether completion obligations are satisfied and what useful work or durable waits remain; they never acquire action authority from their names, results, or persisted state.

The normative contract is [MCP goals specification](../spec/mcp-goals/mcp-goals.spec.md), with [wire schemas](../spec/mcp-goals/wire.schema.json). This design explains the boundaries and migration. The specification governs the proposed goal protocol; existing behavior remains governed by its current contracts until a task explicitly migrates.

## Problem and verified baseline

Inspected `origin/main` at `5a5189ae4a11afd30e142a467fd0ef7cd34cd4a9` (2026-11-16). The working branch started at that same commit. These are source observations, not claims that all older designs are fully implemented:

| Authority or observation today | Verified source | Consequence for migration |
|---|---|---|
| Run endings infer reports, nothing-needed, questions, branches, reviews and completion | [`finish.go`](../../pkg/orchestrator/finish.go), especially `processResultFrom`, `restOnGoal`, `finishNothingNeeded`, `finishAnswered` | Keep recording run/tool evidence; remove lifecycle writes only for migrated tasks. A plain final answer is not generic proof of completion. |
| Go computes state from observations, live run, streak, deferral and admission | [`state.go`](../../pkg/model/state.go), `StateOf`, `AwaitsAck`, `ClosesOnAnswer` | Keep run outcome separate from task outcome and replace precedence in both projections together. |
| SQL independently projects the same state | [`schema.go`](../../pkg/model/schema.go), `task_state`, and [`state_test.go`](../../pkg/model/state_test.go); PostgreSQL uses [`postgres/schema.go`](../../pkg/model/postgres/schema.go) | A protocol-only change cannot fix divergent Go/SQL state. Migration requires parity fixtures and transactional authority selection. |
| Explicit goals already override ordinary finish | [`taskgoals.go`](../../pkg/orchestrator/taskgoals.go), `checkGoal`, `reconcileTaskGoals`, `accountGoalProgress`, `meetGoal` | Preserve evidence collectors, close/rest and no-progress policy, replace the fixed kind switch behind an explicit authority flag. |
| Change synchronization, review and merge have their own facts and actions | [`sync.go`](../../pkg/orchestrator/sync.go), [`mergegates.go`](../../pkg/orchestrator/mergegates.go), [`humanreviewhold.go`](../../pkg/orchestrator/humanreviewhold.go), [`pkg/change`](../../pkg/change), [`pkg/ui`](../../pkg/ui) | Reviews, check runs, Submit, merge and CL submission remain authoritative evidence sources; provider assertions cannot replace them. |
| Wakes and dispatch resume work independently | [`wake.go`](../../pkg/orchestrator/wake.go), [`pkg/dispatch`](../../pkg/dispatch), [`wait_target.go`](../../pkg/model/wait_target.go) | Replace direct wake-to-agent behavior only after completion ownership is centralized. Keep message backstops and durable recovery. |

### Reconciliation with existing contracts

- [Durable tasks](durable-tasks.design.md), particularly DT-4, already says an agent ending a goal task's run does not end the task. Extend its fixed goal kinds to multiple explicit MCP obligations. Its pauses, durable admission, suspensions, idle watchdog, budgets, on-limit and close/rest behavior remain Grain policies. `person-closes` becomes a standing lifecycle policy with an explicit person-close event, rather than an evaluator forced to return unmet forever.
- [Task liveness](task-liveness.design.md) requires every wait to name its resource, driver and escalation. Goal waits add durable cursors, timers and reconciliation to that rule. A wait blocks its dependent work, not all work on the task.
- [Capabilities design](capabilities.design.md), [capability protocol](../spec/capabilities/capabilities.spec.md), and [unified grants](unified-grants.design.md) retain server identity, transport, configuration validation and least authority. `goal_*` is an additive discovery class alongside `mint_*`/`release_*`; it does not mint credentials or become a grant. Goal configuration uses the capability argument-schema subset. The enclosing versioned snapshot protocol has its own schema. Capability mint retry/refusal rules do not become lifecycle decisions for goal errors.
- [Merging](merging.design.md) and [contributions](../spec/contributions/contributions.spec.md) retain exact-head checks, ordinary/targeted/security reviews, human-only holds, explicit gate overrides and approver identity. `goal_merge_ready` reports action prerequisites; it cannot satisfy `goal_integrated`. Submit approves the scoped change; it does not clear findings or finish a review. No new queue ordering, base-tip retest, or review policy is introduced.
- [RBAC](../spec/rbac/rbac.spec.md), [director capabilities](../spec/director/director-capabilities.spec.md), and [operator attribution](../spec/operator-attribution/operator-attribution.spec.md) continue to govern every action. Requirement attachment, evaluation permission, snapshot reads, waiver, Submit and protected execution are separate checks. This proposal deliberately strengthens requirement removal beyond an ordinary metadata edit: agents cannot relax their own completion obligations.
- [Closing from a run](../../docs/closing-a-task-from-a-run.md) and [closed-task messages](../../docs/comments-and-closed-tasks.md) supply compatibility cases, not a new general self-close tool. Preserve report acknowledgment versus report-only child closure, existing-PR precedence over nothing-needed, and historical closed-task messages pending explicit reopening. Atomic close adds a stronger no-unprocessed-message fence.
- [Angular UI](angular-ui.design.md) and the [UI design book](ui-design-book.design.md) govern presentation: server-derived honest state, action on the evidence the person saw, scoped confirmation, accessibility and shared API. New goal UI goes in `ui-ng` only; API compatibility keeps classic readers working. This PR changes neither UI.

The merging design's own **Implementation gaps** section correctly distinguishes intended independent merging from current queue/recheck machinery. Source still has `mergerecheck.go`, lane ordering in `sync.go`, and effective rules in `mergerules.go`; this proposal does not claim those gaps have landed or remove them during compatibility import. Existing effective action prerequisites (including configured bypass policies and their authority) must be captured as versioned policy inputs, not silently replaced with the design's aspirational defaults. Resolving merge-policy gaps remains separate from this documentation-only proposal.

For concrete goal collectors, [`stage1repair.go`](../../pkg/orchestrator/stage1repair.go)'s `nightlyGreenReading` distinguishes verified default-branch push verdicts from ordinary nightly selection: it filters jobs since declaration, selects the newest applicable non-idle-skipped/non-canceled-unjudged run, and accepts success/skipped/neutral job conclusions. [`pipelinesteward.go`](../../pkg/orchestrator/pipelinesteward.go)'s `pipelineStewardQuiet` also checks branch health, stalled nonpaused tasks and merge blockers; it is not simply a notification count. Compatibility imports preserve these predicates and their source provenance before later authorized refinements.

The contributions spec describes `.github/CODEOWNERS` and triggers that are absent at this baseline; actual [`CODEOWNERS`](../../CODEOWNERS) assigns the root owners team. This document uses that actual owner and the prescribed design/spec locations and requirement IDs; it does not create enforcement or deployment configuration to repair unrelated drift.

## Architecture and boundaries {#MGD-1}

```mermaid
flowchart TD
  E[Authoritative collectors and user events] --> J[Durable evidence journal]
  J --> S[Versioned authorized snapshot]
  C[Pinned task contract] --> S
  S --> G[Pure MCP goal evaluators]
  G --> R[Validate responses and CAS state]
  R --> W[Persist waits and reconcile cursors]
  R --> L[Grain lifecycle transaction]
  R --> A[Arbitrate useful work]
  A --> T[Authorized agent tools / registered operations]
  T --> J
  L --> U[Task API and UI projection]
```

An attachment states an obligation. A grant permits a read or action. Neither implies the other. Grain can refuse to evaluate an attached provider until an authorized operator grants the bounded read scope. It shows the missing permission without granting it or dropping the goal. The provider is a trusted choice for interpretation within its pinned contract, but all its output remains untrusted data. Evidence authenticity, authorization, and closure fencing belong to Grain.

The task contract has stable attachment IDs, provider identity and artifact/version pins, goal and state/schema versions, canonical argument hash, child links, and lifecycle policy revision. There are no automatically required goals based solely on tool-name discovery. Grain can offer discovered tools in an editor, then record a separately authorized attachment. Defaults/templates resolve into explicit attachments at admission.

A snapshot represents all relevant authorized facts at a durable evidence revision, including required children, messages and action records. Bounded inline data plus snapshot-bound pages provide full relevant state without dumping transcripts or secrets. Missing necessary data yields unknown or a permission blocker, never a favorable assumption. All results in a decision bundle use the same snapshot. A changed head, contract, message watermark, permissions, child outcome or relevant external evidence invalidates the bundle.

Opaque state is an optimization for provider interpretation, not proof of an approval or CI verdict. Grain persists it with an expected revision, byte/depth bounds, a schema version and attachment identity. State migrations are explicit pure operations through a pinned compatible evaluator mode; recovery can discard state only if the provider declares replay from authoritative evidence safe. Otherwise the task rests visibly for migration or repair.

## Actions, waivers and interface {#MGD-2}

Evaluation does no mutation and has no write credentials. It may return work for an agent and typed requests to registered operations. Grain validates and arbitrates these, then uses the same authorized operation path available to an agent. If the agent lacks a tool or grant, the task offers a permission request; no controller-only privileged shortcut is required to make progress. A future executor optimization must use the same path, preconditions, idempotency and audit.

UI buttons contain a label, registered action ID, constrained input schema and confirmation requirements. Providers cannot send executable JavaScript, arbitrary URLs or an alternate approval mechanism. The server registry controls operation permissions, minimum confirmation, human-only restrictions and evidence scope. The server rechecks all of these at click time and records an immutable event before an idempotent operation is driven. A note is never approval; acknowledgment is receipt only.

A waiver is a Grain-owned, reasoned, actor-attributed exception to a named goal and scoped evidence/contract version. It is shown as **waived**, never **met**. A new head, changed requirement, revoked waiver or changed scoped evidence invalidates it. A waiver of a completion obligation cannot authorize merge, deployment, secret access or another protected action. Existing merge gate overrides stay separate registered operations with `merge:force`, exact-head scope, reason and confirmation.

The task detail shows each goal's result, summary, optional human text, evidence, blockers, waits and buttons. Agent prompts receive optional agent text and actionable work separately. Neither text field is machine authority. The task row has one primary state but exposes every blocker, so a running agent can coexist with a review wait and a permission need.

## Scheduling and closure {#MGD-3}

Grain evaluates when relevant facts or contracts change, not just when a displayed status changes. It coalesces dirty events per task, evaluates one snapshot bundle, commits validated state/results/subscriptions, then computes useful authorized work. Provider-authored summaries, state revisions and progress tokens do not recursively dirty their own evidence.

Arbitration deduplicates work by attachment, registered operation, target, input hash and precondition scope; merges identical requests across goals; honors dependencies and task priority; and serializes conflicting operations on shared resources. One task has at most one active agent run. Independent CI/review external operations can overlap when registered resource locks permit it. A waiting goal never globally vetoes independent useful work. Missing action permission blocks that action only. Pause, budget, admission and backoff can globally stop dispatch as lifecycle policies.

Subscriptions and snapshots share a durable cursor fence. Grain reads snapshot at cursor C, stores subscriptions with C and replays the journal after C, including events received during evaluation. Out-of-order and duplicate external deliveries are deduplicated and reconciled against authoritative source versions. Every wake causes an authoritative re-read and evaluator pass first. Polling where events are unavailable is bounded with backoff; reconciliation catches lost deliveries and expired leases. A timer can trigger reevaluation or escalation, never success.

No-progress accounting uses authoritative operation/evidence changes and processed messages, not provider summary/state churn. Repeated identical work, pending results without a viable wait, malformed responses and provider outages cannot dispatch agents repeatedly. They back off, open a named blocker and escalate after a persisted threshold. Independent work continues. An unmet goal with no safe remedy rests with an explanation; it does not imply useful agent work.

Closure is a transaction over the current contract, evidence revision, message watermark, result set, waiver set and operation ledger. Every required goal must be met or validly waived, all incoming user messages processed, and operations settled or explicitly dispositioned by Grain. If a live run needs stopping, Grain requests graceful stop and waits for settlement before retrying closure. A late push cannot be ignored by simply canceling a run. Retained historical completion evidence is immutable. Later regressions follow an explicit reopen or separate repair policy, never rewrite the old success silently.

Proposed tasks have no execution authority; admission approval fixes an executable contract. Active primary states are running, waiting, needs answer, needs permission and needs acknowledgment, with a separate readiness/scheduling reason for admitted runnable tasks awaiting capacity. Terminal outcomes are succeeded, canceled and failed. A standing person-closes session rests after satisfying current work and closes successfully only on an authorized person's close event; cancel records canceled rather than pretending the session achieved an outcome goal. Exhausted task retry policy may fail a task; an individual evaluator error cannot do so alone.

## Compatibility mapping {#MGD-4}

Names below are proposed examples, not built-in implicit attachments. Every configured task records its actual evaluator and arguments.

| Existing behavior | Proposed evaluator / prerequisite / policy | Compatibility and intentional improvement |
|---|---|---|
| Report / final response | `goal_report` cites durable artifact and processed-message coverage | Top-level report may require separate `goal_ack`; report-only child can omit ack as today. Receipt never approves contents. |
| `nothing_needed` | `goal_no_work` checks scoped no-change claim and run/branch evidence | Preserve pushed-work and already-open-PR precedence; a stale branch alone is not delivered work. An assertion cannot skip attached obligations. |
| Change delivered | `goal_change_delivered` cites branch/CL and expected artifacts | Delivery is not integration or merge permission. |
| Exact-head required checks | `goal_checks` reads repository's configured CI provider/check set | Preserve current-head identity; stale success cannot authorize merge. |
| Ordinary / targeted / security / path-trigger reviews | `goal_review` with pinned policy, scope, reviewer role and head/path digest | Preserve distinct gates and findings; security-sensitive new edits invalidate old scope. Reviewer task finishing with findings completes review work but does not pass merge prerequisites. |
| Operator Submit / human-review hold | Registered Submit/release/override actions plus `goal_approval` evidence | Submit records only its scoped human approval; cannot satisfy review findings. Protected hold rules remain. |
| PR merged / CL submitted | `goal_integrated` with PR or all scoped CL outcomes | Integration evidence satisfies outcome. `goal_merge_ready` only authorizes requesting merge through its protected operation. Closed unmerged is not integrated; unreadable CL is unknown. |
| `tasks-closed` | `goal_children` with explicit terminal-closure or success predicate | Import legacy `until=closed` literally; new required-outcome links default to success. Failed/canceled closure is not successful delivery. `until=merged` imports integration predicate. |
| Base green | `goal_base_green` references recorded branch CI confirmation | Preserve `BaseCICleared` non-reproduced failure as explicit evidence, not a provider inference or generic waiver. In-flight/broken evidence remains unmet/pending. |
| Publishing recovery | `goal_publishing_recovered` reads release health and supersession evidence | Preserve existing `Publishing` option semantics, including missing release row behavior, for compatibility imports; new contracts should require positive publication evidence and freshness. Superseded releases without publication do not demonstrate recovery. |
| Nightly green | `goal_nightly_green` cites configured nightly scopes and verdicts | Preserve existing selection/freshness predicate on import; revisions must make the expected nightly window explicit. |
| Pipeline healthy | `goal_pipeline_healthy` cites notification family/scope and steward quiet evidence | Preserve no-open-notifications **and** quiet condition, not just empty notification list. |
| Person closes | Standing close/rest policy; person-close event | Remains open across ordinary turns and healthy periods; no synthetic permanently unmet goal is needed. |
| Pause, budgets, keep-running, on-limit, close-versus-rest | Grain lifecycle policy | Not provider-defined success criteria; keep-running cannot silently bypass or remove goals. |

## Task examples {#MGD-5}

| Task | Explicit obligations | Work and wake behavior |
|---|---|---|
| Implementation | change delivered, current checks, reviews, scoped approval when required, integration | Request fixes while CI/review waits; request merge only when prerequisites and permission hold; close on observed integration. |
| Report/research | artifact/report coverage, optional receipt acknowledgment | Ask an unresolved research question; otherwise present report and wait for receipt if configured. No PR goal by default. |
| Review | report on exact assigned head and disposition of findings | Record findings as successful review delivery; parent's review gate remains blocked. A new head invalidates assignment scope and triggers explicit reassignment/review policy. |
| Repair | base green, optionally publishing recovered or nightly green | Work on a reproduced failure; wait on CI without rerunning an agent; non-reproduction needs recorded confirmation. Escalate unreadable CI. |
| Coordinator | successful outcomes of named required children plus synthesis report | Advisory and independent children do not hold closure. A failed required child requires repair, replacement via authorized contract edit, or scoped waiver. |
| Director | standing person-close policy, per-request report/child obligations | Rest between requests, wake evaluator on messages/child events, retain session across suspensions; spawning does not automatically require every child to finish. |

## Incremental implementation plan {#MGD-6}

This PR files and implements none of these phases. Dependencies are linear unless noted.

1. **Contract and evidence foundations.** Define DB records, snapshot/reference API, provider registry pins, journal/cursors, action registry and permissions. Retain all current collectors and legacy decisions. Add fixture coverage for current finish/state/goal cases. Capture exact legacy predicates for imports, including release-row absence and nightly windows.
2. **Observation-only evaluation (depends on 1).** Deploy pinned read-only evaluators without lifecycle or dispatch authority. Store differences against legacy decisions, classify incompatibilities, measure boundedness and provider reliability. Shadow errors cannot block current tasks. Expose read-only results clearly labeled observation-only.
3. **Explicit task migration and central completion (depends on 2).** Stop/settle or fence live operations, snapshot each open task, resolve requirements/policies, obtain required authority, and transactionally switch `completion_authority` from legacy to goals. Suppress every legacy completion writer for that task in the same release. Centralize close transaction and Go/SQL projection parity before expanding cohort. Never allow two lifecycle authorities for one task.
4. **Waiting and dispatch (depends on 3).** Move migrated tasks to cursor-fenced subscriptions, timer ledger and action arbitration. Disable legacy direct resumes for those tasks; retain collectors and journal production. Test restart, outage, contention and independent work before widening adoption.
5. **API/tools/UI and generalization (foundation can develop after 1; authoritative rollout depends on 3–4).** Add versioned contract/result/action/waiver APIs, agent tool descriptions and Angular goal panels. Keep existing API fields as documented compatibility projections and regenerate shared route contracts if routes change. Remove legacy authority paths only after every task has migrated or terminated and rollback window has closed.

### Database, upgrades and rollback

Use additive tables for contract revisions/attachments, evaluation bundles and per-goal state CAS revisions, evidence references, subscriptions/cursors, durable timers, progress/no-progress counters, action intents/events, waivers, and closure receipts. Store task authority epoch and lifecycle policy revision. Use unique keys for accepted evaluation IDs, action idempotency and event delivery IDs. Foreign keys and transaction/row locking protect attachment/contract ownership. Bounds apply before storing JSON. Opaque provider state is not indexed as lifecycle evidence.

The canonical lifecycle decision reads a versioned projection record; both `StateOf` and SQL `task_state` must apply the same authority discriminator and precedence. Extend parity tests to active blockers, terminal outcome, live operation and compatibility badges across SQLite and PostgreSQL. Do not repurpose `CompletedAt` into both report-delivered and succeeded; retain legacy fields for old readers with explicit projection rules.

Upgrade readers before writers. A daemon must advertise supported protocol, schema and authority epochs; an older daemon must refuse to drive migrated tasks rather than falling back to legacy. Rolling upgrades fence ownership leases. Rollback retains evidence and records, stops goal-owned dispatch, and uses an explicit per-task reverse handoff only if a validated legacy contract can represent the obligations. Otherwise hold for a compatible daemon. Never silently downgrade a requirement, reset state or reconstruct approval from prose.

Existing open tasks remain legacy until migrated; no universal default goal is inferred from history. Import attaches equivalent predicates and records provenance plus any intentional semantic changes requiring approval. Unavailable providers leave required goals unknown with durable backoff and escalation, never automatically waived or delegated to legacy. Tasks with independent work may continue safely. Provider upgrades require new pins and an audited migration/replay decision before switching contracts.

## Remaining implementation decisions

| Decision | Recommendation |
|---|---|
| Initial hosting and trust | Reuse configured capability MCP server identity/transport; separate read-only evaluator credentials and allowlisted scope. Start with in-tree providers behind the same protocol. |
| Protocol limits | Adopt the concrete v1 limits in the spec; measure shadow evaluations before changing them through a protocol/config revision. |
| Snapshot retention | Retain decision evidence and closure receipts for task audit lifetime; expire bulk pages after a configured window and rebuild a new snapshot rather than mixing revisions. |
| Historical regression | Default to notify plus separately authorized repair; opt into automatic reopen explicitly for monitored outcomes. Standing sessions rest instead of closing. |
| Requirement editing authority | Person-authorized relaxation with reason and audit; permit agents to propose edits and append obligations only within preauthorized template constraints. |

These recommendations leave rollout choices for later work, not the agreed separation of goals, permissions, actions, lifecycle, and durable wakes.
