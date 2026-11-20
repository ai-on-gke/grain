# Health gate shadow observations

Task #68 builds on merged canonical-spec PR #1719 (Task #59), initial-framework
PR #1739 (Task #61) and framework-review PR #1803 (Task #64). All three merge
commits were verified ancestors of the implementation checkout. The implementation
pin for that delivery was `builtin-task-observer-v7`; public contracts remain
`grain.gates/v1`. The [combined audit](gate-coverage-audit.md) records the current
thirteen-type registry and shared `builtin-gate-observer-v10` pin.

**OBSERVATION-ONLY.** Every binding is a nonrequired monitor with action=null,
authority=legacy and lifecycle/action/dispatch/automatic-cutover flags false.
Results, including predictions of remediation or escalation, never enqueue agents,
request CI/review, publish, clear notifications, approve, merge, close, rest or
change goal limits. Existing collectors, scheduling, waits and lifecycle writers
retain authority. No deployment, configuration, grants, migration or observation
window is activated by this change.

## Selection and permission

The existing disabled-by-default `GRAIN_GATE_OBSERVATION_TASKS` selection remains
bounded to 32 explicit task IDs. Only existing base-green (optionally publishing),
nightly-green and pipeline-healthy goals select health pairs. Other selected tasks
retain the report/receipt projection; they acquire no health requirement. Each
health pair has a labeled legacy reading and a stronger current prediction.
Declaration timestamp, declarer, goal specification and implementation pin form
binding provenance. A stronger monitor is not represented as an imported legacy
contract.

The ordinary authorized task API and Angular card display the same additive
sidecar, neutral shadow labels, repository/ref, run/head/evidence versions,
unknown/error/stale explanations, inert waits and retained comparison history.
There is no new evaluator API, MCP broker, snapshot transport or action control.

Task visibility alone does not authorize health evidence. Current caller
`ci:read` and `repos:read` ceilings apply to resource reads; publication also
requires `releases:read`, and pipeline task scans require `tasks:read`.
Legacy steward quiet additionally requires explicit deployment-wide
`deployment:read`, task, CI and repository read ceilings. The source context
includes an opaque JSON-safe digest of the current effective read ceiling; it is
a diagnostic authorization fence, not an enforcement admission revision.
A narrow reader never receives a broader cached result/history. Partially denied
health pairs are evaluated transiently without persisted cache/history reuse;
authorized current scoped pipeline predictions can coexist with denied legacy
broad reads. Retained history requires unrestricted task-read access, preserving
the upstream repository-move protections. Where task reads are unrestricted but
health resource visibility is narrower, health samples are filtered against the
current ceiling and aggregate metadata is withheld. No scoped task caller reads
retained history. Current repository task visibility is rechecked in the collection
transaction before cache/backoff replay and again for diagnostic failure writes.

## Profiles and sources

| Pair | Legacy observation | Stronger current observation |
| --- | --- | --- |
| Base | Actual recorded Grain row: no in-flight/broken verdict and success or BaseCICleared. | Exact collector tip and source-row/settings revisions; complete configured Grain plan on that commit/ref. Non-reproduction additionally requires the actual separate successful noncached recheck, covering the recorded confirmation scope and every failed original job. |
| Nightly | Actual newest-500 stored-job selection since declaration, case-insensitive workflow/default-like/RC refs, idle-gate/all-skipped and unjudged-cancellation filtering; accepted success/neutral/skipped jobs. Gating workflow imports use the saved legacy verified-push goal disposition, never arbitrary jobs named main. | Exact workflow/ref and same repository issuer/head, branch source event, workflow path, run/attempt/commit and complete raw job provenance. Select the newest eligible collected attempt in [max(declared since, now-window), now]. The comparison window is explicitly 86400 seconds. Idle-skipped/cancelled newest runs are pending; expired/absent evidence is unknown. |
| Publishing | Calls the actual BaseBranchPublishingOf reader: absent row/no finding passes. Failures and superseded attempts retain existing thresholds, including the running code's three-failure/default-three-hour/environment-override behavior. | Predicate requires a complete positive target set and authenticated successful target/commit/artifact/version receipts for the exact expected commit/current tip. Workflow/build/signing success, absence and release supersession do not supply that proof. |
| Pipeline | Newest-200 legacy notifications: repository-filtered goal check plus deployment-wide steward quiet check (the latter excludes the steward-regression subject), followed by deployment-wide recorded base/task/queue scan, including empty/success conclusions and paused/steward exclusions. A truncated apparent quiet scan is explained unknown. | SQL-scoped complete notification/task/queue reads for configured repositories, complete expected managed base refs and current base proofs, excluding paused/self tasks. Missing expected repositories/refs or scan provenance is unknown; observed problems are unmet. |

The base collector reuses its existing tip and CI run reads. A small diagnostic
record pins the exact base row, current CI policy, tip, plan completeness and
non-reproduction coverage. Configured managed refs require WatchDefaultBranch,
an authenticated default-branch read and explicit port refs. A recorded-row
inventory alone cannot establish the expected set. These proofs have a two-minute
source lease and are invalidated by row/policy/head changes or source-read failure.

The existing concluded-workflow collector retains its exact legacy latest-job
input and callbacks. Shadow evidence uses a separate ordinary GET for the exact
run attempt, with total-count and pagination completeness checks. It never
substitutes that stronger listing for legacy input or synthesizes missing job
conclusions. The collector also retains the workflow's overall conclusion:
a failed run cannot become healthy from green listed jobs, cancelled/skipped runs
remain pending, and missing/stale conclusions remain unknown. Old diagnostic
records without a run conclusion cannot establish current health. The existing
authenticated default-branch push collector marks the
diagnostic source after its own verification; no PR/fork job row can mark it.
Denied, missing, partial, oversized or unsupported readers produce incomplete
proof. No diagnostic error is returned into legacy collection. Source interests are
restricted to the existing health goals in the explicit cohort; selecting only
report tasks does not start unrelated health source reads.

Pipeline collection is a bounded transaction over current local source records.
Current scoped SQL does not first read unrelated tasks/notifications. Legacy
global reads require broad scope. Existing TaskStallNote thresholds are pinned by
tests at their exact boundary; expiry only shortens a diagnostic lease. No stall
timer, goal wake or escalation is installed. An all-repositories legacy steward
can inspect authorized inventory, but absent configured expected repositories
still leaves the stronger profile unknown.

## Persistence and bounds

The existing serialized diagnostic writer, replay/backoff handling and history
retention are shared by all thirteen registered types. Error reconstruction regenerates the
current binding shape and discards accepted evidence/work/actions/waits. Safe
binding/permission selection precedes persisted backoff; expensive fact collection
runs only after retry is due. Window, threshold and source expiry shorten the
ordinary one-minute lease, so unchanged rows cannot retain an expired positive
prediction. Source changes are shadow fences, never legacy Changed notifications
or operational dirty/wake events.

History remains 72 hours / 1024 samples per task, 32 current caches, 32768 total
samples, with at most 20 returned samples. The two small controller-owned source
tables have no lifecycle/outbox references: at most 256 base proofs, and 128
workflow attempts per repository / 1024 globally within seven days. Workflow
proofs are limited to 512 jobs / 256 KiB and exact-job pagination to six pages.
Source pruning runs on diagnostic writes, not as a new background service.
Workflow proof updates atomically replace their selection/pruning timestamp too:
resolving an incomplete run's time cannot keep an older green run ahead of a
newer failure, and losing time provenance installs a newest unknown marker.
Pipeline collection caps base rows at 256, refs at 64 per repository,
notifications/tasks at 1000 and configured repositories at 64. Exceeding a
collection bound yields unknown/incomplete rather than an empty healthy set.
Local health collection/acceptance keeps its 250 ms budget and failure persistence
keeps its 50 ms budget. A bounded 50 ms goal selection prevents per-request change-CI
reads for health goals. Collector database writes have 50 ms budgets and never
signal Changed.

Comparison labels distinguish profile agreement, stronger-profile differences and
unknown/incomplete evidence. These are retained diagnostic samples, not an
independent proof that legacy lifecycle behavior was correct or a time-weighted
availability measure. Existing close/rest/on-limit behavior is unchanged.

## Material limitations

- Existing release collectors record workflow conclusions and supersession, not
  authenticated complete publication target/commit/artifact receipts. The positive
  publication predicate is implemented and tested, but its ordinary runtime
  projection deliberately remains explained unknown. No publication receipt,
  expected target policy or successful publishing fact is invented.
- Current base completeness requires an explicit nonempty configured Grain CI job
  set. An empty `BaseBranchCIJobs` means the entire push plan, whose expected job
  inventory is not carried by the retained run; it remains unknown rather than
  relabeling an earlier narrow run as complete after a policy expansion. Other
  CI sources/effective-policy shapes without the exact source proof remain unknown. Scoped pipeline health also remains unknown when a required base/ref
  proof is absent; it does not treat an empty legacy row set as healthy.
- Current nightly results describe the newest eligible **collected** authenticated
  run, not proof of a lossless GitHub event stream. Older clients without counted
  exact-attempt reads and old records without issuer/attempt provenance are
  unknown. Retention, failed collectors, polling limits and no read activity can
  reduce coverage. A saved legacy verified-push goal disposition is weaker than
  the current run evidence and never upgrades the stronger profile.
- These are observations, not enforcement-ready complete contexts, publication
  adapters or exact-version action receipts. Any future activation requires the
  separate canonical admission/authority work and explicit rollout authorization.

## Validation

Pure predicate tests cover exact/stale/missing base tips and CI coverage,
authentic versus missing/disabled non-reproduction, old/future/skipped/cancelled/
failed nightly runs, workflow/job/head/source/attempt mismatches, publication
receipt coverage and supersession, pipeline scan completeness and broad visibility.
The same real-source SQLite/PostgreSQL suite covers source/policy events,
restart/replay, threshold/window expiry, newest-500 selection, permission denial,
error backoff, scoped/paused/self pipeline reads and absent expected refs. It
snapshots legacy task/goal/run/tool/comment/grant/readiness/event/notification/
base/release/merge state and Changed signals around diagnostic reads and errors.
Collector tests preserve actual legacy inputs while varying stronger evidence,
verify exact recheck scope and pin stall thresholds to the legacy classifier.
API tests exercise resource ceilings after privileged cache population and scope
edits; actual accepted/denied/pending/stale/error views validate against canonical
schemas. Angular tests cover visible legacy/current differences, exact provenance,
expiry and absence of operational controls. Existing framework retention,
contention, concurrent failure and API regression suites remain required.
The merged CI/check and review/approval extensions are covered on both database
engines: PR to health-goal and back transitions during error backoff select the current family,
including the external-CI preflight, stale CI input and restart. A late pre-goal
CI request cannot rewind a newer accepted health timestamp or replay it across a
local health-source change. Resource-restricted
preflight reads filter old health samples and withhold aggregate metadata;
scoped collection and failure writes reauthorize the task, and current-only reads
remain independent of retained history. The shared registry implementation pin is
v7; all eleven registered types retain their v1 profiles and wire contracts.

The delivery/integration/children port shares this registry and persistence path.
Health read ceilings travel in ordinary scoped Go context, independently of
explicit child comparison configurations. Non-health tasks retain the upstream
outcome, CI and review projections; health goals retain only their selected
health profiles. Child history reauthorization and health-resource history
filtering both apply, and source tables remain deployment-local diagnostics.
