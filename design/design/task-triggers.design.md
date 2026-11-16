# Extensible capability-backed task triggers

**Owner:** @ai-on-gke/ai-factory-root-owners

**Status:** proposed for human review. Documentation only; no provider, API, subscription, migration, configuration change or implementation task is installed or authorized by this proposal.

**Normative companion:** [Task trigger specification](../spec/task-triggers/task-triggers.spec.md).

## Decision

A **tracked resource** is observed through a **resource provider**. A **task dependency** remains the existing prerequisite relationship. A **gate** interprets evidence for existing work. A **trigger** decides whether an event should create new work. Share durable event collection, not authority: observing a resource never authorizes filing a task. Grain owns ingestion, cursors, matching, authorization and transactional task filing. Capability providers describe bounded source contracts and supply authenticated observations/events; they cannot select task authority or write task state.

Use three layers:

1. **Event-source adapters** collect webhooks, streams, polls, time occurrences and internal transitions into a durable scoped journal. A subscription can serve resource-provider Observe/Watch, gate waits and triggers, with separate consumer checkpoints and access checks. An event need not denote an enduring tracked resource: a review-request occurrence and a time tick are valid events themselves.
2. **Trigger bindings** pin a provider contract, source scope, bounded declarative filter, template revision and parameter mapping, target, identity, grants, approval and repeat/budget policy. Grain records every match and disposition. A binding is a separate audited standing filing authorization.
3. **Durable filing** revalidates authority and current policy, composes the approved template inputs, then atomically records a unique logical firing and its task or coalescing receipt. Leased workers and an outbox recover crashes. External delivery is at least once or weaker according to the adapter; observable exactly-once logical filing requires durable stable identity and transactional idempotency.

Schedules become the built-in **time provider** and use the same matching/filing ledger. Preserve existing recurrence and ownership semantics under an explicit compatibility profile. Existing workflow, git-ref and path sources use their own adapters; do not pretend they are time ticks or switch all legacy mechanisms at once.

## Review-request example

An authorized person binds GitHub review-request events for a configured user or team in `ai-on-gke/grain` to a pinned review template in that repository. The event supplies only validated PR identity, head and bounded text parameters. The repository, template, capabilities and approval policy are binding constants. A request received twice files once; another request after a completed review is a distinct occurrence and may create another task. A changed head invalidates review scope but is not itself a new request unless explicitly configured. Coalescing preserves an immutable list of requests rather than overwriting a running prompt.

## Coordination contracts

Task #45's tracked-resource/resource-provider design and Tasks #39/#44's MCP gates work are independent workstreams. Final reconciliation inspected Task #44's documentation rename merged into `main` as `befe68b54` (PR #1699), and Task #45's unmerged resource-provider draft at `6d2ffe338`. The canonical evaluator documents are now [mcp-gates.design.md](mcp-gates.design.md) and [mcp-gates.spec.md](../spec/mcp-gates/mcp-gates.spec.md); the #45 branch supplies `design/design/tracked-resources.design.md` and `design/spec/tracked-resources/tracked-resources.spec.md`. The shared terms are tracked resources and resource providers; task dependencies remain prerequisite relationships. These documentation revisions establish proposed contracts, not runtime implementation or protocol support. Existing concrete task goals keep their names and semantics.

| Shared surface | Proposed integration contract | Authority stays with |
|---|---|---|
| Resource-provider Observe/Watch (#45) | Use #45 TDS-2/3/4/5: `resource_<method>_{resolve,observe,watch}_v1`, explicit provider/schema/config pins, Watch describe/ensure/cancel, fence before baseline Observe, independent dirty generations and equality-only revision tokens. Bridge authenticated hints to current observations; do not treat hints as occurrence evidence. | Separate tracked-resource attachment and read grant; never trigger filing authority |
| Gate waits (#39/#44, MGS-6) | Register cursor-bound waits over the same journal; replay after snapshot cursor; reread current truth on wake. No one subscription cursor may advance another consumer. | Gate attachment/read grant and Grain lifecycle evaluator |
| Typed actions (#44, MGS-4) | Register `task.create_from_trigger` and `task.coalesce_trigger` as proposed Grain actions with fixed schemas, preconditions, request identity and audit receipts. These names are new proposals, not current registry entries. | Grain action registry and current standing authorization |
| Capability protocol | Reuse canonical provider identity, immutable artifact pin, local argument schemas, secret references and lifecycle conventions; discover a distinct event extension, never infer one from `mint_*`. | Capability installation/configuration and explicit binding approval |

The resource-provider protocol and event occurrence protocol intentionally have different envelopes; use the explicit registry/identity bridge in TTS-2, not a wire-level alias. A resource-provider Watch hint can dirty an observation but cannot reconstruct a review-request occurrence. Shared collection uses the stricter applicable retention, authorization and service limits. If the sibling contracts evolve, implement adapters or revise this spec through review before implementation. Neither tracked-resource state nor a gate result is automatically a trigger; a separately authorized binding can consume a versioned transition event. Gate "met" also never grants approval to create work.

## Compatibility and evidence

Baseline inspected: `d8ff32019`. The [spec's inventory](../spec/task-triggers/task-triggers.spec.md) cites code and distinguishes current behavior from proposed requirements. In particular, current schedule task insertion and cadence advancement are separate store calls; tag-based open-task suppression is not a proof of transactional exactly-once filing. The new ledger is a deliberate stronger requirement. Current template references float; new bindings pin by default, with explicit compatibility tracking for migrated schedules.

Configuration belongs in the deployment's state repository (`grain/config` here), or approved repository declarations with bounded authority. Cursor, event, match, firing, lease and replay records are runtime state, backed up but never reset by a configuration PR. This PR changes neither configuration source.

## Tradeoffs and recommended defaults

Use deterministic, bounded predicates and typed data mapping rather than provider/agent code. Default to future events only on enable, no initial-state firing, no automatic replay, proposed tasks requiring approval, pinned templates, one active task per resource/request recipient, and visible held overflow. Existing approved schedules retain their recorded standing approval only within their existing scope. Human preview must show the effective template, grants, placement, costs, recovery limits and sample dispositions before approval.

Unknown protocol versions, absent providers, revoked grants and unrecoverable event gaps hold work with a visible reason. Polling can recover current state; it cannot reconstruct every removed/re-added review request. The UI must expose that distinction. Provenance bounds automation chains, while a later authenticated human request starts a new request epoch rather than inheriting a permanent suppression flag.

Open choices have recommended defaults in the spec: common journal ownership/naming with #45, operational quotas/retention, explicit calendar DST policy for newly created schedules, and eventual retirement of legacy compatibility profiles. None block this documentation review or justify changing live behavior.

## Phases, not filed tasks

1. Reconcile the journal/Observe/Watch seam with #45 and typed action/wait terminology with #39/#44; ratify schemas and threat model. These workstreams need not merge first to review this document.
2. Add scoped durable journal, recovery-gap accounting and transactional firing ledger; prove authorization and crash invariants in both database backends before enabling any binding.
3. Add preview and binding management, template pin storage and scoped agent contracts; implement the time adapter in shadow mode and compare schedules without filing twice.
4. Migrate selected task schedules with fenced handoff and rollback; retain legacy suite/workflow behavior until equivalent typed executors are ready.
5. Add GitHub user/team review-request adapter with authenticated collection and explicit recovery limitations; qualify repeat, head, quota, privacy and loop behavior before opt-in enablement.
6. Adapt remaining git-ref/path/workflow sources and tracked-resource transition events only with compatibility tests and an authorized migration.

These phases describe dependencies and review gates only. This PR must not file them or activate the repository's design planner; `docs/design-docs.md` documents that this repository does not declare that automatic trigger.
