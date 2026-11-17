# API gap audit and readiness

Task #59 follows the merged Task #57 / ai-on-gke/grain#1710 design. Audit baseline:
2ed47263f. This is documentation/schema work, not endpoint implementation,
configuration, rollout authorization or automatically filed phases.

## Initial rollout boundary

The first implementation/display increment may ship only two selected gates as
read-only observations while legacy lifecycle and action logic remain authoritative.
The [observation-only API/display subset](http-api.spec.md#initial-observation-only-apidisplay-subset)
and closed observation_view schema define that contract. It may reuse the ordinary
task read's optional gate_observations field; it need not implement the full future
mutation/enforcement API, policy storage/authority handoff or protected adapters.
Diagnostics never drive closure, dispatch, permissions, approvals or merging.
The full API remains specified for later implementation.

The operator's day-or-two comparison window does not authorize automatic cutover.
A separate explicit decision plus the relevant handoff/admission qualification is
required. The blockers below concern authoritative/enforced use; they are not a
demand that the first two observation-only gates implement every listed subsystem.

## Gaps found and decisions made

| Gap in merged specification | Normative resolution |
| --- | --- |
| BG-1 only sketched Evaluate with undefined Subject/ReadView/Result and generic config | [Internal API](internal-api.spec.md) defines registry, concrete twelve configs, controller context/state, typed result/evidence/dependency/work/wait/action variants, read helpers, admission and acceptance. Package structure remains implementation freedom. |
| Registry discovery/validation and unsupported versions had no callable contract | [HTTP discovery](http-api.spec.md) and descriptor/registry schemas pin twelve v1 IDs, local config schemas/digests, implementation build, supported subjects/purposes/reads/actions; unknown admission is 422, unknown discovery is 404, persisted unknown holds. |
| UI action names lacked exact operation bindings and same-authority tool contracts | [operations.json](operations.json) binds every method/path/tool and scopes; existing task/config/CI/review/Submit/reply/findings/events services are reused with exact gate profiles. Tools cannot grant authority by rendering a button. |
| No complete request/response contracts for task gates, edits, results, history, reevaluation or asynchronous state | [api.schema.json](api.schema.json) defines each request/response, expected tokens, closed bodies, accepted versus invalidated state, blockers, operation states, receipts and lists. [Exchanges](api-examples.json) cover every operation. |
| Expected head/context did not explicitly carry evidence/epoch fences in action requests | action_request now requires expected_evidence_digest and expected_authority_epoch. Both expectation envelopes must match. Dispatch and queue final execution revalidate current dependency union, policy/permission and expected remote head; local CAS alone cannot guarantee remote review races. |
| Weakening/admission authority, materialization preview and CAS unspecified | Task-owned replacement preserves inherited union. Gate profile edits require full expectations/reason/idempotency, exact authority rules and transactional cycle/limit checks. Pinned preview+filing rejects changed sources; handoff advances epoch only after writer fencing. |
| Completion waiver versus force semantics had no issue/revoke protocol | Shared task/resource exception service defines human authority, source ownership, action-specific merge:force/repo-admin checks, record revisions, expiration/revocation, exact evidence and reserved operation identity. No permission/approval override. |
| Async “success,” key retry, timeout and uncertain merge recovery were ambiguous | 202=requested; operation status distinguishes executing/completed/blocked/failed/unknown/canceled. Submit queue enrollment uses no_effect, integration needs succeeded receipt. Durable idempotency, >=7-day retention, lock/intent/outbox and reconciliation semantics are explicit. |
| Pending questions, access, acknowledgment and failed children lacked API flows | Task projection includes controller blockers; exact question/report versions fence reply/receipt. Existing request_access requests permission without granting it. [Five flows](http-api.spec.md#end-to-end-flows) cover repair/review/CI/merge, override, stale head, question/receipt and failed child. |
| Delivery/pagination and old clients might imply complete current evidence or bypass guards | Existing events/outbox is reused, with identifiers-only new event types and collector-owned dirty recovery. Snapshot-fenced history lists and legacy/428 behavior are specified; current met and historical met are distinct. |
| No comprehensive schema/operation/example linkage or generation audit | [validate.py](validate.py) validates canonical assets and [validate_api.py](validate_api.py) checks all operation references, exchanges, semantic denials, local schema links, and current shared route/tool/scope mappings. Current generated runtime files stay unchanged. |

## Coverage/readiness matrix

“Specified” means a reviewable normative contract with schemas and examples.
It does not mean deployed or ready to enforce. Existing behavior refers to source,
not to the proposed gate-aware profile.

| Interface | Source today | Specification/assets | Runtime readiness |
| --- | --- | --- | --- |
| Registry/evaluator/reads/acceptance | Existing domain readers, no general built-in registry | Exact types/signatures, bounds, no private state, local schema catalogue, errors/cancellation/fences | Specified; registry/read capture/acceptance not implemented |
| Discovery/config schemas | Hub registry is unrelated | Two proposed built-in discovery routes, concrete config schema examples | Specified; proposed routes absent |
| Task contract/current evidence/history/edit | Shared task detail/update and legacy state | Changed task profiles; proposed history; closed schemas and CAS/source authority | Specified; persistence/central writer and source projections required |
| Repo/template materialization | Shared settings/template/composition/filing | Changed gate profiles plus read-only preview; pinned provenance and inheritance | Specified; gate fields/admission/handoff absent |
| Reevaluation/operation state | Existing reconcilers and CI/review status | Proposed request/status/reconcile; explicit requested/executing/completed/unknown semantics | Specified; durable aggregate request/state/outbox required |
| Guarded action/queue | Existing Submit/merge/CL services and review/CI guards | Shared Submit profiles, current availability, exact evidence/head, receipts and overrides | Specified; enforcement boundary and conditional effect adapters unqualified |
| Waiver/override/revoke | Legacy findings/force behavior, no gate exception records | Proposed shared exception service, full records, revisions, ownership and expiry | Specified; store/RBAC/use-time fences required |
| Questions/ack/required children | Existing conversation, state/goal/wait APIs | Versioned shared reply/close profiles and child outcome fences | Specified; immutable question/report/outcome generations needed |
| Wake/event/UI/tool delivery | Existing events, generated tools and two UIs | New event types on same service, shared operation schemas/authority | Specified; additive projections/tool registration and UI integration required |
| Credential/remote race exclusion | Deployment-dependent identities; push identity can merge | AGB enforcement/refusal rules retained | **Not enforcement-ready**; no live verification performed |

## Remaining implementation blockers and recommended decisions

These are required implementation/qualification work, not unanswered API payload
placeholders and not newly filed tasks.

1. **Persistence, authorization and source resolution:** store contracts/source
   revisions, persistent resource-policy coverage/index, exception revisions,
   idempotency responses and operation/audit history. Wire the specified existing
   scopes to explicit ownership/person checks; do not treat tasks:write/admin as
   policy/force authority. Recommended: implement readers/storage and shadow
   validation before accepting mutation profiles.
2. **Current complete evidence:** capture all local record/set/message/child/auth
   versions and real collection timestamps; immutable exact-head approvals/reviews,
   absence proofs and observation dirty/coverage sequences are required. Recommended:
   conservative unknown for collectors that cannot prove completeness, not a
   successful empty scan. Implement the read/acceptance ledger before protected use.
3. **One lifecycle owner and compatibility parity:** fence every legacy completion,
   goal shortcut, UI and recovery writer per epoch, settle live/uncertain effects,
   and make Go/SQLite/Postgres projections agree. Recommended: shadow comparisons,
   then explicit finite imports retaining concrete goal semantics; hold any reverse
   migration that cannot represent the new obligations.
4. **All action entry points and durable effects:** converge task/taskless/API/tool/
   UI/queue/capability/proxy paths into shared domain admission, durable fenced lock/
   intent/receipt/outbox and uncertain-effect reconciliation. Recommended: final
   queue execution revalidates current expected evidence/head; approval/enrollment
   receipts never claim integration.
5. **Remote protection and credential boundary:** inventory/probe effective agent,
   operator, automation, CI, minted/refreshed, PAT/App/SSH and alternate-host rights.
   Existing merge identity's push fallback is not exclusion. Prove forge rules/
   conditional effect or refuse enforced execution for that cohort. Recommended:
   observation-only without Grain protected execution until exclusion is proven.
   Exact CL reviewed-snapshot/renumbering and race exclusion remain separate adapter
   blockers; custom QA/publishing/config effects are outside admitted v1 actions.
6. **API/tool/UI integration and recovery qualification:** register gate profiles,
   profile-aware oneOf request schemas in the existing single-body generator,
   audit reason/expect wrappers and lossless HTTP/MCP result/error transport,
   new route scopes/descriptions/query schemas, response validation, rate/size
   bounds, audit/outbox, Angular views and permission-filtered compatibility.
   Recommended: qualify every [acceptance scenario](acceptance.md), including
   idempotent crash recovery, redaction, concurrent edits/revocations/head moves,
   stale queue execution, lost wakes and denied person actions, before enforcement.

No MCP gate evaluator, universal snapshot paging, capability back-call broker or
external provider framework is a prerequisite. Runtime package names, DB physical
layout and lock implementation are internal choices under these contracts.
Operational retention may exceed the specified minimum; deployment cohort and live
configuration choices require their own review and are not changed here.
