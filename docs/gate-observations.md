# Built-in gate observations

This is an observation-only implementation of Task #59's `grain.gates/v1`
contracts. It ships exactly two v1 types: `delivery` with
`{"product":"report","semantics":"current_v1"}`, and `acknowledgment` with
`{"mode":"explicit_receipt"}`. Other catalogue profiles/types are unsupported.
Their closed schemas are pinned to the canonical catalogue and evaluated by pure
local Go functions. There are no remote evaluator calls or private evaluator state.

Legacy services still own every task lifecycle, queue/run/wait, approval, report,
acknowledgment, merge and submit operation. There is no authority migration or
cutover code. Every binding is a nonrequired monitor; all effect flags are false.
Diagnostic acceptance means the observation was recorded, not task readiness,
successful completion, or permission to act. Work/actions arrays are always empty.
Proposed receipt remediation and waits are inert descriptions with no buttons.

## Shipped read surfaces

The existing permission-filtered `GET /api/tasks/{id}` adds optional
`gate_observations` (the closed canonical observation_view) and sibling
`gate_observation_diagnostics`. Ordinary whole, paged, comments-since and task
summary reads reuse the same projection; pagination never supplies gate evidence.
The ordinary `api_get_task` read carries the additive fields. Angular task detail
at `/next/tasks/<id>` shows status/explanation, source/config/build provenance,
evaluation/evidence versions, completeness/freshness, errors, inert predictions,
retained comparison counts and newest 20 samples. The classic UI remains compatible.
There is no new read route, registry-discovery route, `?view=gates` profile,
mutation/reevaluation/action/waiver endpoint or MCP evaluator.

## Bounded selection and evidence

Collection/display is disabled by default. A deployment-local explicit allowlist
selects at most 32 task IDs. There is no automatic cohort expansion or lifecycle
hook. Choose a small representative set of current finite analysis/report tasks,
including a report awaiting receipt and a report acknowledged by a verified person.
Optionally include a code/standing task to inspect the explained unknown state;
never interpret that acknowledgment observation as a code completion requirement.

Only noninteractive `analyze` tasks without changes, goals, standing/keep-running
behavior, console hosting, or report-only child auto-closure are applicable. A
successful recorded no-work invocation is excluded. Unsupported shapes return
unknown with a not-applicable explanation for both monitors.

Scoped internal reads capture one task's relevant shape, legacy state/settlement,
latest run, latest own non-handover agent/Grain-relayed comment, comment-set fence,
attachment presence and exact current close event in one database transaction.
A body report must be nonempty, at most 8192 characters, associated with the latest
settled run and at/before its completion. Attachment-bearing reports and comments
that cannot be correlated with settlement are incomplete/unknown, with specific
reasons. They cannot silently pass. An ordinary person's reply is not a report or
receipt. A complete empty report collection is unmet for delivery and unknown for
receipt. Live producers are pending for delivery.

Receipt requires a verified human close event at the current closure timestamp,
after both the current report and settlement. Legacy unattributed, fallback or
automation closes are unknown, not synthetic receipts. A new report/run, question,
message, attachment, relevant task shape, or registry/config build changes the
source fence; old receipt observations cannot silently cover a changed report.
These local immutable body/comment and close-event records are the only supported
evidence. No permission revisions or complete remote evidence are claimed.
Registry/context digests use canonical JSON for this slice's ASCII-keyed wire
values; internal task/evidence source versions are local opaque SHA-256 identities.

## Collection, replay and isolation

Reads collect current local evidence and evaluate only a changed source fence or
an expired 60-second diagnostic lease. Selected task reads bypass the ordinary
body cache so expiry/errors cannot be hidden by 304 responses. Bursts of events
between reads coalesce into one current collection; many concurrent readers reuse
the accepted generation. This is display/poll-driven level collection, not a
subscription that wakes agents. No history is promised for tasks never read.

SQLite's immediate transaction and Postgres's existing writer serialization keep
local evidence capture and diagnostic acceptance consistent. An event racing a
read is included in that transaction or invalidates the next read; no accepted
old read overwrites a newer committed source. State/config/build/message/evidence
versions form the source/context fence. Unrelated legacy observation heartbeats
are excluded. There is no universal snapshot paging or new callback broker.

Controller generations, accepted observations, rejected errors and retry deadlines
persist in separate local diagnostic tables. Restart reuses accepted versions and
retry backoff. Infrastructure failures are rejected safe diagnostics, never agent
failures; persisted exponential retry is 1, 2, 4 … 60 seconds plus up to 20% jitter.
The next authorized read after retry collects again. Collection has a 250ms budget,
plus up to 50ms to retain a safe failure; a totally unavailable database can prevent
error persistence, in which case the response still shows an unavailable error.
A malformed/unregistered/old cached profile becomes an explained unknown under
the regenerated observation profile before any fresh supported result is shown.

Diagnostic transactions bypass Store.write, Changed notifications, task integration
outboxes, task failure accounting and all dispatch/lifecycle writers. No operational
wait is installed. The browser's expiry timer changes its stale label only.
Disabling collection also hides its projection; no timer can enable enforcement.

## History and comparisons over 24–48 hours

History records complete bounded observation views (task/source/config/context,
evidence identities/versions, status/reasons and legacy state), evidence changes,
rejected errors and an hourly sample during reads. Stable minute refreshes update
current results/evaluation totals without flooding history. Retention is at most
72 hours and 1024 samples per task, 32 current caches and 32768 samples globally.
Pruning runs during diagnostic writes, including failure writes. Idle/disabled
records are not swept until collection resumes. Rotation or unusually frequent
changes can shorten the retained window; counts describe retained samples, not
all evaluations or time-weighted reliability. Current/history data is local to the
database and deliberately excluded from state-repository exports.

The comparison labels are:

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
- `delivery_discrepancy` / `receipt_discrepancy`: a positive predicate without its
  corresponding local milestone; investigate the evidence versions first.

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
forensics may inspect the two `gate_observation_shadow*` tables through the
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
   receipt is never inferred for code/standing/no-work/child-auto-close tasks.
4. Confirm legacy queue/run/wait/completion/merge/submit and acknowledgment/reply
   behavior stayed authoritative, and shadow outputs produced no operational work.
5. Make any enforcement decision separately. Authoritative dispatch, actions,
   closure, guarded actions, waiver mutations and task migration remain deferred.
   No elapsed duration or passing observation can cut over automatically.

## Validation

Shared SQLite and real Postgres integration tests exercise durable report/close
records, changed attachments/messages/reports, restart/replay, concurrent event and
read coalescing, unsupported persisted type/version/config, errors/backoff and
unchanged legacy tables/views/wake signals. Pure tests cover malformed configs,
complete-empty/incomplete/stale reads and inert output. Ordinary API tests cover
report/receipt/reply behavior, additive JSON, authorization and diagnostic redaction.
The browser/component checks cover provenance/history, errors, expiry with unchanged
responses, inert controls, escaping and responsive rendering. Full future enforcement
acceptance scenarios are not claimed by these read-only checks.
