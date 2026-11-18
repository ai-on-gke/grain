# Built-in gates HTTP and tool contracts

**Status:** normative proposed v1, documentation/schema only. No route, scope, tool,
runtime gate or configuration is installed by this change. BCP 14 applies.
[BG-1–8](gates.spec.md) and [AGB-1–8](action-bindings.spec.md) still govern admission,
evidence and effects. [Internal API](internal-api.spec.md) defines evaluation and
acceptance. This document supplies the previously missing operator/agent contract.

## Reading the contracts

[operations.json](operations.json) is the normative operation index: exact HTTP
method/path, generated tool name, existing/changed/proposed status, scopes, person
restriction, request/response definition and success code for **every** operation.
[api.schema.json](api.schema.json) defines complete closed requests/responses, with
local references to [contracts.schema.json](contracts.schema.json); neither payloads
nor concurrency rules are deferred to implementation. Each operation has a concrete
success and denial exchange in [api-examples.json](api-examples.json). The
[error exchanges](api-error-examples.json) cover status/recovery distinctions;
[invalid exchanges](api-invalid-examples.json) distinguish schema rejection from
semantic rejection. Illustrative digest values are not real authorizations.

Request schemas use current generated-tool arguments: path parameters at top level,
a query object, a body object and the existing optional reason/expect wrappers.
For HTTP, URL-encode/substitute path parameters,
encode query values (arrays are not used in query), and send only body as JSON.
For example the submit example is POST /api/tasks/57/submit?view=gates with its
body, or api_submit({id:"57",query:{view:"gates"},body:...}). Top-level reason is the existing tool audit reason, forwarded as
X-Grain-Acting-Reason, and is required for tasks:admin operations (issue/revoke).
Where both it and mutation.reason are supplied they MUST match after trimming;
a mismatch is 400 invalid. All mutating gate bodies retain their own required
reason where specified. HTTP clients performing those dangerous calls set the
same reason header; it carries no concurrency/permission authority.
The existing optional expect={state?,title_prefix?,kind?} wrapper is supported for
task mutations as a preliminary tool-side task check; it is never a substitute
for the atomic mutation.expected contract/evidence fence. On HTTP, those optional
preliminary checks may be omitted; they are not silently added to the JSON body.
Use the explicit body wrapper for gate profiles; the generic bridge's legacy
flat-body spelling must normalize to this exact closed body before validation.

There are no custom
concurrency headers to lose through the MCP bridge. All expectations are explicit
body fields. Authentication and X-Grain-Acting-Task remain existing verified headers,
never input fields that let a caller choose another principal.

**Changed** means the path/tool exists today but this gate profile/behavior is new.
The exact profile is selected by view=gates on shared routes. Its request admits
only the fields in that operation's schema; unrelated metadata/grants/config edits
use a separate ordinary call. Ordinary shared responses and fields remain unchanged.
The profiles deliberately avoid duplicating task, template, repository settings,
CI, conversation, findings and Submit services. **Proposed** paths have no existing
suitable service projection: compiled registry (not Hub /api/registry), gate history,
explicit reevaluation, shared asynchronous status, contract preview and exception
records. No arbitrary action execution or permission-grant endpoint is introduced.

HTTP JSON responses/failures use application/json and Cache-Control: private,
no-store; SSE uses text/event-stream under existing event headers. There are no
unversioned cached readiness responses. Location on 202 identifies the operation.
All fields are required unless absent from the schema's required array. Null is
explicit only where allowed; empty arrays are legitimate complete sets where
specified, never missing evidence. String identifiers are case-sensitive opaque
values <=256 characters, not trusted URLs/SQL selectors. Digests are SHA-256 over
RFC 8785 canonical content. Version integers are exact JSON-safe integers through
2^53-1. Requests disallow duplicate JSON keys, unknown properties, depth >16 and
bodies >256 KiB; oversize is 413. Contract <=256 KiB, result <=128 KiB, config schema
<=64 KiB. Page responses <=16 MiB (return fewer items plus next cursor when a page hits
this byte bound); if a single authorized item cannot fit, return
413 rather than silently truncate it. Registry discovery is at most twelve v1
entries and may total <=1 MiB; the twelve config schemas are individually bounded.
These transport limits do not increase the evaluator's BG-3 bounds.

## Tool result transport

Each operation's tool_result field names the closed MCP tool_result schema.
For the proposed gate profile a successful tool call returns exactly one text
content item containing the complete JSON HTTP response body, with isError=false.
A failure returns isError=true and one text item:
TOOL_NAME + " failed: HTTP " + decimal status + ": " + JSON failure body.
The per-operation examples include concrete tool_success and tool_error objects
as well as the decoded HTTP bodies; validation checks that both carry identical
payloads/status. This preserves the current generated API tool text convention
while preventing loss of the structured failure code/recovery/operation ID.
MCP request/response JSON-RPC IDs remain transport IDs, not action idempotency IDs.
Controller grain_invoke dispatches the same named operation through its existing
arguments/reason wrapper and MUST preserve that logical status/body contract.
Pre-dispatch bridge failures (invalid tool name, missing wrapper reason or malformed
arguments) are MCP errors without an HTTP operation: isError=true and a diagnostic
text, no claimed accepted operation or effect. Once dispatched, the HTTP failure
body is authoritative. SSE clients use HTTP; agent tools use JSON/long-poll.

## Initial observation-only API/display subset

The initial implementation increment may expose only the two selected built-in
gate observations and read-only UI/agent display, while legacy logic remains
authoritative. The full future API below is still specified; implementing every
registry, mutation, waiver, reevaluation, action or enforcement endpoint is **not**
a prerequisite for this first increment. This document does not select or deploy
the two gate types: the same closed catalogue/config schemas apply to the selected
subset. The observation example is illustrative, not a rollout configuration.

Existing GET /api/tasks/{id} (api_get_task, tasks:read, existing task/repository
visibility) may add optional gate_observations to its ordinary TaskDetail response,
without adding request fields or replacing other existing task fields.
gate_observations MUST validate as api.schema.json $defs.observation_view;
[api-observation-example.json](api-observation-example.json) is its complete value.
This permits the existing task API and ordinary generated tool to carry the first
read-only display without implementing the future view-dependent mutation schemas
or a separate agent API. The existing legacy-state projection remains authoritative.

If the future GET /api/tasks/{id}?view=gates profile is exposed during this stage,
it MUST use the same gate_observations value, authority=legacy,
contract=decision=null, and empty top-level results/actions/exceptions as shown in
[api-observation-task-example.json](api-observation-task-example.json).
No observation result is published in an authoritative decision/result slot.
Normal GET callers that do not support the additive field keep existing behavior.
Omitting gate_observations means no observation projection is available, not that
gates passed or were waived. A configured monitor with no cached result must be
shown as unknown/pending collection with dirty=true; it cannot silently disappear
into an apparently complete comparison. Other new read endpoints are optional conveniences
for the first increment; unimplemented proposed routes are not advertised as
available. Any full-index endpoint that is advertised must still honor its exact
schema/authority contract; the ordinary task read plus sidecar is sufficient and
does not require the full discovery catalogue to be implemented first. No gate
mutation or enforcement endpoint is required in that increment.

Every observation has explicit mode=observation_only, authority=legacy,
lifecycle_effects=false, action_effects=false, dispatch_effects=false and
automatic_cutover=false. source_revision identifies the compared legacy fact set;
compared_at is its comparison time, and legacy_state is the legacy projection at
that time. Existing TaskDetail state remains current authority if it has moved
since. bindings are monitor-only (required=false, action=null), not newly admitted
completion obligations or resource guards. Controller-derived provenance/config
pins may be copied from existing legacy fields for comparison without creating a
gate contract, migrating a task or exposing configuration mutation APIs.

Observation results retain type/version, status, summary, evidence/dependency
versions, collection/freshness and invalidation metadata. disposition=accepted here
means a validated diagnostic observation, never an authoritative predicate or
permit. Unavailable/denied/incomplete/stale evidence remains unknown/error or
invalidated with safe display text; do not fabricate completeness, source time or
authority. Missing version/freshness support may therefore limit diagnostics
without requiring lifecycle/policy storage or enforcement to be implemented first.
The controller MUST preserve observation-only provenance with cached results and
reject their IDs as authoritative completion/action evidence. A later admitted
evaluation may read the original domain facts under its own fresh authorized
context; it cannot promote the sidecar's old results by copying them.

Observation results MUST have empty work/actions arrays. Wait descriptors may
identify an existing pending process/deadline for display, as BG-3 requires; they
MUST NOT be registered as new task waits, holds, escalation or dispatch triggers.
Existing collectors or display refresh may update diagnostic cache/versions; they MUST NOT install
task holds, dispatch remediation, ask questions, grant permissions, process
messages, acknowledge reports, alter goals/dependencies/counters, close/rest/reopen
tasks, set approval/AutoMerge, change merge-queue decisions or invoke protected
effects because of these observations. Existing legacy actions and authorization
continue under their existing rules. The UI and agent MUST label these results
“Observation only; legacy controls remain authoritative”, keep observed status
separate from actual task/action readiness, and render no gate-driven mutation or
enforcement buttons. A met observation never enables a legacy action; an
unmet/unknown observation never disables one that legacy policy would allow.

This initial shadow profile is distinct from admission of a built-in-protected
resource cohort. action_preview.coverage=observation_only still means that the
**new built-in-protected execution** path is unavailable; if that preview is
implemented, available=false and its new submission action is disabled.
It does not switch off existing legacy actions during the unconverted display
increment or claim that those actions have a new enforcement guarantee.
Existing separately admitted resource guards, if any, retain their own authority;
diagnostic observations cannot add, remove or bypass them.

The operator may inspect comparisons for a day or two before deciding whether to
cut over. That is an observation window, not a timer-based rollout rule. Elapsed
time, successful diagnostics or an evaluator retry MUST NOT activate mutations,
migrate authority/epochs or enable action enforcement. Any cutover requires a
separate explicit operator decision, authorized conditional handoff/admission and
the applicable qualification in api-readiness.md. This PR changes no live settings.

## Authentication, authority and permission requests

Scopes in the index are minimums, intersected with repository/task grants and current
RBAC on every read, write, retry and status lookup. Attachment grants no observation
or effect permission. Actor/verified delegation come from authentication. Bodies
cannot set actor, imported provenance, receipt timestamps, authority epochs,
accepted status, grants or approver identity. Human-only means authenticated direct
eligible person; an agent/token holding the scope, or a free-text claim of acting
for a person, still fails 403 person_required. Existing verified console relay can
deliver a person's answer only under its existing identity checks; it cannot relay
waiver, Submit, policy weakening or findings acceptance as agent authority.

* Contract addition: tasks:write, own task and all subject observation scopes.
  Agents may add obligations; adding a resource-action guard additionally requires
  repos:write and explicit resource-policy attach authority within the owning task's
  scope. Read access to another resource alone cannot add a cross-task merge hold.
  Removals/retargeting, predicate/version/lifecycle or
  required-child changes need a person with tasks:admin and contract ownership.
  An exact preauthorized transform must already exist in controller policy with
  matching before/after digests; a caller cannot authorize it in this body.
* Repository/resource guard policy: person repository policy owner, repos:write
  and repository administrative permission. Template edits: templates:write and
  source/template ownership within repository ceilings. An agent can strengthen
  defaults only within an explicit existing template-edit grant; weakening needs
  the eligible person plus the owning repository's policy authority.
* Completion waiver/revocation: person with tasks:admin and contract ownership.
  Action override/revocation additionally requires merge:force, repos:write and
  current repository-admin permission, preserving the existing force boundary.
  tasks:admin alone MUST NOT imply merge:force. Source-policy authority is required
  for each inherited binding named by an exception.
* Submit and CL submission retain current person-only rules; approval eligibility
  is checked per requirement, separately from execution permission. Internal queue
  automation executes only as a persisted admitted delegation, not by impersonating
  the approving person. Findings acceptance retains the unlimited operator role.
  Reply requires the addressed eligible person; acknowledgment requires the
  report's eligible recipient. CI requires existing CI grants as well as ci:write.

Reads filter/redact under the same rules in both UIs and tools. Public gate IDs and
a safe permission blocker may be shown; private evidence/child/resource IDs, denied
existence and dependency detail MUST NOT leak. Authorized current-evidence digests
are opaque decision tokens, not proofs to reconstruct inaccessible facts. A caller
with task read but not a required source read gets an unknown permission blocker.
Unknown and inaccessible object status lookups both return 404; explicit known-scope
denial is 403. No redacted empty list can authorize success.

A gate operation never grants permission. For an actionable access blocker agents
use the **existing request_access** tool, with summary (<=100 characters), reason
and typed requests (read/write repository, capability+with, or network), as defined
in [access_request_tool.go](../../../pkg/mcp/access_request_tool.go). It validates
against current grants, records one turn-ending request and parks only at run
settlement; its immediate success means recorded for delivery, not granted.
Unknown/already-held/inadmissible items fail without filing. Grant/Deny still uses
the existing human access-request card/domain service, current grant authority and
audit, invalidates authorization/evidence and wakes reevaluation. A denial wakes
with its reason and does not authorize repeated automatic requests. A plain reply,
completion waiver or action override cannot grant access. For an approval, waiver,
question or acknowledgment decision, use existing ask_question with a precise
summary and target/head; do not request broader access to evade a person-only rule.
These existing tools are integrated, not replaced or given new input fields.

## Identity, expectations and atomic mutation rules

Every mutation except new filing includes mutation = {idempotency_key, reason,
expected}. All three are required; reason is nonempty <=2048 characters, key <=128.
expected has contract_revision, authority_epoch, policy_digest, context_digest,
evidence_digest, nullable head and resource_version. Read responses provide this
whole token; clients copy it, not synthesize it. Policy-only/template-only reads use
contract_revision as that object's gate policy revision and authority_epoch=0.
Taskless action context uses both numeric fields=0 but includes the live union of
all protecting source revisions in policy_digest. No task identity is fabricated.
resource_version is the canonical target version, or contract/policy/template
record token for non-change operations. expected.head is null for non-change
subjects. Context includes current authorization/build/source epochs. Its exact hashed payload
is api.schema.json $defs.context_set ([example](api-context-example.json)): complete
per-binding evaluation contexts, effective attachments, authenticated actor,
resolved read scope, source revisions and registry digest. Sort contexts/bindings
by qualified binding ID, source revisions by source, and scope sets by canonical
JSON bytes. No duplicates. Evidence digest hashes the captured dependency union,
sorted by key; duplicate keys must have identical complete values or acceptance
fails. Timings/authorization/negative-set dependencies are not omitted.

The expected context token identifies an immutable persisted evaluation cut, not
a hash recomputed using a later wall clock/evaluation ID. At use, load that cut and
validate its recorded facts, expiry and current versions. Reusing an accepted
evaluation requires this immediate validation. Creating a new audit/evaluation ID
alone is not a source change; a recollection that advances a captured source
version invalidates the old token and requires refresh. Protected execution cannot
silently upgrade to newly met evidence or retarget a stale expected head.

For freshness_seconds=0, recollection is required in that same persisted decision
cycle; validity is the earlier source expiry and cycle deadline, at most 10s after
collection (not source-time plus zero). A preview may return that short-lived cut;
execution must use it within its deadline with all current fences intact, otherwise
409 stale_evidence requires a fresh preview. It cannot extend/relabel a cached read
as a new cycle. Completion evaluation can collect and decide within one cycle.
Other freshness budgets use the BG-4 source-age minimum. This gives zero a concrete
API meaning without accepting old evidence or promising a global remote snapshot.

Under one short transaction, edits verify all token fields and source revisions,
validate source authority, cycles, catalogue support and inheritance, record the
before/after audit and advance the affected revision. A mismatch returns 409 with
a safe current token where readable, no partial mutation, and recovery=refresh.
Compare-and-swap is mandatory even if the new value looks identical. Two differing
edits to one revision cannot both succeed. Client must refresh, review and use a
new key; never automatically rebase a weakening edit. Same-key exact replay is
recognized before CAS and returns its original authorized response.

Gate edit supplies the full desired **task-owned** attachment set and lifecycle,
plus authority_handoff (null for ordinary edits). A non-null handoff names the
desired authority and current epoch; only a person tasks:admin contract owner may
request it. The central owner must settle/fence every old writer before atomically
advancing the epoch. Unrepresentable reverse imports fail 422; no downgrade.
Omitted task-owned bindings are removal, not a patch typo; immutable inherited
bindings are appended by the server. A caller cannot supply inherited origin,
imported=true, an unresolved wildcard, or a source revision it does not own.
Server assigns the next source revision/provenance; submitted origin must match
the existing editable source. Weakening checks compare effective semantics, not
just required flags. Moving from completion to monitor, mode=none, weaker children,
or deleting a child is weakening. Effective overflow (64 completion or 64 guards)
and cycles reject atomically with 422; no truncation or silent inheritance loss.
An accepted edit invalidates affected results/buttons/waits/exceptions and unstarted
intents, including resource-policy decisions across tasks.

The shared /api/gates/exceptions service supports both task and taskless resource
exceptions. List selects exactly one task_id or complete authority/repository/
change_kind/change_id tuple; no unrestricted global listing. Issue has nullable
task_id; null is permitted only for action_override with resource policy authority.
Task provenance cannot select or exclude another protecting binding.
POST exception records have server-assigned ID, actor and timestamps. Exception records carry a monotonic revision (issue=1, first revoke=2),
supplied as expected_exception_revision on revoke.
task_view.exceptions and action_preview.exceptions contain only currently valid,
applicable exceptions after server-side authority, scope, expiry and revocation
checks. Expired/revoked records remain readable through exception lists/history;
their existence alone is never a valid waiver marker.
Issue/revoke advances the exception revision and invalidates cached availability,
not the predicate dependency/evidence digest or contract semantics;
it MUST NOT change a gate's met/unmet status. Clients reread availability after either operation; final dispatch validates the
current exception revision/expiry/revocation independently of the predicate ledger. An already revoked exception can be replayed with the
same key; a new-key stale CAS conflicts.

Idempotency scope is authenticated tenant+actor/delegation+canonical operation+
resource, independent of route aliases (task and PR submit MUST converge).
Canonical input includes every semantic typed field, resolved target, intent, expectation
and exception selection; key and transport profile are excluded. Alias spellings and
path-only provenance are normalized to the canonical target before hashing, so the
task/PR forms cannot create independent effect keys for equal canonical requests. Equal key+input
returns the original operation/resource ID and response, even after an edit/head
change; authorization is still rechecked and a now-denied replay returns 403.
Different input with that key is 409 idempotency_conflict. Accepted and blocked
attempt keys are retained >=7 days and through any live/uncertain effect; immutable
receipts and audit survive for the BG-1 audit lifetime. Within retention a retry
MUST NOT create another task, review assignment, CI attempt, reply or effect.
After retention clients MUST read domain status first; an expired key cannot
silently re-execute a protected operation (409, refresh required). Reads are
idempotent; previews have no effects and can be retried.

Audit includes request/operation IDs, authenticated actor/delegation, canonical
input digest, source before/after revisions, reason, timestamps, every denied,
blocked, stale, dispatched and settled attempt, evidence/exception IDs and
invalidation cause. Secret payloads are excluded. Authorization failures use the
security audit without leaking private history to the denied caller.

## Current evidence, asynchronous work and protected effects

GET /api/gates/evidence?id=...&version=... reads one immutable exact evidence
record with typed facts for all fourteen evidence kinds (including check attempts,
review findings/carry-forward, eligible approval identity, report/receipt version,
child outcome, health/policy coverage and operation receipt). Identity/version and
the complete captured dependency set accompany the value; no arbitrary JSON fact
payload is permitted. The source read authority and task/resource visibility are
required at lookup. A missing/unknown/inaccessible version returns 404; a known
authorized payload expired by retention returns 410 evidence_expired/refresh (identity
audit remains). This read never recollects or makes historical evidence current.
Its version must match the accepted result citation, not just a familiar check name.
Facts are bounded as in the schema and BG-3; an incomplete source remains unknown.

task_view.results carries versioned EvaluationState with disposition, invalidation
reason/time, dirty/covered generation and retry time. Result.status=met is not
current acceptance. Only disposition=accepted with matching current token, full
dependency union, current expiry and no dirty gap can contribute; invalidated met
is history. task_view.blockers also includes controller-owned pending questions,
permissions, acknowledgment, child failures, live operations and message fences.
The decision and legacy_state are projections, never writable completion authority.
Legacy completion-only tasks return authority=legacy and contract/decision=null.
Initial shadow monitoring uses the separate gate_observations projection above,
with empty authoritative result/action slots; it retains existing concrete goal
and finish semantics. Separately admitted resource action policy still applies
independently and must not be inferred from diagnostic observations.

Reevaluate records an operation and marks selected bindings dirty; [] selects all.
It does not launch an agent, CI or review, approve/merge, process messages or close
a task in the HTTP handler. Aggregation evaluates the full required union before
acceptance; selected gates cannot leave an old incompatible result usable. The
central owner may subsequently close/rest only after its own full fences.
Reevaluation is useful evidence collection, not progress or a trigger occurrence.
Same-key retries reuse one evaluation request; coalesced requests can share result
IDs but retain distinct audit/request identities.

202 always means a durable operation in requested state, with no assertion that
the external action has started or succeeded. Location points to
/api/gates/operations/{operation_id}; GET returns current revision/state:
* requested: intent committed, no executor started;
* executing: a fenced owner started work (external dispatch may have occurred);
* completed: durable outcome recorded; submit requires succeeded/no_effect receipt;
* blocked: admission found an unsatisfied/stale/incomplete prerequisite, no effect;
* failed: known failure with no uncertain effect, error/recovery included;
* outcome_unknown: effect may have happened; resource remains held for reconciliation;
* canceled: unstarted work canceled by policy/revocation/context invalidation.

Operation.submit_intent and receipt.intent distinguish approve_and_queue from
execute for every reader; operation_link.submit_intent declares which submission
mode a rendered action offers (null for other operations).
A submitted PR approve_and_queue operation may complete with a no_effect receipt
meaning exact-head approval and queue enrollment were recorded. This mode may
record an eligible person's approval/enrollment while checks or reviews are
unmet/pending, preserving current Submit behavior including requesting fresh
nonpassing Grain CI. Its rendered head/policy/evidence token must still be current,
and it does not override any blocker. Action preview.available describes protected
execution; its approve_and_queue submission link may remain enabled when execution
is blocked. Missing policy identity/coverage, permission or eligible person still
refuses the approval request. It MUST NOT carry
a succeeded integration receipt. A separate queue execution operation/receipt with a new request ID/key records
merging. The queue derives a fresh execution context/evidence token under its
current automation/delegation authority and retains the exact approved head. It
cannot reuse the approval-enrollment receipt as execution evidence or advance to
a new head without a new eligible approval. All action guards and expected evidence
versions are checked at final execution. execute is available only to a person with current execution
authority and an admitted adapter; it never adds implicit approval. Missing prior
approval blocks it. For CL, intent must be execute (approve_and_queue is 422 invalid); person-only
submit uses exact reviewed snapshot,
temporary grant and provider requirements; unsupported exact-snapshot exclusion
returns 503 boundary_unavailable, never a pretend generic merge. Non-submit
completion means its domain mutation/collection finished; external_ids points to
the review task or CI run, whose successful result is still pending. requested
and executing operations have receipt=null; errors do not masquerade as receipts.

Polling GET operation is read-only and permission-filtered; use events or 1..30s
backoff. Canceling an HTTP connection does not cancel a committed operation. Reads
and precommit validation have <=30s server deadline; evaluation <=10s/aggregate
30s. If timeout precedes durable commit, return 504 with no operation ID; retry
same key. If committed, return 202 if possible; lost response is recovered using
same key. An effect timeout sets outcome_unknown and returns its operation ID.
Reconcile requests recollection of that effect's existing intent, never submits
it again. Network/5xx retry uses same key with bounded 1..60s jittered backoff.
429 supplies Retry-After seconds (1..60); clients wait. Never turn an uncertain
merge into a fresh key.

Action preview is read-only current availability, not a permit. It returns all
effective bindings/source revisions through their origins, accepted freshness,
caller permission, override markers, blockers and submission contract. available
requires current permission, evidence, policy coverage and enforceable boundary;
observation_only or unknown coverage makes it false. Missing provider, unknown
evidence or no guard coverage is a blocker, not an empty success. Complete explicit
unguarded policy can pass the guard-set test but cannot skip execution permission,
conflicts, expected head or adapter boundary. Approval and completion are distinct.

Every action_request requires request_id, idempotency_key, canonical action/target,
expected_head/resource_version/contract_revision/authority_epoch/policy_digest/
context_digest/**evidence_digest**, and nullable reason. It MUST agree exactly with
mutation.expected and its idempotency key. request_id becomes the stable execution
operation ID (unique within tenant; no caller can read another actor's operation).
Expected head is an exact opaque reviewed commit/snapshot, not a branch.
Previews don't choose the request_id; clients choose a new <=256 character ID.
PR alias must canonicalize to the same target, and task submit must resolve that
task's specific resource, never choose the first of an ambiguous set.

Immediately before admission and again before external dispatch, the shared
domain service reauthorizes, unions policy across all protecting tasks, takes the
resource/effect fenced lock, revalidates core and complete evidence/dependency
versions and persists intent. The remote write uses expected head/exact CL
snapshot. Queues repeat these checks at final execution. Head H2 after rendered
H1 is 409 stale_head and no dispatch; evidence/review/policy/authorization races
are stale_evidence/conflict, even when CI still displays green. A new live read
cannot silently change the expected context. External review/base races require
forge constraints/exclusion in addition to the head conditional; if they cannot
be guaranteed, boundary_unavailable refuses enforced execution. A receipt means
only its recorded disposition; positive external integration can later satisfy
completion but is attributed external, not guard-authorized.

## Exceptions, configuration and delivery

config_digest for an exception hashes the sorted array of complete canonical
attachment JSON for exactly its binding_ids, including evaluator type/version,
materialized config, resolved subject, source and purpose. A multi-binding
exception cannot substitute one gate's config hash for the whole named scope.
Completion waivers name exact completion bindings and their subject, config,
contract/policy/evidence digest and expiry <=7 days; head is null only for a
non-change subject. They cannot suppress unprocessed messages, pending questions,
permission requests, live writers or unsettled effects. Required-child outcome/
membership changes invalidate affected waivers. A failed child remains unmet;
the person must explicitly waive that obligation, repair/retry or cancel.
Waiver neither edits the contract nor permits merge.

Action overrides name **all** unsatisfied eligible checks/review bindings, exact
target/head, current config/policy/evidence and one operation_id; expiry <=1h.
For issue.kind=action_override, action_request must be non-null and operation_id
must equal action_request.request_id; head/target/expectations must agree.
Issuance reserves that action's canonical input against the ID without dispatch.
The later submit key/request must match; eligible override IDs are resolved by
the server from that reservation and included in the immutable receipt. Issuing
does not create a merge request. Completion waiver has operation_id/action_request
null. Revocation before dispatch cancels unstarted intents; dispatched/uncertain
operations reconcile and cannot be claimed rolled back. Expires/head/policy/config/
required-child/auth changes invalidate exceptions at use. Approval, caller
permissions, conflicts, unknown identity/provider/evidence, incomplete coverage
and boundary failure cannot be overridden. Legacy force/base repair requires
explicit imported eligible bindings; own CI and other guards remain mandatory.

Repo settings view=gates reads/writes one complete gate_policy via the existing
settings service/export path. Live policy edits advance the policy revision,
invalidate indexed resource decisions immediately, preserve protecting bindings
after task closure/deletion and require explicit retirement. Task detach cannot
retire a resource guard. repo_edit.retire_guard_bindings (required array, empty for
an ordinary settings edit, <=64 entries) explicitly retires exact binding_id,
canonical target and action tuples from the persistent resource index. It requires
the owning repository/source-policy authority and the current full policy digest,
which fences index membership as well as the repository row. Target must be a
concrete change in that repository, not task_changes or *. Retirement atomically
removes only the named protection, invalidates decisions/unstarted intents and
records source/actor/reason; it does not delete historical contracts or waive
completion. A conflicting index edit is 409; retiring the last guard requires
explicit authorized unguarded policy or execution remains unknown/held. A repository policy source/revision cannot be forged by
a task request. Resource-specific policies use exact selectors in this same
repository-owned policy; no redundant resource configuration endpoint is added.
Template composition view=gates returns pinned gate_defaults; an undeclared
legacy template returns null. Template edit changes future filing defaults only.

Template ID/revision must both be non-null or both null; without a template,
fields must be empty. A supplied template lifecycle must exactly echo its pinned
defaults; the filing profile cannot weaken it. Existing task-admission/person-only
rules for standing intent and lifecycle budgets still apply. The gate profile
cannot add grants or bypass approval/scheduling by choosing a lifecycle value.
Preview resolves repository completion defaults, pinned template defaults and
task policy (union, never replacement) with typed declared field substitution.
No arbitrary expressions/URLs; self resolves at filing. repo=null with
no_repo=true preserves standalone tasks and applies no repository defaults;
repo non-null requires no_repo=false. A repository-scoped template cannot
materialize a standalone task. Body repo/no_repo/template/fields
must match the materialize object at filing. Preview expires within 300 seconds;
digest covers registry, complete source revisions, inputs and resolved contract
with the reserved self placeholder. Filing validates the exact preview against
current sources and atomically allocates ID, replaces self, stores materialized
defaults/provenance/authority and gate contract with the unapproved task.
The existing /api/tasks/from-template alias has the same gate profile and atomic
filing/idempotency semantics, restricted to its repository-declared template and
existing nested task admission/grants. Its body remains <=64 KiB and approved=false.
The server canonicalizes its stem/full template name, requires matching preview
repo/template/fields and forwards the preview/key without dropping gates. Existing
template creation remains ordinary POST /api/templates; install gate_defaults via
the conditional template_edit profile before opting in to filing. Legacy filing
against a template with gate defaults cannot omit materialization: return 428 to
an old client lacking the required preview, rather than create an ungated task.
Changed sources or expired preview return 409; invalid/unknown type, selector,
scope, duplicate qualified ID, over-limit or cyclic requirement returns 422.
There is no half-filed task. Existing-template migration is an explicit conditional
contract edit with before/after source revisions, person authority and lifecycle
epoch handoff under BG-8; preview/filing never silently migrates existing tasks.
No goal field/name or autonomous goal-mode tool is removed or repurposed.

History/exception lists use optional limit (default20, 1..100), cursor (<=1024),
exact binding_id/kind filters on history, kind/active plus the required task or
resource selector on exceptions. active defaults
false (all records, including revoked/expired); authorization/filtering precedes
pagination. History kinds are the history_item enum; unknown filter is 400.
Order is sequence ascending; exception order uses issuance sequence then ID.
First page latches an immutable snapshot revision/high-water mark. Cursor binds
actor visibility revision, task, filters and snapshot; mismatch is 409, retention
expiry 410 cursor_expired (refresh from current view). Next cursor null means
complete through that snapshot; concurrent arrivals appear in a new traversal.
No totals can reveal hidden objects. Task current projection is bounded by BG
contract limits and is not independently paged into a partial decision.

GET /api/events reuses existing ordered event/outbox, repo filtering, long-poll
and SSE delivery. New types: task.gates, task.gate_contract, task.gate_exception,
task.gate_operation. They carry only IDs, timestamp, safe summary and operation
reference (externalRef when applicable), no evidence payload/secret/private text.
Existing unrelated event types keep their current schemas; this API schema's
events profile selects only the four new types via required types. since accepts
existing decimal cursor or now. Omitted/empty starts at the current authorized
baseline for SSE/long-poll and at the beginning for immediate JSON;
wait default0 <=25 seconds, stream=1/true (or Accept: text/event-stream) selects
SSE, stream=0/false selects JSON, otherwise existing Accept negotiation applies; limit default100 (agent20)
<=500. JSON cursor reflects authorized delivered events; the server may track a separate
internal scan position past invisible events without exposing their IDs;
truncated/nextCursor means more pages, not a complete evidence collection.
SSE emits id/event/data using the same gate_event object, keepalive every15s;
Last-Event-ID resumes when since omitted, explicit since wins. No acknowledgment
or subscription creation mutation exists. Redelivery is possible; dedupe by event
ID then refresh authoritative view. Long-poll timeout returns 200 empty events,
cursor and timedOut=true, never gate success. Cursor auth/retention recovery uses
the existing event service's checks; clients refresh on a gap/reconnect and resume
from the returned cursor. Durable dirty markers and collector reconciliation,
not browser delivery, own wakes. Unreadable source, missed event or expired timer
cannot turn a required gate green.

## Status/error contract

Every gate-profile failure has the complete failure schema. The per-operation
examples show authorization denial; the shared examples supply other exact bodies.
Status precedence: authenticate, authorize without revealing existence, decode
bounds/shape, idempotency lookup, CAS/context, semantic admission, dispatch.
No error body is an accepted evaluation result or action success.

| HTTP | code / recovery | Required behavior |
| --- | --- | --- |
| 400 | invalid / fix_request | Invalid JSON/query/unknown field; safe JSON pointer when known. |
| 401 | unauthenticated / authenticate | No trusted actor; no mutation. |
| 403 | forbidden or person_required / request_access or ask_person | No bypass by changing task/tool/alias. |
| 404 | not_found / fix_request | Missing or inaccessible object; registry unknown version also 404. |
| 409 | revision_conflict, stale_head, stale_evidence / refresh | Current readable token returned; refresh and reconfirm; no effect. |
| 409 | idempotency_conflict / fix_request | Changed payload under old key; no second request. |
| 409 | blocked / refresh | Trustworthy unmet/pending/unknown required condition; blockers in current view. |
| 409 | outcome_unknown / poll_operation | Existing effect uncertain; operation ID required, resource held. |
| 410 | cursor_expired or evidence_expired / refresh | Restart traversal/current projection; not proof of no history. |
| 413 | too_large / fix_request | Byte/depth/record bound exceeded; no truncation into success. |
| 422 | unsupported, incomplete / upgrade or fix_request | New config/contract cannot be admitted; no missing gate silently dropped. |
| 428 | upgrade_required / upgrade | Legacy unversioned client attempts protected effect on migrated resource/task. |
| 429 | rate_limited / wait | Retry-After; keep same key. |
| 503 | unavailable / same_key | Temporary provider/store/coverage outage; existing operation ID if committed. |
| 503 | boundary_unavailable / ask_person | Enforced adapter/bypass proof unavailable; operation held, no unsafe fallback. |
| 504 | deadline / same_key | Precommit deadline; if effect might exist use outcome_unknown operation instead. |

A missing/unknown provider at evaluation time returns unknown/unavailable in a 200
task view, not 404 on the task. A technical evaluator failure is error in the
diagnostic result, never an HTTP successful effect. New attachment of unknown
type/version is 422; unsupported persisted contracts hold and project unknown.
Current legacy routes may use their existing string-error shape outside this
profile; new gate-aware tools consume structured failure. On migrated tasks,
unversioned close/Submit/findings/force calls fail 428 if they could change
protected state without required expectations; no old alias can evade admission.
Legacy task completion retains its old behavior until authorized epoch migration.
An old daemon cannot process migrated tasks/resources and MUST hold them.
Normal task detail/list responses gain only optional gate_summary validating as
api.schema.json $defs.gate_summary (absent before migration, not synthesized legacy
success);
they must not overwrite concrete goal, state, approval, permission or scheduler
fields. Both classic and Angular use the same central authoritative operations.

## Operation index

The following table is a readable index of the machine-readable contracts.
Request/response links select complete schema definitions; the operation's entry
in api-examples.json is its full success and error exchange. Mutations share the
CAS/idempotency/audit and asynchronous rules above unless a stricter rule applies.

| ID / status | Exact HTTP operation / tool | Authority (plus ownership) | Complete request → response / success |
| --- | --- | --- | --- |
| registry / proposed | GET /api/gates<br>api_list_builtin_gates | tasks:read; agent/person | [registry_request](api.schema.json#/$defs/registry_request) → [registry_response](api.schema.json#/$defs/registry_response) / 200 |
| schema / proposed | GET /api/gates/{type}/{version}<br>api_get_builtin_gate | tasks:read; agent/person | [schema_request](api.schema.json#/$defs/schema_request) → [schema_response](api.schema.json#/$defs/schema_response) / 200 |
| task / changed | GET /api/tasks/{id}<br>api_get_task | tasks:read; agent/person | [task_request](api.schema.json#/$defs/task_request) → [task_response](api.schema.json#/$defs/task_response) / 200 |
| edit / changed | PATCH /api/tasks/{id}<br>api_update_task | tasks:write; agent/person | [edit_request](api.schema.json#/$defs/edit_request) → [edit_response](api.schema.json#/$defs/edit_response) / 200 |
| evidence / proposed | GET /api/gates/evidence<br>api_get_gate_evidence | tasks:read; agent/person | [evidence_request](api.schema.json#/$defs/evidence_request) → [evidence_response](api.schema.json#/$defs/evidence_response) / 200 |
| history / proposed | GET /api/tasks/{id}/gate-history<br>api_list_gate_history | tasks:read; agent/person | [history_request](api.schema.json#/$defs/history_request) → [history_response](api.schema.json#/$defs/history_response) / 200 |
| reevaluate / proposed | POST /api/tasks/{id}/gate-evaluations<br>api_evaluate_task_gates | tasks:write; agent/person | [reevaluate_request](api.schema.json#/$defs/reevaluate_request) → [reevaluate_response](api.schema.json#/$defs/reevaluate_response) / 202 |
| operation / proposed | GET /api/gates/operations/{id}<br>api_get_gate_operation | tasks:read; agent/person | [operation_request](api.schema.json#/$defs/operation_request) → [operation_response](api.schema.json#/$defs/operation_response) / 200 |
| action_preview / proposed | POST /api/gates/action-preview<br>api_preview_guarded_action | tasks:read; agent/person | [action_preview_request](api.schema.json#/$defs/action_preview_request) → [action_preview_response](api.schema.json#/$defs/action_preview_response) / 200 |
| submit / changed | POST /api/tasks/{id}/submit<br>api_submit | tasks:write; person only | [submit_request](api.schema.json#/$defs/submit_request) → [submit_response](api.schema.json#/$defs/submit_response) / 202 |
| pr_submit / changed | POST /api/pull-requests/submit<br>api_submit_pull_request | tasks:write; person only | [pr_submit_request](api.schema.json#/$defs/pr_submit_request) → [pr_submit_response](api.schema.json#/$defs/pr_submit_response) / 202 |
| exceptions / proposed | GET /api/gates/exceptions<br>api_list_gate_exceptions | tasks:read; agent/person | [exceptions_request](api.schema.json#/$defs/exceptions_request) → [exceptions_response](api.schema.json#/$defs/exceptions_response) / 200 |
| issue / proposed | POST /api/gates/exceptions<br>api_issue_gate_exception | tasks:admin; person only | [issue_request](api.schema.json#/$defs/issue_request) → [issue_response](api.schema.json#/$defs/issue_response) / 201 |
| revoke / proposed | POST /api/gates/exceptions/{id}/revoke<br>api_revoke_gate_exception | tasks:admin; person only | [revoke_request](api.schema.json#/$defs/revoke_request) → [revoke_response](api.schema.json#/$defs/revoke_response) / 200 |
| repo_read / changed | GET /api/repos/{owner}/{name}/settings<br>api_get_repo_settings | repos:read; agent/person | [repo_read_request](api.schema.json#/$defs/repo_read_request) → [repo_read_response](api.schema.json#/$defs/repo_read_response) / 200 |
| repo_edit / changed | PUT /api/repos/{owner}/{name}/settings<br>api_set_repo_settings | repos:write; person only | [repo_edit_request](api.schema.json#/$defs/repo_edit_request) → [repo_edit_response](api.schema.json#/$defs/repo_edit_response) / 200 |
| template_read / changed | GET /api/templates/{id}/composition<br>api_template_composition | templates:read; agent/person | [template_read_request](api.schema.json#/$defs/template_read_request) → [template_read_response](api.schema.json#/$defs/template_read_response) / 200 |
| template_edit / changed | PATCH /api/templates/{id}<br>api_update_template | templates:write; agent/person | [template_edit_request](api.schema.json#/$defs/template_edit_request) → [template_edit_response](api.schema.json#/$defs/template_edit_response) / 200 |
| preview / proposed | POST /api/tasks/gate-preview<br>api_preview_gate_contract | tasks:write; agent/person | [preview_request](api.schema.json#/$defs/preview_request) → [preview_response](api.schema.json#/$defs/preview_response) / 200 |
| file / changed | POST /api/tasks<br>api_create_task | tasks:write; agent/person | [file_request](api.schema.json#/$defs/file_request) → [file_response](api.schema.json#/$defs/file_response) / 201 |
| from_template / changed | POST /api/tasks/from-template<br>api_create_task_from_template | tasks:from-template, tasks:write; agent/person | [from_template_request](api.schema.json#/$defs/from_template_request) → [from_template_response](api.schema.json#/$defs/from_template_response) / 201 |
| review / changed | POST /api/pull-requests/review<br>api_review_pull_request | tasks:write; agent/person | [review_request](api.schema.json#/$defs/review_request) → [review_response](api.schema.json#/$defs/review_response) / 202 |
| ci / changed | POST /api/ci/runs<br>api_start_ci_run | ci:write; agent/person | [ci_request](api.schema.json#/$defs/ci_request) → [ci_response](api.schema.json#/$defs/ci_response) / 202 |
| rerun / changed | POST /api/ci/runs/{id}/rerun<br>api_rerun_ci_run | ci:write; agent/person | [rerun_request](api.schema.json#/$defs/rerun_request) → [rerun_response](api.schema.json#/$defs/rerun_response) / 202 |
| reply / changed | POST /api/tasks/{id}/reply<br>api_reply_task | tasks:write; person only | [reply_request](api.schema.json#/$defs/reply_request) → [reply_response](api.schema.json#/$defs/reply_response) / 202 |
| ack / changed | POST /api/tasks/{id}/close<br>api_close | tasks:write; person only | [ack_request](api.schema.json#/$defs/ack_request) → [ack_response](api.schema.json#/$defs/ack_response) / 202 |
| findings / changed | POST /api/tasks/{id}/accept-findings<br>api_accept_findings | tasks:write; person only | [findings_request](api.schema.json#/$defs/findings_request) → [findings_response](api.schema.json#/$defs/findings_response) / 202 |
| reconcile / proposed | POST /api/gates/operations/{id}/reconcile<br>api_reconcile_gate_operation | tasks:write; agent/person | [reconcile_request](api.schema.json#/$defs/reconcile_request) → [reconcile_response](api.schema.json#/$defs/reconcile_response) / 202 |
| events / changed | GET /api/events<br>api_events | tasks:read; agent/person | [events_request](api.schema.json#/$defs/events_request) → [events_response](api.schema.json#/$defs/events_response) / 200 |

## Generation and documentation validation

The current authoritative registrations are [server.go](../../../pkg/ui/server.go),
[route_scopes.go](../../../pkg/ui/route_scopes.go) and
[agent_api_tools.go](../../../pkg/ui/agent_api_tools.go). Tools derive names from
handleX handlers (api_ + snake(X)), with stable collision resolution. Future handlers
for proposed routes MUST use the exact index tool names; no separate hand-maintained
agent execution API is authorized. Each gate profile must register its typed body,
query (including view), description and field docs in routeBodies, routeQueries,
routeDocs and routeFieldDocs; scope-totality and body/query generation checks apply.
The current generator has one reflect-derived body type per route and string
query metadata; it does not yet advertise these query-dependent closed profiles.
Implementation MUST add profile-aware schema metadata/selection to the existing
generator: oneOf(ordinary request with query.view != gates, exact gate-profile
request). Proposed routes use their exact request definition directly. Preserve
the wrapper reason/expect semantics, path parameter strings and typed query-to-URL
conversion. Old schemas/ordinary responses stay in the ordinary branch; no second
agent execution tool or weaker permissive body branch is allowed for view=gates.
This generator change and gate-profile transport/error serialization are explicit
implementation blockers, not functionality claimed by this documentation.
The normalizers/semantic checks described here run after generated schema checks.
Both UIs, CLI and controller MCP call those same domain operations and authorization.

This PR changes no runtime registrations or generated Angular routes. When runtime
routes are later implemented, regenerate ui-ng/src/app/api/generated-routes.ts with
GRAIN_UPDATE_NG_ROUTES=1 go test ./pkg/ui -run TestAngularRouteTableInSync, and run
TestRouteScopesAreTotal, TestGeneratedToolsMatchTheRouteTable,
TestEveryScopedRouteIsDescribed and TestToolSchemasTypeBodiesAndQueries. Operation
index validation already checks claimed existing paths/scopes/tool names against
these source registrations. API payloads are explicit schemas here; implementing
a route does not permit replacing them with an untyped argument map.

## End-to-end flows

1. **Blockers to guarded merge:** api_get_task(view=gates) reports checks unmet and
   review pending with exact remediation contracts. Agent calls api_review_pull_request
   for the requirement/head or api_start_ci_run/api_rerun_ci_run under its existing
   grants. 202=requested; follow operation external_ids to the review task/CI record.
   CI/review collectors dirty evidence and emit task.gates. Agent calls
   api_evaluate_task_gates, waits for completed, then rereads gates/action preview.
   Eligible person calls api_submit approve_and_queue with current expected head
   and evidence; the response records queue intent, not merge. At final execution
   the queue reauthorizes/revalidates all guards and conditional head, emits an
   immutable succeeded receipt, then integration observation can satisfy completion.
   A review report with findings can be delivered successfully while merge remains
   blocked; receipt/ack/children still determine task closure.
2. **Authorized override:** checks are definitively failed, review is clean, approval
   and permission are current. Person with contract/source authority, merge:force
   and repo-admin permission issues a <=1h action_override naming exact eligible
   check bindings and future request_id. Server reserves canonical action input,
   returns exception record, not dispatch. Person refreshes the token and submits
   the reserved action (reservation is validated against current evidence; issuing
   the exception itself does not change predicate/evidence digest). Guard admission
   records exception IDs and executes only that exact head once. Revocation/expiry/
   H2 blocks an unstarted request. A completion waiver has no role in this flow.
3. **Stale-head refusal:** person rendered H1 and evidence E1; push creates H2.
   Submit H1/E1 returns 409 stale_head with readable H2 token; no merge dispatched,
   old approval/buttons/override invalidated. Client refreshes, obtains H2 review/
   approval/check evidence and uses a new request ID/key. Same-key retry of the old
   request returns the original refusal; it cannot retarget H2.
4. **Pending question and receipt:** delivered report v2 has a pending user question.
   task_view activity is needs_answer and close cannot process it. Eligible person
   replies via api_reply_task with exact question version; 202 records requested,
   completed means reply stored and wake queued, not that the new work was done.
   Agent processes the incoming watermark and delivers report v3. Person acknowledges
   report v3 with api_close(disposition=acknowledge); v2 receipt is stale. Central
   lifecycle closes only after all gates and message/live-operation fences pass;
   a standing task rests instead. Agent never answers/acknowledges on the person's
   behalf. A permission request uses request_access, whose Grant changes authority,
   not report receipt.
5. **Failed required child:** children(success) sees child failed with versioned
   outcome, returns unmet and parent remains open while independent work continues.
   Child-of/advisory relation alone is not required. Parent may repair/retry child;
   success at a new outcome generation invalidates old evidence and reevaluates.
   Otherwise eligible person may issue an exact completion waiver <=7 days or cancel
   parent; it does not authorize merge. A newly attached/reopened child invalidates
   the waiver/closure candidate. Imported children(closed) keeps visibly different
   legacy semantics until an explicit authorized edit.

[API audit and readiness](api-readiness.md) inventories the resolved gaps and
remaining implementation prerequisites; schema validation alone does not prove
runtime, credential isolation, remote race exclusion or implementation readiness.
