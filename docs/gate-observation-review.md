# Initial observation framework review

This records Task #64 at its reviewed head. Task #67 extends the fixed-pair
projection using these conventions; see [Review and approval observations](gate-review-observations.md)
for the current registration, source adapter, limits and validation.

Task #64 reviews the implementation delivered by Task #61, PR #1739
(merge `6bd221161`), against the canonical contracts delivered by Task #59,
PR #1719 (merge `2e45dca71`). Both merges are ancestors of this review branch.
Task #64's actual dependency record lists only #61; it names no additional
framework-review task. This review preserves exactly the two shipped v1 monitor
profiles and the public observation wire contracts.

## Repairs

- An older failed collection could replace a newer accepted observation.
  Failure persistence now compares collection start times under the same database
  writer lock as acceptance. A success wins a timestamp tie. Concurrent failures
  during backoff reuse the existing diagnostic generation and retry.
- Failure persistence previously trusted the caller's status, acceptance time,
  evidence, predictions and comparison time. A shared unavailable-view builder
  now creates rejected error results from the fixed registry. It discards old
  evidence/work/actions/waits and has no positive freshness lease. A rejected
  cache must recollect when retry expires, even if its source fence is unchanged.
- A close and a revised report at the same timestamp could falsely satisfy receipt.
  The body-only reader now requires a nonempty verified human identity and a
  close strictly after both report and settlement. Ambiguous ordering is unknown.
  Legacy acknowledgment behavior is unchanged.
- Global history pruning used tuple `NOT IN` against 32768 retained keys.
  A full-capacity SQLite regression took about 4.7 seconds, exceeding collection's
  250 ms budget while holding the shared writer lock. Indexed ordered cutoffs
  replace the large anti-join. The same local test took about 28 ms after repair;
  these measurements are examples, not a service latency guarantee. The composite
  history index covers expiry and tie-breaking order; Init replaces the old prefix
  index on both engines.
- SQLite writer acquisition could wait five seconds despite a diagnostic context
  deadline. Diagnostic transactions now cap the busy timeout on a leased
  connection to the remaining budget, restore it before pool reuse, and discard
  the connection if restoration fails. Postgres continues to use its existing
  context-aware advisory lock. Contention tests verify bounded return, no partial
  diagnostic commit, unchanged legacy state/wakes and restored legacy timeouts.
- Removed a redundant attachment count and centralized unavailable projection
  construction. The implementation pin advances to `builtin-report-observer-v2`;
  gate/config/API versions remain v1. Older cached profiles regenerate through
  the existing explained-unknown path.

## Review coverage and boundaries

| Area | Finding and qualification |
| --- | --- |
| Registration/config | Fixed typed registry; exact property names, closed profiles, duplicate-key/trailing-data rejection, canonical schema pins and immutable discovery copies. Unsupported types/profiles remain rejected. |
| Evidence | Reads one authorized task's complete local scope inside the writer transaction. Only the latest own non-handover body report correlated with a settled run is supported. Complete-empty, oversized, attachment-bearing and missing-provenance cases remain distinct. |
| Versions/events | Latest run/settlement, task applicability, report/receipt versions and latest comment fence drive collection. Reads coalesce events and lease refreshes; no source event is consumed or acknowledged. |
| Replay/retry | Accepted generations and error backoff survive a new Store instance. Failure bursts deduplicate, stale failure completion cannot rewind acceptance, expired rejection recollects, and cache eviction resumes above retained history keys. |
| Isolation | Gate code has no lifecycle/action/dispatch client. Diagnostic transactions bypass Store.write and Changed. Shared tests snapshot task, observation, runs/tools, comments, attachments, goals, grants, links, readiness and event outboxes before/after observations and errors. Wait expiry/retry only refreshes diagnostics. |
| API/UI | Existing task authorization runs before projection. Missing tasks remain 404; hidden tasks do not expose a sidecar. Diagnostic storage outage preserves ordinary task reads and legacy state, redacts infrastructure errors and shows rejected errors. Angular renders neutral observation labels, expiry/errors, provenance and inert text without controls. |
| Storage/load | Both engines exercise current-cache, per-task, global and age retention, tied timestamps, index upgrade/idempotent Init, full-capacity collection, real lock-contention deadlines, restart and error recovery. History reads return 20 samples and retained counts; source capture uses bounded body reads and indexed task queries. |

No gate result, including met, error, stale, unknown or pending, is consulted by
legacy enqueue, failure accounting, questions/acknowledgment, merge/submit or
closure. Work/actions remain empty. Wait descriptors are inert JSON, with no
timer registration, escalation, queue insertion, integration event or task hold.
Diagnostic table writes can contend briefly with other writers; they are bounded
by request budgets and the optimized retention queries. They confer no authority.

These tests qualify the implemented observation slice. They do not prove the
future enforcement acceptance scenarios, nor independent end-to-end parity for
every legacy outcome. Comparison labels correlate the same local report/close
facts with the pure predicates; a retained equivalent label is not a second
independent correctness oracle.

## Extension conventions

1. **Registration:** add a specific typed pure evaluator and canonical closed
   schema/profile validation. Update schema-pin/config-denial tests and runtime
   coverage in the catalogue. Change the implementation pin when reader or
   evaluator semantics change. Keep existing public v1 contracts and rejected
   unsupported profiles explicit.
2. **Readers:** authorize the subject through the existing domain read before
   collecting. Read only scoped immutable records and complete collections, with
   exact resource/head/policy/authorization versions where relevant. Empty is
   negative evidence only when completeness is proven. Missing, denied, partial,
   oversized or unverifiable input stays unknown/error. Never derive gate facts
   from paged UI comments, private evaluator state or claimed tool output.
3. **Dirty hooks:** existing domain collectors retain event and wake ownership.
   Update a diagnostic source fence for relevant committed facts, and coalesce
   through authorized reads. Reuse the bounded diagnostic transaction helper rather
   than changing pooled connection settings or lock ownership independently.
   Do not call Store.write, signal Changed, register
   gate waits or route predictions into dispatch. Expiry/retry are diagnostic
   refresh deadlines. Any later event-driven collection must preserve these
   ownership rules, transaction fences, restart behavior and bounded load.
4. **Projection:** keep the ordinary permission-filtered optional sidecar.
   Every binding stays a nonrequired monitor with action=null, legacy authority
   and all effects/cutover flags false. Use the shared unavailable-view builder
   for this fixed projection. Preserve history bounds, redaction, explicit
   unknown/error/stale text and the Angular card's lack of mutation controls.
   Shadow evaluation IDs must never become authoritative result/action IDs.
5. **Tests:** cover real source records and predicates, complete-empty versus
   missing/denied input, new/unseen messages and revisions, stale reads, concurrent
   collection/failure ordering, retries, restart/eviction and history capacity on
   SQLite and required real Postgres. Retain legacy table/view and wake snapshots,
   API authorization/cache/pagination checks, canonical response validation and
   Angular inertness/expiry tests.

## Material limits for later ports

- The controller, failure builder and milestone comparison intentionally project
  a fixed report/receipt pair. Comparisons and cache validation assume that pair
  and result order. Adding a descriptor alone is not a gate port: explicitly
  extend the relevant reader, binding projection, failure/comparison logic and
  tests together. Do not invent an execution platform around this slice.
- Body-only evidence has an 8192-character bound. Attachments are deliberately
  incomplete; their exact immutable versions/complete collection must be supplied
  before supporting them. Legacy close events do not pin the rendered report
  version or current eligibility policy. Strict timestamps are conservative
  diagnostic evidence, not a future exact-version receipt/admission contract.
- The comment-set fence notices unseen messages but does not process them.
  Delivery/receipt observations do not attest that all messages were answered or
  that closure is ready. Legacy message/question/ack writers remain authoritative.
- Resource heads, policy/authorization revisions, remote collection completeness,
  protected operation receipts and enforcement admission are not implemented by
  these two task-local monitors. Later source-specific ports must establish their
  own scoped evidence contracts before claiming coverage.
- Collection is read-driven. The separate authorized allowlist/enable timestamp
  and 24–48 hour review in the [operator guide](gate-observations.md) are still
  required to obtain operational comparisons. Counts are retained samples, not
  elapsed coverage. Churn/rotation shortens coverage; no read means no sample.
  No rollout, configuration change, task migration or observation window was
  started by this review.

These are explicit extension constraints, not additional tasks or authorization
to activate enforcement. No production deployment/configuration/grants change.

## Outcome extension (Task #65)

The fixed-pair and unsupported-resource notes above describe the reviewed #61/#64
baseline. The subsequent outcome port extends the same registry/read/persistence
path with delivery/acknowledgment variants, integration and selected children;
see the current [coverage table](../design/spec/gates/catalogue.md#runtime-observation-coverage)
and [operator guide](gate-observations.md). Cached projections validate every
monitor rather than assuming two results; failure reconstruction preserves the
validated current projection while discarding accepted evidence. The original
body/receipt ordering, retry, contention, retention and legacy-isolation repairs
remain regression requirements. Compact bounded typed domain provenance stays within ordinary scoped internal
reads and introduces no provider back-call. Shadow success is not an
authority/closure decision, and all enforcement and rollout activation stays deferred.
