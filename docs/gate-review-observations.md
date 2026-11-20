# Review and approval observations

Task #67 extends the observation framework reviewed in Task #64. The Task #59
specification merge `2e45dca71`, Task #61 implementation merge `6bd221161`
and Task #64 review commit `de688e249` were verified as merged ancestors before
extension. That delivery used `builtin-task-observer-v7`; the
[combined audit](gate-coverage-audit.md) records the shared
`builtin-gate-observer-v10` pin. Public gate,
configuration and observation contracts remain v1.

Everything is **OBSERVATION-ONLY**. Existing review/security/path/takeover
reconcilers, Submit, accept-findings, scheduling, waiting, force overrides and
merge services retain authority. A recorded/accepted diagnostic is not an
approval or readiness decision. All bindings are nonrequired monitors with
`action=null`, legacy authority and every effect/cutover flag false. Review and
approval work, actions and waits are empty; human/agent remediation is inert text.
No agent, review, CI request, timer, approval, closure or waiver is created.
There is no evaluator broker, MCP callback or snapshot transport.

## Registered predicates and imported comparisons

The registry pins the canonical closed `review_config` and `approval_config`
schemas. Duplicate/unknown/case-mismatched keys, extra values, invalid categories,
empty or duplicate requirements and unsupported versions are rejected.

| Profile | Pure predicate |
| --- | --- |
| `review/current_head_v1`, ordinary/security/path/takeover | An authenticated eligible review disposition for the exact assignment, head, policy and authorization version; complete findings/questions, base-owned diff classification and linked fix outcomes. Active reviews are pending; failed reviews and deferred/unintegrated/closed-unmerged fixes cannot pass. Missing/denied assignments and incomplete evidence are unknown. |
| Exact-scope review carry-forward | The original review must remain eligible under the same assignment/policy/authorization. Carry-forward additionally names this destination head and policy and proves a complete base-owned scope comparison. A bare carried/cleared stamp cannot substitute. |
| Findings acceptance | An authenticated eligible human's reason and disposition pin this review ID/version, category, assignment, current findings/questions digest, head, policy and authorization version. New findings, reviews, head/policy/role changes invalidate it. Reviewer self-clear is refused. Acceptance cannot integrate outstanding fixes. A subsequent clean review clears only its own scope. |
| `review/legacy_completion_v1` | Explicit imported shadow monitors only. Ordinary review uses the existing closed-or-completed-without-PR predicate, including the existing missing-reference success. Other comparisons label the existing aggregate security/code/targeted gate or takeover clearance; they never claim that an aggregate pass proves one individual assignment. |
| `approval/exact_head_v1` | Every configured requirement has a verified human receipt for the exact rendered and current head, policy, requirement and authorization version. Partial eligibility covers only its named scopes. Missing receipt is pending when the scope/eligibility reads are complete; inaccessible identity/scope is unknown. Revoked/ineligible/stale receipts do not pass. |

Review output, review completion, a person's Submit, security findings acceptance,
report receipt and AutoMerge queue intent are separate facts. None implies the
others. Human-only, required and conditional holds remain owned by their existing
services. Existing named force overrides, base-repair exceptions (including their
own CI requirement) and post-merge review ownership are described as legacy
context, never converted into a new approval or shadow waiver.

## Scoped source adapter and material limits

Selected PR tasks retain the report/receipt pair (with its existing applicability
rules) and add one current and one explicit imported comparison per review
assignment/scope, plus individual approval scopes. Requirements distinguish
ordinary/security assignments, each path/targeted trigger assignment, takeover
assignments, current unfiled gating base rules, named approvers and human holds.
A newly synchronized gating path rule is unknown even if no review has been filed.
No duplicate review filing occurs.

The adapter reads only the authorized task, its own repository's base-owned
trigger/merge declaration records and explicitly linked review/fix/dependency
tasks in that same repository, under the existing bounded diagnostic transaction.
Cross-repository assignments remain inaccessible even for an unrestricted reader:
persisted history therefore cannot expose their bodies, links, head or progress
to a later restricted reader. No deployment-wide States/PathTriggers scan or
external provider call is introduced. Task-read authorization still precedes the
optional projection, including paged/comments-since/summary reads. The complete
sidecar/history additionally requires repository-wide task-read permission;
callers without that permission are refused before projection. Scoped callers
receive current predictions only; full history requires unrestricted task-read
access because older samples can predate a task's repository move. Current cached
resource subjects are reauthorized, and repository authorization is checked in
the collection transaction before cache replay or transitive reads.

Normal assignment/scope changes evaluate immediately; only unsupported persisted
profiles use the explained unknown regeneration path. Imported comparisons declare
only their local predicate dependencies, not unavailable current-head/role proof.
Source fences include task assignments/links, local findings/questions and
acceptances, Submit/named approval markers, related review/fix state and relevant
settlement, comments/events, review tags, base policy contents/commits and
declaration failures. Changes coalesce on the next authorized read and survive
restart. Policy identity hashes only policy inputs, independently of approval or
findings changes. Local review/fix evidence and partial approvals are compared
without allowing their weak legacy predicates to pass current mode.

**The legacy store does not supply an authenticated current provider head,
complete current provider diff/findings collection, or a rendered-head,
current-policy/role-version Submit or accepted-findings receipt.** The adapter
does not manufacture these pins. Consequently current review/approval observations
from this adapter remain **unknown**, even when imported legacy comparisons pass.
The exact-head, carry-forward, acceptance and partial/revoked approval predicates
are implemented and tested over typed immutable facts; positive provider-backed
runtime qualification is not claimed. Empty local link collections are not proof
of an externally clean review. A CI commit, closed review, clean takeover count,
carried stamp, accepted-findings audit link or external approval comment cannot
fill that gap. No unversioned role read is represented as current eligibility.

This extension projects the task's primary PR only. Multiple own change links
make the collection explicitly incomplete; no omitted change can gain a pass.
CL provider snapshots/authority and approval receipts require their existing
provider adapter's exact scoped evidence and are not projected by this PR reader.
The registered predicates admit canonical change subjects, including CLs, but
do not invent PR-head semantics for CL snapshots. Enforcement, rendered-head
mutation admission, role/receipt issuance, automatic reevaluation and authority
handoff remain deferred.

Collection is bounded to 31 review/approval scopes, 64 related tasks, 64 base
trigger rows and 65536 characters per declaration. The displayed projection
shares a 64-monitor budget with outcome and CI observations; overflow is explicitly
incomplete. Each inspected task has at most 512 links and 65536 bytes of linked metadata admitted to diagnostic hashing. Overflow/malformed data is explicitly
incomplete and cannot yield a current pass. Retention and load bounds remain
60-second leases, the 250ms collection/50ms error budgets, 32 selected tasks,
72 hours/1024 samples per task/32768 globally, at most 20 newest samples per read, subject to the shared 512 KiB history-view budget (two full 64-binding views; the newest row is always inspected), and
hourly stable-source samples. Errors preserve validated cached scopes but discard
accepted evidence and predictions. Failed/old/unsupported collections retain the
reviewed retry, ordering, lock and retention rules. Read sampling and eviction
can shorten an observation window.

## Display and validation

The Angular observation card distinguishes **Current-scope prediction · Shadow
only** from **Imported legacy comparison · Shadow only**. It shows each scope's
status, missing evidence, local findings/fix counts, legacy differences, source
and configuration versions, completeness/expiry, bounded history and inert
agent/person guidance. Canonical 240-character summaries overflow into bounded
guidance. It adds no mutation controls. Existing Submit and Accept findings
controls retain their original service behavior and permission checks.

Pure tests exercise every review category; exact-head and approved carry-forward;
complete/empty/denied/missing/stale data; active/failed reviews; deferred/failed
fixes; new findings/questions; exact human acceptance and self-clear refusal;
partial approvals, stale rendered heads, assignment/policy/authorization changes,
revocation and forged/unattributed comments. Canonical schema pins and denial
tests cover the new profiles.

Shared SQLite and real PostgreSQL tests use actual task, observation, finding,
approval, acceptance, declaration and linked-fix records. They verify source
invalidation, restart/replay, error recovery, permission-safe persistence and
unchanged legacy tables/readiness/outboxes/wakes. API tests preserve authorization,
paged evidence and existing person Submit without accepting findings. Component
tests check legacy/current labels, partial approval text and inert controls.
The canonical example validators and actual ordinary API observation payloads
are checked against the unchanged v1 schemas. These checks qualify the stated
observation adapter; they do not prove future enforcement or positive provider
receipt coverage. No rollout or observation window is started by this change.
