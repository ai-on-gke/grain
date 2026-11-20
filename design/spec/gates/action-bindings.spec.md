# Action gate bindings and protected operations

**Status:** proposed normative documentation, not implemented. No live credentials, rules, configuration or runtime changes are authorized.

**Extends:** [the canonical gate specification](gates.spec.md), especially BG-2/4/7. This evolves Task #50's AGB requirements to compiled built-in evaluators. The old remote 3.0 profile/schema is [deferred history](history/action-bindings.spec.md), not a prerequisite. [contracts.schema.json](contracts.schema.json) supplies the initial attachment/action/exception/receipt shapes; [examples](examples/README.md) and [acceptance scenarios](acceptance.md) cover initial admission.

## AGB-1: Purpose and authority {#AGB-1}

A guard is an explicitly attached prerequisite for a canonical protected operation/resource. Required current-head checks, review/security/path dispositions and scoped human approval precede merge; PR integrated is a later task outcome and MUST NOT guard that same merge. Approval, successful guard, execution permission and completion waiver are distinct. Grain remains the sole admitted action/lifecycle authority. All agent/person/automation entry points to an admitted protected effect MUST traverse the same domain action service; no task/tool alias bypass.

## AGB-2: Policy resolution and coverage {#AGB-2}

Use the existing PR merge and CL submit services, with a small closed action identity map (`change.merge`, `change.submit`); this is not a generic execution registry. A binding is a BG-2 qualified `purpose=guard` attachment, source revision/provenance, canonical subject, action, typed evaluator version/config, read scope and policy-defined preconditions/override eligibility. Effective policy MUST union deployment/repository/resource and every active protecting task's bindings. Taskless callers resolve by resource/effect, not invented task identity. Explicit authorized unguarded policy is the only valid empty set; missing store/index/coverage is refusal. Conflicting inherited rules require policy repair, not last-writer-wins. Unsupported effect aliases or adapters cannot enter enforced admission. Resource guards apply independently of a task's legacy/built-in completion authority and survive originating task closure/deletion or reference removal until an authorized explicit policy retirement. Observation-grant revocation makes required evidence unavailable; it does not remove the binding.

## AGB-3: Evaluation context {#AGB-3}

The controller derives `$defs.context` and its canonical digest from authenticated actor/delegation, canonical resource/head/base or reviewed CL snapshot/version, effective policy/source revisions, contract/authority epochs, authorization and evaluator build identity. Configuration is the registered typed attachment, never an untyped command. Built-in read helpers capture dependencies and complete-set/negative evidence as BG-4 requires; no MCP hosting, snapshot ID, paging or remote outer envelope is required. Results used together MUST validate as one current Grain-recorded context; independent external atomicity is not promised. Approval evidence and action exceptions MUST bind exact rendered head/resource/policy scope and be checked at use. New head or policy invalidates stale approvals/buttons/overrides. Shadow and optional monitoring cannot contribute required success.

## AGB-4: Transaction and failure semantics {#AGB-4}

The domain service MUST authenticate/authorize the actor, validate typed inputs, resolve current policy, acquire fenced cross-task resource/effect locks, reread current resource and dependencies, evaluate all required built-ins, and validate complete current evidence. Pending/unmet/unknown/error/stale guards block unless a separately valid eligible named exception applies. Store an idempotent intent before dispatch; no transaction spans network calls. Recheck current authority/policy/versions before issuing expected-head merge or exact-reviewed-snapshot CL submit. If a source cannot condition/exclude the relevant execution race, refuse enforced admission rather than claiming protection. Delayed queues must protect final execution, not only enrollment.

A same-key retry with equal canonical inputs returns the existing authorized receipt; differing inputs conflict. Uncertain effect keeps its resource held and reconciles the forge/CL before retry. Immutable receipt, operation attribution, evidence invalidation and wake/outbox commit durably; crash/reply failure after effect does not roll back or repeat success. Policy/revocation cancels unstarted intents; already dispatched uncertain effects still reconcile without claiming rollback. Record blocked and unknown attempts as well as success. Existing reconciliation/adapters are reused and strengthened where necessary; no universal action platform is introduced.

## AGB-5: Enforceable boundary {#AGB-5}

Admission requires a concrete inventory and effective-rights probes for agent, operator, automation, CI, capability mint/refresh, proxy, SSH, PAT/App and alternate-host paths. Direct protected base writes/merge/submit/publish authority MUST be excluded or constrained to the admitted domain service and its exact resource policy. Feature-branch writes remain possible only where that boundary holds. Current `pkg/github/mergeidentity.go` can fall back to push identity; repository-scoped contents-write is not proof that direct merge is impossible. AutoMerge=false, hidden buttons and prompt instructions are insufficient.

If exclusion/conditional execution cannot be proved, hold/refuse enforced execution or use observation-only with no Grain protected execution for that cohort. Independent out-of-band identities remain outside the guarantee unless separately constrained; observed external integration is attributed external, never guard-authorized. Old daemons/queues cannot keep an ungated execution path beside migrated resources. Rollback of lifecycle does not relax this boundary. No live inventory or credential change is performed by this PR.

## AGB-6: Overrides and interface {#AGB-6}

Use BG-7's exact reasoned, expiring, evidence-scoped `action_override` record and named eligible check/review bindings. Caller permission, required human approval, actual conflict, unavailable identity/policy/boundary are not overridable. A completion waiver cannot merge; an action override cannot close outstanding child/integration/report obligations. Findings acceptance is a separate one-review/head disposition, not broad Submit or completion waiver. Preserve authorized legacy base-repair/force semantics only through explicit imported policy and current eligibility, including own-CI requirement.

The UI/API must show action, canonical target/head, inherited source revisions, each guard and freshness, caller authorization, valid exception markers, remediation and operation pending/unknown/receipt. Registered buttons map to existing typed domain actions and current server-side checks; the name/action result itself grants no authority. Stale rendered head is a conflict requiring refresh. Agents see concrete existing tools or a named person decision; new Angular views do not imply a classic API bypass.

The exact operator/agent method/path/tool, request/response, authority, exception,
idempotency and asynchronous operation contracts are in
[http-api.spec.md](http-api.spec.md), indexed in [operations.json](operations.json)
and validated by [api.schema.json](api.schema.json). Action requests now explicitly
include expected authority epoch and evidence digest in addition to head/context;
both expectation envelopes MUST match and be revalidated before dispatch. Shared
Submit queue enrollment is not an integration receipt. Taskless actions use the
same resource-policy admission and shared exception service. The evaluator's exact
read/acceptance types are in [internal-api.spec.md](internal-api.spec.md).

## AGB-7: Qualification {#AGB-7}

[acceptance.md](acceptance.md) requires blocked-then-allowed merge, task/tool/taskless bypass probes, head/policy/revocation races, all caller paths, external integration attribution, uncertain outcome recovery and distinct completion/action exceptions. Schemas/examples validate records, not credential isolation or actual remote conditional effects.

## AGB-8: Migration and blockers {#AGB-8}

Readers/storage/coverage first, shadow comparison second; before admitting each resource/effect cohort, fence every equivalent old route/worker and verify effective credential exclusion and conditional/reconciliation adapter behavior. Lifecycle migration is separate and per task epoch. Keep contracts/receipts/audit during rollback; unsupported binaries hold, never downgrade.

Required implementation remains: policy persistence and coverage/index, built-in exact-head review/approval evidence and complete versioned reads, uniform domain-service checks, durable lock/intent/receipt recovery, guarded queue final execution and deployment boundary verification. CL exact-snapshot/renumbering/exclusion must be proven before its enforced cohort. Deployment/publishing/config/QA custom effects are not initial admitted adapters; extending them requires typed domain-specific design/qualification. Remote provider/profile support and custom snapshot transport are explicitly absent from the blocker list.
