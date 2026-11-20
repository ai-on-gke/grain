# Combined legacy-to-shadow coverage audit

Task #70 audits merged source at `04d78d400`, rather than task closure.
The canonical contract is [design/spec/gates](../design/spec/gates/gates.spec.md).
All observations remain nonrequired monitors, `action=null`, legacy authority,
empty work/actions and false lifecycle/action/dispatch/cutover flags.
No production observation window, deployment, configuration, grants, migration
or authority handoff is included.

## Delivered foundation

These actual merge commits are ancestors of the audited source:

| Delivery | PR | Merge |
| --- | --- | --- |
| Canonical specification #59 | #1719 | `2e45dca71` |
| Initial framework #61 | #1739 | `6bd221161` |
| Initial framework review #64 | #1803 | `07e43408c` |
| Outcomes #65 | #1816 | `281e37a9a` |
| Checks #66 | #1824 | `f022b607f` |
| Reviews/approval #67 | #1813 | `f7a0152ed` |
| Health #68 | #1825 | `e9c203750` |
| QA/release #69 | #1830 | `04d78d400` |

The registry has the original twelve canonical types plus Task #69's
`release_qualification`: **thirteen**, without duplicate IDs. Schema pins and
the complete canonical attachment type set are checked in
[registry_test.go](../pkg/gates/registry_test.go).
Shared implementation pin: `builtin-gate-observer-v10`; wire/config versions stay v1.
Old provenance pinned to an older implementation is conservatively unavailable
until an ordinary source refresh. No bulk rewriting of old records occurs.

## Supported, unknown and deferred matrix

**Supported** means the described predicate can be evaluated over admitted
readable facts, including negative/pending results. **Unknown** identifies a
concrete missing record, authorization or adapter proof; it is never a pass.
**Deferred** describes authority/effects that this observation implementation
does not activate. Positive pure-predicate tests do not prove runtime source availability.

| Type and every configuration mode | Actual reader / predicate | Supported runtime observations and exact limits |
| --- | --- | --- |
| delivery: report/change/review/nothing_needed/any; current_v1 and legacy_finish_v1 | [captureReportFacts](../pkg/model/gate_observations.go), [captureOutcomeFacts](../pkg/model/gate_outcomes.go), [evaluateDelivery](../pkg/gates/outcomes.go) | Settled own durable report (on `analyze` or report-only `implement` tasks with `AwaitsAck` true, no change link, non-review and non-`nothing_needed`), typed finish precedence, authenticated own change/in-place owning-change acceptance, delivered review, validated no-work. Questions/access/secrets/parking and pushed changes retain precedence; no_action/interruption is not delivery; settled change/review/no-work tasks with comments never fall back to report products without current finish provenance. Missing source is unknown. Reports over 8192 characters, attachments and uncorrelated runs remain incomplete. Product delivery does not attest all messages answered or lifecycle completion. |
| acknowledgment: explicit_receipt | captureReportFacts / captureOutcomeFacts / [Evaluate](../pkg/gates/registry.go) | Applies only to settled report products (`analyze` or report-only `implement` tasks with `AwaitsAck` true); never applies to change, review, no-work, blocked or unknown-provenance tasks even when a closing comment exists. Verified identified person's close strictly after current body report and settlement. Equal clocks, unverified actor and missing eligibility/ordering proof stay unknown. Legacy close events lack rendered report/policy receipts, so this conservative observation is not future action admission. |
| acknowledgment: legacy_report, legacy_child_answer, legacy_nothing_needed_ack | captureOutcomeFacts / evaluateLegacyAck | Actual original child filing provenance and configured no-work ack remain distinct. Reviews never acquire a second compatibility acknowledgment. This audit pins no-work receipt to the typed finish even without a report body; incidental oversized reports do not hide it. Unattributed/simultaneous close stays unknown. |
| integration: pr_merged/cl_submitted; positive_v1 | [gateIntegrationResources / captureGoalIntegration](../pkg/model/gate_outcomes.go), [evaluateIntegration](../pkg/gates/outcomes.go) | Complete owned current PR or every scoped CL; external attribution, canonical renumbering, exact head/snapshot and policy. Positive landed/empty equivalence needs proof/base head. Closed-unmerged is negative. Missing CL snapshot/conditional provider receipt is unknown, never inferred from branch disappearance. A selected same-scope goal target does not widen task permissions. |
| children: success/closed/merged | captureChildOutcome / evaluateChildren | Explicit selections only, plus labeled tasks-closed goal import. Reopen/messages/finish/relation revisions invalidate. Closed/merged imports preserve weaker predicates; failed/canceled/unproven closure never proves success. Missing/private child and bounded graph incompleteness stay unknown. Standing parent policy no longer hides independently selected readable outcomes. |
| checks: effective/github/grain/both/provider/none; extra_jobs union; empty unknown/explicit_none/legacy_settled; current_v1/legacy_ci_v1 | [collectGateChecks](../pkg/ui/gate_checks.go), [recordedGateChecks](../pkg/model/gate_checks.go), [EvaluateChecks](../pkg/gates/checks.go) | Base-owned required definitions and exact-head complete source collections; authorized none only. GitHub neutral/skipped differ from Grain's required exact-head success. Latest identity/run/attempt membership, renamed jobs, recheck overlays and explicit legacy empty-clock/narrowing/budget exceptions are separate. CL uses provider snapshot presubmit. Missing attempt/count/policy/authorization proof is unknown; provider error is error. CI success alone predicts no merge attempt. |
| review: ordinary/security/path/takeover; current_head_v1/legacy_completion_v1 | [captureChangeGateFacts](../pkg/model/gate_review_observations.go), [evaluateChange](../pkg/gates/review.go) | Primary-PR assignments, legacy completion, findings/questions/fix scopes and current negative context. Current positive review needs attributed exact assignment/head/policy/role version, complete findings/diff/fixes and approved carry-forward. Legacy store lacks those provider/role receipts: runtime positive current review remains unknown. Missing reference/empty-handed legacy completion is explicitly imported only. CL/multiple own changes are incomplete adapters. |
| approval: exact_head_v1; named requirement_ids | captureChangeGateFacts / evaluateChange | Submit, named/path/security and partial approval requirements stay independent. Existing links do not retain rendered-head/current-policy/role-version receipts, so positive runtime current approval is unknown. Typed predicates cover exact named partial decisions, mismatch and revocation. AutoMerge is queue intent only. |
| base_health: legacy_recorded_v1/current_tip_v1; allow_nonreproduction true/false | [captureGateFacts](../pkg/model/gate_health_observations.go), [health predicates](../pkg/gates/health.go), [source hooks](../pkg/model/gate_health_sources.go) | Existing base-green goals. Legacy exact recorded-row predicate; current tip/source/policy and complete explicit nonempty job selector, including authentic exact-commit noncached confirmation. Whole-plan completeness without an independent expected set is unknown. Non-reproduction is an explicit disposition, not a waiver. |
| nightly_health: legacy_500_v1/legacy_verified_push_v1/window_v1; exact workflow/since/window | same health reader / nightlyHealth | Existing nightly goals. Legacy newest-500 case-insensitive default-like/RC selection and saved verified-push disposition. Current authenticated repository/event/ref/head/run/attempt, complete counted jobs and declaration/window expiry. Idle-skipped/canceled newest run cannot prove health; retained samples are not a lossless event stream. |
| publishing_health: legacy_no_finding_v1/positive_current_v1; current_tip or exact expected_commit | captureGateFacts / evaluateHealth | Optional publishing on base-green. Actual BaseBranchPublishingOf threshold/supersession, including absent-row/no-finding legacy pass. The running code's default is three failures or three hours, with existing environment override; historical 24-hour prose is not the predicate. Complete commit/artifact/version/target publication receipts are absent from existing collectors: current positive runtime publication remains unknown. |
| pipeline_health: legacy_quiet_v1/scoped_v1; explicit repos | captureGateFacts / evaluateHealth | Existing pipeline-healthy goals. Legacy filtered notifications AND deployment-wide steward quiet need deployment read authority; self/regression and person-paused exclusions retained. Current repository managed-ref policy, base proofs, notifications/tasks/queue are scoped and bounded. Missing expected sets, visibility or completeness stay unknown. |
| qa_stage: s3/s4/s5; flow_revision; treatment/shadow | [CollectQualificationObservations](../pkg/gateobserve/gate_qualification_observations.go), [EvaluateStage](../pkg/gates/qualification.go) | Authentic exact-head/flow/engine/arm, actual enabled S3/S3.5 rules, sticky earlier same-head failure, conditional/grouped custom checks, S4 bounded rounds/salvage and S5 TriageDecision/human floors. Task monitors work before PR creation. Unsupported composite/custom policy pins or unavailable applicability/result stay unknown. Control/shadow never gains treatment authority. |
| release_qualification: suite/provenance/soak/person/breaking_change/signing_version/publication_readiness/promotion_policy | qualification collector, [readReleaseReadiness](../pkg/gateobserve/gate_release_readiness.go), [EvaluateRelease](../pkg/gates/qualification.go) | Real repository qualification-suite tasks only. Authenticated highest-attempt aggregate suite check; complete RC tag/head/ancestry and breaking range; pinned release.conf veto/version facts. Qualification PASS exemption is distinct from recorded soak. Missing effective minimum/short-soak/breaking answers, signed human/artifact proof, complete assets and effective promotion policy stay unknown. Synced release without release-tag history cannot prove a nonbreaking range. |

Full family-specific evidence and test inventories remain in
[outcomes/operator](gate-observations.md), [checks](gate-check-observations.md),
[review](gate-review-observations.md), [health](gate-health-observations.md)
and [QA/release](gate-qa-release-observations.md).

## Every original mapping row and controller policy

The source inventory is the actual Task #51 document at
`f764e02e0:docs/mcp-gates-migration-coverage.md`, including its route and writer
tables. Its transport proposal is superseded. The table below accounts for every
behavior row (original line numbers), and all corresponding
[compatibility rows](../design/spec/gates/compatibility.md).
No policy is replaced by an always-false gate.

| Task #51 rows | Current authoritative source and observation placement |
| --- | --- |
| 19: proposal/admission/dependency closure | [schema.go](../pkg/model/schema.go), [state.go](../pkg/model/state.go), finish proposedDependencies. Approval/admission and dependency blocking remain controller policy. Only explicit child monitors observe outcomes; dangling links never become successful absence. |
| 20: model/team/base/target/catalogue/grants | [cycle.go](../pkg/orchestrator/cycle.go), [capability_pass.go](../pkg/orchestrator/capability_pass.go), targetrepos. Controller authorization/setup/capability policy. No evaluator mints grants or overrides a hard hold. |
| 21: capacity/pause/budgets/retries | [dispatch](../pkg/dispatch/dispatch.go), repair reserve, [CI SharedGate](../pkg/ci/gate.go), state failure streak exclusions. Scheduler policy. A met outcome says nothing about worker fit, pause exceptions, capacity, OOM/environment/interruption retries or failure exhaustion. |
| 22: finish precedence and branch salvage | [processResultFrom / finishNothingNeeded](../pkg/orchestrator/finish.go), outcome source hooks. Delivery predicts actual authenticated product; live requests/branch retirement remain legacy disposition. |
| 23: reports/child answer/review/no-work receipt | state AwaitsAck/AwaitsSubmit, [nothing_needed.go](../pkg/model/nothing_needed.go), finishAnswered, [review.go](../pkg/orchestrator/review.go), UI reopenAcknowledgeable. Separate delivery/ack scopes; typed no-work receipt repair above. No approval is granted by Close. |
| 24: task/CI/message/time waits | [wait_target.go](../pkg/model/wait_target.go), [wait_task.go](../pkg/ui/wait_task.go), [wake.go](../pkg/orchestrator/wake.go). Actual OR/mutual-exclusion rules, settled-versus-met, all-terminal CI wake, park-owned link cleanup and backstops remain controller policy. Shadow waits never register with these services. |
| 25: durable goals/limits/person-closes/standing | [taskgoals.go](../pkg/orchestrator/taskgoals.go), [task_goal.go](../pkg/model/task_goal.go), standingwork. Existing on_met close/rest, GoalDefaults, max_resumes/stall, on_limit hold/notify and progress accounting remain policy. Saved MetAt is no new positive receipt. Independent resource observations can pass on a standing task without closing/resting it. |
| 26: autonomous goal mode | [goal_mode.go](../pkg/orchestrator/goal_mode.go). Verified complete_goal strings/checkout and continuation/runtime/empty-progress limits remain run policy. They do not establish authenticated external outcomes or action permits. |
| 32: CI | Checks matrix above: exact-head source-specific verdicts, narrowing/empty exceptions, complete base-owned definitions, retained overlay. Missing evidence is not success. |
| 33: queue/lane/behind/repair/overlap | [mergepredicate.go](../pkg/orchestrator/mergepredicate.go), [mergerecheck.go](../pkg/orchestrator/mergerecheck.go), [githubqueue.go](../pkg/orchestrator/githubqueue.go). Existing executor policy; checks expose separate inert conflict/base/recheck context. Full atomic queue/merge-group admission receipt is unavailable, so no would-attempt-merge proposal is emitted. Base red never becomes a blanket merge guard. |
| 34: ordinary/takeover review | review/takeover/reviewfeedback writers, current/legacy review predicates. Deadline/missing-reference weak import never becomes current positive approval. |
| 35–36: security/path/trust/approvers | [securityhold.go](../pkg/orchestrator/securityhold.go), inheritance, repoguard/trustboundary/pathtriggerhold/pathtriggerreview. Current typed scope/category/head/policy unions and carry-forward; bounded/truncated diff stays unknown. Human no/conditional/required and self-clear restrictions remain legacy authority; missing current diff/role provenance is explicit above. |
| 37: findings/questions/acceptance | [accept_findings.go](../pkg/ui/accept_findings.go), reviewquestions, [review_findings_accept.go](../pkg/model/review_findings_accept.go). Typed exact review/version/head/category/digest/person/reason/policy acceptance and clean subsequent review clear only their scope. Existing runtime lacks the stronger immutable receipts. Thread resolution and Submit do not accept findings. |
| 38: Submit/partial approvals/fresh CI | [client.go](../pkg/ui/client.go), approvals. Person-only existing service remains authoritative, including fresh nonpassing Grain CI request. Observer records scope limits and cannot call it. Click-time legacy head is weaker than rendered-head receipt. |
| 39: force/repair bypass/post-merge ownership | model merge_gates, [baserepairbypass.go](../pkg/orchestrator/baserepairbypass.go), mergegates/reviewfeedback. Named override/head/reason context is observed; empty legacy override list means CI only, approval is excluded. Base-repair exception still needs own CI; post-merge findings ownership persists. No shadow waiver or new override admission exists. |
| 40: QA | QA matrix above; S0/S2 are prompt/execution telemetry, not extra completion gates. Bounded repair/custom effects/circuit-breaker rollout and human trust/review floors retain their writers. |
| 41: PR/CL integration and direct closure | sync recordPullRequestEvents, landed, [change/submit.go](../pkg/change/submit.go), CL reviews and UI change_submit. Positive integration is separate from delivery/readiness. Temporary person-only CL grants, sync/conflict/presubmit and direct legacy closures stay authoritative. No conditional CL snapshot effect adapter is claimed. |
| 47–49: base/nightly/publishing/pipeline | Four health rows above, with actual collector thresholds, complete-set and broad-scope limits. No quiet notification scan stands for an absent expected base set. |
| 50: work ownership | [work_ownership.go](../pkg/model/work_ownership.go), schedule/base/release/post-merge/port/triage producers. Existing automatic-work routing policy; manually requested CI/merge/repair and config-repo required CI exceptions remain authoritative. No completion gate or authority is synthesized. |
| 51: candidate/release qualification and safe promotion | QA/release matrix and source audit. Suite aggregate observes the trusted legacy JudgeQualificationAttempt/DecideReleaseQualification result (footer SHA/tag, retries, final ordering, failed/canceled/proposal/PR/scratch child/ref restrictions); it does not independently attest private child evidence. Default-branch overwrite/foreign prod history, release concurrency/daily cap/interval/exclusion, signing/publish and custom promotion effects remain legacy executor policy, with no complete current receipt. |

Rows 59–62 (API/tool/credential paths) and 89–93 (legacy writer families) retain
authority unchanged. This includes agent tools/CLI/proxy/controller routes,
person Submit/accept/force/CL/config approval, queues, release automation and
credential mint/refresh. There is no observation-result consumer in those
decision paths. Source hooks record bounded diagnostics after ordinary domain
facts; they do not consult predictions. No remote credential/exclusion probe or
grant change was performed.

## Shared integration, isolation and load

The [store](../pkg/model/gate_observations.go) serializes bounded diagnostic
transactions separately from Store.write/Changed. There is one registry,
canonical config validator, acceptance/history path and unavailable-view builder.
This audit shares report/no-work receipt reading rather than duplicating close
provenance logic. No general rewrite or second evaluator service is introduced.

Bindings reserve actual outcome, CI and QA/release contributions before review
fanout within 64 monitors. Review overflow is incomplete, not an omitted passing
guard. Health-goal selection bypasses task QA/release collectors; family switches,
failure reconstruction and restart/backoff retain the current family.
Result identities/comparisons use binding identity rather than cross-family
position. Work/actions remain empty; waits and human/agent guidance are inert
descriptions. There is no aggregate completion/dispatch/action permit: collecting
all met results cannot bypass standing, admission, capacity, pause, budgets,
unprocessed messages or executor preconditions. Proposed authority scopes remain
distinct (report receipt, findings acceptance, Submit, override, integration).

Record and complete-set revisions cover exact task/link/run/finish/close,
head/snapshot, CI attempt/membership/definition, review assignment/policy,
authorization, goal declaration, health row/plan and qualification/assignment
collections. Local acceptance rechecks optimistic external source fences. The task-local
dependency also retains its own fence independently of combined remote input.
A late older request cannot replay a cache after a local finish/receipt/child
changes; it reevaluates current local facts at a monotonic acceptance time and
keeps older external CI/qualification evidence unknown until recollection.
Unchanged local context still replays newer accepted external evidence.
Freshness is source-specific: minute remote leases, source/window expiry and
retry deadlines; future observations now stay unknown consistently across
outcomes/checks/review as well as health/qualification. Event hooks are ordinary
scoped source updates, coalesced on authorized reads. No read means no sample;
no shadow timer wakes a task. Heartbeats/unchanged text do not recursively dirty
the source, count progress or enqueue work.

[UI/API projection](../pkg/ui/gate_observations.go) checks current task repository
before replay and related resources before display. CI/release/health permissions
are independent of task visibility. Same-repository/workspace relation scope
does not authorize cross-task repository reads. Scoped callers receive current
authorized predictions without retained historical payloads; old samples can
refer to former repository scopes. The Angular card displays explicit shadow,
current/legacy, provenance, stale/error and inert guidance; no operational controls
or route mutations were added.

Existing bounds remain: 32 selected tasks/current caches; 64 monitors; eight CI
changes, 512-row admitted collections and six CI pages; bounded related/review
scopes; 256 KiB combined view and 64 KiB qualification contribution; 1024 history
samples/task, 32768 globally and 72 hours. History reads decode up to 20 complete
newest samples within 64 KiB, always retaining the newest complete sample even
when it alone exceeds that budget (up to the view cap). Older retained samples
still count. Outcome provenance is separately bounded/expired. Diagnostic local
collection has 250 ms and failure recording 50 ms budgets. QA/release external
reads have a three-second acceptance context; legacy contextless forge interfaces
can still run to their transport timeout and may paginate before admission caps.
These are documented load/latency limits, not guaranteed complete streaming.

## Discrepancy inspection for the planned 1–2 days

This PR starts no window. After a separately authorized rollout, retain the
operator guide's explicit UTC enable/build/cohort marker; started_at alone can
predate a disable/re-enable. Choose representative report, no-work-ack,
review/PR/CL, explicit child, health and QA/qualification tasks without changing
their requirements. Existing read-only selection is capped at 32.

1. Read each selected task's ordinary GET /api/tasks/{id} or /next/tasks/{id}.
   Save build, UTC read time, source_revision, bindings/config/subject/origin,
   result context/evidence/dependency versions, valid_until, retry and family
   comparison counts. Check false effects, nonrequired monitors and legacy authority.
2. Compare the same exact resource/head/policy/collection at the same time with
   the authoritative legacy state, CI, review/approval, goal and queue views.
   A later head or partial history page is not a like-for-like discrepancy.
3. Classify current-versus-legacy strengthening, unavailable adapter/provenance,
   denied scope, incomplete set, stale/future clock, source error/backoff, or
   actual predicate/integration defect. Keep each independent scope visible;
   passing CI cannot erase pending review, approval, findings or child outcomes.
4. Re-read after source event, expiry or retry; check restart replay and no
   generation/history storm on unchanged reads. History is sampled and bounded:
   report gaps/churn and the newest-complete-sample byte limit explicitly.
5. Check real run/queue/wait/action/approval/merge/close/progress events for
   noninterference. A shadow remediation string is never a requested action.
   Preserve evidence for human discrepancy review; do not automatically file work,
   change configuration, promote authority or activate enforcement after elapsed time.

The final independent thorough E2E qualification follows this audit. This change
adds focused predicate/source/replay/noninterference tests and retains the
existing family/framework/API suites; it does not claim that source blockers or
future effect-admission contracts are qualified.

## Validation record

The initial implementation at `050a8b5e0` passed the full evaluator/source and SQLite
native suites, focused observation suites on SQLite and required real PostgreSQL,
and API/controller/legacy writer regressions for review, approval, Submit,
findings, security/path/takeover, goals, waits and merge predicates. Affected
Go vet checks passed. All 61 canonical examples, 13 types and 29 API operation
contracts passed, as did 76 actual persisted/current/API/history observation
views, including the repaired no-work and standing-task cases, against the
canonical observation schema.

Race validation passed gates, qualification sources and SQLite, followed by
bounded PostgreSQL and API/controller groups. The initial combined cold command
hit the 600-second command limit after the first three groups passed; remaining
groups were completed separately. One API race run returned the intended
250 ms diagnostic-unavailable fallback. Three isolated repetitions and the
broader API/controller race run then passed without code, budget or assertion
changes. Base-status had no recorded verdict, so this is not attributed to an
inherited base failure. Final hosted verdicts are recorded on
[PR #1875](https://github.com/ai-on-gke/grain/pull/1875).

The disposable sandbox needed `postgresql` and `python3-jsonschema`; no deployment
or application configuration was changed. Independent thorough E2E qualification
and every future authority/effect contract remain outside this audit's claims.

Final replay inspection reproduced two additional defects before repair: an older
request reused a newer cached no-work receipt after reopening, and a late CI read
reused a closed selected child after that child reopened. The repair persists the
independent local fence in the existing task dependency, refreshes changed local
facts without rewinding acceptance, and invalidates older external evidence until
fresh recollection. No public field, database schema or authority changes were
needed. Both stores pass native and focused race regressions for these cases,
coexistence, qualification, family transitions and failure ordering. Current API
and controller regressions and affected Go vet pass; 81 actual wire views and all
canonical examples/contracts pass. CI/review now share canonical summary bounding:
a reproduced 295-character legacy CI diagnosis retains its complete explanation
in bounded inert guidance while honoring the 240-character summary contract.
