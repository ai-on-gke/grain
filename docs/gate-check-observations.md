# CI checks and merge context in shadow

Task #66 extends the observation-only framework delivered by Task #61 and
reviewed by Task #64. Before implementation, the checkout was verified to contain
the merged spec PR #1719 (`2e45dca71`), framework PR #1739 (`6bd221161`), and
review PR #1803 (`07e43408c`, including `de688e249`). The canonical contracts
remain [design/spec/gates](../design/spec/gates/).

**OBSERVATION-ONLY.** The existing allowlist is disabled by default. This change
does not enable it, start an observation window, deploy anything, change grants
or settings, migrate tasks, or introduce a cutover. Every attachment is a
nonrequired monitor with action=null, legacy authority and all effect flags false.
Work/actions arrays remain empty. Run/retry/fix, person Submit, automatic merge and
wait descriptions are inert text/JSON. No code consumes them to dispatch an agent,
request checks/review, approve, merge, submit, close, or install a lifecycle wait.

## Profiles and interpretation

Each scoped task-owned change adds two labeled `checks` v1 monitors:

- Current: `mode=effective, extra_jobs=[], empty=unknown, semantics=current_v1`.
- Explicit legacy import: `mode=effective, extra_jobs=[], empty=legacy_settled,
  semantics=legacy_ci_v1`, with imported=true provenance.

The closed canonical schema also admits github/grain/both/provider/none modes,
extra-job union and explicit_none. An explicit source must equal the effective
source; callers cannot switch providers or remove inherited requirements.
CLs accept effective/provider only. No-CI requires an explicit effective policy
and no inherited or extra requirement. A repository with no reported checks is
not an authorized no-CI declaration.

| Source | Current predicate and collection |
| --- | --- |
| GitHub | Exact head, all check-run attempts plus Actions workflow verdicts, including workflows which fail before creating a job. Both enumerations require consistent total_count, all pages, unique identities and matching heads. Actions job identities include app, workflow/event/ref lineage and name, correlated through check_suite_id; workflow attempts compare run ID and attempt as a tuple. Same-named suites without a proved lineage, or contradictory job/run generation ordering, remain unknown. A positive workflow alone cannot pass an empty job-check set. Success/neutral/skipped pass; failure/timed_out/startup_failure fail; running, cancelled, action_required, stale and no conclusion remain pending. Missing required identities are pending. Empty, denied or incomplete collections cannot pass. |
| Grain | The task CIStatus must concern the exact current head. Current proof additionally requires the retained complete run, matching repository/head/conclusion, applicable base-owned workflow identities, and successful required jobs. Recheck jobs overlay only a retained original on that same repository/head. Missing/stale/truncated originals or runs remain unknown. A complete absence of an exact-head run remains pending. Aggregate success with no jobs does not establish current proof. |
| CL provider | Ordinary provider Observe for the task's exact linked target and positive snapshot. PresubmitPassed is the required aggregate snapshot verdict, independent of optional display checks. Failed is unmet, running/not_run pending, unknown/missing unknown, and technical provider failures rejected errors. No GitHub empty-check timer or heuristic applies. |

Base definitions use the engine's ordinary layered Source and event/branch
planner. Its returned commit must equal the independently read current base tip.
Required base identities union policy jobs; the change cannot remove its own
requirements. Configured no-CI and explicit repository source selection retain
their existing meaning. Active accepted merge rules are compared with all
supported rule-file paths at the exact base. Inactive files do not activate policy.
A changed, unreadable or unaccepted base policy is unknown, not an inferred waiver.

The explicit legacy import preserves the existing empty-head registration clock
and the durable exact-head Grain CIStatus predicate, including named-job narrowing
after a failed full run and the narrow spending-limit exception after Grain success.
Its dependency is labeled `legacy-status` when only the recorded Grain verdict is
being interpreted: that positive legacy fact does **not** prove today's complete
job set. Actions-only fallback is labeled incomplete for current proof even when
a legacy import can interpret its workflow verdicts. No legacy import authorizes
future enforcement or bypasses the current resource/policy fences.

## Readers, versions and merge context

The reader uses ordinary scoped domain methods, not MCP evaluator calls, callback
brokers, transported snapshots, or a new source-state table. Task authorization
precedes the sidecar; ci:read is checked for every change. Retained history is
checked again against the viewer's current resource permissions. An unauthorized
sidecar is hidden, including old resource history after retargeting.

Collection happens outside the diagnostic writer transaction. Acceptance rechecks
the task/observation, grants, repository settings, accepted merge rules, declared
workflow versions, scoped base-CI/recheck records, and recorded GitHub CI facts
for the exact repository/head. Late older accepted collections cannot rewind a
newer accepted result, including after restart. A concurrent local source change
still invalidates a cached success. Remote complete sets must include and agree
with already recorded check identities/conclusions and known Actions attempts;
replica disagreement stays unknown until the sources agree. Log enrichment and
other heads do not change this semantic fence. A final forge read verifies
head, base ref and base tip. Check attempts and collection generations are hashed
with their actual source versions; provider details/errors and CI logs are not
copied into diagnostics. Context changes invalidate comparisons without changing
the legacy task, CI verdict, queue or wake ownership.

CI success is deliberately separate from executor readiness. Human text includes
conflicts/behind/unknown mergeability, the recorded legacy queue state/reason,
current or stale overlap-recheck context, and exact-tip or missing/stale recorded
base-CI context. Base red is not a universal check prerequisite, and repair
exceptions never erase own-head CI. Review/approval/queue ordering remain legacy
prerequisites. GitHub merge-group and legacy queue observations lack an atomic
current-head/base receipt here; absence is an explicit readiness gap.

CL SubmitBlockers supplies review, provider reasons, conflicts/behind and Grain
review prerequisites separately from presubmit. The core provider contract exposes
a task-local provider namespace and current snapshot, not an atomic conditional
receipt for the expected reviewed snapshot and external authority revision.
The projection cannot admit Submit or replace that adapter contract.

The existing forge and CI collectors retain all event/wake/timer ownership.
Read-only inspection of existing current-head registration and stall clocks never
creates, resets or clears them. Missing sightings, including after restart, remain
unknown. Shadow wait descriptors only describe checks/change/timer refreshes;
expiry and retry never register operational waits or escalate a task.

## Bounds, replay and diagnostics

Selection remains at most 32 tasks, with at most eight distinct changes per task.
GitHub reads bound each collection to 512 check attempts and 512 workflow rows,
at most six pages per endpoint. Missing/changing totals, duplicate/foreign
identities and over-limit collections cannot yield a passing result. Definition
reads bound workflows/jobs to 512 and parsed definitions to 1 MiB, coalesce behind one context-aware read per
client, and cache at most 32 immutable base/declared-definition keys for a minute.
Scoped local readiness and recorded exact-head CI collections also have a
512-row bound. An indexed repository/commit lookup bounds local collection work.

Collection has a three-second context; paged reads check cancellation before
issuing another request. Older ordinary forge/runner methods retain their transport
timeout for an individual request. An expired/canceled collection is discarded;
there is no background read after the collector returns. Diagnostic acceptance
retains its 250 ms writer budget and separate 50 ms failure budget. Infrastructure
errors/panics use the existing persisted exponential 1..60 second backoff with
jitter; backed-off reads do not repeat source collection.

The existing diagnostic tables, current-cache/age/per-task/global row bounds and
20-sample history projection are reused. A serialized observation is additionally
bounded to 128 KiB. Histories are samples, not continuous observation windows.
Comparison labels distinguish current status, current/legacy differences and
source gaps. They correlate shared evidence, not an independent replay of the
real merge decision or a readiness oracle. Expired cards remain visibly stale.

## Validation and remaining boundaries

Tests exercise actual predicates, bounded HTTP pagination/count/provenance,
latest/replaced/cancelled attempts, delayed/missing jobs, source denial/errors,
head/base/definition changes, accepted declaration events, both CI sources,
exact-head Grain status and recheck overlay, stale originals, CL snapshot/provider
errors, and inert remediation. Shared SQLite and required real Postgres tests cover
replay/restart, out-of-order accepted reads, concurrent CI events, lagging
remote replicas, local acceptance fences, backoff, foreign resources and snapshots
of legacy task/run/tool/comment/attachment/goal/grant/link/readiness/event/queue
tables and Changed/commit counters. Existing framework retention, contention,
failure ordering and API authorization regressions remain in place.

Actual results and ordinary API fixtures are validated against canonical schemas.
Angular tests/rendering cover scoped current/legacy labels, expiry, errors,
remediation text, escaping, accessibility and absence of shadow controls. The
browser fixture is generated by the real ordinary task API test.

This is the checks port and its explanatory merge context. Other gates, complete
external branch-protection/ruleset admission, speculative merge-group execution,
atomic CL expected-snapshot receipts, and enforcement/cutover remain outside this
observation implementation. No new work is automatically filed.
