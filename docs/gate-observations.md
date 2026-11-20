# Built-in gate observations

This is an observation-only implementation of Task #59's `grain.gates/v1`
contracts. The independent [framework review and extension guidance](gate-observation-review.md)
records repairs and extension conventions. The registry now implements thirteen
v1 types: all `delivery` products (`report`, `change`, `review`,
`nothing_needed`, `any`) with current/legacy finish semantics; all four
`acknowledgment` profiles; `integration` for PR merge and all scoped CL submission;
and explicitly selected `children` success/closed/merged predicates. The shipped `checks` observer and existing canonical `review` and `approval` observers are also retained; the
QA stage and release qualification observers are also registered, with source limitations recorded in [the QA/release audit](gate-qa-release-observations.md). See the [review/approval coverage
guide](gate-review-observations.md) for its source adapter and provenance gaps. Closed schemas are pinned to the
canonical catalogue and pure local Go evaluators. There are no remote evaluator
calls or private evaluator state.

See [CI checks in shadow](gate-check-observations.md) for its source and permission boundaries.

The four [health profiles](gate-health-observations.md) observe existing base,
nightly, publishing and pipeline goals with explicit legacy/current labels.

Legacy services still own every task lifecycle, queue/run/wait, approval, report,
acknowledgment, merge and submit operation. There is no authority migration or
cutover code. Every binding is a nonrequired monitor; all effect flags are false.
Diagnostic acceptance means the observation was recorded, not task readiness,
successful completion, or permission to act. Work/actions arrays are always empty.
Proposed remediation and waits are inert descriptions with no buttons.

Independent isolated qualification and the read-only 24–48 hour inspection
checklist are in [the end-to-end qualification report](gate-e2e-qualification.md).
This qualification did not start a production observation window. Restored history
must retain the observation-only boundary: unsafe, malformed or oversized samples
are withheld with `unknown_invalid_history` diagnostics, while safe older-build
provenance remains readable. Evaluator errors are rejected diagnostics, never
accepted predicates; source changes or the recorded lease allow a later read to
retry without creating work or installing an operational wait.

## Shipped read surfaces

The existing permission-filtered `GET /api/tasks/{id}` adds optional
`gate_observations` (the closed canonical observation_view) and sibling
`gate_observation_diagnostics`. Ordinary whole, paged, comments-since and task
summary reads reuse the same projection; pagination never supplies gate evidence.
The ordinary `api_get_task` read carries the additive fields. Scoped callers
receive only the authorized current projection; retained diagnostic history
requires unrestricted task-read access because samples can predate repository
moves. Authorization is checked before collection/cache replay, and cached
resource subjects are reauthorized before display. Angular task detail
at `/next/tasks/<id>` shows status/explanation, source/config/build provenance,
evaluation/evidence versions, completeness/freshness, errors, inert predictions,
retained comparison counts and newest 20 samples. The classic UI remains compatible.
There is no new read route, registry-discovery route, `?view=gates` profile,
mutation/reevaluation/action/waiver endpoint or MCP evaluator.

## Bounded selection and evidence

Collection/display is disabled by default. A deployment-local explicit allowlist
selects at most 32 task IDs. There is no automatic cohort expansion or lifecycle
hook. Choose a small representative set of finite implementation, review,
report-only child and coordinator tasks. Standing/interactive/console policies
remain controller behavior and produce explained unknown completion predictions.
The original report/receipt pair remains visible. Finite, non-interactive
`implement` tasks whose latest settled run produced no change link
(`AwaitsAck` true, not a review and not a `nothing_needed` run) and ended on a
durable closing note are projected as report products for
`delivery(report/current_v1)` and `acknowledgment(explicit_receipt)` alongside
`analyze` tasks (such as upgrade checks). Settled tasks with a change link
(`LinkFixes` or `LinkChangelist`), review tasks (`IntentReview` or
`ReasonReview`), and `nothing_needed` runs are never projected as report
products or explicit report receipts even when they carry a closing comment.
Implementation/review/child and validated no-work shapes also expose selected
product and compatibility monitors.
Existing goal declarations propose monitor-only mappings: `pr-merged` to exact
PR integration, `cl-submitted` to every scoped CL (or its explicit target), and
`tasks-closed` to the original `closed`/`merged` predicate. Stored goal `MetAt`
never substitutes for fresh predicate evidence. Limits, close/rest, person-closes,
message delivery and actual waits stay with their existing controllers.

Selected existing health goals use their health profiles in place of these
outcome/change/child comparisons; those goals acquire no unrelated obligations.

Optional `GRAIN_GATE_OBSERVATION_CHILDREN` process configuration is a JSON object
from allowlisted parent IDs to at most three canonical children configurations,
for example `{"123":[{"tasks":["456"],"predicate":"success"}]}`. It does not
add requirements or waits; the task allowlist independently controls display.
At most 64 distinct children (including a legacy goal selection) and four child
predicates are read per task. Empty sets, duplicate IDs, malformed profiles and
oversized selections are rejected. A spawned/provenance-only relation is never
selected automatically. These are supported read-only configuration surfaces,
not changes to this deployment's environment or grants.

Scoped internal reads capture one task's relevant shape, legacy state/settlement,
latest run, latest own non-handover agent/Grain-relayed comment, comment-set fence,
attachment presence and exact current close event in one database transaction.
A body report must be nonempty, at most 8192 characters, associated with the latest
settled run and at/before its completion. Attachment-bearing reports and comments
that cannot be correlated with settlement are incomplete/unknown, with specific
reasons. They cannot silently pass. An ordinary person's reply is not a report or
receipt. A complete empty report collection is unmet for delivery and unknown for
receipt. Live producers are pending for delivery.

Receipt requires a nonempty verified human identity on the close event at the
current closure timestamp, strictly after both the current report and settlement.
Equal timestamps cannot prove ordering and remain unknown, including a revised
report written after a close with the same timestamp. Legacy unattributed,
fallback or
automation closes are unknown, not synthetic receipts. A new report/run, question,
message, attachment, relevant task shape, or registry/config build changes the
source fence; old receipt observations cannot silently cover a changed report.
These body/comment and close-event records retain the conservative receipt
boundary from the review: legacy close events have no rendered-report/current-role
pins and are not a future enforcement receipt. The acknowledgment compatibility
profiles keep person receipt, provenance-backed report-only child answer, and
configured no-work acknowledgment distinct; report delivery alone is not receipt.
Review/approval also reads scoped assignment, hold, finding, fix and base-policy
evidence; it never fabricates complete remote or rendered-context/role receipts.

See [CI checks in shadow](gate-check-observations.md) for its source and permission boundaries.

Health uses its scoped source records and resource permissions as described in
the [health guide](gate-health-observations.md). Missing current publication proof
remains explained unknown.
The comment-set fence does not process unseen messages or prove closure readiness.
Registry/context digests use canonical JSON for this slice's ASCII-keyed wire
values; internal task/evidence source versions are local opaque SHA-256 identities.

### Outcome provenance and limitations

Evidence uses the canonical API envelopes: scoped change observations use
`integration`, accepted in-place fixes use `review`, selected child outcomes use
`child_outcome`, and authenticated no-work dispositions use `report`. The delivery
profile and source-version digest preserve the precise product and provenance;
an integration evidence envelope alone does not assert a positive merge.
Resource IDs omit redundant self-aliases. If composite identity/attribution
metadata exceeds the wire budget, an opaque digest pins the complete tuple and
an explicitly shortened attribution label remains readable; subject and head
stay exact. These IDs do not change the outcome predicate.

Existing successful finish, verified PR webhook/detail reads, ordinary CL
provider reads/polls and submission settlement emit compact typed provenance into
`gate_outcome_source`. This is a bounded diagnostic receipt cache, not snapshot
transport or a new collector/broker. Evaluators make ordinary scoped database
reads and never invoke providers. A bare URL/link, branch disappearance, tool
success or `no_action` cannot prove a product or integration. Final text counts
only after legacy finish has actually relayed a durable report. Question/access/
suspension takes precedence; this run's pushed/open change precedes no-work and
report; unchanged stale branches follow the existing retirement policy.

Change delivery requires settled finish provenance and complete exact-head
resource observations. In-place fixes additionally require the owning PR and
accepted head; the review's own push claim is insufficient. Review delivery can
include findings and grants no approval. PR identity is resolved from the complete
link set by the existing current-PR selector. CL integration requires **all**
scoped CLs, their exact snapshots, and canonical submitted renumbering links.
Provider responses lacking a submitted snapshot remain unknown even when legacy
submission closes the task. Forge/CL attribution is retained; externally merged
work is not credited as an automatic Grain action. Landed/empty equivalence passes
only with positive exact-head/base proof; older readers without pins remain unknown.

Each resource receipt pins local resource/authorization policy and complete link
selection. A changed head, workspace/grant policy, missing/denied read, reopen,
relation/goal declaration, incoming message or report revision invalidates the
relevant source fence. Live source observations expire after a minute; immutable
positive integration receipts have a bounded 72-hour retention lease. Task reads
never renew provider evidence. Historical data cannot prove current readiness.
Collection gaps, truncated scope, missing identity/snapshot and stale receipts are
unknown. Source failures carry no raw infrastructure error or evidence bodies.

Related reads stay within the parent's exact repository/workspace scope; missing
or private children are unknown, with no private evidence. API history is also
filtered against current related-task scope. Explicit `closed` can pass for a
failed/canceled child; success cannot. New success has no exact legacy counterpart:
it needs positive integration or a provenance-backed report/no-work outcome and
no observed unseen-message/goal gap. Unverifiable legacy closure stays unknown.
Reopened children need new finish or integration proof for success; clearing
completion also invalidates prior report/no-work success. Explicit closure
predicates continue to reflect whether the child is closed. Diagnostic relation
traversal follows explicit goal edges only, is bounded to 64 nodes, and diagnoses cycles/incomplete
graphs without touching dispatch links or waits.

## Collection, replay and isolation

Reads collect current local evidence and evaluate only a changed source fence or
an expired diagnostic lease (at most 60 seconds, shortened by source, threshold
and window expiry). Selected task reads reuse the cached gate-free legacy detail
body when the domain token is stable, then splice fresh gate fields per read and
compute the ETag over the spliced response so expiry, errors and scope
differences cannot be hidden by 304 responses. Bursts of events
between reads coalesce into one current collection; many concurrent readers reuse
the accepted generation. This is display/poll-driven level collection, not a
subscription that wakes agents. No history is promised for tasks never read.

A read-only snapshot pre-check (SQLite deferred WAL `BEGIN`; Postgres
`REPEATABLE READ READ ONLY`) serves steady-state unchanged reads inside the
lease, active persisted error backoff, and access-limited transient health
projections without acquiring SQLite's write lock or Postgres's advisory writer
lock. Only cache misses, changed source fences, expired leases and due retries
fall through to the serialized diagnostic writer transaction, where SQLite's
immediate transaction and Postgres's existing writer serialization keep
local evidence capture and diagnostic acceptance consistent. An event racing a
read is included in that transaction or invalidates the next read; no accepted
old read overwrites a newer committed source. State/config/build/message/evidence
versions form the source/context fence. Unrelated legacy observation heartbeats
are excluded. There is no universal snapshot paging or new callback broker.

**Read cost.** Because steady-state reads stay in a read-only snapshot
transaction and reuse cached legacy detail bodies on both `GET /api/tasks/{id}`
and `GET /api/tasks/summaries`, polling all 32 allowlisted tasks from 1 or 6
concurrent tabs does not contend with ordinary writers on SQLite or Postgres
(ordinary write latency matches the collection-off baseline, with zero
`unknown_error` budget overruns). Re-evaluating on a changed fence or after the
60-second lease expires still takes the bounded diagnostic writer transaction
once per task and coalesces concurrent readers onto the new generation.

Controller generations, accepted observations, rejected errors and retry deadlines
persist in separate local diagnostic tables. Restart reuses accepted versions and
retry backoff. Infrastructure failures are rejected safe diagnostics, never agent
failures; persisted exponential retry is 1, 2, 4 … 60 seconds plus up to 20% jitter.
Failure bursts within backoff share one generation/retry. An older failed read
cannot overwrite a newer accepted read; acceptance wins a timestamp tie. Errors
are reconstructed without accepted evidence or predictions and have no positive
evidence freshness lease. The next authorized read after retry collects again,
even if the last source fence is unchanged. Collection has a 250ms budget,
plus up to 50ms to retain a safe failure. SQLite diagnostic lock acquisition caps
its leased connection's busy timeout to the remaining budget, then restores it
before reuse (or discards the connection if restoration fails); ordinary writer
settings are preserved. Postgres retains its context-aware advisory writer lock.
A totally unavailable database can prevent
error persistence, in which case the response still shows an unavailable error.
A malformed/unregistered/old cached profile becomes an explained unknown under
the regenerated observation profile before any fresh supported result is shown.

Diagnostic transactions bypass Store.write, Changed notifications, task integration
outboxes, task failure accounting and all dispatch/lifecycle writers. No operational
wait is installed. The browser's expiry timer changes its stale label only.
Disabling collection also hides its projection; no timer can enable enforcement.

## History and comparisons over 24–48 hours

Tasks with both outcome and review/approval monitors retain both independent
comparison labels, separated by `;` in a history sample. Diagnostic counts count
each family separately, so their sum can exceed the number of retained samples.
CI, outcome and review/approval comparisons remain independent; a provenance gap
in one family never hides a positive or discrepant milestone in another. Up to
16 outcome monitors compose with at most eight current/legacy CI pairs. The review
reader admits up to 31 scopes, but the displayed projection shares a 64-binding
limit. An overflowing review/approval subset is explicitly incomplete and cannot
pass current semantics. No scope is converted into an operational requirement.


History records complete bounded observation views (task/source/config/context,
evidence identities/versions, status/reasons and legacy state), evidence changes,
rejected errors and an hourly sample during reads. Stable minute refreshes update
current results/evaluation totals without flooding history. Outcome source versions
include collector refreshes. Each serialized observation has a 256 KiB evidence
budget, at most 64 combined monitors, 64 resources and 64 distinct children; overflow is a
safe diagnostic failure. History reads return at most 20 newest complete samples within
512 KiB of stored view JSON (`MaxGateObservationHistoryBytes`, sized to two full
64-binding / 256 KiB views), always inspecting the newest row even if an oversized
corrupt/restored record exceeds that budget. Older samples remain persisted and included
in counts. We sized the read budget to the 64-binding maximum rather than compacting or
truncating stored `observation_view` samples so historical entries remain valid canonical
views with full evidence, dependency, and provenance fields for child/resource scope
re-authorization (`redactGateHistoryScopes`, `observationVisible`) and old-build
inspection, while realistic ~18-binding PR projections (~44–74 KiB per sample) return
several complete samples (including samples predating a repository move for unrestricted
readers) and even 64-binding maximum projections retain before/after history inside the
250 ms local diagnostic read budget; no individual observation is truncated. Source receipts are at most 4096 rows globally, 4 KiB per
row, retained for at most 72 hours and pruned on source writes. Retention is at most
72 hours and 1024 samples per task, 32 current caches and 32768 samples globally.
Pruning runs during diagnostic writes, including failure writes. Indexed time/key
cutoffs bound per-task/global overflow work without a large retained-key anti-join.
The composite history index is installed by idempotent Init on both engines and
replaces the original expiry-only index. Idle/disabled records are not swept until collection resumes. Rotation or unusually frequent
changes can shorten the retained window; counts describe retained samples, not
all evaluations or time-weighted reliability. If cohort rotation evicts a current cache while history remains, accepted/error
replay resumes above retained generations and restores the first retained
collection marker. The evaluation counter is reconstructed from the last sample;
unsampled minute refreshes after that point cannot be recovered. Aggregate retained
comparison counts remain exact. Current/history data is local to the
database and deliberately excluded from state-repository exports.

For PR tasks the comparison labels are `review_approval_provenance_gap` and
`review_approval_incomplete`; individual results explain current versus imported
legacy outcomes. The report/receipt comparison labels are:

- `equivalent_milestones`: report delivery and receipt agree with corresponding
  local legacy facts; pending receipt is not a delivery discrepancy.
- `delivery_evidence_gap`: legacy settlement exists without supported authenticated
  body-report evidence. Inspect the run/comment before deciding whether this is
  a reader gap or a legacy outcome that needs explanation.
- `receipt_provenance_gap`: legacy closure exists without a verified receipt for
  the current report. Older/unattributed/fallback closes are expected limitations,
  not automatically lifecycle bugs.
- `not_applicable`, `unknown_incomplete`, `unknown_unsupported_config`,
  `unknown_error`: explicit selection, collection, config or infrastructure limits.
- `equivalent_outcome_milestones` / `outcome_discrepancy`: compare positive
  integration or explicitly selected legacy closed/merged milestones with their
  corresponding current legacy facts. This is never a close/rest decision.
- `outcome_evidence_gap`: an outcome monitor cannot establish its scoped proof.
- `explicit_success_contract_only`: selected success has no exact legacy closure
  contract, so no parity claim is made.
- `observed_<product>_milestone`: a delivered product, independent of integration,
  receipt or lifecycle completion.

Inspect the expanded comparison history on `/next/tasks/<id>`, or use the ordinary
read API, for example from an already-authorized Grain CLI:

```sh
grain api GET /api/tasks/123 | jq '{
  state,
  observations: .gate_observations,
  diagnostics: .gate_observation_diagnostics
}'
```

Counts are per selected task. Inspect each allowlisted task; this slice adds no
cross-task diagnostic query endpoint. The newest 20 full samples are returned in
the read API, with aggregate counts over all retained samples. More detailed local
forensics may inspect the `gate_observation_shadow*` and `gate_outcome_source` tables through the
operator's existing authorized database tooling; no new database-access grant is
created by this feature.

## Separate authorized rollout and observation start marker

No deployment/configuration is changed by this PR. On a separately authorized
rollout, deploy the reviewed build, set the daemon process environment explicitly,
and restart it through the deployment's normal mechanism:

```sh
# Example process environment only; choose actual representative task IDs.
GRAIN_GATE_OBSERVATION_TASKS=123,456 grain daemon <existing daemon arguments>
```

An absent/empty value disables collection. Invalid/duplicate IDs or more than 32
IDs disable it with a startup log reason. There is no repository/task config
mutation surface and no task completion-authority import. To disable, remove or
empty the variable and restart the daemon; existing lifecycle behavior continues.
Deployment environment changes belong to the deployment's configuration process,
not this repository's production manifests.

At enablement record the UTC deployment/enable time and selected IDs in the
operator's rollout record: **that is the observation-window start marker**. Read
each task once and confirm mode/effect flags, source revision and
`gate_observation_diagnostics.started_at` (first retained collection for that task).
That field survives process restarts and may predate a later disable/re-enable;
use the new rollout record's timestamp when beginning a new window. PR merge and
task filing do not begin the window. Do not run a 24–48h job as part of this task.

Review after a day or two of separately enabled operation:

1. Confirm the actual start marker, cohort, deployed build and coverage from reads;
   identify missing intervals, high churn/retention truncation and stale results.
2. Inspect delivery and receipt milestones separately, using exact report/close
   versions. Classify provenance/shape gaps before calling a difference a bug.
3. Inspect unknown/error reasons, retry recovery and aggregate counts; confirm
   receipt and compatibility child-answer/no-work policies remain separately labeled.
4. Confirm legacy queue/run/wait/completion/merge/submit and acknowledgment/reply
   behavior stayed authoritative, and shadow outputs produced no operational work.
5. Make any enforcement decision separately. Authoritative dispatch, actions,
   closure, guarded actions, waiver mutations and task migration remain deferred.
   No elapsed duration or passing observation can cut over automatically.

## Validation

Shared SQLite and real Postgres integration tests exercise durable report/close
records, changed attachments/messages/reports, restart/replay, concurrent event and
read coalescing, unsupported persisted type/version/config, stale failure
completion, coalesced
failure bursts, canonical error reconstruction, retry expiry, ambiguous receipt
ordering, contended lock deadlines/restoration, retention at full capacity and
unchanged legacy tables/views/wake signals. Pure tests cover malformed configs,
complete-empty/incomplete/stale reads and inert output. Ordinary API tests cover
report/receipt/reply behavior, complete evidence across paged/comments-since reads,
additive JSON, authorization, missing tasks,
diagnostic storage outage/recovery and diagnostic redaction.
The browser/component checks cover provenance/history, errors, expiry with unchanged
responses, inert controls, escaping and responsive rendering. Full future enforcement
acceptance scenarios are not claimed by these read-only checks.

CI-specific runtime profiles, source bounds, version fences, comparison differences
and validation are documented in [CI checks in shadow](gate-check-observations.md).

Review/approval validation and its material source limitations are recorded in
[Review and approval observations](gate-review-observations.md). Current results
remain unknown when exact provider/authorization evidence is absent; legacy
Submit/accept-findings services remain authoritative. No production configuration
or grant change accompanies the extension.


## QA/release qualification extension

Task #69 adds qa_stage and release_qualification to the eleven preceding monitor
types. The thirteen-type [combined audit](gate-coverage-audit.md) includes the
original Task #51 mapping, controller policies, source gaps and discrepancy workflow. The [source audit](gate-qa-release-observations.md) documents the exact
versioned reads, custom stage restrictions, and honest unknowns for unavailable
signed/effective-policy/asset evidence. The existing task/repository and linked
resource authorization remains in force; scoped callers receive no retained
history. Selection, legacy authority and all effect/cutover flags are unchanged.
