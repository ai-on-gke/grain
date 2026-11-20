# Isolated end-to-end gate qualification

Task #71 qualifies the merged observation framework from baseline `7123a3460`
(the Task #70 merge), using the canonical [specification](../design/spec/gates/gates.spec.md),
[API contracts](../design/spec/gates/http-api.spec.md),
[catalogue](../design/spec/gates/catalogue.md), and the source-backed
[combined audit](gate-coverage-audit.md). Tasks #65–#70 are merged ancestors.
Task #58's PR-feedback implementation is also present (`c1a0b5a5d`).

This is code qualification in disposable environments, not live deployment
qualification. No production configuration, grant, deployment, migration,
observation window, merge, release or cutover was performed. No implementation
proposal was filed. Advancing a fixture clock by 48 hours does not constitute
observing a deployment for 48 hours.

## Findings and repairs

1. **Unsafe retained history bypassed the current-view shadow boundary.**
   Fault injection into persisted current and history JSON reproduced this on
   SQLite and PostgreSQL: current replay rejected enforcement mode, built-in
   authority, each effect flag, required/guard/action bindings and nonempty
   work/actions, but history returned those same declarations unchecked.
   Additional restore regressions reproduced unregistered types, future versions
   and invalid/open configurations escaping through history; those are withheld
   too. Identical immutable configs are validated once per bounded history page.
   This was an API/history projection defect; the test did not demonstrate
   execution of a historical action. The new shared inert-envelope check
   withholds unsafe, malformed or oversized historical samples and exposes
   `unknown_invalid_history`. It preserves the stored records for normal
   retention and safe older-build provenance for inspection. Current evidence
   remains independently available, and subsequent source events produce safe
   history normally. The history decoding byte budget also counts withheld
   records, preventing invalid samples from bypassing that bound.
2. **Evaluator errors were persisted as accepted predicates.**
   Canonical schema validation of 606 actual persisted views found 56 error
   entries with disposition `accepted`; all other wire checks passed. The
   lifecycle matrix then reproduced the defect on both engines. Persistence
   now rejects evaluator errors, clears `accepted_at`, leaves their coverage
   unaccepted and exposes the source lease as the read-driven retry boundary.
   Changed source evidence can recover sooner. A regression with actual recorded
   QA error rows exercises collection → rejection → fresh pass recovery. Cached
   or historical accepted-error rows cannot be replayed as valid shadow evidence.
3. **Angular duplicated guidance and exact-head labels.**
   The type-by-status renderer regression failed for all thirteen types because
   agent guidance appeared twice. Source inspection also found the duplicated
   evidence-head rendering. Both duplicate template blocks are removed. The
   regression now covers all 65 type/status combinations and requires guidance
   and evidence head to appear once, predicted waits to remain descriptive,
   and the absence of mutation controls. A separate test explains withheld
   history to readers.

4. **Duplicate SQL hydration consumed diagnostic budget under race instrumentation.**
   Broad and focused API race runs reproduced bounded-unavailable fallbacks in
   positive/recovery CI scenarios. A CPU profile identified transactional SQL and
   task hydration as the dominant collection cost. CI acceptance was reloading
   the task, links, grants and observation already loaded for local facts. It now
   reuses those rows inside the same authorized transaction, retaining all policy
   and source-fence queries. The complete gate/observation API race group then
   passed in 103.278 seconds. A new HTTP regression adds a named Submit requirement
   during external CI collection: the old collection becomes unknown, a fresh
   collection can pass CI, and the new approval remains pending.

No evaluator predicate, admission budget, collection deadline, authority pin,
production setting or legacy state machine was weakened.

## What is exercised

The existing Go integration, daemon/orchestrator, SQL, fake-forge/CI/CL,
Vitest and Playwright infrastructure is used. No separate testing framework
or external private provider is required. Database tests run against ephemeral
SQLite databases and the repository's real local PostgreSQL harness with
`GRAIN_REQUIRE_POSTGRES=1`; inability to start PostgreSQL must fail.

The persisted integration path is ordinary task/run/tool/comment/link/close,
CI/review/child/health/qualification rows and source hooks, followed by bounded
collection, source fences, evaluator acceptance, current/history persistence,
authorized ordinary task HTTP/agent projection and Angular rendering. Existing
API-generated fixture browser tests cover report and CI cards; the new
`next-gate-live-qualification.spec.ts` additionally traverses the real demo
database and HTTP path without intercepting or replacing responses. The browser
harness is an actual local server with an explicit worker-local task allowlist;
its evidence is synthetic demo data.

The following table identifies regression owners, rather than treating every
pure predicate as a provider-backed E2E success. Shared model suites execute on
**both** SQL engines. Their normal read helpers now check the common shadow
boundary: observation mode, legacy authority, all effects false, matching result
types, nonrequired monitor bindings, no action binding, and empty work/actions.

| Workflow or boundary | Exercised coverage and practical interpretation |
| --- | --- |
| Report → awaiting receipt → existing human acknowledgment | `Run` and `RunReviewRegressions` in [the shared database suite](../pkg/model/gateobservationtest/suite.go), and `TestGateObservationsOrdinaryAPIReportReceiptAndLegacyReply`: real attempts/comments, complete-empty vs missing/truncated/attachment evidence, strict human close ordering, revision/reopen, pagination, authorization and existing reply writer. A report receipt is not findings acceptance or Submit. |
| Delivery variants and child imports | [RunOutcomes](../pkg/model/gateobservationtest/outcomes.go) and [predicate cases](../pkg/gates/outcomes_test.go): report/change/review/no-work/any, typed finish precedence, no-action/interruption, configured no-work acknowledgment, standing policies, failed/canceled children, explicit success/closed/merged distinctions, reopen, cross-repository children and relation cycles. Stronger success semantics stay separate from weak legacy closure imports. |
| PR/CL integration | `pr-head-policy-source-and-restart`, `multiple-cl-renumbering-and-completeness`, and [CL API tests](../pkg/ui/gate_checks_test.go): exact resource/head/policy, every scoped target, renumbering, closed-unmerged/abandoned negatives, missing snapshot and collection proofs. Branch disappearance is not integration proof. |
| Checks and independent merge preconditions | [Checks predicates](../pkg/gates/checks_test.go), [recorded checks](../pkg/model/gateobservationtest/checks.go), [ordinary HTTP/fake forge/CI/CL](../pkg/ui/gate_checks_test.go), [forge collection tests](../pkg/forge/checks_observation_test.go): effective/github/grain/both/provider/authorized-none, extra-job unions, required identities, base-owned definitions, exact-head attempts/recheck membership, complete-empty and truncated sets, permissions, errors, conflicts/behind/base and overlap context. Current and imported legacy verdicts remain distinct. Passing CI alone does not prove merge admission. |
| Ordinary/security/path/takeover review and scoped Submit | [Change model suite](../pkg/model/gateobservationtest/change.go), [review predicates](../pkg/gates/review_test.go), `TestReviewApprovalObservationsPreserveAuthorizationAndSubmit`, and the legacy review/approval/merge suites: independent assignments, findings/questions/fix scopes, partial named approvals, acceptance with reason, revocation, self-clear and role/head/policy boundaries. Existing Submit may resolve its named approvals without accepting findings; shadow unknown cannot block or issue it. |
| Base repair, non-reproduction and nightly | [Health model suite](../pkg/model/gateobservationtest/health.go), [health source ordering](../pkg/model/gateobservationtest/health_ordering.go) and [health predicates](../pkg/gates/health_test.go): legacy recorded vs current-tip profiles, explicit selectors, confirmation/noncached evidence, non-reproduction dispositions, expected complete set, workflow ordering/window expiry and source bounds. |
| Publishing and pipeline health | The health suites and `TestHealthGateAPIPermissionsAndProjection`: publication target/commit/artifact receipts, repository vs deployment-wide scope, managed plan/alert completeness, pauses/self-exclusion, current/legacy comparisons and independent task/CI/release read permissions. Absent complete publication proof stays unknown. |
| QA assignment/stage and release qualification | [Qualification source suite](../pkg/gateobserve/observations_test.go), [persisted qualification suite](../pkg/model/gateobservationtest/qualification.go), [typed predicates](../pkg/gates/qualification_test.go): experiment arm/assignment/head/flow/engine, S3/S4/custom evidence, actual recorded QA and release observations, missing tags, provenance/expiry, policy/assignment changes and bounded combined family overflow. A control arm cannot become treatment authority. |
| Combined family switches | [RunObservationFamilies](../pkg/model/gateobservationtest/families.go): task CI/review/QA/release to health goals and back, stale late external reads, repository-only health projections, failures/backoff/restart and independent per-family history counts. |
| Feedback during CI/person wait and completion | Existing Task #58 feedback and race suites plus [TestGateShadowPRFeedbackDispatchNoninterference](../pkg/orchestrator/gate_feedback_qualification_internal_test.go): actual webhook writer → durable comment/receipt → shadow read → real dispatch. Two unseen comments produce precisely their legacy dispatches; duplicate delivery and reads do not add dispatches, acknowledge unseen comments, consume a secret, or discard the held CI head. A normal question can legitimately allow a feedback run while keeping the question unanswered; a secret hold retains its stronger policy. |
| Every result status across lifecycle controls | [RunShadowQualification](../pkg/model/gateobservationtest/qualification_matrix.go): 35 status/lifecycle combinations per engine, restart/replay and synthetic 48-hour advancement. Real queued/live/question/stopped/paused/closed/standing records, wait descriptions and queue context remain unchanged by each read. |
| Persisted upgrade/restore safety | [RunShadowCacheSafety](../pkg/model/gateobservationtest/cache_safety.go) and [HTTP recovery](../pkg/ui/gate_history_safety_test.go): fifteen unsafe envelope/binding/output/profile variants on both stores, invalid newest history, safe prior-build history, recovery, single/paginated/batch HTTP reads, no unsafe payload disclosure, no legacy mutation/wake. |
| Long histories and broad child graphs | `RunWideShadowGraph`: 32 explicit children, 1,024 persisted comments, 17 unchanged reads, one accepted evaluation, bounded view and detection of direct child reopen with a missed dirty notification. Existing retention tests cover 1,030 per-task input samples, 35 rotated caches, 32,768 global samples, 72-hour expiry, newest-complete sample byte paging and source retention bounds. |
| UI and endpoint contracts | Angular type/status matrix, gate component tests, real live-browser regression, existing report/CI browser tests, full task API/auth/agent/route tests, config/registry tests and canonical validators. Browser history disclosure is keyboard-accessible, cards fit phone/desktop and light/dark, and inspection/reload makes no mutation requests. Canonical proposed gate mutation/admission APIs are documentation contracts, not deployed operational endpoints. |

## Noninterference evidence

The lifecycle matrix snapshots the legacy task, observation, run/tool, comment,
attachment, goal, ready/state, pause, link, grant/grant-wait, event, integration,
merge-batch, recorded CI, QA assignment/check/finding, enrollment/experiment and
release-approval tables around each shadow operation. It also checks store commit
accounting and the dispatch notification channel. Existing family fixtures
add their health/repository/notification tables and provider spies. Unexpected
forge mutation methods panic; fake CI captures starts/cancels; CL adapters
record submissions. The shared assertion checks the returned binding/output
boundary across family suites.

The HTTP contract-change regression also verifies that transactional row reuse
never substitutes the external preflight's old task/approval policy for current
local rows. All current policy/fence reads remain within acceptance.

Actual scheduling is separately exercised with shadow reads inserted before and
after legacy webhook and dispatch operations. Mutations such as human close,
Submit, reply, pause and feedback dispatch are deliberately performed by their
existing legacy writer. Tests snapshot after that writer and require shadow
evaluation to preserve its result, rather than attributing a legitimate legacy
change to shadow evaluation or preventing it. Predicted waits are text/receipts;
the held CI head and unanswered questions remain authoritative.

A historical corrupt declaration being withheld is not an authority migration.
Elapsed 24–48 hours, all-met results, error recovery and a restarted store cannot
enable enforcement, request work, create tasks, close/reopen tasks, change grants
or approvals, submit/merge/promote, affect failure accounting or replace actual
waits. UI inspection offers disclosures only.

## Execution record

Local native qualification passed the full affected packages:

```sh
GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/gates ./pkg/gateobserve ./pkg/forge ./pkg/github ./pkg/change ./pkg/model ./pkg/model/sqlite ./pkg/model/postgres ./pkg/ui ./pkg/daemon ./pkg/orchestrator -count=1 -timeout=8m
```

All eleven packages passed. The longest package was `pkg/ui` at 328.557 seconds;
PostgreSQL took 229.921 seconds. This full run included the history repair;
after the wire validation found the accepted-error defect, focused dual-engine
rejection/recovery checks passed and the final affected/race/CI runs below
qualify that additional repair.

Angular lint, changed-file formatting, all **827 unit tests in 76 files** and the
production build passed. The existing agent CSS (8.47 kB / 8 kB) and settings CSS
(8.03 kB / 8 kB) warnings remain nonfatal. The final browser coverage completed **15 SQLite checks** and **four PostgreSQL
live-path checks** across desktop/phone/dark projects. This includes all existing
report/CI card checks, unmocked persisted reads/paging/reload/keyboard/axe, the
new persisted comment-event → evaluation/history → Angular refresh workflow,
all four full-system destination walks and the primary-destination contrast check.
The initial combined browser command reached its 300-second command cap after
13 of 14 checks passed; the unfinished contrast check passed separately in
56.7 seconds, and the new comment-event case also passed separately. No unfinished
check was counted as a pass and no assertion was removed.

Final pre-hydration-repair native core checks passed gates, qualification sources,
SQLite and required PostgreSQL (107.763 / 315.830 seconds for the two databases).
The initial model race group passed both database engines (208.751 / 54.359
seconds); all evaluator and qualification-source race tests also passed in their
unfiltered run. The broad controller race group passed orchestrator (336.948
seconds) and daemon (51.968 seconds).

The initial concurrent API race group returned the intended inert 250 ms
unavailable fallback in these cases rather than the expected positive observation:

- \`TestGateChecksOrdinaryAPIEventsAndNoninterference\`
- \`TestGateChecksGrainBaseDefinitionsRechecksAndBothSources\`
- \`TestGateChecksWorkflowSuitesDoNotMaskFailures\`
- \`TestGateChecksRecordedEventsAndLaggingReplica/during-read\`
- \`TestGateChecksLatestWorkflowNeedsCurrentAttemptJobs/same-run-rerun\`

Two focused runs still reproduced CI recovery/workflow fallback cases. These
were investigated and led to the transactional hydration repair above, followed
by the passing full gate/observation API race group. No data race, predicate,
lock, diagnostic budget or assertion was suppressed. \`grain pr base-status\`
reported no base CI verdict, so no failure is attributed to an inherited base
failure. Post-hydration-repair native observations passed on SQLite/PostgreSQL in
11.495 / 21.232 seconds, followed by focused observation/family race suites in
150.044 / 54.283 seconds. The scoped contract-during-CI HTTP regression passed
natively, and the recorded QA source suite passed. Later history-profile restore
cases passed on both engines as well; the final delivered-profile/CI checks are
recorded in the closing evidence.

All **606 exported persisted views plus six actual authorized HTTP current/history
views** passed the canonical observation-view schema, spanning all thirteen types
and all five statuses; all **160 error entries were rejected**. The canonical
validator passed 61 examples, 13 types, 29 API operation/tool contracts, structural
and semantic denials, typed evidence, async flows and current route/scope mappings.
Affected Go vet and the full repository hygiene suite passed. These are wire and
contract checks, not certification of proposed future mutation endpoints.

To reproduce persisted wire export using the existing shared harness, set
\`GRAIN_TEST_QUALIFICATION_VIEWS=/tmp/gate-views.jsonl\` on the model observation
suites. Each line is an actual returned/persisted view, suitable for the local
\`api.schema.json#/$defs/observation_view\` validator with the local canonical
contracts resolver. API tests retain their existing report/check/review/health
fixture export environment variables. No external schema resolution is needed.

Hosted CI is run after pushing the implementation branch. The task's closing
report records the immutable delivered head, PR/run links and final hosted
verdict; a pending check is not a qualification verdict.
The disposable environment uses Debian, Go from the sandbox image, Node/npm,
Chromium from `/var/cache/playwright`, packaged PostgreSQL and
`python3-jsonschema`. The last two packages were installed only in this sandbox.

Representative **synthetic**, non-race 32-child / 1,024-comment run:

| Engine | Returned view | Reads / evaluations | Measured elapsed for 17 reads |
| --- | ---: | ---: | ---: |
| SQLite | 31,673 bytes | 17 / 1 | 0.972 seconds |
| PostgreSQL | 31,673 bytes | 17 / 1 | 1.233 seconds |

These are fixture measurements, not deployment throughput, SLOs, real forge
latency or a capacity claim. Tests deliberately avoid asserting wall-clock
performance thresholds under instrumentation. Collection still needs to inspect
the selected local resources to detect missed notifications; coalescing means
no repeated accepted evaluations/history/dispatch, not zero SQL reads.

## Gaps and residual risks

- Positive current review and approval runtime parity remains blocked by missing
  immutable provider head/diff/findings, assignment/role/policy and rendered
  human-receipt evidence. Exact-head/carry-forward/acceptance/partial approval
  predicates are tested on immutable typed facts; this is not proof that a
  real provider collector supplies those facts.
- Complete publication/artifact receipts, signed effective release-policy proofs,
  some CL complete-set/snapshot proofs, atomic queue admission and lossless
  external event streams remain the audit's explicit unknown/deferred boundaries.
  Existing fake adapters do not certify private real forges or hardware.
- New output controls remain deliberately absent. No operational gate mutation
  endpoint, grant of action authority, automatic remediation, authority epoch
  handoff or timed enforcement was qualified or enabled.
- Sampling is read-driven. No read means no sample. History is retained for
  72 hours / 1,024 samples per task / 32,768 globally, with at most 32 current
  caches. Display decodes up to 20 newest samples within 64 KiB, allowing the
  newest single admitted full sample up to 256 KiB. Unsafe historical samples
  consume the read budget and are counted as withheld; rows remain persisted
  until normal retention. Aggregate comparisons are not a lossless timeline.
- Existing local diagnostic (250 ms), failure recording (50 ms), collection,
  resource-count and external interface/transport limits still apply. Synthetic
  runs cannot establish production tail latency or historical event completeness.
- Config/source/role/head changes are tested at supported immutable and local
  acceptance fences. This does not supply an atomic provider/action transaction.
  Differences caused by stronger semantics, denied evidence, missing proof or
  freshness are classifications, not automatically legacy regressions.
- No live 1–2 day window was started. A human must separately review observation
  discrepancies and decide any future rollout/cutover using its own authorization.

## Practical read-only 24–48 hour inspection checklist

This checklist is for an independently authorized observation deployment. It
does not authorize or carry out deployment/configuration/grant changes here.

1. Record an explicit UTC observation start, build/commit, allowlist/cohort,
   observer implementation pin and the person owning discrepancy review. The
   allowlist is off by default and capped at 32. Re-enabling requires its own
   marker; `started_at` may predate re-enablement. Elapsed time cannot activate
   enforcement.
2. Choose representative report/no-work acknowledgment, PR and CL, explicit child,
   review/security/path/Submit, repair/nightly/publishing/pipeline and QA/release
   tasks already within the reader's permissions. Include quiet, busy, paused,
   stopped and closed examples. Keep their legacy requirements authoritative.
3. Use ordinary read-only `GET /api/tasks/{id}` or the Angular task page at
   `/next/tasks/{id}`. Batch inspection can use
   `GET /api/tasks/summaries?ids={id1},{id2}`; comment/attempt pagination must not
   change gate collection. Authorized agents use the same task GET under their
   existing task and independent CI/repository/release read ceilings. Scoped
   readers may receive current predictions without retained history.
4. Save UTC read time, build, `gate_observations` source revision, exact binding
   subject/config/origin, result/evidence/dependency versions, status, validity,
   dirty/retry/disposition and `gate_observation_diagnostics` family counts and
   returned samples. Verify observation mode, legacy authority, all effect flags
   false, nonrequired monitors, null binding actions and empty work/actions.
   In Angular expand **Evidence and evaluation versions** and **Configuration,
   provenance and comparison history**; no shadow action button should appear.
5. Compare the equivalent legacy milestone at the same resource/head/policy/time.
   Classify stronger semantics, missing/inaccessible provider proof, incomplete
   or complete-empty collections, stale/future evidence, source error/backoff,
   changed authorization and genuine defects separately. CI success cannot stand
   in for review, Submit, findings acceptance, integration or child success.
6. Re-read after meaningful events, retry/freshness expiry and one authorized
   restart. Check current predictions, sample gaps, bounded storage, withheld
   history and unchanged-read coalescing. Include feedback arriving during a CI
   wait/person wait and while completion is being processed; verify legacy
   receipt/delivery cursors and precisely the existing dispatches.
7. Compare authoritative task/run/queue/question/grant/approval/merge/release and
   failure records before/after diagnostic reads. Retain evidence of the writer
   responsible for any legitimate state change. Stop the qualification claim
   if shadow creates a mutation, effectful binding/output or operational control.
8. End with a human discrepancy review and an explicit decision. Missing proof
   or unsupported behavior remains unknown; neither elapsed time nor agreement
   across sampled observations is permission to enforce. Do not automatically
   file implementation work, deploy, grant, merge, release or cut over.

Companion operational limits and source-family details are in
[the observation guide](gate-observations.md),
[checks](gate-check-observations.md),
[reviews](gate-review-observations.md),
[health](gate-health-observations.md), and
[QA/release](gate-qa-release-observations.md).
